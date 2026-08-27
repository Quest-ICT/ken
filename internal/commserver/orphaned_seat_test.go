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

// *** THE SECOND DOOR: REVOKE THE OWNER TOKEN AND THE 3.40.0 GATE NEVER FIRES. ***
//
// ken-prod-ops found this on the live estate through an entirely ordinary operator action — Vlad
// revoked a machine's API tokens during credential cleanup and one of them owned a comm endpoint.
// `store.RevokeToken` writes `api_token` and nothing else; it never opens comm.db. So
// `endpoint.revoked_at` stays NULL while the endpoint is exactly as dead as a revoked one: nobody
// can present the revoked token, and any other token fails `auth`'s owner comparison.
//
// THIS TEST MUST DRIVE A REAL MCP CLIENT, and that is not a stylistic choice. The first probe
// written for this called `comm.Send` directly and reported SEND ACCEPTED against the fixed build —
// because the gate lives in the tool handler, where both databases are in hand, and comm.db cannot
// see `api_token` at all. A store-level test here is structurally blind to its own subject, which
// is the same trap prod warned about in the verification procedure for the FIRST door.
func TestSendingToASeatOwnedByARevokedTokenIsRefused(t *testing.T) {
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

	tokA := mintToken(t, st, "sender-agent", "comm")
	tokB := mintToken(t, st, "peer-agent", "comm")
	pa, err := authenticate(ctx, st, tokA, ScopeComm)
	if err != nil {
		t.Fatal(err)
	}
	pb, err := authenticate(ctx, st, tokB, ScopeComm)
	if err != nil {
		t.Fatal(err)
	}

	sender, senderSecret, err := cs.RegisterEndpoint(ctx,
		comm.Owner{TokenID: pa.TokenID, ActorID: pa.ActorID}, "sender", "")
	if err != nil {
		t.Fatal(err)
	}
	// UNBOUND: a bound seat is collectable by a successor and must keep working — see below.
	peer, _, err := cs.RegisterEndpoint(ctx,
		comm.Owner{TokenID: pb.TokenID, ActorID: pb.ActorID}, "peer", "")
	if err != nil {
		t.Fatal(err)
	}
	code, err := cs.MintPairingCode(ctx, pa.ActorID, "sender<->peer")
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
	cli := mcp.NewClient(&mcp.Implementation{Name: "orphan", Version: "0"}, nil)
	sess, err := cli.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: srv.URL,
		HTTPClient: &http.Client{Transport: hdrRT{
			token: tokA, epID: sender.EndpointID, epSecret: senderSecret, base: http.DefaultTransport,
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

	// CONTROL FIRST: while the peer's token lives, a real client sends fine.
	if res := send("before"); res.IsError {
		t.Fatalf("a healthy channel refused a real MCP send: %s", resultText(res))
	}

	// THE ORDINARY OPERATOR ACTION. Not the endpoint — its OWNER TOKEN.
	if err := st.RevokeToken(ctx, pb.TokenID); err != nil {
		t.Fatal(err)
	}
	// And confirm the premise rather than assume it: endpoint.revoked_at is untouched, so the
	// 3.40.0 gate genuinely cannot see this. If this ever stops being true the test below is
	// passing for the wrong reason.
	if _, _, err := cs.ChannelFor(ctx, sender, ch.ChannelID); err != nil {
		t.Fatalf("ChannelFor now refuses this, so the premise changed and this test no longer "+
			"exercises the second door: %v", err)
	}

	res := send("into the second door")
	if !res.IsError {
		t.Fatal("a real MCP client sent to a seat owned by a revoked token, and got a success result")
	}
	got := resultText(res)
	// MATCHED ON THE DISCRIMINATOR, NEVER ON "revoked" — prod's point about the FIRST door applies
	// here verbatim: the generic channel-closed string also contains that word, so asserting it
	// would pass on the failure.
	if !strings.Contains(got, "revoked token") || !strings.Contains(got, "never bound to a station") {
		t.Fatalf("the caller reads %q — which does not tell them the peer is permanently gone", got)
	}
	if strings.Contains(got, "both sessions must join the pairing code") {
		t.Errorf("the caller got the generic channel-closed advice: %q", got)
	}
}

// *** THE HALF THAT MUST NOT BREAK, AGAIN: A BOUND SEAT SURVIVES ITS OWNER TOKEN. ***
//
// Its mail files under `s:<station>`, so a successor endpoint on that station collects it. Revoking
// a machine's tokens is routine credential cleanup — if it silently killed every station-bound
// channel that machine ever opened, the cleanup would be destroying working relationships.
func TestSendingToABoundSeatSurvivesItsOwnerTokenBeingRevoked(t *testing.T) {
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

	tokA := mintToken(t, st, "sender-agent", "comm")
	tokB := mintToken(t, st, "peer-agent", "comm")
	pa, _ := authenticate(ctx, st, tokA, ScopeComm)
	pb, _ := authenticate(ctx, st, tokB, ScopeComm)

	sender, senderSecret, err := cs.RegisterEndpoint(ctx,
		comm.Owner{TokenID: pa.TokenID, ActorID: pa.ActorID}, "sender", "")
	if err != nil {
		t.Fatal(err)
	}
	peer, _, err := cs.RegisterEndpoint(ctx,
		comm.Owner{TokenID: pb.TokenID, ActorID: pb.ActorID}, "peer", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := cs.BindEndpointToStation(ctx, peer.EndpointID, "stn_prod", "kens_b"); err != nil {
		t.Skipf("cannot bind in this harness (%v) — the bound case is covered in internal/comm", err)
	}
	code, _ := cs.MintPairingCode(ctx, pa.ActorID, "sender<->peer")
	if _, err := cs.JoinChannel(ctx, sender, code); err != nil {
		t.Fatal(err)
	}
	ch, err := cs.JoinChannel(ctx, peer, code)
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(NewHTTPHandler(Deps{Comm: cs, Store: st}))
	t.Cleanup(srv.Close)
	cli := mcp.NewClient(&mcp.Implementation{Name: "bound", Version: "0"}, nil)
	sess, err := cli.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: srv.URL,
		HTTPClient: &http.Client{Transport: hdrRT{
			token: tokA, epID: sender.EndpointID, epSecret: senderSecret, base: http.DefaultTransport,
		}},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })

	if err := st.RevokeToken(ctx, pb.TokenID); err != nil {
		t.Fatal(err)
	}
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name:      "comm_send",
		Arguments: map[string]any{"channel_id": ch.ChannelID, "body": "a successor should get this"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("a BOUND seat was refused after its owner token was revoked: %s\n"+
			"This breaks successor inheritance — the mail files under s:<station> and the next "+
			"session on that post collects it.", resultText(res))
	}
}
