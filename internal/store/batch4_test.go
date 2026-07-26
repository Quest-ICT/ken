package store

import (
	"context"
	"errors"
	"testing"
)

func TestPromoteDoubleIsRejected(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	sr, err := st.Save(ctx, SaveInput{Kind: "reference", Content: Content{Title: "T", Summary: "s"}, AuthorKind: "ai"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Promote(ctx, PromoteInput{Slug: sr.Slug, VersionID: sr.VersionID, ActorKind: "human"}); err != nil {
		t.Fatal(err)
	}
	// The same (now curated) version can't be promoted again — the state check rejects it.
	if err := st.Promote(ctx, PromoteInput{Slug: sr.Slug, VersionID: sr.VersionID, ActorKind: "human"}); !errors.Is(err, ErrBadVersion) {
		t.Fatalf("double promote should be ErrBadVersion, got %v", err)
	}
	if e, _ := st.GetEntry(ctx, sr.Slug); e.CuratedRev != 1 {
		t.Fatalf("curated_rev should stay 1 after a rejected re-promote, got %d", e.CuratedRev)
	}
}

func TestActorUniqueness(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	a1, err := st.FindOrCreateActor(ctx, "ai", "agent")
	if err != nil {
		t.Fatal(err)
	}
	a2, err := st.FindOrCreateActor(ctx, "ai", "agent")
	if err != nil {
		t.Fatal(err)
	}
	if a1 != a2 {
		t.Fatalf("get-or-create must return the same id, got %d and %d", a1, a2)
	}
	if _, err := st.CreateHumanUser(ctx, "bob", "h1"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateHumanUser(ctx, "bob", "h2"); err == nil {
		t.Fatal("a duplicate human user must be rejected")
	}
}
