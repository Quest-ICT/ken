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

// The console is the operator's brake on a standing relationship — and since links are created
// automatically on first contact, it is the ONLY brake there is.
//
// This asserts the whole round trip, because a store function with no route is the same kind of
// unfinished as a flag with no reader; the earlier version of this test caught exactly that, when
// the console promised "one click" and no click existed.
//
// SUSPEND, NOT REVOKE, and the word is the decision: "'suspend' button instead of revoke button (I
// want to be able to 'resume' it). 'revoke' concept is out of the table." A relationship between
// two of one human's own stations is never terminal, so the operation that ends traffic must be
// one they can undo.
func TestStationsConsoleSuspendsALinkAndWithdrawsThePairScope(t *testing.T) {
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
	if _, err := st.EnsureStationLink(ctx, devSt.StationID, prodSt.StationID, actorID); err != nil {
		t.Fatal(err)
	}
	link := linkBetween(t, ctx, st, devSt.StationID, prodSt.StationID)

	// A mailbox at each end, so the pair has somewhere to deliver and the mirror has
	// something to withdraw.
	if _, err := cs.MailboxFor(ctx, devSt.StationID, comm.Owner{TokenID: "tok-a", ActorID: actorID}); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.MailboxFor(ctx, prodSt.StationID, comm.Owner{TokenID: "tok-b", ActorID: actorID}); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(Handler(Deps{Store: st, Comm: cs}))
	defer srv.Close()
	jar, _ := cookiejar.New(nil)
	cli := &http.Client{Jar: jar}
	lcsrf := extract(t, cli, srv.URL+"/login", `name="lcsrf" value="([^"]+)"`)
	postForm(t, cli, srv.URL+"/login", url.Values{"name": {"admin"}, "password": {"supersecret"}, "lcsrf": {lcsrf}})

	// The page must offer the control, and must show the blast radius before the
	// click. A suspend button with no number is one people avoid or press twice.
	page := get(t, cli, srv.URL+"/stations")
	if !strings.Contains(page, "/stations/links/"+link.LinkID+"/suspend") {
		t.Fatal("the links table renders no suspend control — the store function stays unreachable, and an off-switch nobody can reach is the same as no off-switch")
	}
	if !strings.Contains(page, "data-confirm=") {
		t.Fatal("the suspend control carries no confirmation")
	}

	csrf := extract(t, cli, srv.URL+"/stations", `name="csrf" value="([^"]+)"`)
	postForm(t, cli, srv.URL+"/stations/links/"+link.LinkID+"/suspend", url.Values{"csrf": {csrf}})

	got, err := st.StationLinkByID(ctx, link.LinkID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "suspended" {
		// The message used to say `want "revoked"` while the check compared against "suspended" —
		// a failure that would have reported the wrong expectation to whoever hit it.
		t.Fatalf("link state is %q after suspend, want %q", got.State, "suspended")
	}
	// *** SUSPEND AND RESUME ARE NOW EXACT INVERSES, WHICH THEY WERE NOT. ***
	//
	// This used to assert that suspending also closed the link's live channels — the permission
	// and its traffic, because ending one without the other was the defect. Slice 7 deleted the
	// channel, so there is no second thing to end, and the asymmetry goes with it: suspend used to
	// be partly irreversible, since resume restored the permission and never the channels.
	//
	// So the assertion moves to the property that replaced it. Resume must put the link back
	// exactly as it was, with nothing left behind.
	csrf = extract(t, cli, srv.URL+"/stations", `name="csrf" value="([^"]+)"`)
	postForm(t, cli, srv.URL+"/stations/links/"+link.LinkID+"/suspend",
		url.Values{"csrf": {csrf}, "resume": {"1"}})

	back, err := st.StationLinkByID(ctx, link.LinkID)
	if err != nil {
		t.Fatal(err)
	}
	if back.State != "active" {
		t.Fatalf("link state is %q after resume, want %q — suspend and resume must be exact "+
			"inverses now that there is no traffic left for suspend to destroy", back.State, "active")
	}
}

// WITH NO COMM HANDLE THERE IS NOTHING TO SEVER, and the suspend must still land rather than 500.
//
// This is a FAULT PATH, not a configuration: nothing in Ken turns COMM off — "IN KEN NOTHING IS
// OPTIONAL" — so a nil handle means comm.db failed to open, which is exactly when the human most
// needs the durable half of the console to keep working. The link lives in ken.db and the decision
// is recorded there whether or not the message database is reachable.
func TestStationLinkSuspendWorksWithNoCommHandle(t *testing.T) {
	st, ctx, cli, base, actorID := stationsHarness(t)

	devSt, err := st.CreateStation(ctx, "dev", "", actorID)
	if err != nil {
		t.Fatal(err)
	}
	prodSt, err := st.CreateStation(ctx, "prod", "", actorID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnsureStationLink(ctx, devSt.StationID, prodSt.StationID, actorID); err != nil {
		t.Fatal(err)
	}
	link := linkBetween(t, ctx, st, devSt.StationID, prodSt.StationID)

	csrf := extract(t, cli, base+"/stations", `name="csrf" value="([^"]+)"`)
	postForm(t, cli, base+"/stations/links/"+link.LinkID+"/suspend", url.Values{"csrf": {csrf}})

	got, err := st.StationLinkByID(ctx, link.LinkID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "suspended" {
		t.Fatalf("link state is %q with COMM off, want %q", got.State, "suspended")
	}
}

// RESUME MUST NOT REPORT THAT THE LINK ENDED.
//
// `suspend := r.FormValue("resume") != "1"` was the only place the two verbs were distinguished, so
// every exit path below it flashed a revoke-era key and pressing Resume — the whole point of
// replacing revoke with a reversible control — told the operator "Link ended". The state change was
// correct; only the sentence was wrong, which is the kind of defect a suite full of state
// assertions never sees.
func TestResumeDoesNotTellTheOperatorTheLinkEnded(t *testing.T) {
	st, ctx, cli, base, actorID := stationsHarness(t)
	a, err := st.CreateStation(ctx, "dev", "", actorID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateStation(ctx, "prod", "", actorID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnsureStationLink(ctx, a.StationID, b.StationID, actorID); err != nil {
		t.Fatal(err)
	}
	link := linkBetween(t, ctx, st, a.StationID, b.StationID)

	csrf := extract(t, cli, base+"/stations", `name="csrf" value="([^"]+)"`)
	postForm(t, cli, base+"/stations/links/"+link.LinkID+"/suspend", url.Values{"csrf": {csrf}})
	csrf = extract(t, cli, base+"/stations", `name="csrf" value="([^"]+)"`)
	// THE FLASH IS READ OFF THE REDIRECT'S OWN BODY. A flash is consumed by the page that renders
	// it, so a later GET sees nothing and "the wrong message is absent" would be true of every
	// build, including one that says nothing at all.
	page := postForm(t, cli, base+"/stations/links/"+link.LinkID+"/suspend",
		url.Values{"csrf": {csrf}, "resume": {"1"}})
	// CONTROL: the state actually changed, so a passing message assertion is not covering an
	// operation that silently did nothing.
	got, err := st.StationLinkByID(ctx, link.LinkID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "active" {
		t.Fatalf("link state is %q after Resume, want active", got.State)
	}
	for _, ended := range []string{"Link ended", "Enlace terminado", "Lien terminé"} {
		if strings.Contains(page, ended) {
			t.Errorf("the page after Resume says %q — the control exists precisely because the "+
				"relationship did NOT end, and an operator reading that will not trust it again", ended)
		}
	}
	if !strings.Contains(page, "Link resumed") {
		t.Error("the page after Resume does not say the link was resumed, so a successful reversal " +
			"is indistinguishable from a no-op")
	}
}
