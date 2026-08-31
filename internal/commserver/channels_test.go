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

// TestCommChannelsServesAnUnboundEndpoint IS DELETED. There is no unbound mailbox: a station comes
// with one, so "an endpoint with no station" is not a state any more.

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
	for _, c := range out.Pairs {
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
	if _, err := dirComm.BroadcastTo(ctx, sender, []string{"s:" + dirStation}, "to every station on this Ken", comm.SendOpts{}); err != nil {
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

// ONE RESULT MUST NOT CONTRADICT ITSELF. This is the failure as a session meets it: the
// per-channel and per-room numbers and the total are assembled from four different
// queries into ONE comm_channels result, and the frozen instruction block tells sessions
// to read pending_total FIRST. A session that reads 0 there and stops is looking at a row
// that says 1 beside it.
//
// AT THE TOOL, not at the store, because the contradiction is a property of the RESULT —
// each store counter was internally consistent and it was the assembled answer that lied.
func TestCommChannelsCountsDoNotContradictTheTotal(t *testing.T) {
	sess, _, ctx := dirHarness(t)
	seedRoom(t, "ops", "s:"+dirStation, "s:sender-station")
	sender := stationBoundEndpoint(t, "sender-station")

	// A channel message and a room message, so both counters that lacked the clause are
	// in the same result.
	me, err := dirMailbox(t, ctx)
	if err != nil {
		t.Fatal(err)
	}
	linkedTo(t, dirComm, me, sender)
	if _, err := dirComm.SendToStation(ctx, sender, me.StationID, "on the pair", comm.SendOpts{}); err != nil {
		t.Fatal(err)
	}
	if _, err := dirComm.SendToRoom(ctx, sender, "ops", "in the room", comm.SendOpts{}); err != nil {
		t.Fatal(err)
	}

	// POSITIVE CONTROL: every number sees its message while it is live, so the zeros
	// asserted below cannot pass on an empty fixture.
	out := callChannels(t, sess, ctx)
	if out.PendingTotal != 2 || len(out.Rooms) != 1 || out.Rooms[0].Pending != 1 ||
		len(out.Pairs) != 1 || out.Pairs[0].Pending != 1 {
		t.Fatalf("POSITIVE CONTROL failed: total=%d rooms=%+v pairs=%+v, want 2 with 1 each",
			out.PendingTotal, out.Rooms, out.Pairs)
	}

	// Both messages expire, and NO SWEEP RUNS — the window this defect lives in. The
	// clock is moved in the data rather than waited on.
	res, err := dirComm.W.ExecContext(ctx,
		`UPDATE message SET expires_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','-1 second')`)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := res.RowsAffected(); n != 2 {
		t.Fatalf("expiring the fixture matched %d message rows, want 2", n)
	}
	var queued int
	if err := dirComm.R.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM delivery WHERE party_key=? AND state='queued'`, "s:"+dirStation).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 2 {
		t.Fatalf("%d deliveries still queued, want 2 — the sweeper must not have run, or this "+
			"test is checking the state AFTER the fix instead of the window before it", queued)
	}

	out = callChannels(t, sess, ctx)
	// THE ROWS ARE ROOMS AND PAIRS NOW. The channel row this loop was written for is gone, and the
	// property is not about channels: NO per-scope count may exceed the total sitting beside it in
	// the same result, whatever namespace the row belongs to.
	for _, c := range out.Pairs {
		if c.Pending > out.PendingTotal {
			t.Errorf("pair %s reports pending=%d beside pending_total=%d in ONE result — "+
				"a session told to read the total first is sent away from mail the row beside it claims",
				c.StationID, c.Pending, out.PendingTotal)
		}
	}
	for _, r := range out.Rooms {
		if r.Pending > out.PendingTotal {
			t.Errorf("room %s reports pending=%d beside pending_total=%d in ONE result",
				r.RoomID, r.Pending, out.PendingTotal)
		}
	}
	if len(out.Pairs) != 1 || len(out.Rooms) != 1 {
		t.Fatalf("after expiry the rows themselves vanished (pairs=%d rooms=%d) — a row that "+
			"disappears is a different claim from one that reports zero, and only the second is "+
			"what this asserts", len(out.Pairs), len(out.Rooms))
	}
	if out.PendingTotal != 0 || out.Pairs[0].Pending != 0 || out.Rooms[0].Pending != 0 {
		t.Fatalf("after expiry: total=%d pair=%d room=%d, want 0/0/0 — a poll would hand over "+
			"none of it", out.PendingTotal, out.Pairs[0].Pending, out.Rooms[0].Pending)
	}
}

// seedRoom writes room membership through the mirror the running server actually reads.
func seedRoom(t *testing.T, roomID string, parties ...string) {
	t.Helper()
	if err := dirComm.ReplaceRoomMirror(context.Background(),
		map[string][]string{roomID: parties}, 1); err != nil {
		t.Fatal(err)
	}
}

// stationBoundEndpoint returns another station's mailbox, so a test can send INTO the room from
// somewhere other than the caller. It used to register an endpoint and bind it; a station comes
// with a mailbox, so naming the station is the whole of it.
func stationBoundEndpoint(t *testing.T, stationID string) *comm.Endpoint {
	t.Helper()
	ep, err := dirComm.MailboxFor(context.Background(), stationID,
		comm.Owner{TokenID: "tok-other", ActorID: 9})
	if err != nil {
		t.Fatal(err)
	}
	return ep
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

// THE IDENTITY ECHO IS NEVER SILENTLY EMPTY.
//
// "" is indistinguishable from a field the server failed to populate, and the entire purpose of an
// identity echo is that a caller can check it. This used to prove the point by UNBINDING the
// mailbox, which is no longer a state — a station comes with one — so it proves it on the ordinary
// path instead: the echo must name the station, not be blank.
func TestTheIdentityEchoIsNeverSilentlyEmpty(t *testing.T) {
	sess, _, ctx := dirHarness(t)
	got := callChannels(t, sess, ctx).YouAre
	if got == "" {
		t.Fatal("you_are is empty — a caller cannot tell that from a server that did not fill the " +
			"field in")
	}
	if !strings.Contains(got, "mine") {
		t.Errorf("you_are = %q, want it to name the station the caller is working as", got)
	}
}

// AN ARCHIVED STATION'S SESSION IS REFUSED ON COMM — the promise docs/STATIONS.md has been
// making since stations shipped, which nothing implemented.
//
// auth() checked endpoint revocation and station-KEY revocation and never station state, so a
// session bound before the archive polled, sent, broadcast and acked forever. The doc and the
// code disagreed and the code is the one users met.
func TestAnArchivedStationCannotUseComm(t *testing.T) {
	sess, st, ctx := dirHarness(t)

	// CONTROL FIRST: these exact credentials work while the station is active, so a refusal
	// below is about archiving rather than about a harness that never authenticated.
	if res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "comm_channels", Arguments: dirCreds()}); err != nil || res.IsError {
		t.Fatalf("the control call failed before archiving: err=%v res=%+v", err, res)
	}

	if err := st.ArchiveStation(ctx, dirStation, true); err != nil {
		t.Fatal(err)
	}
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "comm_channels", Arguments: dirCreds()})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("a session bound to an ARCHIVED station still uses COMM.\n" +
			"docs/STATIONS.md promises archiving severs live endpoints; nothing did it, so a retired " +
			"post kept polling, sending and acking indefinitely.")
	}
	msg := ""
	for _, ct := range res.Content {
		if tc, ok := ct.(*mcp.TextContent); ok {
			msg += tc.Text
		}
	}
	// The refusal must carry the REMEDY. Under the freeze an error string is the only
	// channel that reaches a session already running — it cannot be sent a corrected
	// description, so what it needs to know has to be in the refusal itself.
	low := strings.ToLower(msg)
	if !strings.Contains(low, "archived") || !strings.Contains(low, "unarchive") {
		t.Errorf("the refusal does not name the state and the remedy: %q", msg)
	}

	// REVERSIBLE WITH THE SAME CREDENTIALS. This is what makes refuse-at-use the right shape
	// rather than revoking the endpoint: revocation is one-way and would turn unarchiving
	// into a re-registration — a new secret onto disk and a fresh voucher.
	if err := st.ArchiveStation(ctx, dirStation, false); err != nil {
		t.Fatal(err)
	}
	if res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "comm_channels", Arguments: dirCreds()}); err != nil || res.IsError {
		t.Fatalf("unarchiving did not restore the SAME endpoint: err=%v res=%+v", err, res)
	}
}

// TestArchiveStateIsNotReadableWithoutTheSecret IS DELETED with the secret it was about. Archive
// state is now behind station.Resolve, which requires state='active' and answers every miss with
// one wording — so there is no second answer to compare against.

// linkedTo gives two station mailboxes the LINK they need and returns the peer's station id.
//
// It replaces openChannel, which opened a channel row between them. Both answered the same
// question — "these two can write to each other, and here is the address" — and slice 7 changed
// only the answer: a link, addressed by station id.
func linkedTo(t *testing.T, st *comm.Store, a, b *comm.Endpoint) string {
	t.Helper()
	if err := st.ReplaceLinkMirror(context.Background(),
		[][2]string{{a.StationID, b.StationID}}, 1); err != nil {
		t.Fatalf("link %s <-> %s: %v", a.StationID, b.StationID, err)
	}
	return b.StationID
}
