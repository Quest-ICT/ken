package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Retiring and restoring an entry — the power the console gained when the curation gate left.
//
// *** THIS FILE EXISTS BECAUSE DELETING THE GATE WITHOUT IT WOULD HAVE BEEN A NET LOSS. ***
//
// Before 6.0.0, Reject was the ONLY control in the tree that could mark anything a dead end, and
// it could only reject a version that had never been served. `lifecycle` carried 'deprecated' and
// 'archived' in its CHECK constraint with NO WRITER ANYWHERE, and there is no `DELETE FROM entry`
// in the codebase. So the true state of affairs was: nobody — not the AI, not the human — could
// retire a bad entry once it was live. Removing the queue without adding this would have traded a
// delay barrier for a permanent inability to take anything back.
//
// Retiring is REVERSIBLE and never destructive. The entry keeps every version, keeps its history,
// and kb_get still answers for it — loudly, saying it is retired and why. Only discovery stops:
// browse and the default search skip it. That asymmetry is deliberate. A session that followed a
// link or holds a slug from an older conversation must be TOLD the knowledge was retired; being
// told "not found" would leave it to conclude the entry never existed and write it again.

// ErrBadLifecycle rejects a target state outside the two the console offers.
var ErrBadLifecycle = errors.New("lifecycle must be 'archived' or 'active'")

// ErrReasonRequired refuses a retirement with no stated reason.
//
// The reason is not ceremony. An entry disappears from every search the moment this runs, and the
// next session to wonder where it went has only the reflog to read. A retirement with no note is
// indistinguishable, later, from a mistake.
var ErrReasonRequired = errors.New("retiring an entry needs a reason — it stops appearing in every search, and the note is the only explanation the next session will find")

// SetLifecycle retires an entry or restores a retired one.
//
// `to` is 'archived' or 'active'. Archiving requires a non-empty note. Both emit a curation_event
// so the activity feed shows the act with its reason — the feed IS the oversight surface now, and
// a retirement that did not appear in it would be the quietest possible way to lose knowledge.
func (s *Store) SetLifecycle(ctx context.Context, slug, to, note string, actorID int64, actorKind string) error {
	to = strings.TrimSpace(to)
	if to != "archived" && to != "active" {
		return ErrBadLifecycle
	}
	note = strings.TrimSpace(note)
	if to == "archived" && note == "" {
		return ErrReasonRequired
	}

	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var entryID int64
	var from string
	err = tx.QueryRowContext(ctx, `SELECT id, lifecycle FROM entry WHERE slug=?`, slug).Scan(&entryID, &from)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	// Not an error, and deliberately so: a second click, or two sessions reaching the same
	// conclusion, must not produce a failure the caller has to interpret.
	if from == to {
		return nil
	}

	if _, err := tx.ExecContext(ctx, `
UPDATE entry SET lifecycle=?1, lock_version=lock_version+1,
       updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'), updater=?2
 WHERE id=?3`, to, authorLabel(actorKind), entryID); err != nil {
		return err
	}

	event := "restored"
	if to == "archived" {
		event = "archived"
	}
	// The event carries the entry's CURRENT head so the feed can link to what was retired. A
	// retired entry may legitimately have no head (every one of its versions was rejected before
	// 6.0.0), in which case the event carries none rather than inventing one.
	var head sql.NullInt64
	_ = tx.QueryRowContext(ctx, `SELECT curated_version_id FROM entry WHERE id=?`, entryID).Scan(&head)
	var vid int64
	if head.Valid {
		vid = head.Int64
	}
	if err := insertEvent(ctx, tx, entryID, vid, event, from, to, actorID, actorKind, "", note); err != nil {
		return err
	}
	return tx.Commit()
}

// RetiredReason returns the note from an entry's most recent retirement, for the refusal text a
// reader gets when it asks for a retired slug.
//
// Reads the REFLOG rather than a column on `entry`, because the reason belongs to the ACT and an
// entry can be retired, restored and retired again for different reasons. A column would carry
// only the latest and would silently outlive a restore.
func (s *Store) RetiredReason(ctx context.Context, slug string) (string, error) {
	var note string
	err := s.R.QueryRowContext(ctx, `
SELECT COALESCE(ce.note,'') FROM curation_event ce
  JOIN entry e ON e.id = ce.entry_id
 WHERE e.slug = ? AND ce.event_type = 'archived'
 ORDER BY ce.created_at DESC LIMIT 1`, slug).Scan(&note)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("retired reason for %q: %w", slug, err)
	}
	return note, nil
}
