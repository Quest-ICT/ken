package commserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Quest-ICT/ken/internal/comm"
	"github.com/Quest-ICT/ken/internal/version"
)

// comm_channels HAD NO TOOL-LEVEL TEST AT ALL, and the defect it carried was a defect of
// the RESULT SHAPE — a room contributed no row, not a wrong count. Nothing below this layer
// could have caught it: the store functions were each correct about their own scope, and
// the handler simply never asked about rooms.
//
// This is the same lesson as the error mapper that discarded a correct error one layer up.
// A test that stops at the boundary cannot see a layer that omits.
func TestCommChannelsListsRoomsWithPendingAndMemberNames(t *testing.T) {
	sess, _, ctx := dirHarness(t)
	seedRoom(t, "ops", "s:"+dirStation, "s:someone-else")

	out := callChannels(t, sess, ctx)

	if len(out.Rooms) != 1 {
		t.Fatalf("comm_channels returned %d rooms, want 1.\n"+
			"A room the caller is IN contributes no row at all — and this is the surface every "+
			"session is instructed to consult before it speaks.", len(out.Rooms))
	}
	r := out.Rooms[0]
	if r.RoomID != "ops" {
		t.Fatalf("room_id = %q, want ops", r.RoomID)
	}
	// MEMBERS ARE NAMES. A list of opaque `s:<id>` keys answers "how many" and not "who",
	// and "who is in this room" is the question a sender actually has.
	if len(r.Members) != 2 {
		t.Fatalf("the room lists %d members, want 2: %+v", len(r.Members), r.Members)
	}
	for _, m := range r.Members {
		if strings.HasPrefix(m, "s:") {
			t.Errorf("member %q is a raw party key, not a station name", m)
		}
	}
	if !strings.Contains(r.Members[0]+r.Members[1], "mine") {
		t.Errorf("the caller's own station is not named among %+v", r.Members)
	}
	// AND HOW TO SEND THERE. The most expensive failure this surface caused was a station
	// that had a room id and could not work out that it goes in to_room.
	if !strings.Contains(r.AddressWith, "to_room") || !strings.Contains(r.AddressWith, "ops") {
		t.Errorf("address_with = %q — it must name the parameter and the id", r.AddressWith)
	}
}

// `rooms` MUST BE PRESENT AND `[]`, NEVER `null` OR ABSENT.
//
// ASSERTED ON THE RAW JSON, deliberately. Decoding into a typed struct makes `null`,
// `[]` and a missing key all arrive as a nil slice — so a typed assertion here would pass
// against every one of the three, including the two that break the caller's ability to
// tell "you are in no rooms" from "this build has no rooms field". That distinction is the
// entire argument for shipping this as a result field rather than a new tool, and it rests
// on this one assertion.
func TestCommChannelsRoomsIsAlwaysPresentAndNeverNull(t *testing.T) {
	sess, _, ctx := dirHarness(t)
	// No room seeded: the caller is in none.

	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "comm_channels", Arguments: dirCreds()})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("comm_channels errored: %+v", res.Content)
	}
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	got, ok := raw["rooms"]
	if !ok {
		t.Fatal("the `rooms` key is ABSENT for a caller in no rooms.\n" +
			"A caller cannot then distinguish \"you are in none\" from \"this server predates rooms\", " +
			"which is exactly the ambiguity a structural absence creates.")
	}
	if string(got) != "[]" {
		t.Fatalf("rooms = %s, want []. `null` is what a nil slice marshals to, and it is "+
			"indistinguishable from an absent field to most clients.", got)
	}
	// The two counters are non-omitempty for the same reason: 0 is an answer.
	for _, k := range []string{"pending_total", "broadcast_pending", "ken_version"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("the %q key is absent — a caller cannot tell zero from unsupported", k)
		}
	}
}

