package commserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Quest-ICT/ken/internal/comm"
)

// *** THE ASSERTION NEITHER MY SUITE NOR PRODUCTION COULD MAKE, MADE HERE. ***
//
// ken-prod-ops took 3.40.0, satisfied the migration declaration 15/15, and then said the thing
// worth saying: the check I most wanted — the dead-seat refusal reaching a REAL CALLER over the
// wire, naming revocation rather than the generic pairing-code advice — "remains unmade". It could
// not run it: on that deployment zero revoked endpoints sit on a channel, and manufacturing one
// means revoking an endpoint on a LIVE channel, which is a console action that breaks working
// traffic to test a gate. Correctly refused.
//
// THREE LAYERS, AND EACH ONE HAS ALREADY HIDDEN A DEFECT OF EXACTLY THIS KIND:
//
//	comm.ChannelFor   raises the refusal        — a store test here survived a CallerSafe-less mutant
//	commError         maps it for the caller    — 3.3.0 shipped a correct string this layer discarded
//	the MCP handler   serializes it to a client — UNTESTED until now, and the layer the caller reads
//
// The middle layer is covered by TestTheDeadSeatRefusalSurvivesTheErrorMapper. This one crosses the
// last seam: a real MCP client, a real HTTP transport, a real tools/call, reading the text a
// session would actually see. If the SDK replaced the message with something generic, every test
// below that seam would still pass and the caller would still learn nothing.
func TestTheDeadSeatRefusalReachesAnMCPCaller(t *testing.T) {
	ctx := context.Background()
	st := newKB(t)
	cs, err := comm.Open(filepath.Join(t.TempDir(), "comm.db"), comm.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	if err := cs.Migrate(); err != nil {
		t.Fatal(err)
	}

	tok := mintToken(t, st, "wire-agent", "comm")
	prin, err := authenticate(ctx, st, tok, ScopeComm)
	if err != nil {
		t.Fatal(err)
	}
	owner := comm.Owner{TokenID: prin.TokenID, ActorID: prin.ActorID}

	sender, senderSecret, err := cs.RegisterEndpoint(ctx, owner, "sender", "")
	if err != nil {
		t.Fatal(err)
	}
	// UNBOUND, deliberately: a bound peer keys as s:<station> and is collectable by a successor.
	peer, _, err := cs.RegisterEndpoint(ctx, owner, "peer", "")
	if err != nil {
		t.Fatal(err)
	}
	code, err := cs.MintPairingCode(ctx, prin.ActorID, "sender<->peer")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cs.JoinChannel(ctx, sender, code); err != nil {
		t.Fatal(err)
	}
	ch, err := cs.JoinChannel(ctx, peer, code)
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(NewHTTPHandler(Deps{Comm: cs, Store: st}))
	t.Cleanup(srv.Close)
	cli := mcp.NewClient(&mcp.Implementation{Name: "wire", Version: "0"}, nil)
	sess, err := cli.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: srv.URL,
		HTTPClient: &http.Client{Transport: hdrRT{
			token: tok, epID: sender.EndpointID, epSecret: senderSecret, base: http.DefaultTransport,
		}},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })

	send := func(body string) *mcp.CallToolResult {
		t.Helper()
		res, err := sess.CallTool(ctx, &mcp.CallToolParams{
			Name:      "comm_send",
			Arguments: map[string]any{"channel_id": ch.ChannelID, "body": body},
		})
		if err != nil {
			t.Fatalf("transport error rather than a tool result: %v", err)
		}
		return res
	}

	// CONTROL FIRST: while both seats live, a real client can send. Without this arm the refusal
	// below could be a broken fixture, a bad credential, or the wrong channel id — any of which
	// would produce an error containing nothing about revocation and fail for the wrong reason.
	if res := send("before"); res.IsError {
		t.Fatalf("a healthy channel refused a real MCP send: %s", resultText(res))
	}

	if err := cs.RevokeEndpoint(ctx, peer.EndpointID); err != nil {
		t.Fatal(err)
	}

	res := send("nobody can ever read this")
	if !res.IsError {
		t.Fatal("a real MCP client sent into a seat nothing can ever hold, and got a success result")
	}
	got := resultText(res)

	if !strings.Contains(got, "revoked") {
		t.Fatalf("the CALLER reads %q.\n"+
			"The refusal names revocation two layers down and does not arrive here — which is the "+
			"3.3.0 defect exactly: a correct string in the binary that no session can see.", got)
	}
	if !strings.Contains(got, "to_station") {
		t.Errorf("the caller is not told what to do instead: %q", got)
	}
	// AND NOT THE GENERIC ADVICE. Re-joining a pairing code cannot help a channel that is open,
	// so this string would send a session round a loop that cannot terminate.
	if strings.Contains(got, "both sessions must join the pairing code") {
		t.Errorf("the caller got the generic channel-closed advice: %q", got)
	}
}

// resultText flattens a tool result's content to the text a session would read.
func resultText(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}
