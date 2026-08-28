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
func TestASessionWithNoStationGetsOneAndWorksImmediately(t *testing.T) {
	d, p := onboardingHarness(t)
	ctx := context.WithValue(context.Background(), ctxKey{}, p)

	if p.StationID != "" {
		t.Fatal("fixture: this principal already has a station, so it cannot exercise onboarding")
	}

	out, err := claimStation(ctx, d, p, "ken-public")
	if err != nil {
		t.Fatalf("a session with no station could not get one: %v — the deadlock is still closed", err)
	}
	if out.StationID == "" {
		t.Fatal("no station id came back; the session has nothing to put in its config")
	}
	if out.Name != "ken-public" {
		t.Errorf("name = %q, want the folder basename — the auto-name is what the human sees on the "+
			"link-approval screen, which is the one moment a bad name matters", out.Name)
	}
	if out.NameSource != "auto" {
		t.Errorf("name_source = %q; a human must be able to tell a name Ken guessed from one they chose", out.NameSource)
	}
	if !out.JustCreated {
		t.Error("the result does not say a station was just created, so a session cannot tell " +
			"'here is your briefing' from 'here is your new station'")
	}

	// *** THE INSTRUCTION MUST NAME THE THING THAT ACTUALLY BRINGS A SESSION BACK. ***
	//
	// It used to require the station id and the X-Ken-Workspace header, on the reasoning that an id
	// dying with the conversation means the next session mints another. The reasoning was right and
	// the remedy was wrong: a claude.ai connector cannot set a custom header, so that advice was
	// unfollowable for the population it was written for — and the header is now deleted outright,
	// measured as never used in the deployment's entire life.
	//
	// What replaces it is session_key, which is an ARGUMENT rather than configuration: nothing to
	// install, nothing for a human to approve, and it survives a client restart.
	for _, want := range []string{"session_key", "rename"} {
		if !strings.Contains(out.PutThisInYourConfig, want) {
			t.Errorf("the instruction handed back does not mention %q; without it the session has "+
				"not been told how to come back here", want)
		}
	}
	// AND IT MUST NOT RESURRECT THE HEADER. A string here is advice a session will act on.
	if strings.Contains(out.PutThisInYourConfig, "X-Ken-Workspace") {
		t.Error("the onboarding instruction still tells a session to ask for a header that no " +
			"longer exists and that its client cannot set")
	}

	// AND IT IS NOT WITHHELD PENDING ANYTHING. §5: "The session works immediately: notebook,
	// tasks, vault, knowledge base. Nothing withheld."
	ok, err := d.Store.StationExists(ctx, out.StationID)
	if err != nil || !ok {
		t.Fatalf("the minted station is not live (%v, %v) — it was created pending something", ok, err)
	}
}

// A MISSING FOLDER NAME STILL GETS A STATION.
//
// Withholding one over an absent hint would rebuild the deadlock for exactly the case it was built
// for: a session that does not know what to call itself is still a session that cannot work.
func TestAStationIsMintedEvenWithNoFolderName(t *testing.T) {
	d, p := onboardingHarness(t)
	out, err := claimStation(context.WithValue(context.Background(), ctxKey{}, p), d, p, "")
	if err != nil {
		t.Fatalf("no name hint meant no station: %v", err)
	}
	if out.StationID == "" || out.Name == "" {
		t.Fatalf("got an unusable station: id=%q name=%q", out.StationID, out.Name)
	}
}

