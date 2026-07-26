package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestContentCapsRejected(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	_, err := st.Save(ctx, SaveInput{Kind: "reference",
		Content: Content{Title: strings.Repeat("x", 400), Summary: "s"}, AuthorKind: "ai"})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized title should be ErrInvalid, got %v", err)
	}
}

func TestDraftReturnsProvisionalBodyAndProvenance(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	sr, err := st.Save(ctx, SaveInput{Kind: "reference",
		Content: Content{Title: "Draft note", Summary: "s", Solution: "draft solution"}, AuthorKind: "ai"})
	if err != nil {
		t.Fatal(err)
	}
	// Not promoted: no curated head, but the provisional body must still be returned.
	e, err := st.GetEntry(ctx, sr.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if e.Lifecycle != "draft" || e.Head == nil || e.Head.Solution != "draft solution" {
		t.Fatalf("draft should return the provisional body: lifecycle=%s head=%+v", e.Lifecycle, e.Head)
	}
	// detailed mode attaches provenance.
	entries, _, err := st.Get(ctx, []string{sr.Slug}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Provenance == nil || entries[0].Provenance.State != "proposed" {
		t.Fatalf("detailed should include provenance (state=proposed): %+v", entries)
	}
}