// AN UNBOUND ENDPOINT MUST STILL BE SERVED. comm_directory refuses one outright; copying
// that here would turn this fix into a regression, because comm_channels is the only inbox
// survey an endpoint with no station has.
func TestCommChannelsServesAnUnboundEndpoint(t *testing.T) {
	sess, _, ctx := dirHarness(t)
	if err := dirComm.UnbindEndpointFromStation(ctx, dirEP); err != nil {
		t.Fatal(err)
	}

	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "comm_channels", Arguments: dirCreds()})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("comm_channels refused an unbound endpoint: %+v.\n"+
			"It is the only survey such an endpoint has; refusing it leaves that session with "+
			"comm_poll as its only way to find out, which takes delivery.", res.Content)
	}
	b, _ := json.Marshal(res.StructuredContent)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["rooms"]; !ok {
		t.Error("an unbound endpoint gets no `rooms` key at all")
	}
}

// THE TOTAL COVERS WHAT THE PER-CHANNEL COUNTS CANNOT. This is the number a session whose
// captured instructions only ever mentioned channels can still act on.
func TestCommChannelsPendingTotalCoversRoomMail(t *testing.T) {
	sess, _, ctx := dirHarness(t)
	seedRoom(t, "ops", "s:"+dirStation, "s:sender-station")
	sender := stationBoundEndpoint(t, "sender-station")
	if _, err := dirComm.SendToRoom(ctx, sender, "ops", "waiting in the room", comm.SendOpts{}); err != nil {
		t.Fatal(err)
	}

	out := callChannels(t, sess, ctx)

	if out.PendingTotal != 1 {
		t.Fatalf("pending_total = %d with one room message waiting, want 1.\n"+
			"A total computed as the sum over channels[] reports 0 here and looks perfectly reasonable.", out.PendingTotal)
	}
	// CONTROL: the per-channel view genuinely cannot see it, so the total is new
	// information rather than a second rendering of something already reported.
	sum := 0
	for _, c := range out.Channels {
		sum += c.Pending
	}
	if sum != 0 {
		t.Fatalf("the per-channel counts sum to %d for room-only mail — the fixture is not testing what it claims", sum)
	}
	if len(out.Rooms) != 1 || out.Rooms[0].Pending != 1 {
		t.Fatalf("the room row does not carry the count: %+v", out.Rooms)
	}
}

// BROADCAST MAIL HAS NOWHERE ELSE TO APPEAR. A channel has a channel row and a room has a
// room row; `b:<sender>` is synthesised per sender and shows up in no list a recipient can
// enumerate — so if this number is not carried, broadcast mail is invisible on this surface
// no matter how many other counts are right.
//
// Added because mutation testing found the gap: setting broadcast_pending to nothing at all
// passed every other test here, since a non-omitempty int is always PRESENT and 0 looked
// like a legitimate answer.
func TestCommChannelsCountsBroadcastMail(t *testing.T) {
	sess, _, ctx := dirHarness(t)
	seedRoom(t, "ops", "s:"+dirStation, "s:sender-station")
	sender := stationBoundEndpoint(t, "sender-station")
	if _, err := dirComm.Broadcast(ctx, sender, "to everyone I share a room with", comm.SendOpts{}); err != nil {
		t.Fatal(err)
	}

	out := callChannels(t, sess, ctx)

	if out.BroadcastPending != 1 {
		t.Fatalf("broadcast_pending = %d with a broadcast waiting, want 1.\n"+
			"Nothing else on this surface can report it: it is in no channel and, being scoped "+
			"b:<sender>, in no room row either.", out.BroadcastPending)
	}
	// CONTROLS: it is genuinely invisible everywhere else, so the number above is the only
	// thing standing between this mail and silence.
	if len(out.Rooms) != 1 || out.Rooms[0].Pending != 0 {
		t.Fatalf("the room row reports %+v for broadcast mail — the fixture is not isolating the broadcast scope", out.Rooms)
	}
	if out.PendingTotal != 1 {
		t.Fatalf("pending_total = %d, want 1 — the total misses broadcast", out.PendingTotal)
	}
}

// COUNTING DELIVERS NOTHING, asserted through the tool rather than the store — because the
// cheapest wrong implementation of any of this is a call to Poll, and it would be invisible
// from below.
func TestCommChannelsDeliversNothing(t *testing.T) {
	sess, _, ctx := dirHarness(t)
	seedRoom(t, "ops", "s:"+dirStation, "s:sender-station")
	sender := stationBoundEndpoint(t, "sender-station")
	if _, err := dirComm.SendToRoom(ctx, sender, "ops", "do not deliver me", comm.SendOpts{}); err != nil {
		t.Fatal(err)
	}

	callChannels(t, sess, ctx)

	var state string
	var delivered any
	if err := dirComm.R.QueryRowContext(ctx,
		`SELECT state, first_delivered_at FROM delivery WHERE party_key=?`, "s:"+dirStation).
		Scan(&state, &delivered); err != nil {
		t.Fatal(err)
	}
	if state != "queued" || delivered != nil {
		t.Fatalf("after comm_channels the delivery is state=%q first_delivered_at=%v — "+
			"the survey took delivery, which is the one thing it exists to avoid", state, delivered)
	}
}