// TWO FOLDERS WITH THE SAME NAME BOTH WORK.
//
// Names are unique per instance, so a collision must decorate rather than refuse — a second
// `ken-public` on one machine is ordinary, and a session that cannot start because another folder
// took the name first is a session waiting on a human again. The IDS are what differ, which is
// COMM.md §3's rule: "a human-chosen name is never an address."
func TestASecondFolderWithTheSameNameStillGetsAStation(t *testing.T) {
	d, p := onboardingHarness(t)
	ctx := context.WithValue(context.Background(), ctxKey{}, p)

	first, err := claimStation(ctx, d, p, "ken-public")
	if err != nil {
		t.Fatal(err)
	}
	second, err := claimStation(ctx, d, p, "ken-public")
	if err != nil {
		t.Fatalf("a second folder with the same name was refused a station: %v — that is the "+
			"deadlock again, in miniature", err)
	}
	if second.StationID == first.StationID {
		t.Fatal("both folders got the SAME station — they would share a notebook, a task list and a vault")
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

// wsRT adds the bearer. It used to add a station HEADER too, which is how a folder's MCP entry
// declared its station — that header is deleted, so the field remains only to keep the helper's
// call sites honest about what they are no longer doing.
type wsRT struct {
	token, station string
	base             http.RoundTripper
}

func (w wsRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Header.Set("Authorization", "Bearer "+w.token)
	return w.base.RoundTrip(r)
}

func connectWS(t *testing.T, srv *httptest.Server, token, station string) *mcp.ClientSession {
	t.Helper()
	cli := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	sess, err := cli.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:             srv.URL,
		HTTPClient:           &http.Client{Transport: wsRT{token: token, station: station, base: http.DefaultTransport}},
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

// *** station_me IS THE PATH, and the auto-provision must be ON it. ***
//
// §5's flow works because station_me is the call every session is told to make first — the fix
// lands on a path a session already walks. Wiring it anywhere else means an onboarding session has
// to know to look, which is the thing that was broken.
//
// Mutation found this too: disabling the branch in the handler left everything green, because the
// other tests call claimStation directly.
func TestStationMeMintsAStationForASessionThatHasNone(t *testing.T) {
	st, srv, _, _ := harness(t)
	actor, err := st.FindOrCreateActor(context.Background(), "ai", "fresh-session")
	if err != nil {
		t.Fatal(err)
	}
	key, err := st.IssueToken(context.Background(), actor, []string{ScopeStation}, "fresh")
	if err != nil {
		t.Fatal(err)
	}

	out := meOverTransport(t, connectWS(t, srv, key, ""), map[string]any{"station_label": "ken-public"})
	if !out.JustCreated || out.StationID == "" {
		t.Fatalf("station_me did not mint a station for a session with none: %+v\n"+
			"That is the deadlock: station_request needed a station-scoped credential, the console "+
			"could not create a station, and a console-minted key could never bind the session it "+
			"was minted for.", out)
	}
	// THE INSTRUCTION MUST NAME session_key, not the station id. The id used to be the thing a
	// human pasted into a header; there is no header, and what brings a session back is the key it
	// sends itself.
	if !strings.Contains(out.PutThisInYourConfig, "session_key") {
		t.Error("the session is not told to keep sending session_key, so it has not been told how " +
			"to come back to this station at all")
	}
}

// *** A PLAIN ken_ TOKEN ONBOARDS, BECAUSE THE CLIENT THAT REPORTED THIS CANNOT RUN OAUTH. ***
//
// 3.25.0 let an OAuth grant reach this surface and 3.26.0 let a session mint its own station.
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

	out := meOverTransport(t, connectWS(t, srv, tok, ""), map[string]any{"station_label": "m600"})
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
// served: `ken_version` and `ken_version_note` came back EMPTY on the station-CREATION call and
// correct on every established one. Two meOut construction sites, one of them stamping.
//
// THE MISS LANDS IN THE WORST AVAILABLE PLACE. That field exists so a session can discover its
// manual is stale — and the session most likely to hold stale text, and least able to suspect it,
// is a brand-new one calling station_me as its first act. The single call where the signal matters
// most was the single call that dropped it.
//
// WHY NO EXISTING TEST SAW IT: every other test here calls claimStation DIRECTLY, and the stamp
// lives in the handler above it. That is this project's most expensive recurring mistake — a fix
// tested one layer BELOW its defect — so this asserts over the real transport, on both paths, the
// way prod discovered it: call the tool from a new station and an established one, and diff.
func TestEveryStationMePathCarriesTheVersion(t *testing.T) {
	st, srv, _, station := harness(t)
	ctx := context.Background()

	actor, err := st.FindOrCreateActor(ctx, "ai", "version-probe")
	if err != nil {
		t.Fatal(err)
	}
	minting, err := st.IssueToken(ctx, actor, []string{ScopeStation}, "minting-key")
	if err != nil {
		t.Fatal(err)
	}
	established, err := st.IssueToken(ctx, actor, []string{ScopeStation}, "established-key")
	if err != nil {
		t.Fatal(err)
	}

	// BOTH PATHS OVER THE REAL TRANSPORT, driven by session_key rather than the deleted header.
	// The established arm claims a station on its first call and returns to it on its second —
	// which is the briefing path, and is now also a check that the key actually resolves.
	created := meOverTransport(t, connectWS(t, srv, minting, ""), map[string]any{"station_label": "brand-new"})

	estSess := connectWS(t, srv, established, "")
	if first := meOverTransport(t, estSess, map[string]any{"session_key": "conv-established"}); !first.JustCreated {
		t.Fatal("the established key did not mint on its first call; the fixture is wrong")
	}
	briefed := meOverTransport(t, estSess, map[string]any{"session_key": "conv-established"})
	_ = station

	// The fixture must actually cover both paths, or this passes by testing one thing twice.
	if !created.JustCreated {
		t.Fatal("the minting key did not mint; the fixture never exercises the creation path")
	}
	if briefed.JustCreated {
		t.Fatal("the established key minted a new station; the fixture never exercises the briefing path")
	}

	for _, c := range []struct {
		path string
		out  meOut
	}{{"station-creation", created}, {"existing-station briefing", briefed}} {
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

// qsRT sends the bearer and NO station header, driving the transport the way a claude.ai
// connector does — which is the whole point: that client refuses custom header names.
type qsRT struct {
	token string
	base  http.RoundTripper
}

func (q qsRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Header.Set("Authorization", "Bearer "+q.token)
	return q.base.RoundTrip(r)
}

// *** A CONVERSATION DECLARES ITSELF AND COMES BACK TO THE SAME STATION. ***
//
// This is the property Vlad specified in his own words: "one (existing) session is always
// connected to the same station (unless explicitly reassigned by the human)", and "if I restart
// the Claude Desktop client, the CC sessions that live within it should reconnect to the station
// they were connected before (because they are not new, they just restarted)."
//
// So the test does not merely call station_me twice on one connection — that would prove a cache.
// It RECONNECTS, which is what a client restart does, and asserts the station survives it.
// Driven with NO station header at all, because the claude.ai connector cannot send one.
func TestAConversationReturnsToItsOwnStationAfterAReconnect(t *testing.T) {
	st, srv, _, _ := harness(t)
	ctx := context.Background()
	actor, err := st.FindOrCreateActor(ctx, "ai", "conversation-session")
	if err != nil {
		t.Fatal(err)
	}
	key, err := st.IssueToken(ctx, actor, []string{ScopeStation}, "conv-key")
	if err != nil {
		t.Fatal(err)
	}
	connect := func() *mcp.ClientSession {
		t.Helper()
		cli := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
		sess, err := cli.Connect(ctx, &mcp.StreamableClientTransport{
			Endpoint:             srv.URL,
			HTTPClient:           &http.Client{Transport: qsRT{token: key, base: http.DefaultTransport}},
			DisableStandaloneSSE: true,
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { sess.Close() })
		return sess
	}

	const conv = "conversation-uuid-2e415ff7"
	first := meOverTransport(t, connect(), map[string]any{"session_key": conv, "station_label": "ken-public"})
	if !first.JustCreated {
		t.Fatal("the first declaration did not mint a station")
	}
	if first.StationID == "" {
		t.Fatal("no station id came back")
	}

	// *** THE RESTART. A NEW CONNECTION, so the per-connection binding is empty and the answer
	// must come from the database via the declared key. ***
	again := meOverTransport(t, connect(), map[string]any{"session_key": conv})
	if again.JustCreated {
		t.Error("a restarted conversation MINTED A SECOND STATION — its notebook, tasks and vault " +
			"are stranded on the first, which is the orphan-accumulation failure this replaces")
	}
	if again.StationID != first.StationID {
		t.Errorf("came back as %q, want the same station %q", again.StationID, first.StationID)
	}

	// CONTROL: a DIFFERENT conversation gets a DIFFERENT station. Without this the test would
	// pass against an implementation that ignored the key and returned one station to everyone.
	other := meOverTransport(t, connect(), map[string]any{"session_key": "a-different-conversation", "station_label": "elsewhere"})
	if !other.JustCreated {
		t.Error("a new conversation did not get its own station")
	}
	if other.StationID == first.StationID {
		t.Error("two different conversations landed on the SAME station — the key is being ignored")
	}
}

// AND THE REST OF THE SURFACE WORKS AFTER THE DECLARATION, with no header and no argument.
// station_me was always reachable; asserting only on it would re-pass the exact state the clean-VM
// run found, where a station could be minted and nothing else could touch it.
func TestDeclaringAConversationUnlocksToolsBehindRequireStation(t *testing.T) {
	st, srv, _, _ := harness(t)
	ctx := context.Background()
	actor, err := st.FindOrCreateActor(ctx, "ai", "conversation-session-2")
	if err != nil {
		t.Fatal(err)
	}
	key, err := st.IssueToken(ctx, actor, []string{ScopeStation}, "conv-key-2")
	if err != nil {
		t.Fatal(err)
	}
	cli := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	sess, err := cli.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             srv.URL,
		HTTPClient:           &http.Client{Transport: qsRT{token: key, base: http.DefaultTransport}},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	// CONTROL FIRST: before declaring, a tool behind requireStation must REFUSE. Otherwise the
	// success below could be caused by the credential already carrying a station.
	pre, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name: "station_task_add", Arguments: map[string]any{"text": "too early", "blocked_on": "self"}})
	if err != nil {
		t.Fatal(err)
	}
	if !pre.IsError {
		t.Fatal("a tool behind requireStation succeeded BEFORE any station was declared; the " +
			"fixture carries a station and this test proves nothing")
	}

	meOverTransport(t, sess, map[string]any{"session_key": "unlock-conv", "station_label": "unlock"})

	post, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name: "station_task_add", Arguments: map[string]any{"text": "after declaring", "blocked_on": "self"}})
	if err != nil {
		t.Fatal(err)
	}
	if post.IsError {
		t.Fatalf("a tool behind requireStation still refuses after the conversation declared itself: "+
			"%+v — mint works and nothing else does, which is the clean-VM failure unfixed", post.Content)
	}
}

// *** EVERY station_me RESULT TELLS THE SESSION HOW TO KEEP ITS STATION. ***
//
// ken-prod-ops watched a session reason its way to exactly the wrong conclusion on the clean VM:
// "There is no session_key parameter… I called it with no arguments rather than passing an
// unsupported field, which would have failed validation." Its schema was 3.33.0; the server was
// 3.35.0. That is CAREFUL reasoning reaching a false answer, and careful is what we want.
//
// A tool description cannot fix it — descriptions pin at connect time, so the text saying "send
// session_key" is invisible to precisely the sessions that need telling. Only RESULTS cross the
// freeze. So the guidance rides in the result, on every call.
func TestEveryStationMeResultSaysHowToKeepTheStation(t *testing.T) {
	st, srv, _, station := harness(t)
	ctx := context.Background()
	actor, err := st.FindOrCreateActor(ctx, "ai", "guidance-session")
	if err != nil {
		t.Fatal(err)
	}
	key, err := st.IssueToken(ctx, actor, []string{ScopeStation}, "guidance-key")
	if err != nil {
		t.Fatal(err)
	}

	// A session with NO key — the one that must be told, and the one whose schema does not
	// mention the parameter.
	noKey := meOverTransport(t, connectWS(t, srv, key, station.StationID), map[string]any{})
	if noKey.HowToKeepThisStation == "" {
		t.Fatal("a result carries no guidance at all; a session whose schema predates session_key " +
			"has nothing anywhere telling it the parameter exists")
	}
	for _, must := range []string{"session_key", "SEND IT ANYWAY"} {
		if !strings.Contains(noKey.HowToKeepThisStation, must) {
			t.Errorf("the guidance omits %q, so a session that checked its schema and found nothing "+
				"has no reason to try: %q", must, noKey.HowToKeepThisStation)
		}
	}

	// CONTROL: a session that DID send a key gets different, shorter guidance — otherwise the
	// assertion above would pass against a constant string bolted on unconditionally.
	withKey := meOverTransport(t, connectWS(t, srv, key, ""), map[string]any{"session_key": "guidance-conv"})
	if withKey.SessionKeyEcho != "guidance-conv" {
		t.Errorf("the key was not echoed back, so a session cannot confirm Ken received it: %q", withKey.SessionKeyEcho)
	}
	if withKey.HowToKeepThisStation == noKey.HowToKeepThisStation {
		t.Error("the guidance is identical whether or not a key was sent; it is a constant rather " +
			"than a response to what actually arrived")
	}
}

// *** A SECOND CALL WITH A KEY ADOPTS THE STATION THE FIRST CALL MINTED. ***
//
// The pre-3.35.0 tool text told sessions to call station_me with no arguments, so the real-world
// sequence is no-arg then keyed — and on the clean VM that left an orphan station behind, because
// Ken had no way to know the two calls were the same conversation. The connection binding does
// know.
func TestAKeyedCallAdoptsTheStationThisConnectionJustMinted(t *testing.T) {
	st, srv, _, _ := harness(t)
	ctx := context.Background()
	actor, err := st.FindOrCreateActor(ctx, "ai", "adopt-session")
	if err != nil {
		t.Fatal(err)
	}
	key, err := st.IssueToken(ctx, actor, []string{ScopeStation}, "adopt-key")
	if err != nil {
		t.Fatal(err)
	}
	sess := connectWS(t, srv, key, "")

	first := meOverTransport(t, sess, map[string]any{"station_label": "adopt-me"})
	if !first.JustCreated {
		t.Fatal("the no-argument call did not mint; the fixture does not reproduce the sequence")
	}

	// SAME CONNECTION, now declaring a key. It must claim what it already has.
	second := meOverTransport(t, sess, map[string]any{"session_key": "adopting-conversation"})
	if second.StationID != first.StationID {
		t.Errorf("the keyed call minted %q instead of adopting %q — the first station is now "+
			"orphaned, which is exactly the sequence the old tool text produced",
			second.StationID, first.StationID)
	}
	if second.JustCreated {
		t.Error("station_just_created is true on an ADOPTION; the station already existed and " +
			"saying otherwise tells the session it was handed a fresh one")
	}
}
