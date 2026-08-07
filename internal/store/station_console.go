package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// The writes the operator console makes (docs/STATIONS.md §10).
//
// Everything here is HUMAN-ONLY by construction. None of it is reachable from
// /station/mcp, and that is the design rather than an oversight: approving a
// request, typing a station's name, transferring assets and archiving are the
// capabilities the curation gate withholds. A session asks; a person decides.
//
// The one shape worth stating up front, because it is the reason this file is
// separate from stations.go: a request and its consequence must land TOGETHER.
// Approving a station request creates a station AND resolves the request; if the
// second half were a separate call, a crash between them would leave a pending
// request whose station already exists, and the operator would approve it twice.

// ErrRequestNotPending is returned when a request has already been decided. It is
// distinct from "not found" on purpose: the console shows it as "someone already
// handled this" rather than as a broken link, which is the likely truth when two
// browser tabs are open on the same queue.
var ErrRequestNotPending = errors.New("this request is no longer pending — it was already decided")

// ErrTransferCollision reports asset names that exist on BOTH stations. It carries
// the names because the human cannot act on a bare refusal: they have to rename or
// drop something, and they need to know what.
type ErrTransferCollision struct {
	Class     string // "notes" | "locker"
	Colliding []string
}

func (e *ErrTransferCollision) Error() string {
	return fmt.Sprintf("%s transfer refused — %d name(s) exist on both stations: %s (rename or remove them on one side first)",
		e.Class, len(e.Colliding), strings.Join(e.Colliding, ", "))
}

// ApproveStationRequest turns a pending `station` request into a real station, with
// the name the HUMAN typed. The name_hint the agent supplied is not consulted here
// at all — the console may show it, but this function takes only what the operator
// entered (S3).
//
// Atomic: the station is created and the request resolved in one transaction, so the
// queue can never show a pending request whose station already exists.
func (s *Store) ApproveStationRequest(ctx context.Context, requestID, name string, actorID int64) (*Station, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("a station needs a name — the human types it, and there is no default")
	}
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var kind, purpose string
	var spaceID int64
	err = tx.QueryRowContext(ctx,
		`SELECT kind, space_id, COALESCE(purpose,'') FROM station_request WHERE request_id=? AND state='pending'`,
		requestID).Scan(&kind, &spaceID, &purpose)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrRequestNotPending
	}
	if err != nil {
		return nil, err
	}
	if kind != "station" {
		return nil, fmt.Errorf("request %s is a %s request — approve it with ApproveLinkRequest", requestID, kind)
	}

	stationID, err := randBase62(16)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO station(station_id, space_id, name, purpose, created_by_actor_id) VALUES(?,?,?,?,?)`,
		stationID, spaceID, name, purpose, actorID); err != nil {
		if isUniqueViolation(err) {
			return nil, ErrStationNameTaken
		}
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE station_request
   SET state='approved', decided_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'), decided_by_actor_id=?
 WHERE request_id=? AND state='pending'`, actorID, requestID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.StationByID(ctx, stationID)
}

// DenyStationRequest records a refusal. A reason is REQUIRED for the same purpose a
// task's resolution line is required: the next request from the same station arrives
// to a human who can see what was already said no to, instead of re-deciding blind.
//
// For a link request this also feeds the denial ledger, whose mute window is what
// stops a persistent session from re-asking until a tired human says yes.
func (s *Store) DenyStationRequest(ctx context.Context, requestID, reason string, actorID int64) error {
	if strings.TrimSpace(reason) == "" {
		return errors.New("a denial needs a reason — the next request from this station reaches a human who should not have to re-decide blind")
	}
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var kind string
	var spaceID int64
	var fromStation, toStation *string
	err = tx.QueryRowContext(ctx,
		`SELECT kind, space_id, from_station, to_station FROM station_request WHERE request_id=? AND state='pending'`,
		requestID).Scan(&kind, &spaceID, &fromStation, &toStation)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrRequestNotPending
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE station_request
   SET state='denied', decided_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),
       decided_by_actor_id=?, decision_reason=?
 WHERE request_id=? AND state='pending'`, actorID, reason, requestID); err != nil {
		return err
	}

	// A denied LINK escalates the mute window, so a session that keeps asking waits
	// longer each time. A denied station request does not: there is nothing to mute
	// against, and a session with no station has no other way to ask.
	if kind == "link" && fromStation != nil && toStation != nil {
		a, b := orderPair(*fromStation, *toStation)
		if _, err := tx.ExecContext(ctx, `
