package web

import (
	"net/url"
	"strings"
	"testing"
)

// THE CONSOLE IS THE ONLY DOOR, so if it does not work rooms do not exist.
//
// There is no agent-facing create path — a room is a decision about which posts may
// talk to each other — which means every one of these paths is load-bearing in a way
// the station tools are not. A station can at least ASK for a station; nothing can ask
// for a room.
func TestAHumanCanCreateARoomAndFillIt(t *testing.T) {
	st, ctx, cli, base, actor := stationsHarness(t)

	alpha, err := st.CreateStation(ctx, spaceForSession, "prod-ops", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	beta, err := st.CreateStation(ctx, spaceForSession, "infra", "", actor)
	if err != nil {
		t.Fatal(err)
	}

	csrf := extract(t, cli, base+"/stations", `name="csrf" value="([^"]+)"`)
	postForm(t, cli, base+"/rooms", url.Values{
		"csrf": {csrf}, "name": {"deploys"}, "purpose": {"release coordination"}})

	rooms, err := st.ListRooms(ctx, spaceForSession)
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
	station, err := st.CreateStation(ctx, spaceForSession, "prod-ops", "", actor)
	if err != nil {
		t.Fatal(err)
	}

	before, err := st.RosterEpoch(ctx)
	if err != nil {
		t.Fatal(err)
	}

	csrf := extract(t, cli, base+"/stations", `name="csrf" value="([^"]+)"`)
	postForm(t, cli, base+"/rooms", url.Values{"csrf": {csrf}, "name": {"ops"}})
	rooms, _ := st.ListRooms(ctx, spaceForSession)
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
	station, err := st.CreateStation(ctx, spaceForSession, "prod-ops", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	csrf := extract(t, cli, base+"/stations", `name="csrf" value="([^"]+)"`)
	postForm(t, cli, base+"/rooms", url.Values{"csrf": {csrf}, "name": {"ops"}})
	rooms, _ := st.ListRooms(ctx, spaceForSession)
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
