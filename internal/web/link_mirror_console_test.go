package web

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/Quest-ICT/ken/internal/comm"
	"github.com/Quest-ICT/ken/internal/passwd"
	"github.com/Quest-ICT/ken/internal/store"
)

// APPROVING A LINK MUST MAKE THE PAIR SCOPE REACHABLE, AND REVOKING IT MUST NOT.
//
// The console writes the decision to ken.db and comm.db learns it only through the
// mirror refresh. Both halves survived a mutation run that simply DELETED the refresh
// call — the decision still landed, the tests still passed, and `comm_send{to_station}`
// would have refused every message under a permission the human had just granted. That
// is the "a store function with no route" shape this package has shipped before, in the
// other direction.
//
// So this asserts the projection itself, on both edges, through real HTTP form posts.
func TestConsoleLinkDecisionsReachTheCommMirror(t *testing.T) {
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
	a, err := st.CreateStation(ctx, "alpha", "", actorID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateStation(ctx, "beta", "", actorID)
	if err != nil {
		t.Fatal(err)
	}
	reqID, err := st.CreateStationLinkRequest(ctx, "tok", a.StationID, b.StationID, "work together", false)
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(Handler(Deps{Store: st, Comm: cs, StationsEnabled: true}))
	defer srv.Close()
	jar, _ := cookiejar.New(nil)
	cli := &http.Client{Jar: jar}
	lcsrf := extract(t, cli, srv.URL+"/login", `name="lcsrf" value="([^"]+)"`)
	postForm(t, cli, srv.URL+"/login", url.Values{"name": {"admin"}, "password": {"supersecret"}, "lcsrf": {lcsrf}})

	linked := func() bool {
		t.Helper()
		peers, err := cs.LinkedStations(ctx, a.StationID)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range peers {
			if p == b.StationID {
				return true
			}
		}
		return false
	}

	// NEGATIVE CONTROL. Without this the assertion below could pass on a mirror that
	// was already full, and would certify nothing.
	if linked() {
		t.Fatal("the pair is in the mirror before anything was approved")
	}

	csrf := extract(t, cli, srv.URL+"/stations", `name="csrf" value="([^"]+)"`)
	postForm(t, cli, srv.URL+"/stations/requests/"+reqID+"/approve",
		url.Values{"csrf": {csrf}, "kind": {"link"}})

	if !linked() {
		t.Fatal("the human approved the link and comm.db never learned it — every " +
			"comm_send{to_station} would be refused with \"no approved link joins you\", " +
			"naming a decision as missing while it sits in ken.db, intact")
	}

	// AND THE OTHER EDGE, which is the one that matters more: a revoked link must stop
	// authorising. This is why the refresh sits before every early return in the revoke
	// handler rather than after the channel sweep that can fail.
	links, err := st.ListStationLinks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 {
		t.Fatalf("%d links after one approval, want 1", len(links))
	}
	link := links[0]
	csrf = extract(t, cli, srv.URL+"/stations", `name="csrf" value="([^"]+)"`)
	postForm(t, cli, srv.URL+"/stations/links/"+link.LinkID+"/revoke", url.Values{"csrf": {csrf}})

	if linked() {
		t.Fatal("the link was revoked and the mirror still authorises the pair — the " +
			"human believes they withdrew a permission that is still in force")
	}
}

// LinkMirrorRows decides what comm.db is allowed to authorise, so its filter is a
// security predicate rather than a tidiness one. It survived a mutation that removed the
// state filter entirely: revoked links and archived stations would have kept sending.
func TestLinkMirrorRowsExcludesRevokedLinksAndArchivedStations(t *testing.T) {
	st, ctx, _, _, actorID := stationsHarness(t)

	mk := func(name string) *store.Station {
		t.Helper()
		s, err := st.CreateStation(ctx, name, "", actorID)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	link := func(x, y *store.Station) *store.StationLink {
		t.Helper()
		reqID, err := st.CreateStationLinkRequest(ctx, "tok", x.StationID, y.StationID, "r", false)
		if err != nil {
			t.Fatal(err)
		}
		l, err := st.ApproveLinkRequest(ctx, reqID, actorID)
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	live, keep := mk("live-a"), mk("live-b")
	revokedA, revokedB := mk("revoked-a"), mk("revoked-b")
	archived := mk("archived-peer")

	linkLive := link(live, keep)
	linkRevoked := link(revokedA, revokedB)
	link(live, archived)

	if err := st.RevokeStationLink(ctx, linkRevoked.LinkID); err != nil {
		t.Fatal(err)
	}
	if err := st.ArchiveStation(ctx, archived.StationID, true); err != nil {
		t.Fatal(err)
	}

	rows, err := st.LinkMirrorRows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	has := func(x, y string) bool {
		if x > y {
			x, y = y, x
		}
		for _, r := range rows {
			if r[0] == x && r[1] == y {
				return true
			}
		}
		return false
	}
	// POSITIVE CONTROL FIRST: if the active link is missing, "the revoked one is absent"
	// would be true of an empty result and would prove nothing at all.
	if !has(linkLive.StationA, linkLive.StationB) {
		t.Fatal("the active link between two active stations is missing from the mirror rows")
	}
	if has(linkRevoked.StationA, linkRevoked.StationB) {
		t.Error("a REVOKED link is in the mirror rows — revocation is the operation that must work")
	}
	if has(live.StationID, archived.StationID) {
		t.Error("a link to an ARCHIVED station is in the mirror rows — a dormant link authorises " +
			"nothing until it is restored, which is what AreStationsLinked already promises")
	}
}