INSERT INTO station_link_denial(space_id, station_a, station_b, denial_count, muted_until, last_denied_at)
VALUES(?,?,?,1, strftime('%Y-%m-%dT%H:%M:%fZ','now','+1 hour'), strftime('%Y-%m-%dT%H:%M:%fZ','now'))
ON CONFLICT(station_a, station_b) DO UPDATE SET
  denial_count   = station_link_denial.denial_count + 1,
  last_denied_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'),
  muted_until    = strftime('%Y-%m-%dT%H:%M:%fZ','now',
                     CASE station_link_denial.denial_count
                       WHEN 1 THEN '+6 hours'
                       WHEN 2 THEN '+24 hours'
                       ELSE '+7 days'
                     END)`, spaceID, a, b); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// orderPair returns the two ids in the canonical order the CHECK (station_a <
// station_b) constraint requires. Storing the pair unordered would let A→B and B→A
// become two different rows, which would silently double every link and halve every
// mute window.
func orderPair(x, y string) (string, string) {
	if x <= y {
		return x, y
	}
	return y, x
}

// StationUsage is what the console shows against the configured caps.
type StationUsage struct {
	StationID   string
	Notes       int
	NoteBytes   int64
	OpenTasks   int
	TotalTasks  int
	LockerFiles int
	LockerBytes int64
	Keys        int // live keys: neither retired nor revoked
}

// StationAssetUsage counts what a station is holding. Reported per station rather
// than per space because the caps are per station, and a total across stations would
// hide the one that is actually full.
func (s *Store) StationAssetUsage(ctx context.Context, stationID string) (*StationUsage, error) {
	u := &StationUsage{StationID: stationID}
	err := s.R.QueryRowContext(ctx, `
SELECT
  (SELECT COUNT(*)                     FROM station_note   WHERE station_id=?),
  (SELECT COALESCE(SUM(LENGTH(body)),0) FROM station_note   WHERE station_id=?),
  (SELECT COUNT(*)                     FROM station_task   WHERE station_id=? AND state='open'),
  (SELECT COUNT(*)                     FROM station_task   WHERE station_id=?),
  (SELECT COUNT(*)                     FROM station_locker WHERE station_id=?),
  (SELECT COALESCE(SUM(size_bytes),0)  FROM station_locker WHERE station_id=?),
  (SELECT COUNT(*) FROM api_token WHERE station_id=? AND retired_at IS NULL AND revoked_at IS NULL)`,
		stationID, stationID, stationID, stationID, stationID, stationID, stationID).
		Scan(&u.Notes, &u.NoteBytes, &u.OpenTasks, &u.TotalTasks, &u.LockerFiles, &u.LockerBytes, &u.Keys)
	if err != nil {
		return nil, err
	}
	return u, nil
}

// TransferResult reports what an asset transfer actually moved.
type TransferResult struct {
	Notes  int
	Tasks  int
	Locker int
}

// TransferStationAssets moves assets from one station to another — the answer to "a
// session is gone and its work should not be", and to "this machine is being
// replaced".
//
// Three properties, each load-bearing:
//
//   - ATOMIC across every class in one transaction. A half-moved station is worse
//     than an unmoved one: the human would have to reconstruct which classes went.
//   - REFUSED ENTIRELY on any name collision, with the colliding names returned.
//     Merging silently would let a `handoff` page overwrite a `handoff` page — and
//     since every station is expected to keep one, that collision is the COMMON case,
//     not the edge. The human renames or drops, then retries.
//   - The MESSAGE QUEUE NEVER MOVES. It lives in comm.db, it is expendable by
//     design, and pointing it at a new station would drag an expendable pointer into
//     a durable move (S7).
//
// Tasks cannot collide — they are keyed by an opaque id, not a name — so they always
// move cleanly. Notes and locker blobs are keyed by human-chosen names and can.
func (s *Store) TransferStationAssets(ctx context.Context, fromID, toID string, notes, tasks, locker bool) (*TransferResult, error) {
	if fromID == toID {
		return nil, errors.New("source and destination are the same station")
	}
	if !notes && !tasks && !locker {
		return nil, errors.New("nothing selected to transfer")
	}
	for _, id := range []string{fromID, toID} {
		if _, err := s.StationByID(ctx, id); err != nil {
			return nil, fmt.Errorf("station %s: %w", id, err)
		}
	}

	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	// Collisions are checked BEFORE anything moves, and across every selected class,
	// so a refusal reports the whole problem at once. Reporting notes now and locker
	// on the retry would make the human fix it twice.
	if notes {
		names, err := collidingNames(ctx, tx, `
