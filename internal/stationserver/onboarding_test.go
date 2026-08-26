package stationserver

import (
	"context"
	"encoding/json"
	"github.com/Quest-ICT/ken/internal/version"
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
// Names are unique per instance, so a collision must decorate rather than refuse — a second
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

// *** A PLAIN ken_ TOKEN ONBOARDS, BECAUSE THE CLIENT THAT REPORTED THIS CANNOT RUN OAUTH. ***
//
// 3.25.0 let an OAuth grant reach this surface and 3.26.0 let a session mint its own workspace.
// Both were true and both were unreachable for the session that reported the deadlock:
// ken-prod-ops measured that Vlad runs Claude Code inside the desktop app, where sessions are
// NON-INTERACTIVE and cannot perform an OAuth sign-in at all. The client's own message to him:
// "This session is non-interactive, so Claude cannot run the OAuth flow here."
//
// **The fix that unlocked everything for an OAuth client unlocked nothing for his**, and the wall
// was upstream of every check in this package — the flow never reached discovery, never reached
// the scope check, never reached anything. A `ken_` token is the credential such a session already
// holds and can be handed by hand.
//
// This is the fourth wall in a row on one feature: the tool needed a credential nobody could get,
// then the surface refused the credential, then nothing advertised that it had stopped refusing,
// and then the client could not perform the flow being advertised.
func TestAPlainAPITokenCanOnboard(t *testing.T) {
	st, srv, _, _ := harness(t)
	ctx := context.Background()

	actor, err := st.FindOrCreateActor(ctx, "ai", "desktop-session")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := st.IssueToken(ctx, actor, []string{ScopeStation}, "non-interactive client")
	if err != nil {
		t.Fatal(err)
	}

	out := meOverTransport(t, connectWS(t, srv, tok, ""), map[string]any{"workspace_name": "m600"})
	if !out.JustCreated || out.StationID == "" {
		t.Fatalf("a plain ken_ token could not onboard: %+v\nEvery other path needs a browser the "+
			"reporting session does not have", out)
	}
	if out.Name != "m600" {
		t.Errorf("name = %q, want the folder name", out.Name)
	}

	// AND THE SCOPE STILL GATES IT. A comm-only token gains nothing by arriving here — the
	// credential is a different door, not a weaker one.
	commOnly, err := st.IssueToken(ctx, actor, []string{"comm"}, "comm only")
	if err != nil {
		t.Fatal(err)
	}
	cli := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	if _, err := cli.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             srv.URL,
		HTTPClient:           &http.Client{Transport: wsRT{token: commOnly, base: http.DefaultTransport}},
		DisableStandaloneSSE: true,
	}, nil); err == nil {
		t.Error("a token without the station scope reached /station/mcp — the new door is a door, " +
			"not a hole")
	}
}

// *** THE 401 CARRIES THE DISCOVERY CHALLENGE. ***
//
// ken-prod-ops measured this against the live server: `POST /mcp` answered 401 with
// `www-authenticate: Bearer resource_metadata="…"`, and `/station/mcp` answered 401 with three
// headers, none of them that. A client had literally nothing to follow.
//
// ASSERTED OVER HTTP, not on the URL builder, because that is where it failed: the builder was
// fine and the header was never sent. Mutation confirmed the gap — disabling the emission left
// every other test green, including the one that checks the builder produces the right string.
//
// The session on the far side cannot report this. Its words to Vlad: "a 401-without-
// WWW-Authenticate is indistinguishable from a 401 with one: both render as the same 'needs
// authorization' notice." So the only place it can be caught is here.
func TestTheUnauthorized401CarriesTheDiscoveryChallenge(t *testing.T) {
	_, srv, _, _ := harness(t)
	SetResourceMetadata(func(*http.Request) string {
		return "https://kb.example/.well-known/oauth-protected-resource/station/mcp"
	})
	t.Cleanup(func() { SetResourceMetadata(nil) })

	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 — the fixture is not exercising the refusal", resp.StatusCode)
	}
	got := resp.Header.Get("WWW-Authenticate")
	if got == "" {
		t.Fatal("the 401 carries no WWW-Authenticate, so a client has nothing to follow and no way " +
			"to see that anything is missing — this is the wall measured on the live deployment")
	}
	if !strings.Contains(got, "oauth-protected-resource/station/mcp") {
		t.Errorf("the challenge points somewhere other than this surface's metadata: %q", got)
	}
	// Cross-origin clients must be able to READ it, or the header exists and is invisible.
	if exp := resp.Header.Get("Access-Control-Expose-Headers"); !strings.Contains(exp, "WWW-Authenticate") {
		t.Errorf("WWW-Authenticate is not exposed to cross-origin clients (%q); a browser-based "+
			"client sees a bare 401", exp)
	}
}

