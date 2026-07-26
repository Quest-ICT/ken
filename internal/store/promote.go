package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/Quest-ICT/ken/internal/lang"
)

// PromoteInput promotes a proposed version to the curated head (the curation gate).
type PromoteInput struct {
	Slug      string
	VersionID int64
	ActorID   int64
	ActorKind string
	Note      string
	// CurationLangs, when non-empty, enforces the comprehension gate: a version
	// whose detected content language is not one of these is refused with
	// ErrForeignLang. Empty ⇒ no language restriction (feature off). The web
	// handler fills it from the live settings snapshot.
	CurationLangs []string
}

// langAllowed reports whether a version's detected content language may be
// promoted under the given curation languages. It fails OPEN: with no curation
// languages configured (feature off), or an undetected/undetermined version
// (NULL or "und" — e.g. the entire pre-existing corpus), promotion is always
// allowed. The gate only ever blocks a version DETECTED to be in a language the
// curator did not declare they can read.
func langAllowed(contentLang string, curationLangs []string) bool {
	if len(curationLangs) == 0 {
		return true
	}
	contentLang = strings.ToLower(strings.TrimSpace(contentLang))
	if contentLang == "" || contentLang == lang.Und {
		return true
	}
	for _, l := range curationLangs {
		if contentLang == l {
			return true
		}
	}
	return false
}

// LangForeign reports whether a detected content language would be BLOCKED for
// promotion under the given curation languages — the inverse of the internal
// gate. The web layer uses it to flag out-of-language proposals on the review
// queue with the exact same rule the store enforces (no drift between the badge
// and the gate).
func LangForeign(contentLang string, curationLangs []string) bool {
	return !langAllowed(contentLang, curationLangs)
}

// foreignLangErr builds the actionable ErrForeignLang message for a blocked promote.
func foreignLangErr(contentLang string, curationLangs []string) error {
	return fmt.Errorf("%w: this proposal is in %q, but the curation language(s) are %s — re-author it in a curation language, or add %q in Settings → Curation if you can read it",
		ErrForeignLang, contentLang, strings.Join(curationLangs, ", "), contentLang)
}

// Promote is the only operation that moves the curated head. In one IMMEDIATE
// transaction, guarded by the proposal's state='proposed' check (see below), it
// supersedes the old head, marks the proposal curated, advances the head, resets
// staleness, and refreshes the denormalized ranking/browse surface. A duplicate
// or stale promote returns ErrBadVersion from that state check — reconcile, don't
// clobber. (lock_version is still bumped for auditing but is no longer a guard.)
func (s *Store) Promote(ctx context.Context, in PromoteInput) error {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var (
		entryID    int64
		curatedVID sql.NullInt64
	)
	err = tx.QueryRowContext(ctx, `SELECT id, curated_version_id FROM entry WHERE slug=?`, in.Slug).
		Scan(&entryID, &curatedVID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}

	// The concurrency guard is this state check, inside one IMMEDIATE tx on the
	// single writer: a version is promotable only while 'proposed', so a duplicate
	// or stale promote (the version is already curated/superseded) returns
	// ErrBadVersion. A lock_version CAS would add nothing here.
	var vEntry int64
	var vState string
	var vLang sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT entry_id, state, content_lang FROM entry_version WHERE id=?`, in.VersionID).Scan(&vEntry, &vState, &vLang)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrBadVersion
		}
		return err // don't mask a transient error (context cancel, etc.) as ErrBadVersion
	}
	if vEntry != entryID || vState != "proposed" {
		return ErrBadVersion
	}
	// Comprehension gate: never let unreadable content reach the head.
	if !langAllowed(vLang.String, in.CurationLangs) {
		return foreignLangErr(vLang.String, in.CurationLangs)
	}

	if curatedVID.Valid {
		if _, err := tx.ExecContext(ctx,
			`UPDATE entry_version SET state='superseded', superseded_by_version_id=? WHERE id=?`,
			in.VersionID, curatedVID.Int64); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE entry_version SET state='curated', reviewed_by_actor_id=?, reviewed_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`,
		nullActor(in.ActorID), in.VersionID); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `
UPDATE entry SET
  curated_version_id     = ?1,
  provisional_version_id = (SELECT id FROM entry_version
                            WHERE entry_id = ?3 AND state='proposed'
                            ORDER BY rev_no DESC LIMIT 1),
  curated_rev            = curated_rev + 1,
  staleness              = 'fresh',
  lifecycle              = 'active',
  title    = (SELECT title    FROM entry_version WHERE id=?1),
  summary  = (SELECT summary  FROM entry_version WHERE id=?1),
  tags     = (SELECT tags     FROM entry_version WHERE id=?1),
  triggers = (SELECT triggers FROM entry_version WHERE id=?1),
  lock_version = lock_version + 1,
  updated_at   = strftime('%Y-%m-%dT%H:%M:%fZ','now'),
  updater      = ?2
WHERE id = ?3`,
		in.VersionID, authorLabel(in.ActorKind), entryID); err != nil {
		return err
	}

	if err := insertEvent(ctx, tx, entryID, in.VersionID, "promoted", "proposed", "curated",
		in.ActorID, in.ActorKind, "", in.Note); err != nil {
		return err
	}
	return tx.Commit()
}

