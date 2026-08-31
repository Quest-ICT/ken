package commserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Quest-ICT/ken/internal/comm"
	"github.com/Quest-ICT/ken/internal/store"
)

// bearerRT carries the ONLY credential comm accepts now: an OAuth-backed bearer.
//
// It replaces hdrRT, which also carried X-Ken-Endpoint-Id and X-Ken-Endpoint-Secret. Those headers
// are deleted along with the endpoint identity — a caller proves a grant and names a station, and
// the mailbox follows from the station.
type bearerRT struct {
	token string
	base  http.RoundTripper
}

func (a bearerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+a.token)
	return a.base.RoundTrip(r)
}

// commHarness stands up the real surface and returns a client plus a station the caller owns.
//
// EVERY COMM TEST NEEDS A STATION NOW, because a station IS the mailbox. There is nothing to
// register and nothing to bind, so the fixture that used to mint an endpoint and a secret mints a
// station instead and passes its session_key.
func commHarness(t *testing.T, label string) (*store.Store, *comm.Store, *mcp.ClientSession, string, string) {
	t.Helper()
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

	tok := mintToken(t, st, label+"-agent", "comm")
	p, err := authenticate(ctx, st, tok, ScopeComm)
	if err != nil {
		t.Fatal(err)
	}
	stn, err := st.CreateStation(ctx, label, "", p.ActorID)
	if err != nil {
		t.Fatal(err)
	}
	key := "conv-" + label
	if _, _, err := st.ClaimStationForSession(ctx, key, label, p.ActorID, stn.StationID); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(testHandler(t, Deps{Comm: cs, Store: st}))
	t.Cleanup(srv.Close)
	cli := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	sess, err := cli.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             srv.URL,
		HTTPClient:           &http.Client{Transport: bearerRT{token: tok, base: http.DefaultTransport}},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return st, cs, sess, stn.StationID, key
}

// resultText flattens a tool result to the text a session would read.
func resultText(res *mcp.CallToolResult) string {
	var b []byte
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b = append(b, tc.Text...)
		}
	}
	return string(b)
}
