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

func TestAFreshWriteServesItsBodyAndProvenance(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	sr, err := st.Save(ctx, SaveInput{Kind: "reference",
		Content: Content{Title: "Draft note", Summary: "s", Solution: "draft solution"}, AuthorKind: "ai"})
	if err != nil {
		t.Fatal(err)
	}
	// Was "not promoted: no curated head, but the provisional body must still be returned". There
	// is no un-promoted state left — the write IS the head — so what this now pins is the part
	// that always mattered and was always right: GetEntry serves the body. Withholding a body
	// while signalling that an entry exists was the gate's exact mistake.
	e, err := st.GetEntry(ctx, sr.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if e.Lifecycle != "active" || e.Head == nil || e.Head.Solution != "draft solution" {
		t.Fatalf("a fresh write must be active with its body served: lifecycle=%s head=%+v", e.Lifecycle, e.Head)
	}
	// detailed mode attaches provenance.
	entries, _, err := st.Get(ctx, []string{sr.Slug}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Provenance == nil || entries[0].Provenance.State != "curated" {
		t.Fatalf("detailed should include provenance (state=curated): %+v", entries)
	}
}