// THE RUNNING VERSION, asserted against the real constant. Non-empty alone would survive a
// hardcoded literal that drifts at the next release.
func TestCommChannelsReportsTheRunningVersion(t *testing.T) {
	sess, _, ctx := dirHarness(t)
	if got := callChannels(t, sess, ctx).KenVersion; got != version.Version {
		t.Fatalf("ken_version = %q, want %q", got, version.Version)
	}
}

// --- helpers ---------------------------------------------------------------------

// seedRoom writes room membership through the mirror the running server actually reads.
func seedRoom(t *testing.T, roomID string, parties ...string) {
	t.Helper()
	if err := dirComm.ReplaceRoomMirror(context.Background(),
		map[string][]string{roomID: parties}, 1); err != nil {
		t.Fatal(err)
	}
}

// stationBoundEndpoint registers a second endpoint bound to another station, so a test can
// send INTO the room from somewhere other than the caller.
func stationBoundEndpoint(t *testing.T, stationID string) *comm.Endpoint {
	t.Helper()
	ctx := context.Background()
	ep, secret, err := dirComm.RegisterEndpoint(ctx,
		comm.Owner{TokenID: "tok-other", ActorID: 9, SpaceID: 1}, stationID, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := dirComm.BindEndpointToStation(ctx, ep.EndpointID, stationID, "kens_k"); err != nil {
		t.Fatal(err)
	}
	bound, err := dirComm.AuthenticateEndpoint(ctx, ep.EndpointID, secret)
	if err != nil {
		t.Fatal(err)
	}
	return bound
}

// callChannels calls the tool and decodes a successful result.
func callChannels(t *testing.T, sess *mcp.ClientSession, ctx context.Context) channelsOut {
	t.Helper()
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "comm_channels", Arguments: dirCreds()})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		msg := ""
		for _, ct := range res.Content {
			if tc, ok := ct.(*mcp.TextContent); ok {
				msg += tc.Text
			}
		}
		t.Fatalf("comm_channels errored: %s", msg)
	}
	var out channelsOut
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// THE SERVER SAYS WHO IT THINKS IS CALLING.
//
// A session ran with another endpoint's credentials and nothing anywhere told it. Every call
// succeeded, because the credentials were valid — just not its own. comm_directory already
// echoed this; the surfaces a session reads every loop did not, so the one place the mismatch
// was visible was the one place nobody looks when things are working.
func TestCommChannelsSaysWhoTheCallerIs(t *testing.T) {
	sess, _, ctx := dirHarness(t)

	if got := callChannels(t, sess, ctx).YouAre; got != "mine" {
		t.Fatalf("you_are = %q, want the caller's station name %q.\n"+
			"A session cannot detect it is using another endpoint's credentials if no result "+
			"ever names whose endpoint it is.", got, "mine")
	}
}

// AND AN UNBOUND ENDPOINT IS TOLD SO IN WORDS, not with an empty string.
//
// "" is indistinguishable from a field the server failed to populate, and the entire purpose
// of an identity echo is that a caller can check it.
func TestTheIdentityEchoIsNeverSilentlyEmpty(t *testing.T) {
	sess, _, ctx := dirHarness(t)
	if err := dirComm.UnbindEndpointFromStation(ctx, dirEP); err != nil {
		t.Fatal(err)
	}
	got := callChannels(t, sess, ctx).YouAre
	if got == "" {
		t.Fatal("you_are is empty for an unbound endpoint — a caller cannot tell that from a " +
			"server that did not fill the field in")
	}
	if !strings.Contains(got, "no station") {
		t.Errorf("you_are = %q for an unbound endpoint; it should say so plainly", got)
	}
}
