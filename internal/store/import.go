package store

import (
	"context"
	"database/sql"
	"errors"
)

// ImportInput migrates a flat-memory file into Ken as a curated rev-1 entry.
type ImportInput struct {
	Slug       string
	Kind       string
	Content    Content
	ChangeNote string
	Links      []LinkInput
}

// ImportEntry inserts an imported entry directly as curated (lifecycle 'active',
// one 'curated' version rev 1, author_kind 'import') — imported memories are
// already curated knowledge, so they bypass the proposal queue. Idempotent:
// returns created=false without changes if the slug already exists.
func (s *Store) ImportEntry(ctx context.Context, in ImportInput) (created bool, err error) {
	if in.Content.Title == "" || in.Kind == "" {
		return false, errors.New("kind and title are required")
	}
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	var one int
	e := tx.QueryRowContext(ctx, `SELECT 1 FROM entry WHERE slug=?`, in.Slug).Scan(&one)
	if e == nil {
		return false, nil // already present — skip, don't clobber curated work
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return false, e
	}

	res, err := tx.ExecContext(ctx, `
INSERT INTO entry(slug,kind,title,summary,category,tags,triggers,lifecycle,staleness,updater)
VALUES(?,?,?,?,?,?,?, 'active','fresh','import')`,
		in.Slug, in.Kind, in.Content.Title, in.Content.Summary, nil,
		jsonArr(in.Content.Tags), jsonArr(in.Content.Triggers))
	if err != nil {
		return false, err
	}
	entryID, _ := res.LastInsertId()

	vid, _, err := insertVersion(ctx, tx, entryID, 1, "curated", 0, in.Content, 0, "import", "", 1.0, in.ChangeNote,
		s.detectLang(in.Content.prose()...), false) // an import is first-hand by definition
	if err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE entry SET curated_version_id=?, curated_rev=1, lock_version=lock_version+1 WHERE id=?`, vid, entryID); err != nil {
		return false, err
	}
	for _, l := range in.Links {
		if l.ToSlug == "" || l.LinkType == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO entry_link(from_entry_id,to_slug,to_entry_id,link_type)
VALUES(?,?,(SELECT id FROM entry WHERE slug=?),?)`, entryID, l.ToSlug, l.ToSlug, l.LinkType); err != nil {
			return false, err
		}
	}
	if err := insertEvent(ctx, tx, entryID, vid, "promoted", "", "curated", 0, "import", "", in.ChangeNote); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}
