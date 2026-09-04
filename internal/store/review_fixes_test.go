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

// TestProvisionalRecomputedOnPromote IS DELETED (6.0.0). It asserted that promoting one proposal
// must not erase the has_provisional badge while other proposals were still pending. There are no
// pending proposals and no has_provisional: every write is the head, and the versions behind it are
// superseded rather than waiting. The property that replaced it — a revision displaces the head and
// the displaced version stays reachable — is asserted in write_test.go and repromote_test.go.

// A version that is no longer the head is retained and must stay reachable via the history
// search scope — the whole recovery story after 6.0.0 rests on it.
func TestHistoryScopeFindsSupersededContent(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	sr, err := st.Save(ctx, SaveInput{Kind: "project", Content: Content{Title: "Zebra widget", Summary: "zebra frobnicator baseline"}, AuthorKind: "ai"})
	if err != nil {
		t.Fatal(err)
	}
	// A superseded version must stay reachable through the history scope. Before 6.0.0 this used
	// a REJECTED version, because rejection was the only way to make a version non-current. There
	// is no Reject any more; a revision supersedes its predecessor, which is the same question —
	// "can I still find what we used to believe" — reached the way it is now actually reached.
	//
	// Rejected rows themselves are NOT gone: the state stays legal, existing rows keep their
	// meaning, and the history scope still selects them. Nothing new can produce one.
	sol := "zebra frobnicator superseded variant"
	if _, err := st.ProposeEnhancement(ctx, ProposeInput{Slug: sr.Slug, ChangeNote: "x", Patch: Patch{Summary: &sol}, AuthorKind: "ai"}); err != nil {
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

	// The default scope now REACHES a token that lives only in the newest version, because the
	// newest version is the head. This assertion used to require the opposite — that the default
	// must NOT surface it — which is precisely the invisibility 6.0.0 removed.
	if r, _ := st.Search(ctx, "quokka", SearchOpts{Scope: "all"}); !has(r) {
		t.Fatal("all scope should reach a token in the newest version")
	}
	if r, _ := st.Search(ctx, "quokka", SearchOpts{}); !has(r) {
		t.Fatal("the DEFAULT scope must reach a token in the newest version — a write that cannot " +
			"be found by the next search is the barrier this release removed")
	}
	// "all" still finds curated content too.
	if r, _ := st.Search(ctx, "kiwi baseline", SearchOpts{Scope: "all"}); !has(r) {
		t.Fatal("all scope should also find the curated head")
	}
}
