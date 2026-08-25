package stationserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// *** THE DEADLOCK, WALKED END TO END: A SESSION WITH NO STATION GETS ONE AND WORKS. ***
//
// What this replaces, verified against v3.21.0 source on 2026-08-25 and reported by ken-prod-ops
// after Vlad sat at the console unable to give a session Station — his words, quoted rather than
// softened: **"It is absurd the way it works now."**
//
//	station_request lives on /station/mcp
//	/station/mcp    required a station-scoped token
//	a station key   is minted per-station, for an EXISTING station
//	a station       is created by approving a station_request
//
// Every arrow points back one step. `station_request`'s own description said it was "the only tool
// a key with no station may call" — true, and it hid the real constraint: a key with no station
// could call it, and a session with no KEY could not, which is every session being onboarded.
// There was no POST /stations either, so on a deployment with zero stations the first one could
// only come from the CLI — and a console-minted key was issued under the OPERATOR's actor, so it
// could never bind the session it was minted for. Console-first and binding were mutually
// exclusive.
//
// docs/IDENTITY.md §5 decides it: "fully working, auto-named, no approval."
func TestASessionWithNoWorkspaceGetsOneAndWorksImmediately(t *testing.T) {
	d, p := onboardingHarness(t)
	ctx := context.WithValue(context.Background(), ctxKey{}, p)

	if p.StationID != "" {
		t.Fatal("fixture: this principal already has a station, so it cannot exercise onboarding")
	}

	out, err := claimWorkspace(ctx, d, p, "ken-public")
	if err != nil {
		t.Fatalf("a session with no workspace could not get one: %v — the deadlock is still closed", err)
	}
	if out.StationID == "" {
		t.Fatal("no workspace id came back; the session has nothing to put in its config")
	}
	if out.Name != "ken-public" {
		t.Errorf("name = %q, want the folder basename — the auto-name is what the human sees on the "+
			"link-approval screen, which is the one moment a bad name matters", out.Name)
	}
	if out.NameSource != "auto" {
		t.Errorf("name_source = %q; a human must be able to tell a name Ken guessed from one they chose", out.NameSource)
	}
	if !out.JustCreated {
		t.Error("the result does not say a workspace was just created, so a session cannot tell " +
			"'here is your briefing' from 'here is your new workspace'")
	}

	// THE ID MUST REACH THE HUMAN, and the result is the only channel that always arrives.
	// A workspace whose id dies with the conversation is a workspace the next session in this
	// folder cannot find — it would mint a second one, and a third.
	for _, want := range []string{out.StationID, WorkspaceHeader, "rename"} {
		if !strings.Contains(out.PutThisInYourConfig, want) {
			t.Errorf("the instruction handed back does not mention %q; without it the next session "+
				"here starts with no workspace and mints another", want)
		}
	}

	// AND IT IS NOT WITHHELD PENDING ANYTHING. §5: "The session works immediately: notebook,
	// tasks, vault, knowledge base. Nothing withheld."
	ok, err := d.Store.StationExists(ctx, out.StationID)
	if err != nil || !ok {
		t.Fatalf("the minted workspace is not live (%v, %v) — it was created pending something", ok, err)
	}
}

// A MISSING FOLDER NAME STILL GETS A WORKSPACE.
//
// Withholding one over an absent hint would rebuild the deadlock for exactly the case it was built
// for: a session that does not know what to call itself is still a session that cannot work.
func TestAWorkspaceIsMintedEvenWithNoFolderName(t *testing.T) {
	d, p := onboardingHarness(t)
	out, err := claimWorkspace(context.WithValue(context.Background(), ctxKey{}, p), d, p, "")
	if err != nil {
		t.Fatalf("no name hint meant no workspace: %v", err)
	}
	if out.StationID == "" || out.Name == "" {
		t.Fatalf("got an unusable workspace: id=%q name=%q", out.StationID, out.Name)
	}
}

// TWO FOLDERS WITH THE SAME NAME BOTH WORK.
//
// Names are unique per space, so a collision must decorate rather than refuse — a second
// `ken-public` on one machine is ordinary, and a session that cannot start because another folder
// took the name first is a session waiting on a human again. The IDS are what differ, which is
// COMM.md §3's rule: "a human-chosen name is never an address."
func TestASecondFolderWithTheSameNameStillGetsAWorkspace(t *testing.T) {
	d, p := onboardingHarness(t)
	ctx := context.WithValue(context.Background(), ctxKey{}, p)

	first, err := claimWorkspace(ctx, d, p, "ken-public")
	if err != nil {
		t.Fatal(err)
	}
	second, err := claimWorkspace(ctx, d, p, "ken-public")
	if err != nil {
		t.Fatalf("a second folder with the same name was refused a workspace: %v — that is the "+
			"deadlock again, in miniature", err)
	}
	if second.StationID == first.StationID {
		t.Fatal("both folders got the SAME workspace — they would share a notebook, a task list and a vault")
	}
	if second.Name == first.Name {
		t.Errorf("both are named %q; the human cannot tell them apart on the approval screen", second.Name)
	}
	if !strings.HasPrefix(second.Name, "ken-public") {
		t.Errorf("the disambiguated name %q lost its relationship to the folder", second.Name)
	}
}

