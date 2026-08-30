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
	sess, _, _ := unifiedWithHandler(t)
	return sess
}

// unifiedWithHandler additionally returns the served handler and a reconnect function, for the
// tests that have to observe a LIVE settings change on the surface a request actually reaches.
func unifiedWithHandler(t *testing.T) (*mcp.ClientSession, *Handler, func() *mcp.ClientSession) {
	return unifiedAs(t, "")
}

// unifiedAs builds the endpoint and connects with an explicit bearer. An empty token means "mint a
// full-capability one", which is what every ordinary test wants; passing KEN_DEV_TOKEN's value
// exercises the dev bypass over the real transport, which is the only way to see that it
// authenticates AND that a tool call then works.
func unifiedAs(t *testing.T, asToken string) (*mcp.ClientSession, *Handler, func() *mcp.ClientSession) {
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
	if asToken != "" {
		tok = asToken
	}
	commDeps := commserver.Deps{Comm: cs, Store: st}
	handler := NewHTTPHandler(Deps{
		KB:      mcpserver.Deps{Store: st},
		Comm:    commDeps,
		CommH:   commserver.NewHTTPHandler(commDeps),
		Station: stationserver.Deps{Store: st},
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	connect := func() *mcp.ClientSession {
		t.Helper()
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
	return connect(), handler, connect
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
	if len(out.Tools) < 35 {
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

	// EVERY SERVED TOOL IS ASKABLE ABOUT, INCLUDING THE TWO THAT ANSWER FOR THE OTHERS.
	//
	// ken_version and ken_instructions are registered with mcp.AddTool directly rather than through
	// addTool, so they never entered tooldoc and asking about either returned "no tool named X is
	// served here" — the same sentence a retired name gets. The population most likely to ask is
	// the one investigating the freeze, and the answer told them a tool in their own list did not
	// exist.
	tools, err = sess.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range tools.Tools {
		var one struct {
			Tool string `json:"tool"`
		}
		msg := callJSON(t, sess, "ken_instructions", map[string]any{"tool": tl.Name}, &one)
		if one.Tool != tl.Name {
			t.Errorf("ken_instructions{tool:%q} did not answer for it: %s", tl.Name, msg)
		}
	}

	// AND AN UNKNOWN NAME FAILS USEFULLY. A bare refusal leaves a session one typo from the right
	// call with no way to find it; the refusal carries the list instead.
	msg := callJSON(t, sess, "ken_instructions", map[string]any{"tool": "comm_sned"}, nil)
	if !strings.Contains(msg, "comm_send") {
		t.Errorf("the refusal for a mistyped tool name does not name the real ones: %q", msg)
	}
}

// *** A LIVE SETTING MUST CHANGE THE SURFACE A REQUEST ACTUALLY REACHES. ***
//
// This is the gate that was missing, and its absence let a real regression ship into the staged
// release. When the three endpoints collapsed into /mcp, main.go went on constructing
// mcpserver.Handler and stationserver.Handler and wiring live.OnChange to them — handlers nothing
// mounted. Editing a "live" setting therefore changed nothing observable, while the settings page
// went on saying it applied immediately.
//
// BOTH GUARDING TESTS STAYED GREEN, because both drove a per-surface handler directly. That is the
// defect this project keeps paying for: the check and the thing checked rendered identically. So
// this one drives the SERVED handler and reads the answer off the wire.
//
// The curation language is the worse of the two. It is the single rule tooldoc.MustArrive exists
// for — a session cannot pull an answer to a question it does not know to ask — so a stale one
// produces proposals the curator cannot read, and the write SUCCEEDS.
func TestALiveSettingChangeReachesTheServedSurface(t *testing.T) {
	sess, handler, reconnect := unifiedWithHandler(t)

	descOf := func(s *mcp.ClientSession, tool string) string {
		t.Helper()
		tools, err := s.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, tl := range tools.Tools {
			if tl.Name == tool {
				return tl.Description
			}
		}
		t.Fatalf("%s is not in the served tool list", tool)
		return ""
	}

	// CONTROL: with no curation language configured, no curation rule is advertised. Without this,
	// the assertion below would pass on a build that always carried the text.
	const marker = "CURATION LANGUAGE"
	for _, tool := range []string{"kb_save", "kb_propose_enhancement"} {
		if strings.Contains(descOf(sess, tool), marker) {
			t.Fatalf("%s carries the curation rule with no language configured", tool)
		}
	}

	handler.SetCurationLangs([]string{"fr"})

	after := reconnect()
	for _, tool := range []string{"kb_save", "kb_propose_enhancement"} {
		got := descOf(after, tool)
		if !strings.Contains(got, marker) {
			t.Errorf("%s does not carry the curation rule after a live change — the setting says it "+
				"applies immediately and the served surface disagrees, which is how a session writes "+
				"an entry the curator can never promote, with no error anywhere", tool)
			continue
		}
		if !strings.Contains(got, "French") {
			t.Errorf("%s carries a curation rule that does not name the configured language: %q", tool, got)
		}
	}
}

// THE DEV TOKEN MUST REACH TOOLS, NOT JUST THE HANDSHAKE.
//
// The fix that taught all three middlewares to honour KEN_DEV_TOKEN was verified with `initialize`
// and `tools/list`, both of which returned exactly what they should — and the very next call, the
// one the server's own instructions mandate FIRST, died with a raw driver error: the dev principal
// carried ActorID 0, and `station(created_by_actor_id)` is NOT NULL REFERENCES actor(id) with
// SQLite rowids starting at 1.
//
// That is worse than the 401 it replaced. A 401 is legible; "FOREIGN KEY constraint failed" on the
// first mandated call, after a successful handshake, is not — and it sat on the path README tells a
// new user to walk first.
//
// SO THE GATE CALLS TOOLS FROM ALL THREE FAMILIES over the real transport. A handshake proves the
// credential was accepted; only a call proves it can be used.
func TestTheDevTokenCanActuallyBeUsed(t *testing.T) {
	t.Setenv("KEN_DEV_TOKEN", "dev-secret")
	sess, _, _ := unifiedAs(t, "dev-secret")

	var me struct {
		StationID string `json:"station_id"`
	}
	if out := callJSON(t, sess, "station_me", map[string]any{"session_key": "conv-dev"}, &me); me.StationID == "" {
		t.Fatalf("station_me returned no station for the dev token: %s", out)
	}
	// comm and kb too: a mailbox is resolved from the station, so a broken principal fails here as
	// well, and the knowledge base is the one family the bypass was originally scoped to.
	if out := callJSON(t, sess, "comm_directory", map[string]any{"session_key": "conv-dev"}, nil); strings.Contains(out, "constraint") {
		t.Errorf("comm_directory failed for the dev token: %s", out)
	}
	if out := callJSON(t, sess, "kb_search", map[string]any{"query": "anything"}, nil); strings.Contains(out, "constraint") {
		t.Errorf("kb_search failed for the dev token: %s", out)
	}
}

// A LOWERED CAP MUST BIND THE SESSION THAT IS ALREADY OPEN.
//
// Rebuilding the server on a settings change reaches the NEXT session and no other: the SDK
// resolves its server only when no session exists, and an active session keeps its own alive
// indefinitely. So an operator lowering a cap during an incident would not affect the session that
// prompted the change — which is the only session that matters at that moment.
//
// Measured before the fix: one console save lowering the locker blob cap to 1 KiB, and a 4 KiB
// station_locker_put on an open session succeeded. comm never had the problem, because its tools
// read their limits per call; stations baked them into the tool closures at registration. Both
// surfaces mark every field Live:true and OPERATION.md says flatly there is no such thing as a
// restart-level setting, so the asymmetry was invisible from anywhere a human looks.
func TestALoweredStationCapBindsAnOpenSession(t *testing.T) {
	sess, handler, _ := unifiedWithHandler(t)
	if _, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "station_me", Arguments: map[string]any{"session_key": "conv-caps"},
	}); err != nil {
		t.Fatal(err)
	}

	big := strings.Repeat("x", 4096)
	// CONTROL: it fits under the default cap. Without this, the refusal below could be caused by
	// anything at all — a bad argument, a missing station — and would not be about the cap.
	if out := callJSON(t, sess, "station_locker_put", map[string]any{
		"name": "before.txt", "body": big,
	}, nil); strings.Contains(out, "too large") || strings.Contains(out, "cap") {
		t.Fatalf("the control write was already refused, so this test cannot show a cap taking effect: %s", out)
	}

	lim := store.DefaultStationLockerLimits()
	lim.MaxBlobBytes = 1024
	handler.SetStationLimits(store.DefaultStationTaskLimits(), store.DefaultStationNoteLimits(),
		lim, store.DefaultStationVaultLimits())

	// SAME SESSION, no reconnect. That is the whole point.
	out := callJSON(t, sess, "station_locker_put", map[string]any{
		"name": "after.txt", "body": big,
	}, nil)
	if !strings.Contains(out, "1024") && !strings.Contains(strings.ToLower(out), "too large") {
		t.Errorf("a 4 KiB write succeeded on an open session after the cap was lowered to 1 KiB: %s\n"+
			"The settings page reports this field as live, so an operator lowering a cap in an "+
			"incident is told it applies immediately while the session that caused the incident "+
			"keeps writing.", out)
	}
}
