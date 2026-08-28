package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func roomReqFixture(t *testing.T) (*Store, context.Context, int64, string) {
	t.Helper()
	st, ctx, actorID := stationFixture(t)
	s, err := st.CreateStation(ctx, "asker", "", actorID)
	if err != nil {
		t.Fatal(err)
	}
	return st, ctx, actorID, s.StationID
}

// *** A DECISION VLAD MADE ON 2026-08-06, BUILT 2026-08-27. ***
//
// "sessions may REQUEST, human approves — NOT the humans-only option I recommended … the agent
// proposes, the human promotes." It was declined in code on schema-cost grounds that migration
// 0024 shows are no longer true, and never reversed.
//
// The end-to-end property: a session asks, a human approves with THEIR name, and a room exists.
func TestARoomRequestBecomesARoomTheHumanNamed(t *testing.T) {
	st, ctx, actorID, from := roomReqFixture(t)

	id, err := st.CreateRoomRequest(ctx, "tok", from, "ops-suggested", "we need to coordinate three peers", false)
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("the request was silently dropped on a station with no denial history")
	}
	pending, err := st.PendingStationRequests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, p := range pending {
		if p.RequestID == id {
			found = true
			if p.Kind != "room" {
				t.Errorf("filed as kind %q", p.Kind)
			}
			if p.NameHint != "ops-suggested" {
				t.Errorf("name hint is %q, want the session's suggestion", p.NameHint)
			}
		}
	}
	if !found {
		t.Fatal("the room request is not in the console's pending queue, so no human will ever see it")
	}

	// THE HUMAN'S NAME WINS. The hint is documented non-binding; an approval that used it would
	// let a session choose what its human sees in the room list.
	room, err := st.ApproveRoomRequest(ctx, id, "ops", actorID)
	if err != nil {
		t.Fatal(err)
	}
	if room.Name != "ops" {
		t.Errorf("the room is called %q, want the name the HUMAN typed", room.Name)
	}
	if room.Kind != "topic" {
		t.Errorf("room kind is %q, want topic — the same shape CreateRoom produces", room.Kind)
	}

	// AND IT IS EMPTY. Membership is the human's, entirely — that is what keeps migration 0017's
	// surviving argument true with this feature in place.
	members, err := st.RoomMembers(ctx, room.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 0 {
		t.Errorf("the approval added %d members — the agent's ask decided who talks to whom", len(members))
	}
	// The request is settled, so it stops appearing in front of the human.
	after, _ := st.PendingStationRequests(ctx)
	for _, p := range after {
		if p.RequestID == id {
			t.Error("the request is still pending after approval")
		}
	}
}

// A ROOM REQUEST NAMES NOBODY, and that is the safety property rather than a simplification: the
// human stays the sole decider of membership, and there is no station name to resolve, so the
// enumeration oracle the link path needed StationByNameVisibleTo to close cannot arise.
func TestARoomRequestCarriesNoTargetStation(t *testing.T) {
	st, ctx, _, from := roomReqFixture(t)
	if _, err := st.CreateRoomRequest(ctx, "tok", from, "", "because", false); err != nil {
		t.Fatal(err)
	}
	var to *string
	if err := st.R.QueryRowContext(ctx,
		`SELECT to_station FROM station_request WHERE kind='room'`).Scan(&to); err != nil {
		t.Fatal(err)
	}
	if to != nil {
		t.Errorf("to_station is %q — a room request must name no other station", *to)
	}
}

// AT MOST ONE PENDING ASK PER STATION. A session that asks twice must not put two rows in front of
// the human to decide identically — the same rule the link path applies to a pair.
func TestAskingTwiceReturnsTheSamePendingRequest(t *testing.T) {
	st, ctx, _, from := roomReqFixture(t)
	first, err := st.CreateRoomRequest(ctx, "tok", from, "", "one", false)
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.CreateRoomRequest(ctx, "tok", from, "", "one again", false)
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Errorf("a second ask filed %q against the first %q — the human now decides the same thing twice", second, first)
	}
	pending, _ := st.PendingStationRequests(ctx)
	n := 0
	for _, p := range pending {
		if p.Kind == "room" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d room requests are pending, want 1", n)
	}

	// CONTROL: a DIFFERENT station gets its own row. Without this the test would pass against an
	// implementation that collapsed every station's ask into one.
	other, err := st.CreateStation(ctx, "other-asker", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	third, err := st.CreateRoomRequest(ctx, "tok", other.StationID, "", "mine", false)
	if err != nil {
		t.Fatal(err)
	}
	if third == first {
		t.Error("two stations share one room request")
	}
}

