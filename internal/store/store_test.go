package store

import (
	"context"
	"path/filepath"
	"testing"
)

// TestSearchAndGet exercises the core skeleton path end to end against real
// SQLite: migrate (multi-statement), FTS trigger population, hybrid search, get.
func TestSearchAndGet(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	if err := st.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	ctx := context.Background()
	if _, err := st.SeedDemo(ctx); err != nil {
		t.Fatalf("seed: %v", err)
	}

	res, err := st.Search(ctx, "docker build reinstalls dependencies layer cache", SearchOpts{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res) == 0 {
		t.Fatal("expected at least one search hit")
	}
	if res[0].Slug != "docker-copy-manifests-before-source" {
		t.Fatalf("unexpected top hit: %q", res[0].Slug)
	}

	entries, missing, err := st.Get(ctx, []string{res[0].Slug, "does-not-exist"}, true)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(entries) != 1 || entries[0].Head == nil || entries[0].Head.Solution == "" {
		t.Fatalf("bad get result: %+v", entries)
	}
	if len(missing) != 1 || missing[0] != "does-not-exist" {
		t.Fatalf("bad missing list: %v", missing)
	}
}
