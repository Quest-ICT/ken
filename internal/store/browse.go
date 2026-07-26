package store

import (
	"context"
	"fmt"
	"strings"
)

// BrowseFilter parameterizes ListEntries. Every field is optional; the zero
// value lists all non-archived entries, newest-updated first.
type BrowseFilter struct {
	Category  string // exact match; "" = any
	Kind      string // user|feedback|project|reference; "" = any
	Staleness string // fresh|aging|stale|refuted; "" = any
	Lifecycle string // draft|active|deprecated; "" = any non-archived
	Sort      string // updated (default) | title | used | created | kind
	Limit     int    // default 50, max 200
	Offset    int
}

// BrowseRow is one entry as shown in the browse listing. Every field is read
// straight from the denormalized entry row (the curated head's title/summary/
// category are kept in sync on promotion), so browsing never joins entry_version.
type BrowseRow struct {
	Slug           string
	Title          string
	Summary        string
	Kind           string
	Category       string
	Staleness      string
	Lifecycle      string
	CuratedRev     int
	UseCount       int
	HasProvisional bool
	UpdatedAt      string
}

// browseSortSQL maps the (untrusted) sort key to a trusted ORDER BY fragment.
// Never interpolate the caller's value directly — SQLite has no bound-parameter
// form for ORDER BY, so the mapping is the injection guard.
func browseSortSQL(sort string) string {
	switch sort {
	case "title":
		return "title COLLATE NOCASE ASC, updated_at DESC"
	case "used":
		return "use_count DESC, updated_at DESC"
	case "created":
		return "created_at DESC"
	case "kind":
		return "kind ASC, updated_at DESC"
	default: // "updated"
		return "updated_at DESC"
	}
}

// ListEntries returns a filtered, sorted, paginated page of entries plus a
// has-more flag (it over-fetches one row so an exact-limit final page is not
// reported as "more" — the same technique as SearchPage). Archived entries are
// always excluded; a blank Lifecycle filter still shows draft/active/deprecated.
func (s *Store) ListEntries(ctx context.Context, f BrowseFilter) ([]BrowseRow, bool, error) {
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}

	q := `
SELECT slug, title, summary, kind, COALESCE(category,''), staleness, lifecycle,
       curated_rev, use_count, (provisional_version_id IS NOT NULL), updated_at
FROM entry
WHERE lifecycle != 'archived'
  AND (? = '' OR category  = ?)
  AND (? = '' OR kind      = ?)
  AND (? = '' OR staleness = ?)
  AND (? = '' OR lifecycle = ?)
ORDER BY ` + browseSortSQL(f.Sort) + `
LIMIT ? OFFSET ?`

	rows, err := s.R.QueryContext(ctx, q,
		f.Category, f.Category, f.Kind, f.Kind, f.Staleness, f.Staleness, f.Lifecycle, f.Lifecycle,
		limit+1, offset)
	if err != nil {
		return nil, false, fmt.Errorf("browse query: %w", err)
	}
	defer rows.Close()

	var out []BrowseRow
	for rows.Next() {
		var r BrowseRow
		if err := rows.Scan(&r.Slug, &r.Title, &r.Summary, &r.Kind, &r.Category, &r.Staleness,
			&r.Lifecycle, &r.CuratedRev, &r.UseCount, &r.HasProvisional, &r.UpdatedAt); err != nil {
			return nil, false, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	return out, hasMore, nil
}

// DistinctCategories returns the non-empty categories present on non-archived
// entries, alphabetically — the option list for the browse category filter.
func (s *Store) DistinctCategories(ctx context.Context) ([]string, error) {
	rows, err := s.R.QueryContext(ctx, `
SELECT DISTINCT category FROM entry
WHERE lifecycle != 'archived' AND category IS NOT NULL AND TRIM(category) != ''
ORDER BY category COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		if c = strings.TrimSpace(c); c != "" {
			out = append(out, c)
		}
	}
	return out, rows.Err()
}
