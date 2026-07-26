package store

import (
	"context"
	"testing"

	"github.com/Quest-ICT/ken/internal/model"
)

// TestCreateFirstAdminAtomic (review-2 #1): the first-run admin insert is atomic —
// a second attempt (even a different name) creates no further human admin.
func TestCreateFirstAdminAtomic(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	if created, err := st.CreateFirstAdmin(ctx, "admin", "hash1"); err != nil || !created {
		t.Fatalf("first admin should be created: created=%v err=%v", created, err)
	}
	if created, err := st.CreateFirstAdmin(ctx, "other", "hash2"); err != nil || created {
		t.Fatalf("second admin must be refused: created=%v err=%v", created, err)
	}
	if n, _ := st.CountHumanUsers(ctx); n != 1 {
		t.Fatalf("expected exactly one human admin, got %d", n)
	}
}

// TestProvisionalRecomputedOnPromote (review finding #3): promoting one proposal
// must not erase the has_provisional signal when other proposals remain pending.
func TestProvisionalRecomputedOnPromote(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	sr, err := st.Save(ctx, SaveInput{Kind: "project", Content: Content{Title: "E", Summary: "s"}, AuthorKind: "ai"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Promote(ctx, PromoteInput{Slug: sr.Slug, VersionID: sr.VersionID, ActorKind: "human"}); err != nil {
		t.Fatal(err)
	}

	s2 := "v2"
	p2, err := st.ProposeEnhancement(ctx, ProposeInput{Slug: sr.Slug, ChangeNote: "a", Patch: Patch{Summary: &s2}, AuthorKind: "ai"})
	if err != nil {
		t.Fatal(err)
	}
	s3 := "v3"
	if _, err := st.ProposeEnhancement(ctx, ProposeInput{Slug: sr.Slug, ChangeNote: "b", Patch: Patch{Summary: &s3}, AuthorKind: "ai"}); err != nil {
		t.Fatal(err)
	}

	// Promote the OLDER proposal (p2); the newer one stays pending.
	if err := st.Promote(ctx, PromoteInput{Slug: sr.Slug, VersionID: p2.VersionID, ActorKind: "human"}); err != nil {
		t.Fatal(err)
	}
	e, err := st.GetEntry(ctx, sr.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if !e.HasProvisional {
		t.Fatal("entry should still report has_provisional (the newer proposal is still pending)")
	}
}

// TestHistoryScopeFindsRejected (review finding #9): a rejected version is retained
// and must be reachable via the history search scope.
func TestHistoryScopeFindsRejected(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	sr, err := st.Save(ctx, SaveInput{Kind: "project", Content: Content{Title: "Zebra widget", Summary: "zebra frobnicator baseline"}, AuthorKind: "ai"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Promote(ctx, PromoteInput{Slug: sr.Slug, VersionID: sr.VersionID, ActorKind: "human"}); err != nil {
		t.Fatal(err)
	}
	sol := "zebra frobnicator rejected variant"
	p2, err := st.ProposeEnhancement(ctx, ProposeInput{Slug: sr.Slug, ChangeNote: "x", Patch: Patch{Summary: &sol}, AuthorKind: "ai"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Reject(ctx, sr.Slug, p2.VersionID, 0, "human", "no"); err != nil {
		t.Fatal(err)
	}

	found := false
	res, _ := st.Search(ctx, "zebra frobnicator", SearchOpts{Scope: "history"})
	for _, r := range res {
		if r.Slug == sr.Slug {
			found = true
		}
	}
	if !found {
		t.Fatal("history scope should surface the retained rejected version")
	}
	// Curated scope still finds the curated head.
	if r, _ := st.Search(ctx, "zebra frobnicator", SearchOpts{}); len(r) == 0 {
		t.Fatal("curated scope should still find the curated head")
	}
}

// TestAllScopeSpansEveryState: the "all" search scope drops the state filter, so a
// term living only in a still-pending (non-curated) version is reachable — while the
// default curated scope, correctly, does not surface it.
func TestAllScopeSpansEveryState(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	sr, err := st.Save(ctx, SaveInput{Kind: "project", Content: Content{Title: "Kiwi widget", Summary: "kiwi baseline curated"}, AuthorKind: "ai"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Promote(ctx, PromoteInput{Slug: sr.Slug, VersionID: sr.VersionID, ActorKind: "human"}); err != nil {
		t.Fatal(err)
	}
	// A proposed enhancement whose distinctive token exists in NO curated version.
	sol := "kiwi quokka pending proposal"
	if _, err := st.ProposeEnhancement(ctx, ProposeInput{Slug: sr.Slug, ChangeNote: "x", Patch: Patch{Summary: &sol}, AuthorKind: "ai"}); err != nil {
		t.Fatal(err)
	}

	has := func(res []model.SearchResult) bool {
		for _, r := range res {
			if r.Slug == sr.Slug {
				return true
			}
		}
		return false
	}

	// "all" reaches the proposed-only token; curated (default) must not.
	if r, _ := st.Search(ctx, "quokka", SearchOpts{Scope: "all"}); !has(r) {
		t.Fatal("all scope should reach a token that lives only in a pending version")
	}
	if r, _ := st.Search(ctx, "quokka", SearchOpts{}); has(r) {
		t.Fatal("curated scope must NOT surface a pending-only token")
	}
	// "all" still finds curated content too.
	if r, _ := st.Search(ctx, "kiwi baseline", SearchOpts{Scope: "all"}); !has(r) {
		t.Fatal("all scope should also find the curated head")
	}
}
