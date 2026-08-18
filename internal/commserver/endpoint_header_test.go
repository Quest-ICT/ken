package commserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Quest-ICT/ken/internal/comm"
)

// THE CREDENTIAL MAY TRAVEL IN A HEADER INSTEAD OF A TOOL ARGUMENT.
//
// Tool arguments are recorded by the CLIENT in its transcript, on disk, in the clear. Ken cannot
// mitigate that by changing what Ken logs — the recording happens in software neither end ships.
// Moving the credential out of the argument position is the only thing that removes it.
//
// Both arms matter. Extracting when the headers are present is the feature; leaving the context
// UNTOUCHED when they are absent is what keeps every already-running session working, because a
// tool's input schema is captured when its conversation begins and never refreshes.
func TestEndpointCredFromHeader(t *testing.T) {
	base := context.Background()

	t.Run("present: lifted into context", func(t *testing.T) {
		req := &mcp.CallToolRequest{Extra: &mcp.RequestExtra{Header: http.Header{}}}
		req.Extra.Header.Set(hdrEndpointID, "ep-123")
		req.Extra.Header.Set(hdrEndpointSecret, "sec-456")

		got, ok := withEndpointCred(base, req).Value(endpointCredKey{}).(endpointCred)
		if !ok {
			t.Fatal("headers were present and nothing reached the context")
		}
		if got.id != "ep-123" || got.secret != "sec-456" {
			t.Errorf("got %+v, want {ep-123 sec-456}", got)
		}
	})

	// CONTROL. Without this arm a function that stuffed something into the context
	// unconditionally would pass the arm above and break every session still sending
	// arguments.
	for _, c := range []struct {
		name string
		set  map[string]string
	}{
		{"absent entirely", nil},
		{"id only", map[string]string{hdrEndpointID: "ep-123"}},
		{"secret only", map[string]string{hdrEndpointSecret: "sec-456"}},
		{"blank values", map[string]string{hdrEndpointID: "  ", hdrEndpointSecret: "  "}},
	} {
		t.Run("absent: context untouched — "+c.name, func(t *testing.T) {
			req := &mcp.CallToolRequest{Extra: &mcp.RequestExtra{Header: http.Header{}}}
			for k, v := range c.set {
				req.Extra.Header.Set(k, v)
			}
			if _, ok := withEndpointCred(base, req).Value(endpointCredKey{}).(endpointCred); ok {
				t.Error("a partial or empty header pair was accepted — a half-supplied credential " +
					"must fall through to the arguments, not authenticate as an empty one")
			}
		})
	}

	// A nil request must not panic: the wrap runs on every call to every tool.
	t.Run("nil request and nil extra are survivable", func(t *testing.T) {
		if _, ok := withEndpointCred(base, nil).Value(endpointCredKey{}).(endpointCred); ok {
			t.Error("nil request produced a credential")
		}
		if _, ok := withEndpointCred(base, &mcp.CallToolRequest{}).Value(endpointCredKey{}).(endpointCred); ok {
			t.Error("nil Extra produced a credential")
		}
	})
}

// hdrRT sets the bearer AND, optionally, the endpoint credential headers.
type hdrRT struct {
	token, epID, epSecret string
	base                  http.RoundTripper
}

func (a hdrRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+a.token)
	if a.epID != "" {
		r.Header.Set(hdrEndpointID, a.epID)
	}
	if a.epSecret != "" {
		r.Header.Set(hdrEndpointSecret, a.epSecret)
	}
	return a.base.RoundTrip(r)
}

// THE WRAP IS ACTUALLY INSTALLED — proven over real HTTP, not by calling the extractor.
//
// The unit test above exercises withEndpointCred directly, and a mutation run showed why that is
// not enough: DELETING the wrap from addTool left that test passing. That is the same shape as a
// function with no callers, which this project has shipped before — the piece works and nothing
// invokes it.
//
// So this drives a real tool call through NewHTTPHandler with the credential ONLY in headers and
// no endpoint_id/endpoint_secret arguments at all. If the wrap is not in the chain, auth() finds
// nothing in the context, sees empty arguments, and refuses.
func TestEndpointCredHeaderWorksEndToEnd(t *testing.T) {
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
	tok := mintToken(t, st, "hdr-agent", "comm")
	prin, err := authenticate(ctx, st, tok, ScopeComm)
	if err != nil {
		t.Fatal(err)
	}
	ep, secret, err := cs.RegisterEndpoint(ctx,
		comm.Owner{TokenID: prin.TokenID, ActorID: prin.ActorID, SpaceID: prin.SpaceID}, "hdr", "")
	if err != nil {
		t.Fatal(err)
	}

	connect := func(epID, epSecret string) *mcp.ClientSession {
		t.Helper()
		srv := httptest.NewServer(NewHTTPHandler(Deps{Comm: cs, Store: st}))
		t.Cleanup(srv.Close)
		cli := mcp.NewClient(&mcp.Implementation{Name: "hdr", Version: "0"}, nil)
		sess, err := cli.Connect(ctx, &mcp.StreamableClientTransport{
			Endpoint: srv.URL,
			HTTPClient: &http.Client{Transport: hdrRT{
				token: tok, epID: epID, epSecret: epSecret, base: http.DefaultTransport,
			}},
			DisableStandaloneSSE: true,
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = sess.Close() })
		return sess
	}

	call := func(sess *mcp.ClientSession) (*mcp.CallToolResult, error) {
		return sess.CallTool(ctx, &mcp.CallToolParams{
			Name: "comm_channels", Arguments: map[string]any{},
		})
	}

	// CONTROL FIRST: neither headers nor arguments must be REFUSED. Without this arm, a tool
	// that ignored credentials entirely would sail through the positive arm below.
	res, err := call(connect("", ""))
	if err == nil && (res == nil || !res.IsError) {
		t.Fatal("comm_channels succeeded with NO credential at all — the negative control " +
			"failed, so the positive arm would prove nothing")
	}

	// THE POSITIVE ARM: credential in headers, arguments empty.
	res, err = call(connect(ep.EndpointID, secret))
	if err != nil {
		t.Fatalf("comm_channels refused a credential supplied in HEADERS: %v.\n"+
			"Either the per-call wrap is missing from addTool or auth() is not preferring the "+
			"context credential — the whole point is that the secret never has to appear in a "+
			"tool argument.", err)
	}
	if res != nil && res.IsError {
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				t.Fatalf("comm_channels returned a tool error with a header credential: %s", tc.Text)
			}
		}
		t.Fatalf("comm_channels returned a tool error with a header credential: %+v", res.Content)
	}
}
