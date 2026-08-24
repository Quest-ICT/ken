package commserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Quest-ICT/ken/internal/comm"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// *** THE OWNERSHIP CHECK HAD NO TEST. ***
//
// `if ep.Owner.TokenID != p.TokenID` runs at the tail of auth() and every endpoint-bearing
// comm tool passes through it. Delete it and the whole suite stays green: grep its error
// string across *_test.go and you get exactly one hit, and that hit is a COMMENT in
// directory_test.go explaining how another test avoids TRIGGERING it.
//
// So the one control between "holds a valid endpoint id and secret" and "is the right
// principal" was load-bearing and unpinned. It is written FIRST, before anything re-points
// that column, because a characterisation test written after a change cannot tell you
// whether the behaviour it records is the behaviour you had.
func TestAnEndpointIsRefusedToATokenThatDoesNotOwnIt(t *testing.T) {
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

	mineTok := mintToken(t, st, "owner-agent", "comm")
	prin, err := authenticate(ctx, st, mineTok, ScopeComm)
	if err != nil {
		t.Fatal(err)
	}
	ep, secret, err := cs.RegisterEndpoint(ctx, comm.Owner{
		TokenID: prin.TokenID, ActorID: prin.ActorID, SpaceID: prin.SpaceID}, "mine", "")
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(NewHTTPHandler(Deps{Comm: cs, Store: st}))
	t.Cleanup(srv.Close)

	channels := func(tok string) error {
		t.Helper()
		cli := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
		sess, err := cli.Connect(ctx, &mcp.StreamableClientTransport{
			Endpoint:             srv.URL,
			HTTPClient:           &http.Client{Transport: dirRT{token: tok, base: http.DefaultTransport}},
			DisableStandaloneSSE: true,
		}, nil)
		if err != nil {
			return err
		}
		defer sess.Close()
		res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "comm_channels", Arguments: map[string]any{
			"endpoint_id": ep.EndpointID, "endpoint_secret": secret}})
		if err != nil {
			return err
		}
		if res.IsError {
			return errString(res)
		}
		return nil
	}

	// CONTROL FIRST: the OWNING token drives it. Without this the refusal below could be
	// any of a dozen unrelated failures and the test would pass for the wrong reason.
	if err := channels(mineTok); err != nil {
		t.Fatalf("the owning token was refused its own endpoint: %v", err)
	}

	// A second, entirely valid comm token — same human, same machine, different credential.
	// That is the realistic shape rather than a contrived one: a machine can hold several,
	// and an endpoint_id is NOT a secret. It is the routing address, rendered on /comm and
	// printed throughout the runbooks.
	otherTok := mintToken(t, st, "other-agent", "comm")
	err = channels(otherTok)
	if err == nil {
		t.Fatal("a different token drove an endpoint it does not own — the ownership check is gone")
	}
	if !strings.Contains(err.Error(), "does not belong to this token") {
		t.Fatalf("refused, but not by the ownership check: %v", err)
	}
}

func errString(res *mcp.CallToolResult) error {
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			return &plainErr{tc.Text}
		}
	}
	return &plainErr{"tool error with no text"}
}

type plainErr struct{ s string }

func (e *plainErr) Error() string { return e.s }
