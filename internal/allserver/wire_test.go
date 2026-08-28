package allserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Quest-ICT/ken/internal/comm"
	"github.com/Quest-ICT/ken/internal/commserver"
	"github.com/Quest-ICT/ken/internal/mcpserver"
	"github.com/Quest-ICT/ken/internal/stationserver"
	"github.com/Quest-ICT/ken/internal/store"
)

type bearer struct {
	tok  string
	base http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.tok)
	return b.base.RoundTrip(r)
}

// unified builds the real endpoint over real stores and connects a client to it.
func unified(t *testing.T) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "ken.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	cs, err := comm.Open(filepath.Join(t.TempDir(), "comm.db"), comm.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	if err := cs.Migrate(); err != nil {
		t.Fatal(err)
	}
	actor, err := st.FindOrCreateActor(ctx, "ai", "wire")
	if err != nil {
		t.Fatal(err)
	}
	// EVERY SCOPE, because this endpoint requires every capability: the three middlewares are
	// chained and each fails closed on a missing one. A partial credential belongs on no endpoint
	// now that there is only this one.
	tok, err := st.IssueToken(ctx, actor, []string{"read", "write-draft", "propose", "comm", "station"}, "wire test")
	if err != nil {
		t.Fatal(err)
	}
	commDeps := commserver.Deps{Comm: cs, Store: st}
	srv := httptest.NewServer(NewHTTPHandler(Deps{
		KB:      mcpserver.Deps{Store: st},
		Comm:    commDeps,
		CommH:   commserver.NewHTTPHandler(commDeps),
		Station: stationserver.Deps{Store: st},
	}))
	t.Cleanup(srv.Close)

	cli := mcp.NewClient(&mcp.Implementation{Name: "wire", Version: "0"}, nil)
	sess, err := cli.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             srv.URL,
		HTTPClient:           &http.Client{Transport: bearer{tok: tok, base: http.DefaultTransport}},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

func callJSON(t *testing.T, sess *mcp.ClientSession, name string, args map[string]any, into any) string {
	t.Helper()
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: transport error: %v", name, err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	if res.IsError {
		return b.String()
	}
	if into != nil {
		if err := json.Unmarshal([]byte(b.String()), into); err != nil {
			t.Fatalf("%s: result did not parse: %v (%s)", name, err, b.String())
		}
	}
	return b.String()
}

// *** ken_instructions ON THE ENDPOINT THAT ACTUALLY SHIPS, WHICH IS THE ONLY PLACE THE DEFECT
// COULD BE SEEN. ***
//
// Three packages each registered ken_version and ken_instructions. That was correct while there
// were three servers. Collapsed onto one, mcp.AddTool "adds a Tool to the server, OR REPLACES ONE
// WITH THE SAME NAME" — so the last registration won and ken_instructions returned a single
// surface's block, correctly formatted, with nothing to suggest two thirds were missing.
//
// EVERY EXISTING TEST STAYED GREEN. The per-package tests each connect to their own handler, where
// there is no collision to see; the audit test asserted all three packages registered the tool,
// which all three did, and that WAS the defect. Nothing read the served surface. So this does,
// through a real client over real HTTP, which is the layer the mistake lived at.
func TestTheUnifiedEndpointServesOneWholeSetOfInstructions(t *testing.T) {
	sess := unified(t)

	var out struct {
		Version      string   `json:"version"`
		Instructions string   `json:"instructions"`
		Tools        []string `json:"tools"`
		Tool         string   `json:"tool"`
		Note         string   `json:"note"`
	}
	callJSON(t, sess, "ken_instructions", map[string]any{}, &out)

	// IT MUST BE THE MERGED BLOCK, not one surface's. Compared against the constant this endpoint
	// delivers at connect rather than against a phrase, so the two cannot drift apart: what
	// ken_instructions returns and what the session was handed on connect are the same text by
	// construction, and if they ever stop being, this is where it shows.
	if out.Instructions != Instructions {
		t.Errorf("ken_instructions did not return this endpoint's own block.\n got: %.200s…\nwant: %.200s…\n"+
			"A per-surface block here means the three registrations collided again and one of them won.",
			out.Instructions, Instructions)
	}
	// AND ALL THREE FAMILIES ARE IN IT — the property a collision would break, stated directly so a
	// failure names what a session lost rather than only that two strings differ.
	for _, family := range []string{"kb_", "comm_", "station_"} {
		if !strings.Contains(out.Instructions, family) {
			t.Errorf("the returned instructions never mention %q — a session reading this would never learn that surface exists", family)
		}
	}
	if len(out.Tools) < 40 {
		t.Errorf("the answer lists %d tools; with per-tool rules behind ken_instructions{tool:\"…\"}, "+
			"an incomplete list is a session that cannot discover what it may ask about", len(out.Tools))
	}
	if out.Tool != "" {
		t.Errorf("a no-argument call came back labelled as tool %q", out.Tool)
	}
}

// THE `tool` ARGUMENT, END TO END: the rules must arrive in full and must be THIS tool's.
//
// Asserted over the wire because the SDK validates arguments before any handler runs, and this
// project has already shipped one optional field that a unit test accepted and the schema layer
// rejected. A parameter that never reaches the handler is indistinguishable, from the caller's
// side, from a server that does not implement it.
func TestPerToolRulesArriveInFullThroughAResult(t *testing.T) {
	sess := unified(t)

	var out struct {
		Instructions string   `json:"instructions"`
		Tool         string   `json:"tool"`
		Tools        []string `json:"tools"`
		Note         string   `json:"note"`
	}
	callJSON(t, sess, "ken_instructions", map[string]any{"tool": "comm_send"}, &out)

	if out.Tool != "comm_send" {
		t.Fatalf("the answer is labelled %q — a session that asked about one tool and cannot tell which "+
			"it got back has to trust the order of its own calls", out.Tool)
	}
	// THE FULL RULES, not the one-line entry. The list entry is deliberately a prefix of this, so
	// "longer than the entry" is the honest test that nothing was shortened on the way out.
	var listed string
	tools, err := sess.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range tools.Tools {
		if tl.Name == "comm_send" {
			listed = tl.Description
		}
	}
	if listed == "" {
		t.Fatal("comm_send is not in the tool list at all")
	}
	if len(out.Instructions) <= len(listed) {
		t.Errorf("the rules (%d chars) are no longer than the list entry (%d) — the whole point is that "+
			"the entry is one sentence and the rest lives here", len(out.Instructions), len(listed))
	}
	if !strings.HasPrefix(out.Instructions, strings.Split(listed, " — FULL RULES:")[0]) {
		t.Errorf("the list entry is not a prefix of the rules, so the summary can disagree with the detail:\n entry: %q\n rules: %.160s…", listed, out.Instructions)
	}
	// A tool-scoped answer must NOT repeat the whole catalogue: forty-five names under every answer
	// is what trains a session to skim results.
	if len(out.Tools) != 0 {
		t.Errorf("a tool-scoped answer also returned %d tool names", len(out.Tools))
	}

	// AND AN UNKNOWN NAME FAILS USEFULLY. A bare refusal leaves a session one typo from the right
	// call with no way to find it; the refusal carries the list instead.
	msg := callJSON(t, sess, "ken_instructions", map[string]any{"tool": "comm_sned"}, nil)
	if !strings.Contains(msg, "comm_send") {
		t.Errorf("the refusal for a mistyped tool name does not name the real ones: %q", msg)
	}
}
