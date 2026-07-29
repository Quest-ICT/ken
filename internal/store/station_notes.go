package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// The station notebook (docs/STATIONS.md S10) — working state, NOT knowledge.
//
// Nothing here is searched by kb_search, and no function in this file writes a curated
// row. The only route from a note into the knowledge base is a PENDING PROMOTION a human
// converts in the console: deliberately not a kb_save call, because that needs a scope a
// station token is forbidden and a dedup token HMAC-bound to the caller — and because
// routing it through the console carries the write-time hearsay marking server-side,
// where the model cannot retype it. A marking the model retypes is forgeable, which is
// exactly what COMM's provenance work refused to build.

// StationNoteLimits are §9's numbers. Each is a BACKUP decision: every byte lands in the
// live database plus fourteen nightlies plus Litestream, so a cap is really cap × ~15.
type StationNoteLimits struct {
	MaxPageBytes     int // 64 KiB — larger than this is a document, not a note
	MaxRevisionBytes int // 256 KiB per page of history: an undo buffer, not an archive
	MaxNotebookBytes int // 4 MiB of HEAD revisions; history is bounded separately
}

// DefaultStationNoteLimits are §9's numbers.
func DefaultStationNoteLimits() StationNoteLimits {
	return StationNoteLimits{MaxPageBytes: 64 << 10, MaxRevisionBytes: 256 << 10, MaxNotebookBytes: 4 << 20}
}

// HandoffKey is the reserved page the briefing reads first, transfer collides on, and
// every station is expected to keep. A handoff written only on the way out is never
// written, so maintaining it is a duty of the current session.
const HandoffKey = "handoff"

// StationNote is one page. Provenance is ken.db facts only — never an endpoint id,
// which is guaranteed to dangle once the COMM sweep runs and does not exist with COMM
// off (S7).
type StationNote struct {
	Key            string
	Title          string
	Tags           []string
	Body           string
	Rev            int
	Bytes          int
	UpdatedAt      string
	UpdatedByToken string
	HearsayAtWrite bool
}

// ErrNoteRevConflict is returned when an `if_rev` precondition fails. Two sessions may
// staff one station (S4), so a blind write would silently destroy the other's page.
var ErrNoteRevConflict = errors.New("notebook page changed underneath this write")

// ErrNotebookCapReached refuses rather than evicting (S12): silent eviction of a working
// note is data loss the session cannot see, a refusal is an error the model reacts to.
var ErrNotebookCapReached = errors.New("notebook cap reached")

// ListStationNotes returns page metadata and SIZES but never bodies — the AI pays to
// read, so the list exists to let it choose what is worth a second call.
func (s *Store) ListStationNotes(ctx context.Context, stationID string) ([]StationNote, error) {
	rows, err := s.R.QueryContext(ctx, `
SELECT key, title, tags, rev, LENGTH(body), updated_at,
       COALESCE(updated_by_token_id,''), COALESCE(hearsay_at_write,0)
FROM station_note WHERE station_id=? ORDER BY updated_at DESC`, stationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StationNote
	for rows.Next() {
		var n StationNote
		var tags string
		if err := rows.Scan(&n.Key, &n.Title, &tags, &n.Rev, &n.Bytes, &n.UpdatedAt,
			&n.UpdatedByToken, &n.HearsayAtWrite); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(tags), &n.Tags)
		out = append(out, n)
	}
	return out, rows.Err()
}

// ReadStationNote fetches one page, body included.
func (s *Store) ReadStationNote(ctx context.Context, stationID, key string) (*StationNote, error) {
	var n StationNote
	var tags string
	err := s.R.QueryRowContext(ctx, `
SELECT key, title, tags, body, rev, LENGTH(body), updated_at,
       COALESCE(updated_by_token_id,''), COALESCE(hearsay_at_write,0)
FROM station_note WHERE station_id=? AND key=?`, stationID, key).
		Scan(&n.Key, &n.Title, &tags, &n.Body, &n.Rev, &n.Bytes, &n.UpdatedAt,
			&n.UpdatedByToken, &n.HearsayAtWrite)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(tags), &n.Tags)
	return &n, nil
}

// WriteStationNote appends to or replaces a page, creating a revision.
//
// ifRev > 0 is an optimistic-concurrency precondition: the write is refused if the page
// moved underneath it, naming the current revision. Two sessions may staff one station,
// and without a precondition the second writer silently destroys the first's page.
func (s *Store) WriteStationNote(ctx context.Context, lim StationNoteLimits, stationID, key, title, body string,
	tags []string, mode string, ifRev int, tokenID string, actorID int64, hearsay bool) (*StationNote, error) {

	key = strings.TrimSpace(key)
	if key == "" {
		return nil, errors.New("a notebook page needs a key")
	}
	if mode != "append" && mode != "replace" {
		return nil, fmt.Errorf("mode must be append or replace (got %q)", mode)
	}

	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var curBody string
	var curRev int
	var curTags sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT body, rev, tags FROM station_note WHERE station_id=? AND key=?`,
		stationID, key).Scan(&curBody, &curRev, &curTags)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if ifRev > 0 && exists && ifRev != curRev {
		return nil, fmt.Errorf("%w: you wrote against rev %d but the page is at rev %d — read it again before overwriting", ErrNoteRevConflict, ifRev, curRev)
	}

	newBody := body
	if mode == "append" && exists {
		if curBody != "" {
			newBody = curBody + "\n" + body
		}
	}
	if len(newBody) > lim.MaxPageBytes {
		return nil, fmt.Errorf("%w: page would be %d bytes, over the %d-byte cap — a page larger than this is a document, not a note",
			ErrNotebookCapReached, len(newBody), lim.MaxPageBytes)
	}

	// The notebook cap counts HEAD revisions only; history is bounded separately below.
	var otherBytes int
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(LENGTH(body)),0) FROM station_note WHERE station_id=? AND key<>?`,
		stationID, key).Scan(&otherBytes); err != nil {
		return nil, err
	}
	if otherBytes+len(newBody) > lim.MaxNotebookBytes {
		return nil, fmt.Errorf("%w: the notebook would be %d bytes, over the %d-byte cap — past this the routing rule (S10) is being ignored: durable lessons belong in the knowledge base",
			ErrNotebookCapReached, otherBytes+len(newBody), lim.MaxNotebookBytes)
	}

	tj, _ := json.Marshal(tags)
	if tags == nil {
		if exists && curTags.Valid {
			tj = []byte(curTags.String)
		} else {
			tj = []byte("[]")
		}
	}

	newRev := 1
	if exists {
		newRev = curRev + 1
		// Keep the OUTGOING body as history before overwriting.
		if _, err := tx.ExecContext(ctx, `
INSERT INTO station_note_revision(station_id, key, rev, body, updated_at, updated_by_token_id, updated_by_actor_id, hearsay_at_write)
SELECT station_id, key, rev, body, updated_at, updated_by_token_id, updated_by_actor_id, hearsay_at_write
FROM station_note WHERE station_id=? AND key=?`, stationID, key); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE station_note SET title=?, tags=?, body=?, rev=?,
       updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),
       updated_by_token_id=?, updated_by_actor_id=?, hearsay_at_write=?
