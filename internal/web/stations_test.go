package web

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Quest-ICT/ken/internal/comm"
	"github.com/Quest-ICT/ken/internal/passwd"
	"github.com/Quest-ICT/ken/internal/store"
)

// stationsHarness builds a logged-in console with stations ON.
func stationsHarness(t *testing.T) (*store.Store, context.Context, *http.Client, string, int64) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	hash, _ := passwd.Hash("supersecret", passwd.Standard)
	if _, err := st.CreateHumanUser(ctx, "admin", hash); err != nil {
		t.Fatal(err)
	}
	actorID, err := st.FindOrCreateActor(ctx, "human", "admin")
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(Handler(Deps{Store: st}))
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	cli := &http.Client{Jar: jar}
	lcsrf := extract(t, cli, srv.URL+"/login", `name="lcsrf" value="([^"]+)"`)
	postForm(t, cli, srv.URL+"/login", url.Values{"name": {"admin"}, "password": {"supersecret"}, "lcsrf": {lcsrf}})
	return st, ctx, cli, srv.URL, actorID
}

// *** THE STATIONS CONSOLE IS UNCONDITIONAL, AND THIS TEST USED TO ASSERT THE OPPOSITE. ***
//
// It was "TestStationsConsoleIsAbsentWhenTheFlagIsOff", and it passed against a flag that was
// hardcoded `true` at cmd/ken/main.go — so it proved a behaviour no deployment could produce. The
// only way to reach the 404 it asserted was to construct the handler by hand, as it did.
//
// Vlad, on being shown a log line that said stations could be switched off: "IN KEN NOTHING IS
// OPTIONAL!" The flag, the nineteen `if !a.stationsEnabled` guards behind it, the Deps field, the
// nav gate and the dashboard gate are all deleted. This asserts what replaced them.
func TestTheStationsConsoleIsAlwaysServed(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	hash, _ := passwd.Hash("supersecret", passwd.Standard)
	if _, err := st.CreateHumanUser(context.Background(), "admin", hash); err != nil {
		t.Fatal(err)
	}
	// Deps carries NO stations switch — there is nothing to pass, which is the point.
	srv := httptest.NewServer(Handler(Deps{Store: st}))
	defer srv.Close()
	jar, _ := cookiejar.New(nil)
	cli := &http.Client{Jar: jar}
	lcsrf := extract(t, cli, srv.URL+"/login", `name="lcsrf" value="([^"]+)"`)
	dash := postForm(t, cli, srv.URL+"/login", url.Values{"name": {"admin"}, "password": {"supersecret"}, "lcsrf": {lcsrf}})

	if !strings.Contains(dash, `href="/stations"`) {
		t.Error("the nav does not offer /stations — an operator cannot reach a console that is " +
			"always running")
	}
	resp, err := cli.Get(srv.URL + "/stations")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /stations = HTTP %d, want 200 — the console is unconditional", resp.StatusCode)
	}
}

// Approving is the curation gate for identities. The console must take the name the
// HUMAN typed and ignore the agent's hint — if the hint could win, an agent would be
// naming itself through a form the human merely clicked.
func TestApprovingARequestUsesTheTypedNameNotTheAgentsHint(t *testing.T) {
	st, ctx, cli, base, _ := stationsHarness(t)

	reqID, err := st.CreateStationRequest(ctx, "tok_abc", "", "agent-picked-name", "run the deploys")
	if err != nil {
		t.Fatal(err)
	}
	page := get(t, cli, base+"/stations")
	if !strings.Contains(page, "agent-picked-name") {
		t.Fatalf("the queue does not show the request's suggested name: %s", trunc(page))
	}
	csrf := extract(t, cli, base+"/stations", `name="csrf" value="([^"]+)"`)

	postForm(t, cli, base+"/stations/requests/"+reqID+"/approve",
		url.Values{"csrf": {csrf}, "name": {"prod-ops"}})

	stations, err := st.ListStations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(stations) != 1 {
		t.Fatalf("got %d stations after approving one request, want 1", len(stations))
	}
	if stations[0].Name != "prod-ops" {
		t.Fatalf("station named %q — the human typed prod-ops and the agent's hint must carry no weight", stations[0].Name)
	}
	pending, err := st.PendingStationRequests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("%d request(s) still pending after approval", len(pending))
	}
}

