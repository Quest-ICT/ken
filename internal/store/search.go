package store

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"github.com/Quest-ICT/ken/internal/model"
)

// SearchOpts filters a kb_search query.
type SearchOpts struct {
	Kind       string
	Category   string
	Scope      string // curated (default) | proposals | history | all
	K          int
	Offset     int
	QueryVec   []float32 // optional; when set, adds a semantic (vector) arm
	EmbedModel string    // model id of QueryVec; only vectors from this model are compared
}

// searchTemplate is the keyword+vector hybrid fused by Reciprocal Rank Fusion
// (k=60; prose 1.0, code 0.7, vector 1.0). %[1]s is the scope's state predicate
// (a trusted CONSTANT, applied inside the candidate CTEs before the 200-row cap).
// %[2]s is the vector CTE, built in Go from cosine-ranked candidates (VALUES) or
// an empty relation when embeddings are off — so the query shape never changes.
const searchTemplate = `
WITH prose AS (
  SELECT entry_fts.rowid AS version_id,
         row_number() OVER (ORDER BY bm25(entry_fts,10,8,8,5,3,2,1,1)) AS rank
  FROM entry_fts JOIN entry_version ev ON ev.id = entry_fts.rowid
  WHERE entry_fts MATCH ? AND %[1]s
  ORDER BY bm25(entry_fts,10,8,8,5,3,2,1,1)
  LIMIT 200
),
code AS (
  SELECT entry_code_fts.rowid AS version_id,
         row_number() OVER (ORDER BY bm25(entry_code_fts)) AS rank
  FROM entry_code_fts JOIN entry_version ev ON ev.id = entry_code_fts.rowid
  WHERE entry_code_fts MATCH ? AND %[1]s
  ORDER BY bm25(entry_code_fts)
  LIMIT 200
),
vec AS (%[2]s),
fused AS (
  SELECT version_id, SUM(w) AS score FROM (
    SELECT version_id, 1.0/(60+rank)*1.0 AS w FROM prose
    UNION ALL
    SELECT version_id, 1.0/(60+rank)*0.7 AS w FROM code
    UNION ALL
    SELECT version_id, 1.0/(60+rank)*1.0 AS w FROM vec
  ) GROUP BY version_id
)
SELECT e.slug, ev.title, ev.summary, e.kind, COALESCE(e.category,''), e.staleness,
       e.curated_rev, e.use_count, f.score,
       (e.provisional_version_id IS NOT NULL) AS has_provisional,
       COALESCE(ev.content_lang,'')
FROM fused f
JOIN entry_version ev ON ev.id = f.version_id
JOIN entry e          ON e.id  = ev.entry_id
WHERE e.lifecycle != 'archived'
  AND (? = '' OR e.kind = ?)
  AND (? = '' OR e.category = ?)
ORDER BY f.score DESC
LIMIT ? OFFSET ?;`

// Search runs the hybrid keyword+vector search and returns the clamped page.
func (s *Store) Search(ctx context.Context, query string, opt SearchOpts) ([]model.SearchResult, error) {
	res, _, err := s.SearchPage(ctx, query, opt)
	return res, err
}

// SearchPage is Search plus an accurate has-more flag. It over-fetches one row
// (K+1) so an exact-K final page isn't reported as "more" (a false positive).
func (s *Store) SearchPage(ctx context.Context, query string, opt SearchOpts) ([]model.SearchResult, bool, error) {
	k := opt.K
	if k <= 0 || k > 25 {
		k = 12
	}
	offset := opt.Offset
	if offset < 0 {
		offset = 0
	}
	statePred := scopeStatePredicate(opt.Scope)

	ftsQ := ftsQuery(query)
	hasFTS := ftsQ != ""
	if !hasFTS {
		ftsQ = `"zzznomatchzzz"` // valid MATCH that matches nothing (vector-only search)
	}

	var vecPairs []vecPair
	if len(opt.QueryVec) > 0 {
		vp, err := s.vectorCandidates(ctx, opt.QueryVec, opt.EmbedModel, statePred, 200)
		if err != nil {
			return nil, false, fmt.Errorf("vector arm: %w", err)
		}
		vecPairs = vp
	}
	if !hasFTS && len(vecPairs) == 0 {
		return nil, false, nil
	}

	vecCTE, vecArgs := buildVecCTE(vecPairs)
	sqlText := fmt.Sprintf(searchTemplate, statePred, vecCTE)

	args := make([]any, 0, 8+len(vecArgs))
	args = append(args, ftsQ, ftsQ)
	args = append(args, vecArgs...)
	args = append(args, opt.Kind, opt.Kind, opt.Category, opt.Category, k+1, offset)

	rows, err := s.R.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, false, fmt.Errorf("search query: %w", err)
	}
	defer rows.Close()

	var out []model.SearchResult
	for rows.Next() {
		var (
			r          model.SearchResult
			curatedRev int
			useCount   int
		)
		if err := rows.Scan(&r.Slug, &r.Title, &r.Summary, &r.Kind, &r.Category, &r.Staleness,
			&curatedRev, &useCount, &r.Score, &r.HasProvisional, &r.Language); err != nil {
			return nil, false, err
		}
		r.Maturity = maturity(useCount, curatedRev)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hasMore := len(out) > k
	if hasMore {
		out = out[:k]
	}
	return out, hasMore, nil
}