// *** EVERY station_me PATH CARRIES THE VERSION — INCLUDING THE ONE THAT MINTS. ***
//
// ken-prod-ops found this live on 2026-08-25, on the first real onboarding this feature ever
// served: `ken_version` and `ken_version_note` came back EMPTY on the workspace-CREATION call and
// correct on every established one. Two meOut construction sites, one of them stamping.
//
// THE MISS LANDS IN THE WORST AVAILABLE PLACE. That field exists so a session can discover its
// manual is stale — and the session most likely to hold stale text, and least able to suspect it,
// is a brand-new one calling station_me as its first act. The single call where the signal matters
// most was the single call that dropped it.
//
// WHY NO EXISTING TEST SAW IT: every other test here calls claimWorkspace DIRECTLY, and the stamp
// lives in the handler above it. That is this project's most expensive recurring mistake — a fix
// tested one layer BELOW its defect — so this asserts over the real transport, on both paths, the
// way prod discovered it: call the tool from a new workspace and an established one, and diff.
func TestEveryStationMePathCarriesTheVersion(t *testing.T) {
	st, srv, _, station := harness(t)
	ctx := context.Background()

	actor, err := st.FindOrCreateActor(ctx, "ai", "version-probe")
	if err != nil {
		t.Fatal(err)
	}
	minting, err := st.IssueStationKey(ctx, actor, "", "minting-key", []string{ScopeStation})
	if err != nil {
		t.Fatal(err)
	}
	established, err := st.IssueStationKey(ctx, actor, "", "established-key", []string{ScopeStation})
	if err != nil {
		t.Fatal(err)
	}

	created := meOverTransport(t, connectWS(t, srv, minting, ""), map[string]any{"workspace_name": "brand-new"})
	briefed := meOverTransport(t, connectWS(t, srv, established, station.StationID), map[string]any{})

	// The fixture must actually cover both paths, or this passes by testing one thing twice.
	if !created.JustCreated {
		t.Fatal("the minting key did not mint; the fixture never exercises the creation path")
	}
	if briefed.JustCreated {
		t.Fatal("the established key minted a new workspace; the fixture never exercises the briefing path")
	}

	for _, c := range []struct {
		path string
		out  meOut
	}{{"workspace-creation", created}, {"existing-workspace briefing", briefed}} {
		if c.out.KenVersion != version.Version {
			t.Errorf("%s path: ken_version = %q, want %q — a session on this path cannot tell whether "+
				"the instructions it is holding are older than the server answering it",
				c.path, c.out.KenVersion, version.Version)
		}
		if c.out.VersionNote == "" {
			t.Errorf("%s path: ken_version_note is empty — the number arrives with no instruction "+
				"attached, and a bare version tells a session nothing about what to do with it", c.path)
		}
	}
}

// qsRT sends the bearer and NO workspace header, driving the transport the way a claude.ai
// connector does — which is the whole point: that client refuses custom header names.
type qsRT struct {
	token string
	base  http.RoundTripper
}

func (q qsRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Header.Set("Authorization", "Bearer "+q.token)
	r.Header.Del(WorkspaceHeader)
	return q.base.RoundTrip(r)
}