// onboardingHarness builds a store and a principal with NO station — the state every session being
// onboarded is in, and the one the old design had no exit from.
func onboardingHarness(t *testing.T) (Deps, *principal) {
	t.Helper()
	st, _, _, _ := harness(t, ScopeStation)
	actor, err := st.FindOrCreateActor(context.Background(), "ai", "onboarding-session")
	if err != nil {
		t.Fatal(err)
	}
	return Deps{Store: st}, &principal{
		ActorID: actor,
		TokenID: "oauth-1",
		Scopes:  map[string]bool{ScopeStation: true},
		// StationID deliberately empty.
	}
}

// wsRT adds the bearer AND a workspace header, so a test can drive the real transport the way a
// folder's MCP entry does.
type wsRT struct {
	token, workspace string
	base             http.RoundTripper
}

func (w wsRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Header.Set("Authorization", "Bearer "+w.token)
	if w.workspace != "" {
		r.Header.Set(WorkspaceHeader, w.workspace)
	}
	return w.base.RoundTrip(r)
}

func connectWS(t *testing.T, srv *httptest.Server, token, workspace string) *mcp.ClientSession {
	t.Helper()
	cli := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	sess, err := cli.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:             srv.URL,
		HTTPClient:           &http.Client{Transport: wsRT{token: token, workspace: workspace, base: http.DefaultTransport}},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess
}

func meOverTransport(t *testing.T, sess *mcp.ClientSession, args map[string]any) meOut {
	t.Helper()
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "station_me", Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("station_me errored: %+v", res.Content)
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var out meOut
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// *** THE WORKSPACE HEADER RESOLVES A SESSION TO A WORKSPACE, OVER THE REAL TRANSPORT. ***
//
// docs/IDENTITY.md §4: the folder's MCP entry carries a stable opaque workspace id, and the
// credential proves who. Asserted here rather than on the middleware in isolation, because the
// property is about what a CLIENT sending a header actually gets — and a unit test of the resolver
// passes whether or not the header is ever read off the request.
//
// Mutation found the gap: deleting the assignment `sp.StationID = ws` left the whole suite green.
func TestTheWorkspaceHeaderSelectsTheWorkspace(t *testing.T) {
	st, srv, _, station := harness(t)
	ctx := context.Background()

	// A station-LESS key: the state an OAuth grant arrives in, reachable here without one.
	actor, err := st.FindOrCreateActor(ctx, "ai", "roaming-session")
	if err != nil {
		t.Fatal(err)
	}
	roaming, err := st.IssueStationKey(ctx, actor, "", "no-station", []string{ScopeStation})
	if err != nil {
		t.Fatal(err)
	}

	// WITHOUT the header it has no workspace, so station_me mints one — the control proving the
	// header is what does the work below, not the credential.
	//
	// ON A SEPARATE KEY, DELIBERATELY. Driving both halves through one bearer confounds the test:
	// two MCP sessions on one credential share server-side session state, and the second call read
	// the first's principal — which looked exactly like "the header was ignored". Two folders in
	// production hold two credentials anyway, so this is also the honest fixture.
	control, err := st.IssueStationKey(ctx, actor, "", "control-key", []string{ScopeStation})
	if err != nil {
		t.Fatal(err)
	}
	minted := meOverTransport(t, connectWS(t, srv, control, ""), map[string]any{"workspace_name": "somewhere"})
	if !minted.JustCreated {
		t.Fatal("a station-less session with no header did not get a workspace; the fixture is wrong")
	}

	// WITH the header it works as that workspace, and is briefed on it rather than given a new one.
	got := meOverTransport(t, connectWS(t, srv, roaming, station.StationID), map[string]any{})
	if got.JustCreated {
		t.Error("the session minted a NEW workspace despite declaring one — the header was ignored, " +
			"so every folder would accumulate a fresh workspace on every connection")
	}
	if got.StationID != station.StationID {
		t.Errorf("session resolved to %q, want the declared %q", got.StationID, station.StationID)
	}
	if got.Name != station.Name {
		t.Errorf("name = %q, want %q — the session is briefed on the wrong post", got.Name, station.Name)
	}
}

// *** station_me IS THE PATH, and the auto-provision must be ON it. ***
//
// §5's flow works because station_me is the call every session is told to make first — the fix
// lands on a path a session already walks. Wiring it anywhere else means an onboarding session has
// to know to look, which is the thing that was broken.
//
// Mutation found this too: disabling the branch in the handler left everything green, because the
// other tests call claimWorkspace directly.
func TestStationMeMintsAWorkspaceForASessionThatHasNone(t *testing.T) {
	st, srv, _, _ := harness(t)
	actor, err := st.FindOrCreateActor(context.Background(), "ai", "fresh-session")
	if err != nil {
		t.Fatal(err)
	}
	key, err := st.IssueStationKey(context.Background(), actor, "", "fresh", []string{ScopeStation})
	if err != nil {
		t.Fatal(err)
	}

	out := meOverTransport(t, connectWS(t, srv, key, ""), map[string]any{"workspace_name": "ken-public"})
	if !out.JustCreated || out.StationID == "" {
		t.Fatalf("station_me did not mint a workspace for a session with none: %+v\n"+
			"That is the deadlock: station_request needed a station-scoped credential, the console "+
			"could not create a station, and a console-minted key could never bind the session it "+
			"was minted for.", out)
	}
	if !strings.Contains(out.PutThisInYourConfig, out.StationID) {
		t.Error("the workspace id was not handed back in a form the human can act on; the next " +
			"session in this folder would mint another")
	}
}
