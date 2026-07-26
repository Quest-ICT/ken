package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// --- kb_diff ---

// FieldDiff is one field's before/after in a version diff.
type FieldDiff struct {
	Field   string
	Changed bool
	A, B    string
}

// DiffResult is the field-by-field diff of two revisions of an entry.
type DiffResult struct {
	Slug           string
	RevA, RevB     int
	StateA, StateB string
	Fields         []FieldDiff
}

// VersionDiff diffs two revisions of an entry, field by field.
func (s *Store) VersionDiff(ctx context.Context, slug string, revA, revB int) (*DiffResult, error) {
	ca, sa, err := s.contentByRev(ctx, slug, revA)
	if err != nil {
		return nil, err
	}
	cb, sb, err := s.contentByRev(ctx, slug, revB)
	if err != nil {
		return nil, err
	}
	return &DiffResult{
		Slug: slug, RevA: revA, RevB: revB, StateA: sa, StateB: sb,
		Fields: []FieldDiff{
			mkDiff("title", ca.Title, cb.Title),
			mkDiff("summary", ca.Summary, cb.Summary),
			mkDiff("problem", ca.Problem, cb.Problem),
			mkDiff("solution", ca.Solution, cb.Solution),
			mkDiff("rationale", ca.Rationale, cb.Rationale),
			mkDiff("caveats", ca.Caveats, cb.Caveats),
		},
	}, nil
}

func mkDiff(name, a, b string) FieldDiff { return FieldDiff{Field: name, Changed: a != b, A: a, B: b} }

func (s *Store) contentByRev(ctx context.Context, slug string, rev int) (Content, string, error) {
	var vid int64
	var state string
	err := s.R.QueryRowContext(ctx,
		`SELECT ev.id, ev.state FROM entry_version ev JOIN entry e ON e.id=ev.entry_id WHERE e.slug=? AND ev.rev_no=?`,
		slug, rev).Scan(&vid, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return Content{}, "", fmt.Errorf("%w: no rev %d for %q", ErrBadVersion, rev, slug)
	}
	if err != nil {
		return Content{}, "", err
	}
	c, err := loadContent(ctx, s.R, vid)
	return c, state, err
}

// --- kb_record_outcome ---

// RecordOutcome records an agent's outcome report for an entry. 'was-wrong' also
// flags the entry stale (for human review). Returns the entry's staleness after.
func (s *Store) RecordOutcome(ctx context.Context, slug, outcome string, actorID int64, actorKind, sessionID, note string) (staleness string, err error) {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	var entryID int64
	err = tx.QueryRowContext(ctx, `SELECT id, staleness FROM entry WHERE slug=?`, slug).Scan(&entryID, &staleness)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO entry_outcome(entry_id, outcome, actor_id, actor_kind, session_id, note) VALUES(?,?,?,?,?,?)`,
		entryID, outcome, nullActor(actorID), nullStr(actorKind), nullStr(sessionID), nullStr(note)); err != nil {
		return "", err
	}

	if outcome == "was-wrong" {
		if _, err := tx.ExecContext(ctx,
			`UPDATE entry SET staleness='stale', lock_version=lock_version+1, updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, entryID); err != nil {
			return "", err
		}
		if err := insertEvent(ctx, tx, entryID, 0, "flagged_stale", "", "stale", actorID, actorKind, sessionID, "reported was-wrong: "+note); err != nil {
			return "", err
		}
		staleness = "stale"
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return staleness, nil
}

// --- kb_recent_context ---

// RecentEntry is one row of the recent-activity briefing.
type RecentEntry struct {
	Slug, Title, Summary, Kind, LastEvent, LastAt string
}

// RecentContext returns entries with curation activity in the last sinceDays,
// newest first — a compact "what the KB learned recently" briefing.
func (s *Store) RecentContext(ctx context.Context, sinceDays, limit int, kind string) ([]RecentEntry, error) {
	if sinceDays <= 0 {
		sinceDays = 14
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	rows, err := s.R.QueryContext(ctx, `
SELECT e.slug, e.title, e.summary, e.kind,
       MAX(ce.created_at) AS last_at,
       (SELECT event_type FROM curation_event WHERE entry_id=e.id ORDER BY created_at DESC, id DESC LIMIT 1) AS last_event
FROM curation_event ce JOIN entry e ON e.id = ce.entry_id
WHERE ce.created_at >= strftime('%Y-%m-%dT%H:%M:%fZ','now', ?)
  AND (? = '' OR e.kind = ?)
GROUP BY e.id
ORDER BY last_at DESC
LIMIT ?`, fmt.Sprintf("-%d days", sinceDays), kind, kind, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RecentEntry
	for rows.Next() {
		var r RecentEntry
		if err := rows.Scan(&r.Slug, &r.Title, &r.Summary, &r.Kind, &r.LastAt, &r.LastEvent); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