WHERE station_id=? AND key=?`,
			title, string(tj), newBody, newRev, nullStr(tokenID), actorID, boolOrNil(hearsay),
			stationID, key); err != nil {
			return nil, err
		}
	} else {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO station_note(station_id, key, title, tags, body, rev, updated_by_token_id, updated_by_actor_id, hearsay_at_write)
VALUES(?,?,?,?,?,1,?,?,?)`,
			stationID, key, title, string(tj), newBody, nullStr(tokenID), actorID, boolOrNil(hearsay)); err != nil {
			return nil, err
		}
	}

	// Prune history oldest-first. This is S12's carve-out and the ONLY deletion here:
	// revision history is an undo buffer, not content.
	if err := pruneRevisions(ctx, tx, stationID, key, lim.MaxRevisionBytes); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	_ = s.TouchStationActivity(ctx, stationID)
	return s.ReadStationNote(ctx, stationID, key)
}

func pruneRevisions(ctx context.Context, tx *sql.Tx, stationID, key string, maxBytes int) error {
	for {
		var total int
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(SUM(LENGTH(body)),0) FROM station_note_revision WHERE station_id=? AND key=?`,
			stationID, key).Scan(&total); err != nil {
			return err
		}
		if total <= maxBytes {
			return nil
		}
		res, err := tx.ExecContext(ctx, `
DELETE FROM station_note_revision WHERE id = (
  SELECT id FROM station_note_revision WHERE station_id=? AND key=? ORDER BY rev ASC LIMIT 1)`,
			stationID, key)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil // nothing left to prune; the head alone is over the bound
		}
	}
}

// PromoteStationNote opens a PENDING PROMOTION for the human to convert. It writes no
// curated row, calls no kb_* tool, and requires no knowledge-base scope (S10).
func (s *Store) PromoteStationNote(ctx context.Context, stationID, key string) (string, error) {
	n, err := s.ReadStationNote(ctx, stationID, key)
	if err != nil {
		return "", err
	}
	id, err := randBase62(12)
	if err != nil {
		return "", err
	}
	if _, err := s.W.ExecContext(ctx, `
INSERT INTO station_promotion(promotion_id, station_id, note_key, note_rev, hearsay_at_write)
VALUES(?,?,?,?,?)`, id, stationID, key, n.Rev, boolOrNil(n.HearsayAtWrite)); err != nil {
		return "", err
	}
	return id, nil
}

// HandoffStaleness reports how stale the handoff page is, measured in STATION ACTIVITY
// rather than the wall clock (§4): an idle station is never stale, a busy one goes stale
// fast. Activity is counted from ken.db facts only — tasks touched and pages edited
// since the handoff was last written — never messages, which live in the expendable file
// and may be absent entirely.
func (s *Store) HandoffStaleness(ctx context.Context, stationID string) (writtenAt string, activitiesSince int, err error) {
	err = s.R.QueryRowContext(ctx, `SELECT updated_at FROM station_note WHERE station_id=? AND key=?`,
		stationID, HandoffKey).Scan(&writtenAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", -1, nil // no handoff at all: the caller says so rather than reporting 0
	}
	if err != nil {
		return "", 0, err
	}
	// Positional placeholders, repeated: named parameters are not reliably bound by the
	// driver here, and a silently-zero activity count would make the staleness nag inert.
	//
	// The comparison is >= rather than >: timestamps are millisecond-resolution, so an
	// activity landing in the same millisecond as the handoff write would otherwise be
	// dropped. Counting it errs toward reporting the handoff as STALER than it is, which
	// is the correct direction for a nag — over-report, never under-report. (A handoff's
	// own revision row carries the PREVIOUS updated_at, so it can never count itself.)
	err = s.R.QueryRowContext(ctx, `
SELECT (SELECT COUNT(*) FROM station_task WHERE station_id=?
         AND (created_at >= ? OR COALESCE(closed_at,'') >= ?))
     + (SELECT COUNT(*) FROM station_note_revision WHERE station_id=? AND updated_at >= ?)`,
		stationID, writtenAt, writtenAt, stationID, writtenAt).Scan(&activitiesSince)
	return writtenAt, activitiesSince, err
}
