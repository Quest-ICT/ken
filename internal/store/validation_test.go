package store

import (
	"context"
	"errors"
	"testing"
)

// TestSaveEnumValidation locks the write-path error contract: an invalid kind or
// link_type must return an actionable ErrInvalid, never an opaque DB CHECK failure
// (surfaced to the AI as "internal error") or a silently-dropped link.
func TestSaveEnumValidation(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	good := Content{Title: "T", Summary: "S"}

	if _, err := st.Save(ctx, SaveInput{Kind: "recipe", Content: good, AuthorKind: "ai"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad kind: want ErrInvalid, got %v", err)
	}

	// A bad link_type used to be swallowed by INSERT OR IGNORE (save succeeded, link vanished).
	if _, err := st.Save(ctx, SaveInput{Kind: "reference", Content: good, AuthorKind: "ai",
		Links: []LinkInput{{ToSlug: "other", LinkType: "mentions"}}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad link_type: want ErrInvalid, got %v", err)
	}

	// Valid kind + valid link_type (dangling to_slug is allowed) -> success.
	if _, err := st.Save(ctx, SaveInput{Kind: "reference", Content: good, AuthorKind: "ai",
		Links: []LinkInput{{ToSlug: "other", LinkType: "relates"}}}); err != nil {
		t.Fatalf("valid save: %v", err)
	}
}
