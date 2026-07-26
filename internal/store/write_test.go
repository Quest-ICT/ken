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
	if sr.State != "proposed" || sr.Lifecycle != "draft" || sr.RevNo != 1 {
		t.Fatalf("unexpected save result: %+v", sr)
	}

	// A draft is NOT in the default (curated) search, but IS in proposals scope.
	if res, _ := st.Search(ctx, "reciprocal rank fusion bm25", SearchOpts{}); len(res) != 0 {
		t.Fatalf("draft must not appear in curated search, got %d", len(res))
	}
	if res, _ := st.Search(ctx, "reciprocal rank fusion bm25", SearchOpts{Scope: "proposals"}); len(res) == 0 {
		t.Fatal("draft should appear in proposals-scope search")
	}

	// Promote -> curated head.
	if err := st.Promote(ctx, PromoteInput{Slug: sr.Slug, VersionID: sr.VersionID, ActorKind: "human", Note: "ok"}); err != nil {
		t.Fatalf("promote: %v", err)
	}
	if res, _ := st.Search(ctx, "reciprocal rank fusion bm25", SearchOpts{}); len(res) == 0 || res[0].Slug != sr.Slug {
		t.Fatalf("promoted entry not in curated search: %+v", res)
	}

	// Propose an enhancement; curated body must NOT change yet.
	newSol := "score = sum weight_m * 1/(60+rank_m)"
	pr, err := st.ProposeEnhancement(ctx, ProposeInput{
		Slug: sr.Slug, ChangeNote: "add per-arm weights",
		Patch: Patch{Solution: &newSol}, AuthorKind: "ai", Confidence: 0.9,
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if pr.RevNo != 2 || pr.State != "proposed" {
		t.Fatalf("unexpected propose result: %+v", pr)
	}
	props, _ := st.ListProposals(ctx)
	if len(props) != 1 || props[0].Slug != sr.Slug || props[0].LatestVersionID != pr.VersionID {
		t.Fatalf("unexpected proposals: %+v", props)
	}
	if e, _ := st.GetEntry(ctx, sr.Slug); e.Head == nil || e.Head.Solution != "score = sum 1/(60+rank_m)" {
		t.Fatalf("curated head changed before promotion: %+v", e.Head)
	}

	// Promote the enhancement -> head advances, provisional cleared.
	if err := st.Promote(ctx, PromoteInput{Slug: sr.Slug, VersionID: pr.VersionID, ActorKind: "human"}); err != nil {
		t.Fatalf("promote enhancement: %v", err)
	}
	e2, _ := st.GetEntry(ctx, sr.Slug)
	if e2.CuratedRev != 2 || e2.Head == nil || e2.Head.Solution != newSol {
		t.Fatalf("head did not advance: rev=%d head=%+v", e2.CuratedRev, e2.Head)
	}
	if e2.HasProvisional {
		t.Fatal("provisional should be cleared after promotion")
	}

	// Re-promoting a now-superseded version must fail.
	if err := st.Promote(ctx, PromoteInput{Slug: sr.Slug, VersionID: sr.VersionID, ActorKind: "human"}); err == nil {
		t.Fatal("promoting a superseded version should fail")
	}

	// FlagStale.
	if sstate, err := st.FlagStale(ctx, sr.Slug, "spring boot changed this", 0, "ai"); err != nil || sstate != "stale" {
		t.Fatalf("flag stale: %v %s", err, sstate)
	}
}
