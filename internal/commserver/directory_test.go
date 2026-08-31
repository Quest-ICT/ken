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

type dirRT struct {
	token string
	base  http.RoundTripper
}

func (a dirRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+a.token)
	return a.base.RoundTrip(r)
}

// dirHarness builds a COMM MCP endpoint over a knowledge base holding four stations:
// the caller's own, a published stranger, a linked-but-unstaffed peer, and an
// unpublished stranger nobody may see.
func dirHarness(t *testing.T) (*mcp.ClientSession, *store.Store, context.Context) {
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

	tok := mintToken(t, st, "dir-agent", "comm")
	actor, err := st.FindOrCreateActor(ctx, "human", "admin")
	if err != nil {
		t.Fatal(err)
	}
	mk := func(name string, published bool) *store.Station {
		t.Helper()
		s, err := st.CreateStation(ctx, name, "", actor)
		if err != nil {
			t.Fatal(err)
		}
		if published {
			if err := st.SetStationPublished(ctx, s.StationID, true); err != nil {
				t.Fatal(err)
			}
		}
		return s
	}
	mine := mk("mine", true)
	mk("published-stranger", true)
	peer := mk("linked-peer", false)
	mk("unpublished-stranger", false)

	if _, err := st.EnsureStationLink(ctx, mine.StationID, peer.StationID, actor); err != nil {
		t.Fatal(err)
	}

	// NO ENDPOINT TO REGISTER AND NOTHING TO BIND. A station comes with a mailbox, so the fixture
	// claims the caller's station for a conversation and passes that key. What used to be four
	// steps — register, mint a station key, bind, remember the secret — is one.
	prin, err := authenticate(ctx, st, tok, ScopeComm)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.ClaimStationForSession(ctx, "conv-dir", "mine", prin.ActorID, mine.StationID); err != nil {
		t.Fatal(err)
	}

	h := NewHTTPHandler(Deps{Comm: cs, Store: st})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	cli := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	sess, err := cli.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             srv.URL,
		HTTPClient:           &http.Client{Transport: dirRT{token: tok, base: http.DefaultTransport}},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Close() })

	dirComm, dirStation = cs, mine.StationID
	return sess, st, ctx
}

// dirKey is the conversation key the harness claimed the caller's station with. It replaces the
// endpoint id and secret this file used to carry: there is no endpoint credential, and a station is
// named by the conversation holding it.
const dirKey = "conv-dir"

// dirComm is the harness's comm store, exposed so a test can seed room membership
// through ReplaceRoomMirror — the same projection the running server reads — rather than
// reaching into the table behind the code under test.
var dirComm *comm.Store

// dirStation is the station the harness's endpoint is bound to — the party its room
// membership must be keyed by.
var dirStation string

func dirCreds() map[string]any {
	return map[string]any{"session_key": dirKey}
}

// dirMailbox returns the harness station's mailbox.
//
// It replaces AuthenticateEndpoint(dirEP, dirSecret). There is no endpoint credential to
// authenticate with — a station comes with a mailbox, so the lookup is by station.
func dirMailbox(t *testing.T, ctx context.Context) (*comm.Endpoint, error) {
	t.Helper()
	return dirComm.MailboxFor(ctx, dirStation, comm.Owner{TokenID: "tok", ActorID: 1})
}
