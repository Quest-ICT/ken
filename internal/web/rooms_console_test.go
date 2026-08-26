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

// THE CONSOLE IS THE ONLY DOOR, so if it does not work rooms do not exist.
//
// There is no agent-facing create path — a room is a decision about which posts may
// talk to each other — which means every one of these paths is load-bearing in a way
// the station tools are not. A station can at least ASK for a station; nothing can ask
// for a room.
func TestAHumanCanCreateARoomAndFillIt(t *testing.T) {
	st, ctx, cli, base, actor := stationsHarness(t)

	alpha, err := st.CreateStation(ctx, "prod-ops", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	beta, err := st.CreateStation(ctx, "infra", "", actor)
	if err != nil {
		t.Fatal(err)
	}

	csrf := extract(t, cli, base+"/stations", `name="csrf" value="([^"]+)"`)
	postForm(t, cli, base+"/rooms", url.Values{
		"csrf": {csrf}, "name": {"deploys"}, "purpose": {"release coordination"}})

	rooms, err := st.ListRooms(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rooms) != 1 || rooms[0].Name != "deploys" {
		t.Fatalf("rooms after create = %+v, want one named deploys", rooms)
	}
	roomID := rooms[0].RoomID

	page := get(t, cli, base+"/stations")
	for _, want := range []string{"deploys", "release coordination"} {
		if !strings.Contains(page, want) {
			t.Fatalf("the room does not reach the console: %q absent", want)
		}
	}

	// Fill it.
	for _, s := range []string{alpha.StationID, beta.StationID} {
		csrf = extract(t, cli, base+"/stations", `name="csrf" value="([^"]+)"`)
		postForm(t, cli, base+"/rooms/"+roomID+"/members",
			url.Values{"csrf": {csrf}, "station_id": {s}})
	}
	members, err := st.RoomMembers(ctx, roomID)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 2 {
		t.Fatalf("%d members after adding two", len(members))
	}

	// And empty it again — the remove path is what an operator reaches for when a
	// station should stop hearing something, and it must not require deleting the room.
	csrf = extract(t, cli, base+"/stations", `name="csrf" value="([^"]+)"`)
	postForm(t, cli, base+"/rooms/"+roomID+"/members",
		url.Values{"csrf": {csrf}, "station_id": {beta.StationID}, "remove": {"1"}})
	members, err = st.RoomMembers(ctx, roomID)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 1 || members[0].StationID != alpha.StationID {
		t.Fatalf("members after removing one = %+v, want only prod-ops", members)
	}
}

// EVERY MEMBERSHIP WRITE MOVES THE ROSTER EPOCH.
//
// A membership change that did not move it is a change no session can detect: the epoch
// is what tells a session given a standing instruction about a room that the room it was
// told about is not the room that exists now.
func TestEveryMembershipWriteAdvancesTheRosterEpoch(t *testing.T) {
	st, ctx, cli, base, actor := stationsHarness(t)
	station, err := st.CreateStation(ctx, "prod-ops", "", actor)
	if err != nil {
		t.Fatal(err)
	}

	before, err := st.RosterEpoch(ctx)
	if err != nil {
		t.Fatal(err)
	}

	csrf := extract(t, cli, base+"/stations", `name="csrf" value="([^"]+)"`)
	postForm(t, cli, base+"/rooms", url.Values{"csrf": {csrf}, "name": {"ops"}})
	rooms, _ := st.ListRooms(ctx)
	if len(rooms) != 1 {
		t.Fatalf("expected one room, got %d", len(rooms))
	}

	csrf = extract(t, cli, base+"/stations", `name="csrf" value="([^"]+)"`)
	postForm(t, cli, base+"/rooms/"+rooms[0].RoomID+"/members",
		url.Values{"csrf": {csrf}, "station_id": {station.StationID}})

	after, err := st.RosterEpoch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after <= before {
		t.Fatalf("roster epoch %d -> %d after creating a room and adding a member — "+
			"a membership change nothing can detect is a membership change nobody is told about", before, after)
	}
}

// Archiving REMOVES a room from what the mirror will carry, so it stops accepting sends
// at once rather than at the next restart. Asserted through RoomMirrorRows, which is the
// exact query the sync uses — testing the console's own filter rather than a paraphrase.
func TestArchivingARoomTakesItOutOfWhatTheMirrorWillCarry(t *testing.T) {
	st, ctx, cli, base, actor := stationsHarness(t)
	station, err := st.CreateStation(ctx, "prod-ops", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	csrf := extract(t, cli, base+"/stations", `name="csrf" value="([^"]+)"`)
	postForm(t, cli, base+"/rooms", url.Values{"csrf": {csrf}, "name": {"ops"}})
	rooms, _ := st.ListRooms(ctx)
	roomID := rooms[0].RoomID
	csrf = extract(t, cli, base+"/stations", `name="csrf" value="([^"]+)"`)
	postForm(t, cli, base+"/rooms/"+roomID+"/members",
		url.Values{"csrf": {csrf}, "station_id": {station.StationID}})

	// CONTROL: it is carried while active, so the assertion below is about archiving
	// rather than about the mirror query being empty for some other reason.
	live, err := st.RoomMirrorRows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(live[roomID]) != 1 {
		t.Fatalf("an active room contributes %d mirror rows, want 1", len(live[roomID]))
	}

	csrf = extract(t, cli, base+"/stations", `name="csrf" value="([^"]+)"`)
	postForm(t, cli, base+"/rooms/"+roomID+"/archive",
		url.Values{"csrf": {csrf}, "archived": {"1"}})

	after, err := st.RoomMirrorRows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(after[roomID]) != 0 {
		t.Fatalf("an archived room still contributes %d mirror rows — it would keep accepting "+
			"messages until the next restart", len(after[roomID]))
	}
	// The membership itself is NOT deleted: archiving is reversible, and reopening a
	// room whose members had been dropped would be a different, worse operation.
	members, err := st.RoomMembers(ctx, roomID)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 1 {
		t.Fatalf("archiving deleted the membership (%d left) — reopening would not restore the room", len(members))
	}
}

// D1 — A MEMBER THAT CANNOT RECEIVE MUST SAY SO. The root defect of the rooms
// debugging, and the one neither agent found because both had the symptom.
//
// Room membership keys on the party `s:<station_id>`; an endpoint with no station
// resolves to `e:<rowid>`, which can never match. So a station with no bound endpoint is
// a member on paper and deaf in practice — and the console flashed success without a
// word. ken-promo was added that way and concluded from the silence that rooms were
// RECEIVE-ONLY, reporting that to its human. It is the station whose charter is
// describing the product.
//
// Admitted and flagged rather than refused: Vlad's specification is that membership is
// durable — "once a room is created and parties are added, they should permanently be
// able to use it" — so adding a station before its session binds is legitimate and the
// membership is correct. What was missing was any surface saying it cannot yet hear.
func TestTheConsoleShowsAMemberThatCannotReceive(t *testing.T) {
	// A COMM-BACKED harness, because the badge is deliberately silent without one:
	// with COMM off this package has no endpoint table to ask, and printing "not bound"
	// would assert a fact nobody checked. The first version of this test used the plain
	// harness and failed for exactly that reason — which is the rule working, not a bug.
	st, ctx, cli, base, actor := stationsHarnessWithComm(t)
	station, err := st.CreateStation(ctx, "ken-promo", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	csrf := extract(t, cli, base+"/stations", `name="csrf" value="([^"]+)"`)
	postForm(t, cli, base+"/rooms", url.Values{"csrf": {csrf}, "name": {"Ken management"}})
	rooms, _ := st.ListRooms(ctx)
	if len(rooms) != 1 {
		t.Fatalf("expected one room, got %d", len(rooms))
	}
	csrf = extract(t, cli, base+"/stations", `name="csrf" value="([^"]+)"`)
	postForm(t, cli, base+"/rooms/"+rooms[0].RoomID+"/members",
		url.Values{"csrf": {csrf}, "station_id": {station.StationID}})

	page := get(t, cli, base+"/stations")

	// The station appears — the add IS legitimate and must not be refused.
	if !strings.Contains(page, "ken-promo") {
		t.Fatal("the member is not listed at all; the add was refused or lost")
	}
	// AND the console says it cannot hear. Without this the operator sees a correct-
	// looking membership and the session sees silence, which is how a wrong belief about
	// the product gets formed and reported upward.
	if !strings.Contains(page, "not bound") {
		t.Fatal("a station with NO BOUND ENDPOINT is shown as an ordinary member.\n" +
			"The operator gets a success flash, the session gets silence, and nothing anywhere " +
			"connects the two — which is exactly how ken-promo concluded rooms were receive-only.")
	}
	// And the room itself carries the count, so it is visible without expanding.
	if !strings.Contains(page, "cannot receive") {
		t.Error("the room does not summarise that some members cannot receive")
	}
}

// stationsHarnessWithComm is stationsHarness plus a real comm store, so the console can
// answer "is this station bound" instead of "unknown".
func stationsHarnessWithComm(t *testing.T) (*store.Store, context.Context, *http.Client, string, int64) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	cs, err := comm.Open(filepath.Join(dir, "comm.db"), comm.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
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
	harnessComm = cs
	srv := httptest.NewServer(Handler(Deps{Store: st, Comm: cs, StationsEnabled: true}))
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	cli := &http.Client{Jar: jar}
	lcsrf := extract(t, cli, srv.URL+"/login", `name="lcsrf" value="([^"]+)"`)
	postForm(t, cli, srv.URL+"/login",
		url.Values{"name": {"admin"}, "password": {"supersecret"}, "lcsrf": {lcsrf}})
	return st, ctx, cli, srv.URL, actorID
}

// harnessComm is the comm store built by stationsHarnessWithComm, exposed so a test can
// assert on what SendToRoom actually DELIVERS rather than on what a page renders.
var harnessComm *comm.Store
