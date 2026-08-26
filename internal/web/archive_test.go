package web

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/Quest-ICT/ken/internal/comm"
)

// ARCHIVING A STATION MUST ACTUALLY STOP IT, and until now it stopped nothing on COMM.
//
// An archived station stayed a full first-class recipient of room and broadcast mail:
// counted in recipients, audience_size and broadcast_reaches, holding delivery rows nobody
// could read and nobody could ack. Two consequences, both live rather than theoretical —
// the SENDER got a spurious `expired` notice naming the retired post about a message-TTL
// later, on every room message; and because backpressure counts open deliveries per SCOPE,
// the dead member's permanent backlog consumed the LIVE room's budget until everyone was
// refused.
//
// Asserted through what SendToRoom actually delivers, never through the console render: a
// page that stops listing something proves nothing about routing.
func TestArchivingAStationStopsRoomDelivery(t *testing.T) {
	st, ctx, cli, base, actor := stationsHarnessWithComm(t)
	live, err := st.CreateStation(ctx, "still-here", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	retired, err := st.CreateStation(ctx, "retiring", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	// A THIRD, LIVE member. Without it, archiving the only other member makes the room
	// unsendable and the send fails before it can demonstrate anything about routing —
	// which is a real behaviour, tested separately below, but not the one under test here.
	third, err := st.CreateStation(ctx, "also-here", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	csrf := extract(t, cli, base+"/stations", `name="csrf" value="([^"]+)"`)
	postForm(t, cli, base+"/rooms", url.Values{"csrf": {csrf}, "name": {"ops"}})
	rooms, _ := st.ListRooms(ctx)
	roomID := rooms[0].RoomID
	for _, s := range []string{live.StationID, retired.StationID, third.StationID} {
		csrf = extract(t, cli, base+"/stations", `name="csrf" value="([^"]+)"`)
		postForm(t, cli, base+"/rooms/"+roomID+"/members", url.Values{"csrf": {csrf}, "station_id": {s}})
	}

	cs := commOf(t)
	sender := boundEndpoint(t, cs, live.StationID)

	// CONTROL, BEFORE THE ARCHIVE: the retired station really does receive today, so a zero
	// afterwards is about archiving rather than about a room that never worked.
	before, err := cs.SendToRoom(ctx, sender, roomID, "while you are still here", comm.SendOpts{})
	if err != nil {
		t.Fatalf("setup send: %v", err)
	}
	if n := deliveriesFor(t, cs, before.MessageID, "s:"+retired.StationID); n != 1 {
		t.Fatalf("the retired station got %d deliveries BEFORE archiving — the fixture is not wired", n)
	}

	csrf = extract(t, cli, base+"/stations", `name="csrf" value="([^"]+)"`)
	postForm(t, cli, base+"/stations/"+retired.StationID+"/archive", url.Values{"csrf": {csrf}, "archived": {"1"}})

	after, err := cs.SendToRoom(ctx, sender, roomID, "after you retired", comm.SendOpts{})
	if err != nil {
		t.Fatalf("send after archiving: %v", err)
	}
	if n := deliveriesFor(t, cs, after.MessageID, "s:"+retired.StationID); n != 0 {
		t.Fatalf("an ARCHIVED station received %d deliveries.\n"+
			"It is counted in the audience, holds mail nobody can read or ack, its backlog eats the "+
			"live room's backpressure budget, and the sender gets a spurious expiry notice naming it.", n)
	}
	if after.Recipients != 1 {
		t.Errorf("the send reports %d recipients, want 1 — the archived member is still counted", after.Recipients)
	}

	// AND THE PRE-EXISTING MAIL IS UNTOUCHED. Archiving must not tidy away deliveries that
	// were legitimately made while the station was live.
	if n := deliveriesFor(t, cs, before.MessageID, "s:"+retired.StationID); n != 1 {
		t.Errorf("archiving deleted mail queued before it: %d rows left", n)
	}

	// REVERSIBLE, with the membership never having been destroyed.
	csrf = extract(t, cli, base+"/stations", `name="csrf" value="([^"]+)"`)
	postForm(t, cli, base+"/stations/"+retired.StationID+"/archive", url.Values{"csrf": {csrf}, "archived": {"0"}})
	back, err := cs.SendToRoom(ctx, sender, roomID, "welcome back", comm.SendOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if n := deliveriesFor(t, cs, back.MessageID, "s:"+retired.StationID); n != 1 {
		t.Fatalf("unarchiving did not restore delivery (%d rows) — archiving destroyed something", n)
	}
	members, err := st.RoomMembers(ctx, roomID)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 3 {
		t.Fatalf("room membership is %d after an archive round-trip, want 3 throughout", len(members))
	}
}

// THE ROSTER EPOCH MOVES BOTH WAYS. Archiving changes who a room delivers to, which is a
// membership change in all but name — and one nothing can detect is one nobody is told about.
func TestArchivingAStationMovesTheRosterEpoch(t *testing.T) {
	st, ctx, cli, base, actor := stationsHarnessWithComm(t)
	s1, err := st.CreateStation(ctx, "s1", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	before, err := st.RosterEpoch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	csrf := extract(t, cli, base+"/stations", `name="csrf" value="([^"]+)"`)
	postForm(t, cli, base+"/stations/"+s1.StationID+"/archive", url.Values{"csrf": {csrf}, "archived": {"1"}})
	mid, err := st.RosterEpoch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if mid <= before {
		t.Fatalf("archiving left the roster epoch at %d — no session can detect the change", mid)
	}
	csrf = extract(t, cli, base+"/stations", `name="csrf" value="([^"]+)"`)
	postForm(t, cli, base+"/stations/"+s1.StationID+"/archive", url.Values{"csrf": {csrf}, "archived": {"0"}})
	after, err := st.RosterEpoch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after <= mid {
		// Asserted separately: an implementation that bumps only on the way in looks
		// correct in a one-directional test and leaves unarchive undetectable.
		t.Fatalf("UNarchiving left the epoch at %d", after)
	}
}

// --- helpers ---------------------------------------------------------------------

// commOf returns the harness's comm store.
func commOf(t *testing.T) *comm.Store {
	t.Helper()
	if harnessComm == nil {
		t.Fatal("no comm store: use stationsHarnessWithComm")
	}
	return harnessComm
}

// boundEndpoint registers an endpoint and binds it to a station, so it can send to rooms.
func boundEndpoint(t *testing.T, cs *comm.Store, stationID string) *comm.Endpoint {
	t.Helper()
	ctx := context.Background()
	ep, secret, err := cs.RegisterEndpoint(ctx, comm.Owner{TokenID: "tok-" + stationID, ActorID: 7}, stationID, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := cs.BindEndpointToStation(ctx, ep.EndpointID, stationID, "kens_k"); err != nil {
		t.Fatal(err)
	}
	bound, err := cs.AuthenticateEndpoint(ctx, ep.EndpointID, secret)
	if err != nil {
		t.Fatal(err)
	}
	return bound
}

func deliveriesFor(t *testing.T, cs *comm.Store, messageID, party string) int {
	t.Helper()
	var n int
	if err := cs.R.QueryRowContext(context.Background(), `
SELECT COUNT(*) FROM delivery d JOIN message m ON m.id = d.message_row
 WHERE m.message_id=? AND d.party_key=?`, messageID, party).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A ROOM WHOSE OTHER MEMBERS ARE ALL RETIRED REFUSES, AND SAYS WHY.
//
// The console still lists them — membership is durable and archiving is reversible — so an
// error saying "that room has no members" sends its reader to look for a problem the console
// will contradict. The refusal has to name the archived-station case or it is worse than
// silence.
func TestSendingIntoARoomOfArchivedStationsExplainsItself(t *testing.T) {
	st, ctx, cli, base, actor := stationsHarnessWithComm(t)
	live, _ := st.CreateStation(ctx, "still-here", "", actor)
	retired, _ := st.CreateStation(ctx, "retiring", "", actor)
	csrf := extract(t, cli, base+"/stations", `name="csrf" value="([^"]+)"`)
	postForm(t, cli, base+"/rooms", url.Values{"csrf": {csrf}, "name": {"ops"}})
	rooms, _ := st.ListRooms(ctx)
	roomID := rooms[0].RoomID
	for _, s := range []string{live.StationID, retired.StationID} {
		csrf = extract(t, cli, base+"/stations", `name="csrf" value="([^"]+)"`)
		postForm(t, cli, base+"/rooms/"+roomID+"/members", url.Values{"csrf": {csrf}, "station_id": {s}})
	}
	csrf = extract(t, cli, base+"/stations", `name="csrf" value="([^"]+)"`)
	postForm(t, cli, base+"/stations/"+retired.StationID+"/archive", url.Values{"csrf": {csrf}, "archived": {"1"}})

	cs := commOf(t)
	sender := boundEndpoint(t, cs, live.StationID)
	_, err := cs.SendToRoom(ctx, sender, roomID, "anyone?", comm.SendOpts{})
	if err == nil {
		t.Fatal("the send succeeded into a room whose only other member is archived")
	}
	if !strings.Contains(err.Error(), "ARCHIVED") {
		t.Fatalf("the refusal is %q — it does not mention the archived case, so its reader "+
			"goes looking for a membership problem the console will contradict", err)
	}
	// AND THE CONSOLE STILL SHOWS BOTH, which is what makes the wording load-bearing.
	members, _ := st.RoomMembers(ctx, roomID)
	if len(members) != 2 {
		t.Fatalf("the console shows %d members; the point of the wording is that it still shows 2", len(members))
	}
}