// Every mutating route must reject a missing/incorrect CSRF token BEFORE it touches
// the store. Approve is the one that matters most — it is the route that creates an
// identity — so a forged POST must not be able to bring a station into existence.
func TestStationWritesRequireCSRF(t *testing.T) {
	st, ctx, cli, base, _ := stationsHarness(t)
	reqID, err := st.CreateStationRequest(ctx, "tok_abc", "", "hint", "purpose")
	if err != nil {
		t.Fatal(err)
	}

	resp := rawPostForm(t, cli, base+"/stations/requests/"+reqID+"/approve",
		url.Values{"csrf": {"wrong"}, "name": {"sneaky"}})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("approve with a bad CSRF token = HTTP %d, want 403", resp.StatusCode)
	}
	stations, err := st.ListStations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(stations) != 0 {
		t.Fatalf("a CSRF-rejected POST still created %d station(s)", len(stations))
	}
}

// The cross-station view is the reason the page exists: one place showing everything
// waiting on the human, across stations, rather than whatever the session in front of
// them remembers to mention. It must default to blocked_on=human without being asked.
func TestCrossStationViewShowsWhatIsWaitingOnTheHuman(t *testing.T) {
	st, ctx, cli, base, actorID := stationsHarness(t)

	a, err := st.CreateStation(ctx, "prod-ops", "", actorID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateStation(ctx, "promo", "", actorID)
	if err != nil {
		t.Fatal(err)
	}
	lim := store.DefaultStationTaskLimits()
	if _, _, err := st.AddStationTask(ctx, lim,
		store.StationTask{StationID: a.StationID, Text: "decide on the archive retention", BlockedOn: "human"},
		"tok", actorID, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.AddStationTask(ctx, lim,
		store.StationTask{StationID: b.StationID, Text: "approve the launch copy", BlockedOn: "human"},
		"tok", actorID, false); err != nil {
		t.Fatal(err)
	}
	// Blocked on the session itself: must NOT appear in the default view, which
	// answers "what is waiting on ME".
	if _, _, err := st.AddStationTask(ctx, lim,
		store.StationTask{StationID: a.StationID, Text: "refactor the poller", BlockedOn: "self"},
		"tok", actorID, false); err != nil {
		t.Fatal(err)
	}

	page := get(t, cli, base+"/stations")
	for _, want := range []string{"decide on the archive retention", "approve the launch copy", "prod-ops", "promo"} {
		if !strings.Contains(page, want) {
			t.Fatalf("the cross-station view is missing %q — this view is the whole point of the page:\n%s", want, trunc(page))
		}
	}
	if strings.Contains(page, "refactor the poller") {
		t.Fatal("a blocked_on=self task appears in the default view, which is supposed to answer 'what is waiting on ME'")
	}

	// Opting into everything must include it again, or the filter is a trap door.
	all := get(t, cli, base+"/stations?blocked_on=any")
	if !strings.Contains(all, "refactor the poller") {
		t.Fatalf("blocked_on=any omits a self-blocked task: %s", trunc(all))
	}
}

// A transfer that would overwrite a page must be refused, and the console must NAME
// what collided — a bare failure leaves the operator with nothing to act on, and a
// handoff-on-handoff clash is the common case rather than the exotic one.
func TestTransferCollisionIsReportedWithTheCollidingNames(t *testing.T) {
	st, ctx, cli, base, actorID := stationsHarness(t)
	from, err := st.CreateStation(ctx, "old-box", "", actorID)
	if err != nil {
		t.Fatal(err)
	}
	to, err := st.CreateStation(ctx, "new-box", "", actorID)
	if err != nil {
		t.Fatal(err)
	}
	lim := store.DefaultStationNoteLimits()
	for _, s := range []*store.Station{from, to} {
		if _, err := st.WriteStationNote(ctx, lim, s.StationID, "handoff", "Handoff", "state", nil, "replace", -1, "tok", actorID, false); err != nil {
			t.Fatal(err)
		}
	}

	csrf := extract(t, cli, base+"/stations", `name="csrf" value="([^"]+)"`)
	body := postForm(t, cli, base+"/stations/"+from.StationID+"/transfer",
		url.Values{"csrf": {csrf}, "to": {to.StationID}, "notes": {"1"}, "tasks": {"1"}, "locker": {"1"}})

	if !strings.Contains(body, "handoff") {
		t.Fatalf("the refusal does not name the colliding page, so the operator cannot act on it: %s", trunc(body))
	}
	// And it must have moved nothing.
	left, err := st.ListStationNotes(ctx, from.StationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 1 {
		t.Fatalf("source has %d page(s) after a REFUSED transfer, want its original 1", len(left))
	}
}

// A minted key is shown exactly once, because only its hash is stored. If the page
// did not render it, the operator would be holding a credential nobody can read.
func TestMintedStationKeyIsShownOnceAndCarriesThePrefix(t *testing.T) {
	st, ctx, cli, base, actorID := stationsHarness(t)
	s, err := st.CreateStation(ctx, "prod-ops", "", actorID)
	if err != nil {
		t.Fatal(err)
	}
	csrf := extract(t, cli, base+"/stations", `name="csrf" value="([^"]+)"`)
	body := postForm(t, cli, base+"/stations/"+s.StationID+"/key",
		url.Values{"csrf": {csrf}, "label": {"laptop"}})

	if !strings.Contains(body, "kens_") {
		t.Fatalf("the mint response does not show the key; it can never be shown again: %s", trunc(body))
	}
	// A later load must NOT show it — that is what "shown once" means.
	if again := get(t, cli, base+"/stations"); strings.Contains(again, "kens_") {
		t.Fatal("the station key is still on the page after a reload — it must appear exactly once")
	}
}

// Rotation exists ONLY behind curator authentication, and that placement is the
// entire security argument — not a detail of it.
//
// One COMM bearer token covers a whole machine, so the endpoint pair is the only
// thing separating two sessions that share it. Any reissue a SESSION could trigger
// would let any session on that machine seize any endpoint on it, which is why an
// equivalent MCP tool was refused outright. This test pins the two gates that make
// the console version safe: an unauthenticated caller cannot reach it, and neither
// can a forged cross-site POST.
func TestEndpointRotationIsReachableOnlyByAnAuthenticatedCurator(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	cs, err := comm.Open(filepath.Join(t.TempDir(), "comm.db"), comm.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	if err := cs.Migrate(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	hash, _ := passwd.Hash("supersecret", passwd.Standard)
	if _, err := st.CreateHumanUser(ctx, "admin", hash); err != nil {
		t.Fatal(err)
	}
	ep, epSecret, err := cs.RegisterEndpoint(ctx, comm.Owner{TokenID: "tok", ActorID: 7}, "dev", "")
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(Handler(Deps{Store: st, Comm: cs}))
	defer srv.Close()

	// Unauthenticated: bounced to login, and the secret must be untouched afterwards.
	noAuth := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noAuth.PostForm(srv.URL+"/comm/endpoints/"+ep.EndpointID+"/rotate", url.Values{})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatal("an UNAUTHENTICATED POST rotated an endpoint secret — this route is the one place rotation is allowed precisely because it needs a curator")
	}
	if _, err := cs.AuthenticateEndpoint(ctx, ep.EndpointID, epSecret); err != nil {
		t.Fatal("the secret changed despite the request being refused")
	}

	jar, _ := cookiejar.New(nil)
	cli := &http.Client{Jar: jar}
	lcsrf := extract(t, cli, srv.URL+"/login", `name="lcsrf" value="([^"]+)"`)
	postForm(t, cli, srv.URL+"/login", url.Values{"name": {"admin"}, "password": {"supersecret"}, "lcsrf": {lcsrf}})

	// Authenticated but forged: a bad CSRF token must not rotate either.
	bad := rawPostForm(t, cli, srv.URL+"/comm/endpoints/"+ep.EndpointID+"/rotate", url.Values{"csrf": {"wrong"}})
	if bad.StatusCode != http.StatusForbidden {
		t.Fatalf("rotate with a bad CSRF token = HTTP %d, want 403", bad.StatusCode)
	}
	if _, err := cs.AuthenticateEndpoint(ctx, ep.EndpointID, epSecret); err != nil {
		t.Fatal("a CSRF-rejected POST still rotated the secret")
	}

	// The real thing: rotates, reveals once, and the old secret dies.
	csrf := extract(t, cli, srv.URL+"/comm", `name="csrf" value="([^"]+)"`)
	body := postForm(t, cli, srv.URL+"/comm/endpoints/"+ep.EndpointID+"/rotate", url.Values{"csrf": {csrf}})
	if !strings.Contains(body, ep.EndpointID) {
		t.Fatalf("the reveal omits the endpoint id; both halves are needed to use it: %s", trunc(body))
	}
	if _, err := cs.AuthenticateEndpoint(ctx, ep.EndpointID, epSecret); err == nil {
		t.Fatal("the old secret still works after a console rotation")
	}
	// Shown once: a plain reload must not carry it.
	if again := get(t, cli, srv.URL+"/comm"); strings.Contains(again, "New endpoint secret") {
		t.Fatal("the rotated secret survives a reload — it must appear exactly once")
	}
}
