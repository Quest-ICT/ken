package store

import (
	"context"
	"testing"
)

func TestImportEntry(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	created, err := st.ImportEntry(ctx, ImportInput{
		Slug: "foo-bar", Kind: "project",
		Content:    Content{Title: "Foo bar note", Summary: "about foo", Solution: "do foo carefully"},
		ChangeNote: "imported from foo.md",
		Links:      []LinkInput{{ToSlug: "baz", LinkType: "relates"}},
	})
	if err != nil || !created {
		t.Fatalf("import: created=%v err=%v", created, err)
	}

	// Idempotent: a second import of the same slug skips.
	created2, err := st.ImportEntry(ctx, ImportInput{Slug: "foo-bar", Kind: "project", Content: Content{Title: "x", Summary: "y"}})
	if err != nil || created2 {
		t.Fatalf("second import should skip: created=%v err=%v", created2, err)
	}

	// It lands as a curated rev-1 entry and is in the default (curated) search.
	e, err := st.GetEntry(ctx, "foo-bar")
	if err != nil {
		t.Fatal(err)
	}
	if e.Lifecycle != "active" || e.CuratedRev != 1 || e.Head == nil || e.Head.Solution != "do foo carefully" {
		t.Fatalf("imported entry not curated as expected: %+v head=%+v", e, e.Head)
	}
	if res, _ := st.Search(ctx, "foo carefully", SearchOpts{}); len(res) == 0 {
		t.Fatal("imported entry should be found by curated search")
	}
}
