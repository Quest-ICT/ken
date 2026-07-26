package mcpserver

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Quest-ICT/ken/internal/store"
)

// TestBuildInstructionsBaseUnchanged: with no curation language declared the
// AI-facing instructions must be exactly the base guide — an English-only KB
// sees no difference.
func TestBuildInstructionsBaseUnchanged(t *testing.T) {
	if got := buildInstructions(nil); got != baseInstructions {
		t.Fatal("nil curation langs should return the base instructions unchanged")
	}
	if got := buildInstructions([]string{}); got != baseInstructions {
		t.Fatal("empty curation langs should return the base instructions unchanged")
	}
}

// TestBuildInstructionsCurationParagraph: with languages declared, the paragraph
// is appended, names the languages, and keeps the base guide intact.
func TestBuildInstructionsCurationParagraph(t *testing.T) {
	got := buildInstructions([]string{"fr", "zh"})
	if !strings.HasPrefix(got, baseInstructions) {
		t.Fatal("curation instructions should extend, not replace, the base instructions")
	}
	for _, want := range []string{"curated in", "French (fr)", "Chinese (zh)", "never translate", "PROMOTE"} {
		if !strings.Contains(got, want) {
			t.Fatalf("curation paragraph missing %q", want)
		}
	}
	// An unknown code falls back to the bare code (no CLDR table needed).
	if !strings.Contains(buildInstructions([]string{"xx"}), "curated in xx.") {
		t.Fatal("unknown language code should fall back to the bare code")
	}
}

// TestSetCurationLangsLive is the Phase-0 guarantee: a running /mcp Handler
// swaps the AI-facing initialize instructions when the curation language(s)
// change, so a settings edit reaches new connections without a restart.
func TestSetCurationLangsLive(t *testing.T) {
	t.Setenv("KEN_DEV_TOKEN", "dev-secret")
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	h := NewHTTPHandler(Deps{Store: st, DedupSecret: []byte("test-dedup-secret")})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	instructions := func() string {
		t.Helper()
		cli := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
		sess, err := cli.Connect(context.Background(), &mcp.StreamableClientTransport{
			Endpoint: srv.URL, HTTPClient: clientWithToken("dev-secret"), DisableStandaloneSSE: true,
		}, nil)
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		defer sess.Close()
		return sess.InitializeResult().Instructions
	}

	// Before: no curation language declared → base instructions.
	if got := instructions(); got != baseInstructions {
		t.Fatalf("initial instructions should be the base guide, got a %d-char variant", len(got))
	}

	// Operator sets curation_langs live.
	h.SetCurationLangs([]string{"fr", "zh"})

	// After: a NEW connection sees the curation paragraph.
	got := instructions()
	if got == baseInstructions {
		t.Fatal("after SetCurationLangs a new connection should receive the curation paragraph")
	}
	for _, want := range []string{"French (fr)", "Chinese (zh)"} {
		if !strings.Contains(got, want) {
			t.Fatalf("live instructions missing %q", want)
		}
	}
}
