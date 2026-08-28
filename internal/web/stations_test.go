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

// TestMintedStationKeyIsShownOnceAndCarriesThePrefix IS DELETED with station keys — there is no
// key to mint, no `kens_` prefix and no one-time reveal.

// TestEndpointRotationIsReachableOnlyByAnAuthenticatedCurator IS DELETED with rotation. There is
// no secret to rotate, so there is no control to gate.
