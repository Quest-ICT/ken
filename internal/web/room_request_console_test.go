package web

import (
	"net/url"
	"strings"
	"testing"
)

// *** THE HUMAN'S HALF, THROUGH THE MUX. ***
//
// A store function with no button is a decision that was implemented and cannot be exercised —
// this project has a dedicated gate for that class, and the whole point of room requests is that a
// HUMAN decides. So the approval is asserted end to end: the request is filed, the console renders
// it, the form posts, and a room exists with the name the human typed.
func TestApprovingARoomRequestFromTheConsole(t *testing.T) {
	st, ctx, cli, base, actor := stationsHarness(t)
	s, err := st.CreateStation(ctx, "asker", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.CreateRoomRequest(ctx, "tok", s.StationID, "ops-suggested", "three peers to coordinate", false)
	if err != nil {
		t.Fatal(err)
	}

	// IT RENDERS, and it renders as a ROOM rather than falling into the station form. Before this
	// feature the template had no room branch and the station branch emitted no `kind` at all, so
	// an unrecognised kind landed there and was refused with a message naming the wrong function.
	page := get(t, cli, base+"/stations")
	if !strings.Contains(page, "room request") {
		t.Error("the pending queue does not label this as a room request")
	}
	if !strings.Contains(page, `name="kind" value="room"`) {
		t.Fatal("the room branch does not declare its kind, so approving it would take the station " +
			"branch and be refused by the wrong function")
	}
	// The suggestion is offered to the human, and it is only a suggestion.
	if !strings.Contains(page, "ops-suggested") {
		t.Error("the session's name hint is not shown, so the human decides with less than they were given")
	}

	csrf := extract(t, cli, base+"/stations", `name="csrf" value="([^"]+)"`)
	postForm(t, cli, base+"/stations/requests/"+id+"/approve",
		url.Values{"csrf": {csrf}, "kind": {"room"}, "name": {"ops"}})

	rooms, err := st.ListRooms(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var made bool
	for _, r := range rooms {
		if r.Name == "ops" {
			made = true
			// EMPTY. Membership is the human's, added afterwards.
			m, err := st.RoomMembers(ctx, r.RoomID)
			if err != nil {
				t.Fatal(err)
			}
			if len(m) != 0 {
				t.Errorf("the console approval added %d members", len(m))
			}
		}
		if r.Name == "ops-suggested" {
			t.Error("the room was created under the SESSION's suggested name — an agent chose what " +
				"its human sees in the room list")
		}
	}
	if !made {
		t.Fatal("no room called ops exists after approving through the console")
	}
}

// DENYING WORKS THROUGH THE SAME QUEUE. The deny form is kind-agnostic, so this asserts the room
// request settles rather than sticking in front of the human forever.
func TestDenyingARoomRequestFromTheConsole(t *testing.T) {
	st, ctx, cli, base, actor := stationsHarness(t)
	s, err := st.CreateStation(ctx, "asker", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.CreateRoomRequest(ctx, "tok", s.StationID, "", "a reason", false)
	if err != nil {
		t.Fatal(err)
	}
	csrf := extract(t, cli, base+"/stations", `name="csrf" value="([^"]+)"`)
	postForm(t, cli, base+"/stations/requests/"+id+"/deny",
		url.Values{"csrf": {csrf}, "reason": {"use the existing room"}})

	pending, err := st.PendingStationRequests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pending {
		if p.RequestID == id {
			t.Error("a denied room request is still pending")
		}
	}
	if n, err := st.ListRooms(ctx); err != nil {
		t.Fatal(err)
	} else if len(n) != 0 {
		t.Error("denying created a room")
	}
}
