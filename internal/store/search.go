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
       -- THE CURATION GATE: a human promoted something. Necessary, and no longer sufficient.
       (e.curated_version_id IS NOT NULL) AS curated,
       -- DEDUPED BY SESSION. Sessions are cheap to mint, so counting rows would let one
       -- enthusiastic session promote an entry alone; counting distinct sessions is the
       -- cheapest thing that cannot be inflated by repetition. Measured on real data: 8
       -- 'helped' from one session plus 3 from distinct ones is 11 naive and 4 deduped.
       (SELECT COUNT(DISTINCT o.session_id) FROM entry_outcome o
         WHERE o.entry_id = e.id AND o.outcome = 'helped'
           AND o.session_id IS NOT NULL AND o.session_id <> '') AS helped_sessions,
       -- A 'was-wrong' SINCE THE HEAD WAS WRITTEN blocks the top tier.
       --
       -- RE-ANCHORED IN 6.0.0, AND IT HAD TO BE. It used to key on the last 'promoted' event and
       -- COALESCE a miss to '' — so with promotion gone, every entry's anchor would fall to the
       -- empty string, every historical was-wrong would count as "since", and entries would be
       -- permanently stuck below the top tier with nothing failing. The head VERSION's own
       -- created_at is the honest anchor and always exists (no COALESCE): a report against
       -- content that has since been rewritten is answered by the rewrite. It is also
       -- revert-correct — setting the head back to an older version restores that version's
       -- older timestamp, so the reports it had drawn come back with it.
       -- *** AND THE staleness FLAG HOLDS IT EVEN WHEN THE TIMESTAMP DOES NOT. ***
       --
       -- The timestamp arm alone let ANY revision clear a standing was-wrong, including one that
       -- changed nothing a reader can see. A new version always carries a new created_at, so every
       -- prior report falls before it — a tags-only edit, or a one-word touch, and the refutation
       -- is gone. That is an agent erasing the only evidence against its own entry, and with the
       -- curation gate removed there is no longer a human in front of the write to notice.
       --
       -- entry.staleness is the durable half, and ProposeEnhancement deliberately does NOT clear it
       -- unless prose or code actually changed. ORing the two means a real rewrite still answers a
       -- report (which is the correct semantics — the content it was about is gone) while a
       -- cosmetic one cannot.
       (e.staleness IN ('stale','refuted')
        OR EXISTS (SELECT 1 FROM entry_outcome o
                    WHERE o.entry_id = e.id AND o.outcome = 'was-wrong'
                      AND o.created_at > (SELECT created_at FROM entry_version
                                           WHERE id = e.curated_version_id))) AS refuted_since,
       f.score,
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
			r              model.SearchResult
			curated        bool
			helpedSessions int
			refutedSince   bool
		)
		if err := rows.Scan(&r.Slug, &r.Title, &r.Summary, &r.Kind, &r.Category, &r.Staleness,
			&curated, &helpedSessions, &refutedSince, &r.Score, &r.HasProvisional, &r.Language); err != nil {
			return nil, false, err
		}
		r.Maturity = maturity(curated, helpedSessions, refutedSince)
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
		// KEPT AS AN ACCEPTED VALUE, ALIASED TO THE DEFAULT (6.0.0). Nothing is 'proposed' any
		// more, so the old predicate would match nothing, forever, and a scope that silently
		// returns empty reads as "no such knowledge" — the defect class this release exists to
		// end, shipped as its own migration path. A session whose frozen tool schema still offers
		// this scope gets the right answer instead of a convincing void.
		//
		// Returns the default predicate EXPLICITLY rather than falling through: Go's fallthrough
		// enters the NEXT case body in source order, which is 'history', so the tidy-looking
		// version of this silently served superseded content to anyone asking for proposals.
		return "ev.state = 'curated'"
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

// helpedSessionsForBattleTested is how many DISTINCT sessions must have reported 'helped'
// before an entry is called battle-tested. Small enough to be reachable, large enough that
// no single session can promote anything on its own.
const helpedSessionsForBattleTested = 3

// maturity is what every agent is told about how much an entry can be trusted.
//
// THE HUMAN GATE STAYS NECESSARY AND STOPS BEING SUFFICIENT: nothing reaches a tier above
// `seed` without a promotion, so the curation gate is intact — but a promotion alone no
// longer earns the top tier. That is deliberate, and the trade was accepted explicitly: an
// agent-written signal sizes the top tier.
//
// WHAT THIS REPLACES, AND WHY IT HAD TO GO. The old rule was `curated_rev >= 3 && useCount
// >= 10`, and `curated_rev` is a PROMOTION COUNT — `curated_rev = curated_rev + 1` appears
// at promote.go:131 and again inside Repromote at :265, which is the human recovery path for
// promotions applied in the wrong order. So REPAIRING A CURATION MISTAKE RAISED THE BADGE.
// Executed on a two-version entry: ten alternating reverts took curated_rev from 2 to 12,
// reaching "battle-tested" after four clicks of Revert. On the recovery path the signal was
// anti-correlated with quality — and no backfill can fix that, because the counter is exact
// and simply measures the wrong thing.
//
// `use_count` is gone from the badge too: it counts fetches, and being fetched often is
// popularity rather than evidence. An entry nobody could apply is fetched exactly as often
// as one that works.
//
// The evidence used instead was already being collected. entry_outcome has been written on
// every kb_record_outcome since migration 0004 and read by NOTHING — while the connect-time
// instruction told every session "this is how Ken self-curates — do not skip it". This
// function is that promise being kept.
func maturity(curated bool, helpedSessions int, refutedSince bool) string {
	if !curated {
		return "seed"
	}
	if helpedSessions >= helpedSessionsForBattleTested && !refutedSince {
		return "battle-tested"
	}
	return "curated"
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
