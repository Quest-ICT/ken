package store

import (
	"context"
	"errors"
	"testing"
)

// Was TestPromoteDoubleIsRejected. There is no promotion to repeat in 6.0.0; the guard that
// survives is SetHead's, and it is the one that matters now — the human's undo must refuse to
// "revert" to the version that is already serving, or the reflog fills with events that changed
// nothing and a real revert becomes impossible to find among them.
func TestSetHeadRefusesTheVersionAlreadyServing(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	sr, err := st.Save(ctx, SaveInput{Kind: "reference", Content: Content{Title: "T", Summary: "s"}, AuthorKind: "ai"})
	if err != nil {
		t.Fatal(err)
	}
	// Save landed this version AS the head. Asking to set the head to it must be refused.
	if err := st.SetHead(ctx, PromoteInput{Slug: sr.Slug, VersionID: sr.VersionID, ActorKind: "human"}); !errors.Is(err, ErrBadVersion) {
		t.Fatalf("setting the head to the version already serving should be ErrBadVersion, got %v", err)
	}
	if e, _ := st.GetEntry(ctx, sr.Slug); e.Head == nil || e.Head.RevNo != 1 {
		t.Fatal("the head moved on a refused SetHead")
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
