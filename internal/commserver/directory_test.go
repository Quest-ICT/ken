package commserver

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

	reqID, err := st.CreateStationLinkRequest(ctx, "tok", mine.StationID, peer.StationID, "r", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ApproveLinkRequest(ctx, reqID, actor); err != nil {
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

// comm_open_channel must refuse every unavailable target IDENTICALLY.
//
// The three branches used to be distinguishable: "no such station", "not linked", and
// "nobody is staffing <name>" — which let a caller separate existence from
// non-existence, then linked from unlinked, and the third echoed the RESOLVED name so
// guessing "PROD" confirmed the station is really called "prod".
//
// Asserting each call errors would NOT catch a regression here; only comparing the
// texts byte for byte does, which is the point of the const they all share.
func TestOpenChannelRefusalsAreIndistinguishable(t *testing.T) {
	sess, _, ctx := dirHarness(t)

	texts := map[string]string{}
	for _, c := range []struct{ label, target string }{
		{"does not exist", "no-such-station-anywhere"},
		{"exists, not linked", "published-stranger"},
		{"exists and linked, nobody staffing", "linked-peer"},
		{"exists but invisible to me", "unpublished-stranger"},
		{"case-probe of a real name", "LINKED-PEER"},
	} {
		args := dirCreds()
		args["to_station"] = c.target
		res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "comm_open_channel", Arguments: args})
		if err != nil {
			t.Fatalf("%s: transport error: %v", c.label, err)
		}
		if !res.IsError {
			t.Fatalf("%s: the call SUCCEEDED against %q", c.label, c.target)
		}
		txt := ""
		for _, ct := range res.Content {
			if tc, ok := ct.(*mcp.TextContent); ok {
				txt += tc.Text
			}
		}
		if txt == "" {
			t.Fatalf("%s: refusal carried no text", c.label)
		}
		texts[c.label] = txt
	}

	// Anchor to the EXPECTED text first. Comparing the refusals only to each other is
	// vacuous: if every call failed earlier — at auth, say — they would all match and
	// the test would certify an oracle that is still wide open. This is exactly what
	// happened on the first run of this test, and the assertion below is the fix.
	for label, txt := range texts {
		if !strings.Contains(txt, errStationUnavailable) {
			t.Fatalf("%s: refusal was not the unified one, so this call never reached the branch under test:\n  got:  %q\n  want: %q", label, txt, errStationUnavailable)
		}
	}
	var first, firstLabel string
	for label, txt := range texts {
		if first == "" {
			first, firstLabel = txt, label
			continue
		}
		if txt != first {
			t.Fatalf("refusals differ and therefore leak:\n  %s: %q\n  %s: %q", firstLabel, first, label, txt)
		}
	}
	// And it must never echo a name back — that is how the case-probe worked.
	for label, txt := range texts {
		for _, leak := range []string{"linked-peer", "LINKED-PEER", "published-stranger", "unpublished-stranger"} {
			if strings.Contains(txt, leak) {
				t.Fatalf("%s: the refusal echoes %q back to the caller", label, leak)
			}
		}
	}
}

// The directory answers what the refusal deliberately will not, and it answers it
// per-asker: published stations plus my own established peers, and nothing else.
func TestCommDirectoryListsOnlyWhatTheAskerMaySee(t *testing.T) {
	sess, _, ctx := dirHarness(t)

	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "comm_directory", Arguments: dirCreds()})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		msg := ""
		for _, ct := range res.Content {
			if tc, ok := ct.(*mcp.TextContent); ok {
				msg += tc.Text
			}
		}
		t.Fatalf("comm_directory errored: %s", msg)
	}
	var out directoryOut
	if res.StructuredContent != nil {
		b, _ := json.Marshal(res.StructuredContent)
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatal(err)
		}
	}
	if out.YouAre != "mine" {
		t.Errorf("you_are = %q, want %q — a session handed a list of names must know which one it is", out.YouAre, "mine")
	}
	byName := map[string]directoryEntry{}
	for _, e := range out.Stations {
		byName[e.Name] = e
	}
	if len(out.Stations) != 2 {
		t.Fatalf("directory returned %d stations, want 2: %+v", len(out.Stations), out.Stations)
	}
	if _, ok := byName["unpublished-stranger"]; ok {
		t.Error("an unpublished station with no link to me is listed — the directory leaks every station's existence")
	}
	if _, ok := byName["mine"]; ok {
		t.Error("the asking station lists itself")
	}
	if e := byName["published-stranger"]; e.Linked {
		t.Error("a published station I hold no link to reports linked=true")
	}
	if e, ok := byName["linked-peer"]; !ok {
		t.Error("my established peer is not listed")
	} else if !e.Linked {
		t.Error("my established peer reports linked=false")
	} else if e.Staffed == nil {
		// COMM has never seen an endpoint for it, so staffing is genuinely unknown
		// and must be OMITTED rather than reported false.
		t.Log("staffing omitted for a station COMM has never seen — correct")
	} else if *e.Staffed {
		t.Error("a station with no endpoint reports staffed=true")
	}
}

// dirMailbox returns the harness station's mailbox.
//
// It replaces AuthenticateEndpoint(dirEP, dirSecret). There is no endpoint credential to
// authenticate with — a station comes with a mailbox, so the lookup is by station.
func dirMailbox(t *testing.T, ctx context.Context) (*comm.Endpoint, error) {
	t.Helper()
	return dirComm.MailboxFor(ctx, dirStation, comm.Owner{TokenID: "tok", ActorID: 1})
}