// Reject marks a proposed version rejected (retained + searchable as a dead-end).
func (s *Store) Reject(ctx context.Context, slug string, versionID, actorID int64, actorKind, note string) error {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var entryID int64
	var provVID sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT id, provisional_version_id FROM entry WHERE slug=?`, slug).Scan(&entryID, &provVID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}

	res, err := tx.ExecContext(ctx,
		`UPDATE entry_version SET state='rejected', reviewed_by_actor_id=?, reviewed_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		 WHERE id=? AND entry_id=? AND state='proposed'`,
		nullActor(actorID), versionID, entryID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrBadVersion
	}

	if provVID.Valid && provVID.Int64 == versionID {
		var next sql.NullInt64
		_ = tx.QueryRowContext(ctx,
			`SELECT id FROM entry_version WHERE entry_id=? AND state='proposed' ORDER BY rev_no DESC LIMIT 1`, entryID).Scan(&next)
		if _, err := tx.ExecContext(ctx,
			`UPDATE entry SET provisional_version_id=?, lock_version=lock_version+1, updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`,
			nullSQLInt(next), entryID); err != nil {
			return err
		}
	}

	if err := insertEvent(ctx, tx, entryID, versionID, "rejected", "proposed", "rejected", actorID, actorKind, "", note); err != nil {
		return err
	}
	return tx.Commit()
}

// Repromote sets an EXISTING historical version (superseded/rejected/withdrawn —
// anything but the current head or a still-'proposed' version) back as the curated
// head. It is the human recovery path when promotions were applied in the wrong
// order and regressed the head; unlike Promote it does not require state='proposed'
// (proposed versions go through the normal Promote review). One IMMEDIATE tx.
func (s *Store) Repromote(ctx context.Context, in PromoteInput) error {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var (
		entryID    int64
		curatedVID sql.NullInt64
	)
	err = tx.QueryRowContext(ctx, `SELECT id, curated_version_id FROM entry WHERE slug=?`, in.Slug).
		Scan(&entryID, &curatedVID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}

	var vEntry int64
	var vState string
	var vLang sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT entry_id, state, content_lang FROM entry_version WHERE id=?`, in.VersionID).Scan(&vEntry, &vState, &vLang)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrBadVersion
		}
		return err
	}
	// Guard: must belong to this entry, not already be the head, and not be a
	// pending proposal (those use Promote via the review queue).
	if vEntry != entryID || (curatedVID.Valid && curatedVID.Int64 == in.VersionID) || vState == "proposed" {
		return ErrBadVersion
	}
	// Comprehension gate applies to reverts too: don't restore an unreadable head.
	if !langAllowed(vLang.String, in.CurationLangs) {
		return foreignLangErr(vLang.String, in.CurationLangs)
	}

	if curatedVID.Valid {
		if _, err := tx.ExecContext(ctx,
			`UPDATE entry_version SET state='superseded', superseded_by_version_id=? WHERE id=?`,
			in.VersionID, curatedVID.Int64); err != nil {
			return err
		}
	}
	// Clear superseded_by_version_id too: the target may itself have been superseded
	// earlier, and a curated head must not carry a "superseded by" pointer (the
	// invariant Promote keeps for free, since it only ever promotes 'proposed' rows).
	if _, err := tx.ExecContext(ctx,
		`UPDATE entry_version SET state='curated', superseded_by_version_id=NULL, reviewed_by_actor_id=?, reviewed_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`,
		nullActor(in.ActorID), in.VersionID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE entry SET
  curated_version_id     = ?1,
  provisional_version_id = (SELECT id FROM entry_version
                            WHERE entry_id = ?3 AND state='proposed'
                            ORDER BY rev_no DESC LIMIT 1),
  curated_rev            = curated_rev + 1,
  staleness              = 'fresh',
  lifecycle              = 'active',
  title    = (SELECT title    FROM entry_version WHERE id=?1),
  summary  = (SELECT summary  FROM entry_version WHERE id=?1),
  tags     = (SELECT tags     FROM entry_version WHERE id=?1),
  triggers = (SELECT triggers FROM entry_version WHERE id=?1),
  lock_version = lock_version + 1,
  updated_at   = strftime('%Y-%m-%dT%H:%M:%fZ','now'),
  updater      = ?2
WHERE id = ?3`,
		in.VersionID, authorLabel(in.ActorKind), entryID); err != nil {
		return err
	}

	// event_type='promoted' (the CHECK enum has no 'reverted'); the note carries intent.
	if err := insertEvent(ctx, tx, entryID, in.VersionID, "promoted", vState, "curated",
		in.ActorID, in.ActorKind, "", in.Note); err != nil {
		return err
	}
	return tx.Commit()
}

// ProposalRow summarizes an entry that has pending proposals (the review queue).
type ProposalRow struct {
	Slug             string
	Title            string
	Kind             string
	NProposals       int
	LatestRev        int
	LatestVersionID  int64
	LatestConfidence float64
	LatestChangeNote string
	LatestLang       string // detected content language of the latest proposal ("" ⇒ undetected/legacy)
}

// ListProposals returns entries with at least one proposed version, newest first.
func (s *Store) ListProposals(ctx context.Context) ([]ProposalRow, error) {
	rows, err := s.R.QueryContext(ctx, `
SELECT e.slug, e.title, e.kind, COUNT(ev.id) AS n,
       lv.rev_no, lv.id, COALESCE(lv.confidence,0), COALESCE(lv.change_note,''), COALESCE(lv.content_lang,'')
FROM entry e
JOIN entry_version ev ON ev.entry_id=e.id AND ev.state='proposed'
JOIN entry_version lv ON lv.id=(SELECT id FROM entry_version WHERE entry_id=e.id AND state='proposed' ORDER BY rev_no DESC LIMIT 1)
GROUP BY e.id
ORDER BY MAX(ev.created_at) DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProposalRow
	for rows.Next() {
		var r ProposalRow
		if err := rows.Scan(&r.Slug, &r.Title, &r.Kind, &r.NProposals, &r.LatestRev, &r.LatestVersionID, &r.LatestConfidence, &r.LatestChangeNote, &r.LatestLang); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// VersionRow is a row in an entry's version history.
type VersionRow struct {
	VersionID  int64
	RevNo      int
	State      string
	ChangeNote string
	AuthorKind string
	Confidence float64
	CreatedAt  string
}

// History returns all versions of an entry, newest rev first.
func (s *Store) History(ctx context.Context, slug string) ([]VersionRow, error) {
	rows, err := s.R.QueryContext(ctx, `
SELECT ev.id, ev.rev_no, ev.state, COALESCE(ev.change_note,''), COALESCE(ev.author_kind,''), COALESCE(ev.confidence,0), ev.created_at
FROM entry_version ev JOIN entry e ON e.id=ev.entry_id
WHERE e.slug=? ORDER BY ev.rev_no DESC`, slug)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []VersionRow
	for rows.Next() {
		var r VersionRow
		if err := rows.Scan(&r.VersionID, &r.RevNo, &r.State, &r.ChangeNote, &r.AuthorKind, &r.Confidence, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ReviewData is the material for a proposal diff view: the proposal content
// alongside the current curated head content.
type ReviewData struct {
	Slug          string
	EntryTitle    string
	ProposalVID   int64
	ProposalRev   int
	ProposalState string
	ChangeNote    string
	Proposal      Content
	HasCurated    bool
	CuratedRev    int
	Curated       Content
}

// ProvisionalReview returns the review material for an entry's pending proposal
// (its provisional version), or (nil, nil) when the entry has none.
func (s *Store) ProvisionalReview(ctx context.Context, slug string) (*ReviewData, error) {
	var provVID sql.NullInt64
	err := s.R.QueryRowContext(ctx, `SELECT provisional_version_id FROM entry WHERE slug=?`, slug).Scan(&provVID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if !provVID.Valid {
		return nil, nil
	}
	return s.ProposalReview(ctx, provVID.Int64)
}

// ProposalReview loads a version and its entry's curated head for a diff view.
func (s *Store) ProposalReview(ctx context.Context, versionID int64) (*ReviewData, error) {
	var d ReviewData
	var curatedVID sql.NullInt64
	err := s.R.QueryRowContext(ctx, `
SELECT e.slug, e.title, e.curated_version_id, ev.rev_no, ev.state, COALESCE(ev.change_note,'')
FROM entry_version ev JOIN entry e ON e.id=ev.entry_id
WHERE ev.id=?`, versionID).Scan(&d.Slug, &d.EntryTitle, &curatedVID, &d.ProposalRev, &d.ProposalState, &d.ChangeNote)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	d.ProposalVID = versionID
	if d.Proposal, err = loadContent(ctx, s.R, versionID); err != nil {
		return nil, err
	}
	if curatedVID.Valid {
		d.HasCurated = true
		_ = s.R.QueryRowContext(ctx, `SELECT rev_no FROM entry_version WHERE id=?`, curatedVID.Int64).Scan(&d.CuratedRev)
		if d.Curated, err = loadContent(ctx, s.R, curatedVID.Int64); err != nil {
			return nil, err
		}
	}
	return &d, nil
}