// *** A DENIED STATION IS MUTED, AND SILENTLY. ***
//
// Telling the caller it was muted lets a persistent session probe its human's past decisions one
// request at a time — the reason the link path drops silently and answers "pending" regardless.
func TestADeniedStationIsSilentlyMuted(t *testing.T) {
	st, ctx, actorID, from := roomReqFixture(t)
	id, err := st.CreateRoomRequest(ctx, "tok", from, "", "first ask", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DenyStationRequest(ctx, id, "not now", actorID); err != nil {
		t.Fatal(err)
	}

	next, err := st.CreateRoomRequest(ctx, "tok", from, "", "asking again immediately", false)
	if err != nil {
		t.Fatalf("a muted re-ask ERRORED instead of being silently dropped: %v — a caller that can "+
			"tell the difference can probe its human's refusals", err)
	}
	if next != "" {
		t.Error("the re-ask was filed, so a denied session can put the same decision back in front " +
			"of its human immediately")
	}
	pending, _ := st.PendingStationRequests(ctx)
	for _, p := range pending {
		if p.Kind == "room" {
			t.Error("a muted re-ask reached the console queue")
		}
	}
}

// A REASON IS REQUIRED. It is the only thing the human has to decide on, and this project's own
// denial path already refuses a reasonless denial for the mirror-image reason.
func TestARoomRequestNeedsAReason(t *testing.T) {
	st, ctx, _, from := roomReqFixture(t)
	for _, blank := range []string{"", "   ", "\t\n"} {
		if _, err := st.CreateRoomRequest(ctx, "tok", from, "hint", blank, false); err == nil {
			t.Errorf("a request with reason %q was accepted", blank)
		}
	}
}

// APPROVING WITH THE WRONG FUNCTION FAILS LOUDLY, like its sibling. The console dispatches on a
// form field, and a mis-filled form must not take the wrong branch silently.
//
// The other kind used to be 'link'. Link requests are retired — links are created on first contact
// — so the surviving pair is 'station' against 'room', and that is what this crosses now. The
// property is unchanged: each approval names the kind it actually found.
func TestApproveRoomRequestRefusesOtherKinds(t *testing.T) {
	st, ctx, actorID, from := roomReqFixture(t)
	other, err := st.CreateStation(ctx, "peer", "", actorID)
	if err != nil {
		t.Fatal(err)
	}
	stationID, err := st.CreateStationRequest(ctx, "tok", from, "a-post", "I need a post")
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.ApproveRoomRequest(ctx, stationID, "ops", actorID)
	if err == nil {
		t.Fatal("a STATION request was approved as a room — the console would create a room for a request that asked for a post")
	}
	if !strings.Contains(err.Error(), "station") {
		t.Errorf("the refusal does not say what kind it actually is: %v", err)
	}

	// And the mirror: a room request must not be approvable as a station.
	roomID, err := st.CreateRoomRequest(ctx, "tok", other.StationID, "", "we need a room", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ApproveStationRequest(ctx, roomID, "some-name", actorID); err == nil {
		t.Error("a ROOM request was approved as a station")
	}
}

// A NAME COLLISION COMES BACK AS THE SENTINEL THE CONSOLE RENDERS, not a raw constraint error —
// a human approving a second ask for "ops" is the ordinary case, not an internal error.
func TestApprovingIntoAnExistingRoomNameIsRefusedCleanly(t *testing.T) {
	st, ctx, actorID, from := roomReqFixture(t)
	if _, err := st.CreateRoom(ctx, "ops", "", actorID); err != nil {
		t.Fatal(err)
	}
	id, err := st.CreateRoomRequest(ctx, "tok", from, "ops", "coordination", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ApproveRoomRequest(ctx, id, "ops", actorID); !errors.Is(err, ErrRoomNameTaken) {
		t.Fatalf("collision returned %v, want ErrRoomNameTaken", err)
	}
	// AND THE REQUEST IS STILL PENDING, so the human can approve it under another name rather than
	// losing the ask to a failed transaction.
	pending, _ := st.PendingStationRequests(ctx)
	var still bool
	for _, p := range pending {
		if p.RequestID == id {
			still = true
		}
	}
	if !still {
		t.Error("a refused approval consumed the request — the session's ask is gone and it was told not to re-ask")
	}
}

// HEARSAY TRAVELS. A peer can talk this station into asking, and the request then reaches the human
// looking like its own idea. It is the only signal they get, so it must be recorded.
func TestHearsayIsRecordedOnARoomRequest(t *testing.T) {
	st, ctx, _, from := roomReqFixture(t)
	if _, err := st.CreateRoomRequest(ctx, "tok", from, "", "a peer suggested this", true); err != nil {
		t.Fatal(err)
	}
	pending, _ := st.PendingStationRequests(ctx)
	for _, p := range pending {
		if p.Kind == "room" && !p.PromptedByPeerTraffic {
			t.Error("a room request filed mid-conversation is not badged as hearsay, so the human " +
				"cannot tell the idea may not be this station's own")
		}
	}
}
