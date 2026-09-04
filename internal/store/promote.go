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

// *** Promote AND Reject ARE DELETED IN 6.0.0, AND THE GATE GOES WITH THEM. ***
//
// Promote was "the only operation that moves the curated head" and its guard — a version must be
// in state 'proposed' — WAS D4's curation gate, in one line. Reject was its veto.
//
// They are not deleted because review is worthless. They are deleted because THIS review was not
// happening, and the delay was real. The owner's account after using Ken since v1: he opened the
// entry, often read only the title, and approved. docs/STATIONS.md:456-457 had already recorded
// where that ends — "a human who reflexively approves has converted the gate into a rubber stamp.
// No server-side design fixes that, and this document will not pretend otherwise."
//
// A detail that settles it: ListProposals selected the entry's CURATED title while Promote wrote
// the PROPOSED one. The title a rubber-stamping human glanced at was not the title he approved.
//
// What replaces them sits BELOW the write, not in front of it: SetHead moves the head to any
// historical version (the undo), and SetLifecycle retires an entry (the delete Ken never had).
// Neither can block a write; both are one click.
//
// Repromote is renamed SetHead: there is no "promotion" left for it to be a repeat of.

// SetHead sets an EXISTING historical version (superseded/rejected/withdrawn —
// anything but the current head or a still-'proposed' version) back as the curated
// head. It is the human recovery path when promotions were applied in the wrong
// order and regressed the head; unlike Promote it does not require state='proposed'
// (proposed versions go through the normal Promote review). One IMMEDIATE tx.
func (s *Store) SetHead(ctx context.Context, in PromoteInput) error {
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

// ProposalRow, CountProposals and ListProposals ARE DELETED — they read a queue that no longer
// fills. Their replacement is the ACTIVITY feed (ListActivity in v1tools.go), which answers
// "what happened" after the fact instead of "what is waiting for you" in front of it.

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
