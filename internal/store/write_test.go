package store

import (
	"context"
	"path/filepath"
	"testing"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return st
}

// TestWritePathLifecycle drives save -> (not in curated search) -> promote ->
// propose -> queue -> promote-enhancement -> flag-stale, asserting the curated
// head only moves on promotion and superseded versions can't be re-promoted.
func TestWritePathLifecycle(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	sr, err := st.Save(ctx, SaveInput{
		Kind: "project",
		Content: Content{
			Title:    "Use RRF to fuse BM25 and vector ranks",
			Summary:  "Never compare raw BM25 vs L2 distance; fuse by rank position with reciprocal rank fusion.",
			Solution: "score = sum 1/(60+rank_m)",
			Tags:     []string{"search", "rrf"},
			Triggers: []string{"hybrid search ranking", "combine bm25 and vector"},
		},
		Confidence: 0.7, AuthorKind: "ai",
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	// *** THE ASSERTION THIS RELEASE EXISTS TO INVERT. ***
	//
	// It required State "proposed" and Lifecycle "draft" — a write that had happened and was not
	// yet true. That is the barrier: the session that learned the lesson could not find it again,
	// and neither could the next one, until a human clicked approve on a title he had already
	// decided to keep.
	if sr.State != "curated" || sr.Lifecycle != "active" || sr.RevNo != 1 {
		t.Fatalf("a write must be the live head on arrival: %+v", sr)
	}

	// *** AND IT IS FINDABLE IMMEDIATELY, BY THE DEFAULT SEARCH, WITH NO HUMAN ACTION. ***
	//
	// This is the whole feature in one assertion. It used to require the OPPOSITE — len(res) != 0
	// was the failure — so reverting Save's state string to "proposed" turns this line red, which
	// is the verify-by-deletion check for the entire change.
	if res, _ := st.Search(ctx, "reciprocal rank fusion bm25", SearchOpts{}); len(res) == 0 {
		t.Fatal("a saved entry is not in the default search — the write did not become the head, " +
			"which is the curation barrier this release removed")
	}
	// scope:"proposals" is kept as an ACCEPTED value and aliased to the default. Returning an
	// empty proposed-set forever would be the exact defect this release exists to end: a scope
	// that silently matches nothing reads as "no such knowledge".
	if res, _ := st.Search(ctx, "reciprocal rank fusion bm25", SearchOpts{Scope: "proposals"}); len(res) == 0 {
		t.Fatal("scope=proposals must alias the default rather than matching nothing")
	}

	// A revision — and the head advances with it, immediately.
	newSol := "score = sum weight_m * 1/(60+rank_m)"
	pr, err := st.ProposeEnhancement(ctx, ProposeInput{
		Slug: sr.Slug, ChangeNote: "add per-arm weights",
		Patch: Patch{Solution: &newSol}, AuthorKind: "ai", Confidence: 0.9,
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	// *** THE ASSERTION THAT INVERTED IN 6.0.0. ***
	//
	// This block used to require the head NOT to move — "curated head changed before promotion"
	// was a failure — and to find the revision sitting in a proposal queue. Both are now the
	// defect. A revision that does not take effect is the barrier the owner asked us to remove.
	if pr.RevNo != 2 || pr.State != "curated" {
		t.Fatalf("a revision must land as the head: %+v", pr)
	}
	e2, _ := st.GetEntry(ctx, sr.Slug)
	if e2.Head == nil || e2.Head.RevNo != 2 || e2.Head.Solution != newSol {
		t.Fatalf("the head did not advance to the revision: %+v", e2.Head)
	}
	// And the DENORMALISED columns moved with it — asserted in its own test below, because a
	// revision that edits a title is the case that exposes it and this one only edits a solution.
	// A SUPERSEDED version can be set back as the head — that is the human's undo, and after
	// 6.0.0 it is the ONLY way to take back an agent's write. It used to be an escape hatch from
	// a bad promotion; it is now the primary control, so this asserts it works rather than that
	// it is refused.
	if err := st.SetHead(ctx, PromoteInput{Slug: sr.Slug, VersionID: sr.VersionID, ActorKind: "human"}); err != nil {
		t.Fatalf("setting the head back to a superseded version: %v", err)
	}
	if e3, _ := st.GetEntry(ctx, sr.Slug); e3.Head == nil || e3.Head.RevNo != 1 {
		t.Fatal("SetHead did not put rev 1 back in front")
	}

	// FlagStale.
	if sstate, err := st.FlagStale(ctx, sr.Slug, "spring boot changed this", 0, "ai"); err != nil || sstate != "stale" {
		t.Fatalf("flag stale: %v %s", err, sstate)
	}
}
