package store

import (
	"context"
	"testing"

	"github.com/Quest-ICT/ken/internal/embed"
)

// TestEmbeddingMultiModelCoexist proves the (version_id, model_id) primary key
// lets two models' vectors coexist for the same version, and that re-upserting one
// model replaces only its own row (no duplicate, no clobber of the other model).
func TestEmbeddingMultiModelCoexist(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	if _, err := st.Save(ctx, SaveInput{Kind: "reference",
		Content: Content{Title: "Two models", Summary: "one version, two vectors"}, AuthorKind: "ai"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	targets, err := st.VersionsNeedingEmbedding(ctx, "model-a", 0)
	if err != nil || len(targets) == 0 {
		t.Fatalf("targets: err=%v n=%d", err, len(targets))
	}
	vid := targets[0].VersionID

	if err := st.UpsertEmbedding(ctx, vid, "model-a", []float32{1, 0, 0}); err != nil {
		t.Fatalf("upsert a: %v", err)
	}
	if err := st.UpsertEmbedding(ctx, vid, "model-b", []float32{0, 1, 0, 0}); err != nil {
		t.Fatalf("upsert b: %v", err)
	}
	if embedded, _, err := st.EmbeddingStats(ctx); err != nil || embedded != 2 {
		t.Fatalf("both models should coexist: embedded=%d err=%v", embedded, err)
	}

	// Re-embedding model-a replaces its row only — still exactly two rows.
	if err := st.UpsertEmbedding(ctx, vid, "model-a", []float32{0, 0, 1}); err != nil {
		t.Fatalf("re-upsert a: %v", err)
	}
	if embedded, _, err := st.EmbeddingStats(ctx); err != nil || embedded != 2 {
		t.Fatalf("re-upsert must replace, not add: embedded=%d err=%v", embedded, err)
	}
}

// TestVectorSearch proves the semantic (vector) arm participates: both vector-only
// and hybrid queries rank the relevant curated entry first.
func TestVectorSearch(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	e := embed.HashEmbedder{Dim: 128}

	mk := func(title, summary, solution string) string {
		sr, err := st.Save(ctx, SaveInput{Kind: "reference",
			Content: Content{Title: title, Summary: summary, Solution: solution}, AuthorKind: "ai"})
		if err != nil {
			t.Fatal(err)
		}
		return sr.Slug
	}
	db := mk("Postgres pooling", "tune pgbouncer pool size for postgres database connections", "set pool_mode transaction")
	_ = mk("Sourdough bread", "hydration and fermentation schedule for sourdough loaves", "autolyse then bulk ferment")

	// Embed every version with the deterministic hash embedder.
	targets, err := st.VersionsNeedingEmbedding(ctx, e.ID(), 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, tg := range targets {
		v, _ := e.Embed(ctx, []string{tg.Text})
		if err := st.UpsertEmbedding(ctx, tg.VersionID, e.ID(), v[0]); err != nil {
			t.Fatal(err)
		}
	}
	qv, _ := e.Embed(ctx, []string{"postgres database connection pool tuning"})

	// Vector-only: the FTS query has no usable tokens, so results come purely from
	// the vector arm (the Go-built VALUES CTE).
	if res, _ := st.Search(ctx, "  ", SearchOpts{QueryVec: qv[0], EmbedModel: e.ID()}); len(res) == 0 || res[0].Slug != db {
		t.Fatalf("vector-only search should rank %q first: %+v", db, res)
	}
	// Model filter: a query tagged with a different model matches no stored vectors.
	if res, _ := st.Search(ctx, "  ", SearchOpts{QueryVec: qv[0], EmbedModel: "other-model"}); len(res) != 0 {
		t.Fatalf("wrong-model vector query should match nothing, got %d", len(res))
	}
	// Hybrid: keyword + vector both point at the database entry.
	if res, _ := st.Search(ctx, "postgres pool", SearchOpts{QueryVec: qv[0], EmbedModel: e.ID()}); len(res) == 0 || res[0].Slug != db {
		t.Fatalf("hybrid search should rank %q first: %+v", db, res)
	}
	// Embedding stats reflect what we stored.
	if embn, total, _ := st.EmbeddingStats(ctx); embn != total || total == 0 {
		t.Fatalf("embedding stats off: %d/%d", embn, total)
	}
}
