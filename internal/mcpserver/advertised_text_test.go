package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// THE ADVERTISED SCHEMA MUST SAY triggers IS AN ARRAY, AND THE INSTRUCTIONS MUST SAY WHAT
// TO DO WHEN kb_record_outcome IS NOT ON OFFER.
//
// Both are text a caller receives at connect, and both were reported by a real claude.ai
// session on 2026-08-21. `triggers` is []string and its description was prose with no type
// hint — "symptoms that should surface this entry" reads as an invitation to write symptoms,
// and a delimited string is the natural way to write several, which the wire then rejects.
//
// ASSERTED OVER tools/list AND THE DELIVERED INSTRUCTIONS, not over the Go strings. A fix to
// a struct tag that never reached the schema, or an instruction edited in a const that is
// not the one sent, would both pass a source-level check — and this project has shipped that
// exact shape more than once.
func TestAdvertisedTextSaysWhatTheWireActuallyAccepts(t *testing.T) {
	t.Setenv("KEN_DEV_TOKEN", "dev-secret")
	srv, _ := testServer(t)
	ctx := context.Background()

	cli := mcp.NewClient(&mcp.Implementation{Name: "text", Version: "0"}, nil)
	sess, err := cli.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             srv.URL,
		HTTPClient:           clientWithToken("dev-secret"),
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer sess.Close()

	tools, err := sess.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var save *mcp.Tool
	for _, tl := range tools.Tools {
		if tl.Name == "kb_save" {
			save = tl
		}
	}
	if save == nil {
		t.Fatal("kb_save is not advertised")
	}
	blob, err := json.Marshal(save.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	schema := string(blob)

	// POSITIVE CONTROL: the field must actually be in the advertised schema, or the
	// description assertion below would pass on a schema that omits it entirely.
	if !strings.Contains(schema, "triggers") {
		t.Fatalf("kb_save's advertised schema has no triggers field at all:\n%s", schema)
	}
	if !strings.Contains(strings.ToUpper(schema), "ARRAY") {
		t.Errorf("triggers' advertised description does not say it is an ARRAY, so a caller "+
			"reading it will send a delimited string and be rejected by the wire:\n%s", schema)
	}

	// And the instruction that demands a tool the client may not be showing.
	instr := sess.InitializeResult().Instructions
	if !strings.Contains(instr, "kb_record_outcome") {
		t.Fatal("the delivered instructions do not mention kb_record_outcome at all — the " +
			"assertion below would pass for the wrong reason")
	}
	if !strings.Contains(instr, "NOT IN YOUR TOOL LIST") {
		t.Errorf("the instructions demand kb_record_outcome and say nothing about what to do "+
			"when a client is not showing it, so a session silently skips a step its own "+
			"instructions require:\n%s", instr)
	}
}
