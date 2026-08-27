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

// The console is the operator's brake on a standing relationship.
//
// `stations.link_help` has promised since links shipped that "one click revokes it
// later" — and until now there was no click: RevokeStationLink had zero callers. This
// asserts the whole round trip, because a store function with no route is the same
// kind of unfinished as a flag with no reader.
func TestStationsConsoleRevokesALinkAndItsLiveChannels(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	cs, err := comm.Open(filepath.Join(t.TempDir(), "c.db"), comm.DefaultLimits())
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
	actorID, err := st.FindOrCreateActor(ctx, "human", "admin")
	if err != nil {
		t.Fatal(err)
	}

	// Two stations with an approved link between them.
	devSt, err := st.CreateStation(ctx, "dev", "", actorID)
	if err != nil {
		t.Fatal(err)
	}
	prodSt, err := st.CreateStation(ctx, "prod", "", actorID)
	if err != nil {
		t.Fatal(err)
	}
	reqID, err := st.CreateStationLinkRequest(ctx, "tok", devSt.StationID, prodSt.StationID, "work together", false)
	if err != nil {
		t.Fatal(err)
	}
	link, err := st.ApproveLinkRequest(ctx, reqID, actorID)
	if err != nil {
		t.Fatal(err)
	}

	// And a live channel between them, which is the thing the link authorised.
	epA, secretA, err := cs.RegisterEndpoint(ctx, comm.Owner{TokenID: "tok-a", ActorID: actorID}, "dev", "")
	if err != nil {
		t.Fatal(err)
	}
	epB, secretB, err := cs.RegisterEndpoint(ctx, comm.Owner{TokenID: "tok-b", ActorID: actorID}, "prod", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := cs.BindEndpointToStation(ctx, epA.EndpointID, devSt.StationID, "kens_a"); err != nil {
		t.Fatal(err)
	}
	if err := cs.BindEndpointToStation(ctx, epB.EndpointID, prodSt.StationID, "kens_b"); err != nil {
		t.Fatal(err)
	}
	// Re-read so each carries its StationID; OpenLinkedChannel refuses unbound ones.
	if epA, err = cs.AuthenticateEndpoint(ctx, epA.EndpointID, secretA); err != nil {
		t.Fatal(err)
	}
	if epB, err = cs.AuthenticateEndpoint(ctx, epB.EndpointID, secretB); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.OpenLinkedChannel(ctx, epA, epB, actorID, "dev <-> prod"); err != nil {
		t.Fatal(err)
	}
	// PRE-CHECK, so the post-revoke assertion cannot pass vacuously: if the channel
	// were never opened, "0 open channels afterwards" would be true for the wrong
	// reason and this test would certify a revoke that does nothing.
	if n, err := cs.CountOpenChannelsBetweenStations(ctx, devSt.StationID, prodSt.StationID); err != nil || n != 1 {
		t.Fatalf("setup: %d open channel(s) before revoke (err=%v), want 1", n, err)
	}

	srv := httptest.NewServer(Handler(Deps{Store: st, Comm: cs}))
	defer srv.Close()
	jar, _ := cookiejar.New(nil)
	cli := &http.Client{Jar: jar}
	lcsrf := extract(t, cli, srv.URL+"/login", `name="lcsrf" value="([^"]+)"`)
	postForm(t, cli, srv.URL+"/login", url.Values{"name": {"admin"}, "password": {"supersecret"}, "lcsrf": {lcsrf}})

	// The page must offer the control, and must show the blast radius before the
	// click. A revoke button with no number is one people avoid or press twice.
	page := get(t, cli, srv.URL+"/stations")
	if !strings.Contains(page, "/stations/links/"+link.LinkID+"/revoke") {
		t.Fatal("the links table renders no revoke control — the store function stays unreachable and stations.link_help keeps promising a click that does not exist")
	}
	if !strings.Contains(page, "data-confirm=") {
		t.Fatal("the revoke control carries no confirmation")
	}

	csrf := extract(t, cli, srv.URL+"/stations", `name="csrf" value="([^"]+)"`)
	postForm(t, cli, srv.URL+"/stations/links/"+link.LinkID+"/revoke", url.Values{"csrf": {csrf}})

	got, err := st.StationLinkByID(ctx, link.LinkID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "revoked" {
		t.Fatalf("link state is %q after revoke, want %q", got.State, "revoked")
	}
	// The permission AND its traffic. Ending one without the other is the defect.
	n, err := cs.CountOpenChannelsBetweenStations(ctx, devSt.StationID, prodSt.StationID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d channel(s) still open after the link was revoked — the permission ended and the conversation did not", n)
	}
}

// With COMM off there is nothing to sever, and the link revoke must still work rather
// than 500 on a nil comm handle. Stations run with COMM off by design.
func TestStationLinkRevokeWorksWithCommOff(t *testing.T) {
	st, ctx, cli, base, actorID := stationsHarness(t)

	devSt, err := st.CreateStation(ctx, "dev", "", actorID)
	if err != nil {
		t.Fatal(err)
	}
	prodSt, err := st.CreateStation(ctx, "prod", "", actorID)
	if err != nil {
		t.Fatal(err)
	}
	reqID, err := st.CreateStationLinkRequest(ctx, "tok", devSt.StationID, prodSt.StationID, "r", false)
	if err != nil {
		t.Fatal(err)
	}
	link, err := st.ApproveLinkRequest(ctx, reqID, actorID)
	if err != nil {
		t.Fatal(err)
	}

	csrf := extract(t, cli, base+"/stations", `name="csrf" value="([^"]+)"`)
	postForm(t, cli, base+"/stations/links/"+link.LinkID+"/revoke", url.Values{"csrf": {csrf}})

	got, err := st.StationLinkByID(ctx, link.LinkID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "revoked" {
		t.Fatalf("link state is %q with COMM off, want %q", got.State, "revoked")
	}
}
