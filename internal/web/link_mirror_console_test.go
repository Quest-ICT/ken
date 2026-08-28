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

// SUSPENDING A LINK MUST STOP AUTHORISING THE PAIR, AND RESUMING IT MUST START AGAIN.
//
// The console writes the decision to ken.db and comm.db learns it only through the mirror
// refresh. Both halves survived a mutation run that simply DELETED the refresh call — the
// decision still landed, the tests still passed, and `comm_send{to_station}` would have
// refused every message under a permission the human had just granted. That is the "a store
// function with no route" shape this package has shipped before, in the other direction.
//
// APPROVAL IS NO LONGER ONE OF THE EDGES, because there is no approval: links are created on
// first contact by the send path, which pushes the mirror itself. What the console still owns
// is the off-switch and the way back on, and those are what this asserts — through real HTTP
// form posts, in both directions, because a suspend that reaches the mirror and a resume that
// does not is a link the human can see, believe in, and never use.
func TestConsoleLinkSuspendAndResumeReachTheCommMirror(t *testing.T) {
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
	// Seeded the way the send path seeds it: created on first contact, then projected. The
	// projection is done by hand here because the comm SURFACE does it in production and this
	// test drives the console, not the surface.
	if _, err := st.EnsureStationLink(ctx, a.StationID, b.StationID, actorID); err != nil {
		t.Fatal(err)
	}
	syncMirror := func() {
		t.Helper()
		rows, err := st.LinkMirrorRows(ctx)
		if err != nil {
			t.Fatal(err)
		}
		epoch, err := st.RosterEpoch(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := cs.ReplaceLinkMirror(ctx, rows, epoch); err != nil {
			t.Fatal(err)
		}
	}
	syncMirror()

	srv := httptest.NewServer(Handler(Deps{Store: st, Comm: cs}))
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

	// POSITIVE CONTROL. Without this, "the pair is gone after suspend" would be true of a
	// mirror that never held it, and the assertion would certify nothing.
	if !linked() {
		t.Fatal("the pair is not in the mirror before anything was suspended — the seed is broken, so nothing below would mean anything")
	}

	links, err := st.ListStationLinks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 {
		t.Fatalf("%d links after one first contact, want 1", len(links))
	}
	link := links[0]

	// THE EDGE THAT MATTERS MOST: a suspended link must stop authorising. This is why the
	// refresh sits before every early return in the handler rather than after the channel
	// sweep that can fail.
	csrf := extract(t, cli, srv.URL+"/stations", `name="csrf" value="([^"]+)"`)
	postForm(t, cli, srv.URL+"/stations/links/"+link.LinkID+"/suspend", url.Values{"csrf": {csrf}})

	if linked() {
		t.Fatal("the link was suspended and the mirror still authorises the pair — the " +
			"human believes they withdrew a permission that is still in force")
	}

	// AND BACK. Resume is the half that only exists because Vlad asked for an off-switch he
	// could undo: "'suspend' button instead of revoke button (I want to be able to 'resume'
	// it)". A resume that does not reach the mirror gives him a link the console shows as
	// active and comm refuses to use — the same disagreement as the suspend edge, in the
	// direction that is quieter, because nothing looks wrong until a message is sent.
	csrf = extract(t, cli, srv.URL+"/stations", `name="csrf" value="([^"]+)"`)
	postForm(t, cli, srv.URL+"/stations/links/"+link.LinkID+"/suspend", url.Values{"csrf": {csrf}, "resume": {"1"}})

	if !linked() {
		t.Fatal("the link was resumed and comm.db never learned it — the console shows an " +
			"active link that every comm_send{to_station} would refuse")
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
		if _, err := st.EnsureStationLink(ctx, x.StationID, y.StationID, actorID); err != nil {
			t.Fatal(err)
		}
		return linkBetween(t, ctx, st, x.StationID, y.StationID)
	}
	live, keep := mk("live-a"), mk("live-b")
	revokedA, revokedB := mk("revoked-a"), mk("revoked-b")
	archived := mk("archived-peer")

	linkLive := link(live, keep)
	linkRevoked := link(revokedA, revokedB)
	link(live, archived)

	if err := st.SetStationLinkSuspended(ctx, linkRevoked.LinkID, true); err != nil {
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

// linkBetween finds the link EnsureStationLink just created.
//
// It exists because there is no longer an approval that HANDS BACK the link it created:
// EnsureStationLink reports only whether it made one, since the send path that calls it has no
// use for the row. Tests do, so they look it up — and this fails loudly rather than returning
// nil, because a nil link dereferenced three lines later reports a panic instead of the missing
// row that caused it.
func linkBetween(t *testing.T, ctx context.Context, st *store.Store, x, y string) *store.StationLink {
	t.Helper()
	all, err := st.ListStationLinks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := range all {
		if (all[i].StationA == x && all[i].StationB == y) || (all[i].StationA == y && all[i].StationB == x) {
			return &all[i]
		}
	}
	t.Fatalf("no link between %s and %s — EnsureStationLink reported no error and left no row", x, y)
	return nil
}
