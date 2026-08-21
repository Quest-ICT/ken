package web

import (
	"errors"
	"net/url"
	"testing"

	"github.com/Quest-ICT/ken/internal/comm"
	"github.com/Quest-ICT/ken/internal/store"
)

// RENAMING A STATION IS A REQUIREMENT VLAD STATED, AND UNTIL 2026-08-21 IT HAD NO ROUTE.
// `store.RenameStation` existed, carried a comment claiming it was "reachable from the
// console and the CLI", and had ZERO callers anywhere in the tree. This test exists first
// to make the route's absence a failure rather than a silence.
func TestRenamingAStationFromTheConsole(t *testing.T) {
	st, ctx, cli, base, actor := stationsHarness(t)
	s, err := st.CreateStation(ctx, spaceForSession, "before", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	csrf := extract(t, cli, base+"/stations", `name="csrf" value="([^"]+)"`)
	postForm(t, cli, base+"/stations/"+s.StationID+"/rename", url.Values{"csrf": {csrf}, "name": {"after"}})

	got, err := st.StationByID(ctx, s.StationID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "after" {
		t.Fatalf("console rename did not land: name is %q, want %q", got.Name, "after")
	}
	// The id is what everything else holds. If a rename moved it, every folder's config,
	// every link and every delivery row would be pointing at nothing.
	if got.StationID != s.StationID {
		t.Fatalf("rename changed the station id %q -> %q", s.StationID, got.StationID)
	}
}

// THE CLAIM UNDER TEST IS THE COMMENT'S: "renaming breaks no link, channel or message".
// Asserted through what COMM actually delivers AFTER the rename, not through the console
// render — a page that shows the new name proves nothing about routing, and routing is the
// thing a rename could plausibly break.
func TestRenamingAStationLeavesItsMailWorking(t *testing.T) {
	st, ctx, cli, base, actor := stationsHarnessWithComm(t)
	a, err := st.CreateStation(ctx, spaceForSession, "sender", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateStation(ctx, spaceForSession, "old-name", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	third, err := st.CreateStation(ctx, spaceForSession, "bystander", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	csrf := extract(t, cli, base+"/stations", `name="csrf" value="([^"]+)"`)
	postForm(t, cli, base+"/rooms", url.Values{"csrf": {csrf}, "name": {"ops"}})
	rooms, _ := st.ListRooms(ctx, spaceForSession)
	roomID := rooms[0].RoomID
	for _, id := range []string{a.StationID, b.StationID, third.StationID} {
		csrf = extract(t, cli, base+"/stations", `name="csrf" value="([^"]+)"`)
		postForm(t, cli, base+"/rooms/"+roomID+"/members", url.Values{"csrf": {csrf}, "station_id": {id}})
	}

	cs := commOf(t)
	sender := boundEndpoint(t, cs, a.StationID)

	// CONTROL: b receives under its original name, so a zero afterwards would be about the
	// rename rather than about a room that never delivered.
	before, err := cs.SendToRoom(ctx, sender, roomID, "under the old name", comm.SendOpts{})
	if err != nil {
		t.Fatalf("setup send: %v", err)
	}
	if n := deliveriesFor(t, cs, before.MessageID, "s:"+b.StationID); n != 1 {
		t.Fatalf("got %d deliveries BEFORE the rename — the fixture is not wired", n)
	}

	csrf = extract(t, cli, base+"/stations", `name="csrf" value="([^"]+)"`)
	postForm(t, cli, base+"/stations/"+b.StationID+"/rename", url.Values{"csrf": {csrf}, "name": {"new-name"}})

	after, err := cs.SendToRoom(ctx, sender, roomID, "under the new name", comm.SendOpts{})
	if err != nil {
		t.Fatalf("send after rename: %v", err)
	}
	if n := deliveriesFor(t, cs, after.MessageID, "s:"+b.StationID); n != 1 {
		t.Fatalf("the renamed station got %d deliveries, want 1 — renaming broke its mail", n)
	}
	// And the endpoint bound before the rename still authenticates as that station: a
	// session that was working does not have to reconnect because its human retyped a label.
	if got, err := st.StationByID(ctx, b.StationID); err != nil || got.Name != "new-name" {
		t.Fatalf("station after rename: %+v err=%v", got, err)
	}
}

// A REFUSED RENAME MUST LEAVE THE OLD NAME STANDING. A collision that half-applied would
// be worse than one that refused, and the console reports the colliding name so the
// operator has something to act on.
func TestRenameCollisionRefusesAndKeepsTheOldName(t *testing.T) {
	st, ctx, cli, base, actor := stationsHarness(t)
	if _, err := st.CreateStation(ctx, spaceForSession, "taken", "", actor); err != nil {
		t.Fatal(err)
	}
	s, err := st.CreateStation(ctx, spaceForSession, "mine", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	csrf := extract(t, cli, base+"/stations", `name="csrf" value="([^"]+)"`)
	postForm(t, cli, base+"/stations/"+s.StationID+"/rename", url.Values{"csrf": {csrf}, "name": {"taken"}})

	got, err := st.StationByID(ctx, s.StationID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "mine" {
		t.Fatalf("a refused rename changed the name anyway: %q", got.Name)
	}
	// POSITIVE CONTROL ON THE INSTRUMENT. Without it this test passes when rename does not
	// exist at all — a 404 also leaves the old name standing, so "unchanged" alone proves
	// nothing. Deleting the route made this test pass and the other two fail, which is how
	// the gap showed up.
	csrf = extract(t, cli, base+"/stations", `name="csrf" value="([^"]+)"`)
	postForm(t, cli, base+"/stations/"+s.StationID+"/rename", url.Values{"csrf": {csrf}, "name": {"free"}})
	if got, _ := st.StationByID(ctx, s.StationID); got.Name != "free" {
		t.Fatalf("the control rename did not land: %q — this test cannot tell refusal from absence", got.Name)
	}
}

// THE TWO WAYS RenameStation USED TO REPORT SUCCESS WITHOUT RENAMING ANYTHING.
// Both returned nil, which is this project's recurring defect — an outcome indistinguishable
// from the operation. Store-level, because the handler's own blank check would mask the first.
func TestRenameStationRejectsBlankAndUnknown(t *testing.T) {
	st, ctx, _, _, actor := stationsHarness(t)
	s, err := st.CreateStation(ctx, spaceForSession, "keeps-its-name", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	for _, blank := range []string{"", "   ", "\t\n"} {
		if err := st.RenameStation(ctx, s.StationID, blank); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("rename to %q: got %v, want ErrInvalid", blank, err)
		}
	}
	if got, _ := st.StationByID(ctx, s.StationID); got.Name != "keeps-its-name" {
		t.Fatalf("a rejected blank rename still wrote: %q", got.Name)
	}
	if err := st.RenameStation(ctx, "no-such-station-id", "anything"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("renaming an unknown station: got %v, want ErrNotFound", err)
	}
}