SELECT a.key FROM station_note a JOIN station_note b ON a.key=b.key
 WHERE a.station_id=? AND b.station_id=? ORDER BY a.key`, fromID, toID)
		if err != nil {
			return nil, err
		}
		if len(names) > 0 {
			return nil, &ErrTransferCollision{Class: "notebook", Colliding: names}
		}
	}
	if locker {
		names, err := collidingNames(ctx, tx, `
SELECT a.name FROM station_locker a JOIN station_locker b ON a.name=b.name
 WHERE a.station_id=? AND b.station_id=? ORDER BY a.name`, fromID, toID)
		if err != nil {
			return nil, err
		}
		if len(names) > 0 {
			return nil, &ErrTransferCollision{Class: "locker", Colliding: names}
		}
	}

	res := &TransferResult{}
	move := func(sql string) (int, error) {
		r, err := tx.ExecContext(ctx, sql, toID, fromID)
		if err != nil {
			return 0, err
		}
		n, _ := r.RowsAffected()
		return int(n), nil
	}
	if notes {
		// Revisions follow their page: the history is part of the asset, and a page
		// that arrives with no past is a worse handoff than no page at all.
		if res.Notes, err = move(`UPDATE station_note SET station_id=? WHERE station_id=?`); err != nil {
			return nil, err
		}
		if _, err = move(`UPDATE station_note_revision SET station_id=? WHERE station_id=?`); err != nil {
			return nil, err
		}
	}
	if tasks {
		if res.Tasks, err = move(`UPDATE station_task SET station_id=? WHERE station_id=?`); err != nil {
			return nil, err
		}
	}
	if locker {
		if res.Locker, err = move(`UPDATE station_locker SET station_id=? WHERE station_id=?`); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	_ = s.TouchStationActivity(ctx, toID)
	return res, nil
}

// collidingNames runs a pre-flight collision query inside the transfer transaction,
// so the answer cannot go stale between the check and the move.
func collidingNames(ctx context.Context, tx *sql.Tx, query, fromID, toID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, query, fromID, toID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// StationLink is an approved peer relationship. Materializing channels from one is
// slice 4; the console lists them now so approving a link is not a write into a void.
type StationLink struct {
	LinkID     string
	StationA   string
	StationB   string
	NameA      string
	NameB      string
	State      string // active | dormant | revoked
	ApprovedAt string
}

// ListStationLinks returns the space's links with both station names resolved, so the
// console never has to show an opaque id to a human.
func (s *Store) ListStationLinks(ctx context.Context, spaceID int64) ([]StationLink, error) {
	rows, err := s.R.QueryContext(ctx, `
