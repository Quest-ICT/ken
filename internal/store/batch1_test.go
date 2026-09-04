package store

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Quest-ICT/ken/internal/embed"
)

func TestSearchPageHasMore(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := st.Save(ctx, SaveInput{Kind: "reference",
			Content: Content{Title: "widget frobnicator " + string(rune('a'+i)), Summary: "frobnicate the widgets"}, AuthorKind: "ai"}); err != nil {
			t.Fatal(err)
		}
	}
	if r, more, _ := st.SearchPage(ctx, "frobnicate widgets", SearchOpts{K: 2}); len(r) != 2 || !more {
		t.Fatalf("K=2: len=%d more=%v (want 2,true)", len(r), more)
	}
	// Exactly K rows on the last page must NOT report has_more.
	if r, more, _ := st.SearchPage(ctx, "frobnicate widgets", SearchOpts{K: 3}); len(r) != 3 || more {
		t.Fatalf("K=3 exact: len=%d more=%v (want 3,false)", len(r), more)
	}
	if r, more, _ := st.SearchPage(ctx, "frobnicate widgets", SearchOpts{K: 5}); len(r) != 3 || more {
		t.Fatalf("K=5: len=%d more=%v (want 3,false)", len(r), more)
	}
}

func TestZeroQueryVectorSkipsArm(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	e := embed.HashEmbedder{Dim: 64}
	st.Save(ctx, SaveInput{Kind: "reference",
		Content: Content{Title: "Postgres pooling", Summary: "database connection pool"}, AuthorKind: "ai"})
	targets, _ := st.VersionsNeedingEmbedding(ctx, e.ID(), 0)
	for _, tg := range targets {
		v, _ := e.Embed(ctx, []string{tg.Text})
		_ = st.UpsertEmbedding(ctx, tg.VersionID, e.ID(), v[0])
	}
	// "Go" has only <3-char tokens -> the hash embedder returns an all-zero vector,
	// and FTS finds nothing -> the only possible arm is the vector one, which must
	// contribute nothing (not ~200 arbitrary rows).
	zero, _ := e.Embed(ctx, []string{"Go"})
	if res, _ := st.Search(ctx, "Go", SearchOpts{QueryVec: zero[0], EmbedModel: e.ID()}); len(res) != 0 {
		t.Fatalf("zero query vector should yield no results, got %d", len(res))
	}
}

func TestSlugifyRuneSafe(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	sr, err := st.Save(ctx, SaveInput{Kind: "reference",
		Content: Content{Title: strings.Repeat("é", 100), Summary: "x"}, AuthorKind: "ai"})
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(sr.Slug) {
		t.Fatalf("slug must be valid UTF-8, got %q", sr.Slug)
	}
	if n := utf8.RuneCountInString(sr.Slug); n > 80 {
		t.Fatalf("slug should be <=80 runes, got %d", n)
	}
}
