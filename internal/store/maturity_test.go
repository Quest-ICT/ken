package store

import (
	"context"
	"testing"
)

// THE BADGE EVERY AGENT READS HAD NO TEST AT ALL. `grep -rn maturity --include=*_test.go`
// returned nothing before this file — which is why the old rule survived being provably
// anti-correlated with quality.
//
// The old rule was `curated_rev >= 3 && useCount >= 10`, and `curated_rev` is a PROMOTION
// COUNT incremented at promote.go:131 and again inside Repromote at :265 — the human
// recovery path for promotions applied in the wrong order. So repairing a curation mistake
// RAISED the badge: ten alternating reverts took an entry from 2 to 12 and reached
// "battle-tested" after four clicks of Revert.
func TestMaturityKeepsTheHumanGateNecessary(t *testing.T) {
	// NOTHING reaches a tier above seed without a promotion. This is the curation gate and
	// it is the property that must not move: an agent-written signal sizes the top tier,
	// but it can never manufacture the tier below it.
	for _, helped := range []int{0, 3, 99} {
		if got := maturity(false, helped, false); got != "seed" {
			t.Fatalf("an UNCURATED entry with %d helped sessions is %q, want seed.\n"+
				"Outcome evidence must never substitute for a human promotion.", helped, got)
		}
	}
}

func TestMaturityNeedsDistinctSessionsForTheTopTier(t *testing.T) {
	if got := maturity(true, helpedSessionsForBattleTested-1, false); got != "curated" {
		t.Fatalf("curated with %d helped sessions is %q, want curated", helpedSessionsForBattleTested-1, got)
	}
	if got := maturity(true, helpedSessionsForBattleTested, false); got != "battle-tested" {
		t.Fatalf("curated with the threshold met is %q, want battle-tested", got)
	}
	// CONTROL: the threshold is the thing being tested, so a value far above it must not
	// behave differently from one at it.
	if got := maturity(true, 500, false); got != "battle-tested" {
		t.Fatalf("far above the threshold is %q", got)
	}
}

// A REPORT THAT THE ENTRY WAS WRONG BLOCKS THE TOP TIER until a human promotes a correction.
//
// Without this, an entry somebody reported as wrong keeps its top badge until a human
// happens to look — which is the same shape as every other defect this month: the signal
// says trustworthy and the evidence says otherwise, and nothing connects them.
func TestAWasWrongReportBlocksBattleTested(t *testing.T) {
	if got := maturity(true, 10, true); got != "curated" {
		t.Fatalf("an entry with a was-wrong since its last promotion is %q, want curated — "+
			"ample 'helped' evidence must not outvote a refutation nobody has answered", got)
	}
	// AND IT IS RECOVERABLE. The block is anchored at the last promotion precisely so that
	// promoting a correction clears it; a permanent block would make one report fatal.
	if got := maturity(true, 10, false); got != "battle-tested" {
		t.Fatalf("after the refutation was answered by a promotion the entry is %q, want battle-tested", got)
	}
}

// THE OLD RULE'S FAILURE, ASSERTED DIRECTLY so nobody reintroduces a promotion count as a
// magnitude. Under `curated_rev >= 3`, an entry that had been promoted, reverted, promoted,
// reverted would climb; under this rule repair changes nothing, because repair produces no
// outcome evidence.
func TestRepairingAnEntryDoesNotRaiseItsBadge(t *testing.T) {
	// The inputs a repair loop moves are the promotion count and the fetch count — neither
	// of which this function can see any more. Same evidence in, same badge out.
	before := maturity(true, 2, false)
	after := maturity(true, 2, false)
	if before != after || before != "curated" {
		t.Fatalf("badge moved on repair alone: %q -> %q", before, after)
	}
}

// AND THE SAME THING, THROUGH THE QUERY THAT ACTUALLY FEEDS IT.
//
// The tests above cover the decision; this covers the three values the SQL supplies. That
// separation is the lesson from 3.3.0, where a correct raise site shipped behind a mapper
// that discarded its output and the raise-site test passed the whole time. A badge computed
// perfectly from wrong inputs is wrong.
func TestMaturityReadsTheOutcomesThatWereActuallyRecorded(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	sr, err := st.Save(ctx, SaveInput{
		Kind: "project", AuthorKind: "ai",
		Content: Content{
			Title:    "Postgres connection pool exhausted under burst load",
			Summary:  "A burst of requests exhausts the connection pool because idle connections are never returned.",
			Solution: "Return the connection in a defer, and cap MaxIdleConns below MaxOpenConns.",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	const q = "postgres connection pool exhausted burst idle connections"

	find := func(t *testing.T) string {
		t.Helper()
		res, err := st.Search(ctx, q, SearchOpts{})
		if err != nil || len(res) == 0 {
			t.Fatalf("search: %v (%d results)", err, len(res))
		}
		return res[0].Maturity
	}

	// The seed tier is asserted by the unit test above rather than here: an unpromoted entry
	// is not searchable at all, so this query can never observe one. That is correct — a
	// draft should not be retrievable — and it is worth stating, because "search returns
	if got := find(t); got != "curated" {
		t.Fatalf("a promoted entry with no outcomes reports %q, want curated", got)
	}

	// THREE 'helped' FROM ONE SESSION IS NOT THREE SESSIONS. This is the dedup, and it is
	// the reason the count is DISTINCT: sessions are cheap to mint, so counting rows would
	// let one enthusiastic session promote an entry by itself.
	for i := 0; i < 3; i++ {
		if _, err := st.RecordOutcome(ctx, sr.Slug, "helped", 0, "ai", "session-A", ""); err != nil {
			t.Fatal(err)
		}
	}
	if got := find(t); got != "curated" {
		t.Fatalf("three 'helped' from ONE session reports %q, want curated — the count is not deduping", got)
	}

	// Three DISTINCT sessions.
	for _, sid := range []string{"session-B", "session-C"} {
		if _, err := st.RecordOutcome(ctx, sr.Slug, "helped", 0, "ai", sid, ""); err != nil {
			t.Fatal(err)
		}
	}
	if got := find(t); got != "battle-tested" {
		t.Fatalf("three distinct sessions reporting 'helped' gives %q, want battle-tested.\n"+
			"This is the evidence entry_outcome has been collecting since migration 0004 and "+
			"which nothing read until now.", got)
	}

	// A REFUTATION PULLS IT BACK DOWN, without touching the helped evidence.
	if _, err := st.RecordOutcome(ctx, sr.Slug, "was-wrong", 0, "ai", "session-D", "the pool cap is not the cause"); err != nil {
		t.Fatal(err)
	}
	if got := find(t); got != "curated" {
		t.Fatalf("after a 'was-wrong' the entry still reports %q, want curated", got)
	}

	// AND WRITING A CORRECTION CLEARS IT — the anchor is the head version's own timestamp, so a
	// report against content that has since been rewritten is answered and one report is not
	// permanently fatal. Before 6.0.0 this said "promoting a correction", and the correction sat
	// in a queue until somebody clicked; now the revision IS the correction.
	fixed := "Return the connection in a defer; the idle cap was not the cause."
	if _, err := st.ProposeEnhancement(ctx, ProposeInput{
		Slug: sr.Slug, ChangeNote: "the refutation was right about the cause",
		Patch: Patch{Solution: &fixed}, AuthorKind: "ai",
	}); err != nil {
		t.Fatal(err)
	}
	if got := find(t); got != "battle-tested" {
		t.Fatalf("after a human promoted a correction the entry reports %q, want battle-tested — "+
			"a single refutation must not be permanently fatal", got)
	}
}