SELECT l.link_id, l.station_a, l.station_b,
       COALESCE(a.name,'(deleted)'), COALESCE(b.name,'(deleted)'),
       l.state, l.approved_at
  FROM station_link l
  LEFT JOIN station a ON a.station_id = l.station_a
  LEFT JOIN station b ON b.station_id = l.station_b
 WHERE l.space_id=?
 ORDER BY l.approved_at DESC`, spaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StationLink
	for rows.Next() {
		var l StationLink
		if err := rows.Scan(&l.LinkID, &l.StationA, &l.StationB, &l.NameA, &l.NameB, &l.State, &l.ApprovedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// Link requests (docs/STATIONS.md S9).
//
// The pairing code authorizes one CONVERSATION; what the human actually decides is
// that two posts may talk. A link makes the durable object match the decision, which
// removes a step per conversation without removing the decision.

// CreateStationLinkRequest files a request for a peer relationship.
//
// Three properties, and each is load-bearing:
//
//   - THE REASON IS NEVER DELIVERED TO THE TARGET before approval. It is stored for
//     the human and shown only in the console. Without that rule, every request is a
//     one-shot unauthorized message channel: A cannot talk to B, but A could put a
//     paragraph in front of B merely by asking to.
//   - A MUTED PAIR IS SILENTLY DROPPED, and the caller receives the ordinary
//     "submitted, pending review" answer. Telling the caller it was muted would let a
//     persistent session PROBE the human's past decisions, one request at a time.
//     The mute is on the UNORDERED pair, because muting an ordered one would let the
//     same relationship be re-asked from the other side.
//   - hearsay MARKS THE TRANSITIVE PATH. A cannot create a channel, but A can talk B
//     into requesting one to C, and B's request then reaches the human looking like
//     B's own idea. Recording whether the requester was mid-conversation is the only
//     signal the human gets that the idea may not be B's.
//
// Returns the request id, or ("", nil) when it was silently dropped — the caller must
// report success either way.
func (s *Store) CreateStationLinkRequest(ctx context.Context, spaceID int64, tokenID, fromStation, toStation, reason string, hearsay bool) (string, error) {
	if fromStation == "" || toStation == "" {
		return "", errors.New("a link request needs both stations")
	}
	if fromStation == toStation {
		return "", errors.New("a station cannot link to itself")
	}
	if _, err := s.StationByID(ctx, toStation); err != nil {
		// Unknown target: refused, and deliberately with the SAME wording a caller
		// would get for a station that exists but is not published, so the tool
		// cannot be used to enumerate stations.
		return "", errors.New("no such station is available to link to — ask your human for the exact name")
	}

	a, b := orderPair(fromStation, toStation)
	var muted bool
	err := s.R.QueryRowContext(ctx, `
SELECT EXISTS(
  SELECT 1 FROM station_link_denial
   WHERE station_a=? AND station_b=?
     AND muted_until IS NOT NULL
     AND muted_until > strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, a, b).Scan(&muted)
	if err != nil {
		return "", err
	}
	if muted {
		// Silently dropped. The caller is told what every caller is told.
		return "", nil
	}

	// An existing pending request for the same pair is not duplicated: a session that
	// asks twice should not put two rows in front of the human to decide identically.
	var existing string
	err = s.R.QueryRowContext(ctx, `
SELECT request_id FROM station_request
 WHERE kind='link' AND state='pending'
   AND ((from_station=? AND to_station=?) OR (from_station=? AND to_station=?))`,
		fromStation, toStation, toStation, fromStation).Scan(&existing)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}

	id, err := randBase62(12)
	if err != nil {
		return "", err
	}
	if _, err := s.W.ExecContext(ctx, `
INSERT INTO station_request(request_id, space_id, kind, from_station, to_station,
                            from_token_id, reason, prompted_by_peer_traffic)
VALUES(?,?,'link',?,?,?,?,?)`,
		id, spaceID, fromStation, toStation, tokenID, reason, boolOrNilStore(hearsay)); err != nil {
		return "", err
	}
	return id, nil
}

// boolOrNilStore writes 1 or NULL rather than 1 or 0, so "not marked" and "marked
// clean" stay distinguishable. With COMM off there is no hearsay signal at all, and a
// column reading 0 would claim knowledge the server does not have.
func boolOrNilStore(b bool) any {
	if b {
		return 1
	}
	return nil
}