// *** A WORKSPACE CAN BE DECLARED IN THE URL, BECAUSE A CONNECTOR CANNOT SEND A HEADER. ***
//
// The 2026-08-26 acceptance run on a clean VM found the station surface unreachable through
// claude.ai connectors — the onboarding path Ken recommends and the one that propagates to every
// device. The client enforces an allowlist of header names: "Only approved header names are
// accepted." So `station_me` could mint a workspace with zero approvals, exactly as §6 promises,
// and every other station tool refused it one call later.
//
// This drives the REAL transport with the header stripped, which is the only arrangement that
// reproduces what a connector actually does.
func TestAWorkspaceCanBeDeclaredInTheURLWhenTheClientCannotSendHeaders(t *testing.T) {
	st, srv, _, station := harness(t)
	ctx := context.Background()

	actor, err := st.FindOrCreateActor(ctx, "ai", "connector-session")
	if err != nil {
		t.Fatal(err)
	}
	key, err := st.IssueStationKey(ctx, actor, "", "connector-key", []string{ScopeStation})
	if err != nil {
		t.Fatal(err)
	}

	connect := func(url string) *mcp.ClientSession {
		t.Helper()
		cli := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
		sess, err := cli.Connect(ctx, &mcp.StreamableClientTransport{
			Endpoint:             url,
			HTTPClient:           &http.Client{Transport: qsRT{token: key, base: http.DefaultTransport}},
			DisableStandaloneSSE: true,
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { sess.Close() })
		return sess
	}

	// CONTROL FIRST: with NO header and NO query, this credential has no workspace and mints one.
	// Without this the success below could be caused by the credential carrying a station.
	minted := meOverTransport(t, connect(srv.URL), map[string]any{"workspace_name": "nowhere"})
	if !minted.JustCreated {
		t.Fatal("the control did not mint; this key already carries a station and the test proves nothing")
	}

	// AND NOW THE PROPERTY: the same header-less client, with ?workspace= in the URL, resolves to
	// the DECLARED workspace instead of minting another.
	got := meOverTransport(t, connect(srv.URL+"?workspace="+station.StationID), map[string]any{})
	if got.JustCreated {
		t.Fatal("the URL-declared workspace was ignored and a NEW one was minted — a connector " +
			"would accumulate an orphan station on every connection and never reach a working state")
	}
	if got.StationID != station.StationID {
		t.Errorf("resolved to %q, want the declared %q", got.StationID, station.StationID)
	}
	if got.Name != station.Name {
		t.Errorf("name = %q, want %q — the session is briefed on the wrong post", got.Name, station.Name)
	}
}

// THE REST OF THE STATION SURFACE MUST WORK FROM A URL-DECLARED WORKSPACE, not just station_me.
// station_me was always reachable — it is the one tool that does NOT go through requireStation,
// which is exactly why the VM could mint a workspace and then use nothing. Asserting only on
// station_me would re-pass the broken state.
func TestURLDeclaredWorkspaceReachesToolsBehindRequireStation(t *testing.T) {
	st, srv, _, station := harness(t)
	ctx := context.Background()
	actor, err := st.FindOrCreateActor(ctx, "ai", "connector-session-2")
	if err != nil {
		t.Fatal(err)
	}
	key, err := st.IssueStationKey(ctx, actor, "", "connector-key-2", []string{ScopeStation})
	if err != nil {
		t.Fatal(err)
	}
	cli := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	sess, err := cli.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             srv.URL + "?workspace=" + station.StationID,
		HTTPClient:           &http.Client{Transport: qsRT{token: key, base: http.DefaultTransport}},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	// station_task_add goes through requireStation, like every station tool except station_me.
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name:      "station_task_add",
		Arguments: map[string]any{"text": "reachable from a URL-declared workspace", "blocked_on": "self"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("a tool behind requireStation refused a URL-declared workspace: %+v — this is the "+
			"VM failure, unfixed: mint works, everything else does not", res.Content)
	}
}
