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

// TestBuildInstructionsIsTheBaseGuide: the connect-time block is exactly the base guide, and
// nothing appends to it.
//
// It used to take a curation-language argument and this test called it twice, with nil and with an
// empty slice, to prove neither changed the output. The argument is gone — it was discarded — so
// the two calls were the same call. What remains worth asserting is the identity itself, because
// the sibling test below is what stops the curation paragraph from creeping back into a field the
// client truncates.
func TestBuildInstructionsIsTheBaseGuide(t *testing.T) {
	if got := buildInstructions(); got != baseInstructions {
		t.Fatal("the connect-time block is no longer exactly the base guide — something is appending " +
			"to a field the client cuts")
	}
}

// TestCurationSentenceNamesTheLanguages: with languages declared, the sentence names them,
// keeps the language-neutral fields out of the requirement, and stays OUT of the instructions.
//
// It moved from buildInstructions to the descriptions of kb_save and kb_propose_enhancement, for
// the reason recorded on curationSentence: appended to a field the client truncates, it was the
// first thing cut on exactly the deployments that had turned the feature on.
func TestCurationSentenceNamesTheLanguages(t *testing.T) {
	if got := curationSentence(nil); got != "" {
		t.Fatalf("no declared language should produce no text, got %q", got)
	}
	if got := curationSentence([]string{}); got != "" {
		t.Fatalf("an empty language list should produce no text, got %q", got)
	}
	got := curationSentence([]string{"fr", "zh"})
	for _, want := range []string{"curated in", "French (fr)", "Chinese (zh)", "never translate", "promoted"} {
		if !strings.Contains(got, want) {
			t.Fatalf("curation sentence missing %q:\n%s", want, got)
		}
	}
	// An unknown code falls back to the bare code (no CLDR table needed).
	if !strings.Contains(curationSentence([]string{"xx"}), "curated in xx.") {
		t.Fatal("unknown language code should fall back to the bare code")
	}
	// AND IT NEVER RE-ENTERS THE INSTRUCTIONS. buildInstructions takes no argument at all now, so
	// the only way back in is an edit to the block itself — which would refit under the budget for
	// a while and then silently truncate whatever sits last.
	if buildInstructions() != baseInstructions {
		t.Fatal("buildInstructions is appending curation text again — that field is truncated, " +
			"which is where this paragraph spent its whole life undelivered")
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

	// *** THE CURATION RULE MOVED OUT OF THE INSTRUCTIONS AND ONTO THE TWO TOOLS THAT WRITE. ***
	//
	// It used to be a paragraph appended to the instructions by buildInstructions. The MCP client
	// truncates that field at version.InstructionBudget characters, and the paragraph was appended
	// LAST — so on any deployment that declared a curation language, the sentence explaining the
	// requirement was the first thing cut. The operator who configured the feature was the only one
	// whose sessions were guaranteed never to be told about it.
	//
	// It rides on kb_save and kb_propose_enhancement now, which are the only two calls it can
	// change, and which the client delivers intact. This test follows it there rather than being
	// deleted, because the PROPERTY has not changed: declare a language and every session that
	// writes must be told, live, without a restart.
	descOf := func(tool string) string {
		t.Helper()
		cli := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
		sess, err := cli.Connect(context.Background(), &mcp.StreamableClientTransport{
			Endpoint: srv.URL, HTTPClient: clientWithToken("dev-secret"), DisableStandaloneSSE: true,
		}, nil)
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		defer sess.Close()
		res, err := sess.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatalf("list tools: %v", err)
		}
		for _, tl := range res.Tools {
			if tl.Name == tool {
				return tl.Description
			}
		}
		t.Fatalf("tool %s not registered", tool)
		return ""
	}

	const curationMarker = "CURATION LANGUAGE"
	const writers = 2

	// Before: no curation language declared -> no curation text anywhere.
	if got := instructions(); strings.Contains(got, curationMarker) {
		t.Fatal("the curation rule appears in the instructions with no curation language configured")
	}
	for _, tool := range []string{"kb_save", "kb_propose_enhancement"} {
		if strings.Contains(descOf(tool), curationMarker) {
			t.Fatalf("%s carries the curation rule with no curation language configured", tool)
		}
	}

	// Operator sets curation_langs live.
	h.SetCurationLangs([]string{"fr", "zh"})

	// After: a NEW connection sees it on both writing tools, naming both languages.
	seen := 0
	for _, tool := range []string{"kb_save", "kb_propose_enhancement"} {
		got := descOf(tool)
		if !strings.Contains(got, curationMarker) {
			t.Errorf("after SetCurationLangs, %s does not carry the curation rule", tool)
			continue
		}
		seen++
		for _, want := range []string{"French (fr)", "Chinese (zh)"} {
			if !strings.Contains(got, want) {
				t.Errorf("%s's curation rule is missing %q", tool, want)
			}
		}
	}
	if seen != writers {
		t.Errorf("%d of %d writing tools carry the curation rule; a session using the other one is "+
			"never told, and its proposal is stranded unreadable by the curator", seen, writers)
	}

	// AND IT MUST NOT COME BACK INTO THE INSTRUCTIONS, where it does not fit. Restoring it there
	// would push the delivered string past the budget again, silently, and the symptom would be
	// the loss of whatever now sits at the end of the block rather than of the paragraph itself.
	if strings.Contains(instructions(), curationMarker) {
		t.Error("the curation rule is back in the instructions; it is appended text on a truncated " +
			"field, which is where it spent its whole life undelivered")
	}
}