// ApproveLinkRequest turns a pending link request into an active link. Either side
// may then materialize a channel without a fresh pairing code.
func (s *Store) ApproveLinkRequest(ctx context.Context, requestID string, actorID int64) (*StationLink, error) {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var kind string
	var spaceID int64
	var from, to *string
	err = tx.QueryRowContext(ctx,
		`SELECT kind, space_id, from_station, to_station FROM station_request
		  WHERE request_id=? AND state='pending'`, requestID).Scan(&kind, &spaceID, &from, &to)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrRequestNotPending
	}
	if err != nil {
		return nil, err
	}
	if kind != "link" || from == nil || to == nil {
		return nil, fmt.Errorf("request %s is not a link request", requestID)
	}

	a, b := orderPair(*from, *to)
	linkID, err := randBase62(16)
	if err != nil {
		return nil, err
	}
	// An approval CLEARS any past denial for the pair: the human has changed their
	// mind, and leaving the mute in place would silently drop the next request for a
	// relationship they just allowed.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM station_link_denial WHERE station_a=? AND station_b=?`, a, b); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO station_link(link_id, space_id, station_a, station_b, approved_by_actor_id)
VALUES(?,?,?,?,?)
ON CONFLICT DO NOTHING`, linkID, spaceID, a, b, actorID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE station_request
   SET state='approved', decided_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'), decided_by_actor_id=?
 WHERE request_id=? AND state='pending'`, actorID, requestID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	var l StationLink
	err = s.R.QueryRowContext(ctx, `
SELECT l.link_id, l.station_a, l.station_b,
       COALESCE(sa.name,''), COALESCE(sb.name,''), l.state, l.approved_at
  FROM station_link l
  LEFT JOIN station sa ON sa.station_id=l.station_a
  LEFT JOIN station sb ON sb.station_id=l.station_b
 WHERE l.station_a=? AND l.station_b=?`, a, b).
		Scan(&l.LinkID, &l.StationA, &l.StationB, &l.NameA, &l.NameB, &l.State, &l.ApprovedAt)
	return &l, err
}

// AreStationsLinked reports whether an ACTIVE link joins two stations. This is the
// authorization check for materializing a channel without a pairing code: a dormant
// link (either station archived) does not authorize anything until it is restored.
func (s *Store) AreStationsLinked(ctx context.Context, x, y string) (bool, error) {
	a, b := orderPair(x, y)
	var ok bool
	err := s.R.QueryRowContext(ctx, `
SELECT EXISTS(SELECT 1 FROM station_link
               WHERE station_a=? AND station_b=? AND state='active')`, a, b).Scan(&ok)
	return ok, err
}

// RevokeStationLink ends a relationship. S9's trade-off is that approval widens from
// per-conversation to per-relationship, and this is the other half of that bargain:
// revocation is one click. Killing the live channel is the CALLER's job, because the
// channel lives in the expendable database this package must not reach into.
func (s *Store) RevokeStationLink(ctx context.Context, linkID string) error {
	res, err := s.W.ExecContext(ctx, `
UPDATE station_link SET state='revoked', revoked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
 WHERE link_id=? AND state<>'revoked'`, linkID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// StationLinkByID resolves one link with both station names, so the caller can name
// the pair in a confirmation and hand the two station ids to COMM.
//
// Separate from RevokeStationLink on purpose: the revoke is a write and this is the
// read that must happen BEFORE it — once the row says 'revoked' the console still
// needs to say whose relationship just ended.
func (s *Store) StationLinkByID(ctx context.Context, linkID string) (StationLink, error) {
	var l StationLink
	err := s.R.QueryRowContext(ctx, `
SELECT l.link_id, l.station_a, l.station_b,
       COALESCE(a.name,'(deleted)'), COALESCE(b.name,'(deleted)'),
       l.state, l.approved_at
  FROM station_link l
  LEFT JOIN station a ON a.station_id = l.station_a
  LEFT JOIN station b ON b.station_id = l.station_b
 WHERE l.link_id=?`, linkID).
		Scan(&l.LinkID, &l.StationA, &l.StationB, &l.NameA, &l.NameB, &l.State, &l.ApprovedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return StationLink{}, ErrNotFound
	}
	return l, err
}