// buildVecCTE renders the vector candidates as an inline VALUES relation, or an
// empty relation when there are none.
func buildVecCTE(pairs []vecPair) (string, []any) {
	if len(pairs) == 0 {
		return "SELECT NULL AS version_id, NULL AS rank WHERE 0", nil
	}
	var b strings.Builder
	b.WriteString("SELECT column1 AS version_id, column2 AS rank FROM (VALUES ")
	args := make([]any, 0, len(pairs)*2)
	for i, p := range pairs {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString("(?,?)")
		args = append(args, p.vid, p.rank)
	}
	b.WriteString(")")
	return b.String(), args
}

// scopeStatePredicate returns a constant (injection-safe) SQL fragment restricting
// entry_version.state for the given search scope. history surfaces retained
// dead-ends (superseded / rejected / withdrawn) — searchable by design. all drops
// the state filter entirely (every version in every state), so one entry may
// surface once per matching version — the deliberate "show me everything" view.
func scopeStatePredicate(scope string) string {
	switch scope {
	case "proposals":
		return "ev.state = 'proposed'"
	case "history":
		return "ev.state IN ('superseded','rejected','withdrawn')"
	case "all":
		return "1=1"
	default:
		return "ev.state = 'curated'"
	}
}

// ftsQuery turns free user text into a safe FTS5 MATCH expression: alnum tokens
// of >=3 chars (trigram-compatible), quoted as phrases, OR-joined for recall.
func ftsQuery(s string) string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	var terms []string
	for _, f := range fields {
		if len([]rune(f)) < 3 {
			continue
		}
		terms = append(terms, `"`+strings.ToLower(f)+`"`)
	}
	return strings.Join(terms, " OR ")
}

// maturity is a coarse signal derived from promotion count and fetch count.
func maturity(useCount, curatedRev int) string {
	switch {
	case curatedRev >= 3 && useCount >= 10:
		return "battle-tested"
	case curatedRev >= 1:
		return "curated"
	default:
		return "seed"
	}
}

// SearchDiag is what a search can say about ITSELF, so an empty or thin result stops
// being indistinguishable from an absent entry.
//
// ken-prod-ops searched twice for an entry, got nothing, and told their human it "never
// landed" — writing "the proposal was lost" into a task. It had been curated and indexed
// the whole time. Nothing in the result could have told them otherwise: a search that
// matched forty entries and showed ten, and a search that matched nothing at all, return
// the same shape.
//
// This does not improve ranking. It makes the ranking's effect VISIBLE, which is the
// difference between a session that retries with different words and one that concludes
// the knowledge does not exist.
type SearchDiag struct {
	// Matched is how many DISTINCT entries matched at least one search term, before
	// ranking and before the top-K cut. Zero means the words are absent from the corpus;
	// a number larger than the page means ranking chose, and a session that wants the
	// rest should ask differently or page.
	Matched int
	// DeadTerms are the words that matched NOTHING anywhere. This is the actionable
	// half: "generated matched nothing" turns a mystery into a next query, where a bare
	// empty list turns it into a conclusion.
	DeadTerms []string
	// Truncated says the page is a slice of a larger match set.
	Truncated bool
}

// diagTermCap bounds the per-term probes. A long natural-language query is exactly the
// case this feature is for, and it is also the case where unbounded probing would cost
// most — twelve indexed COUNT lookups is the trade.
const diagTermCap = 12

// Diagnose reports what the search matched, independently of what it returned.
//
// Deliberately a SEPARATE call rather than a field on the result: it costs extra queries,
// the console does not need it, and a diagnostic that slows every search is a diagnostic
// an operator turns off. kb_search asks for it because a session acting on an empty
// result is the case that goes wrong.
func (s *Store) Diagnose(ctx context.Context, query string, opt SearchOpts, returned int) (SearchDiag, error) {
	var d SearchDiag
	ftsQ := ftsQuery(query)
	if ftsQ == "" {
		return d, nil
	}
	statePred := scopeStatePredicate(opt.Scope)

	// DISTINCT ENTRIES, not versions: a session thinks in entries, and counting versions
	// would report three for one entry with three revisions and read as a bigger corpus
	// than exists.
	countSQL := `
SELECT COUNT(DISTINCT ev.entry_id)
  FROM entry_fts JOIN entry_version ev ON ev.id = entry_fts.rowid
  JOIN entry e ON e.id = ev.entry_id
 WHERE entry_fts MATCH ? AND ` + statePred + ` AND e.lifecycle != 'archived'`
	if err := s.R.QueryRowContext(ctx, countSQL, ftsQ).Scan(&d.Matched); err != nil {
		return d, err
	}
	d.Truncated = d.Matched > returned

	// Which individual words found nothing. Each probe is the same MATCH the real query
	// uses, one term at a time, so a term reported dead is dead by the same rule the
	// search applied rather than by a different tokenizer.
	seen := map[string]bool{}
	for _, term := range strings.Split(ftsQ, " OR ") {
		if len(seen) >= diagTermCap {
			break
		}
		if seen[term] {
			continue
		}
		seen[term] = true
		var n int
		if err := s.R.QueryRowContext(ctx, countSQL, term).Scan(&n); err != nil {
			return d, err
		}
		if n == 0 {
			d.DeadTerms = append(d.DeadTerms, strings.Trim(term, `"`))
		}
	}
	return d, nil
}
