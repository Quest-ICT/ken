package comm

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// stationEndpoint registers an endpoint and binds it to a station, which is what a room
// member IS — rooms hold posts, not connections.
func stationEndpoint(t *testing.T, st *Store, token, stationID string) *Endpoint {
	t.Helper()
	ctx := context.Background()
	ep, secret, err := st.RegisterEndpoint(ctx, owner(token), stationID, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.BindEndpointToStation(ctx, ep.EndpointID, stationID, "kens_k"); err != nil {
		t.Fatal(err)
	}
	bound, err := st.AuthenticateEndpoint(ctx, ep.EndpointID, secret)
	if err != nil {
		t.Fatal(err)
	}
	return bound
}

// roomFixture builds a room in the MIRROR directly. ken.db owns rooms and this package
// cannot reach it, which is the whole reason the mirror exists — so a comm-level test
// populates the projection, exactly as the boot-time rebuild does.
func roomFixture(t *testing.T, st *Store, roomID string, parties ...string) {
	t.Helper()
	rooms := map[string][]string{roomID: parties}
	if err := st.ReplaceRoomMirror(context.Background(), rooms, 1); err != nil {
		t.Fatal(err)
	}
}

// ONE BODY, N DELIVERIES — the shape of the whole feature.
//
// A fan-out that copied the body per recipient would multiply every size bound by the
// audience, so this asserts the storage shape and not merely that everyone got mail.
func TestARoomMessageIsStoredOnceAndDeliveredToEveryMember(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	sender := stationEndpoint(t, st, "tok-a", "st-alpha")
	roomFixture(t, st, "room1", "s:st-alpha", "s:st-beta", "s:st-gamma")

	m, err := st.SendToRoom(ctx, sender, "room1", "morning, all", SendOpts{})
	if err != nil {
		t.Fatal(err)
	}

	var messages, deliveries, bodies int
	if err := st.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM message`).Scan(&messages); err != nil {
		t.Fatal(err)
	}
	if err := st.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM delivery`).Scan(&deliveries); err != nil {
		t.Fatal(err)
	}
	if err := st.R.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM message WHERE body IS NOT NULL`).Scan(&bodies); err != nil {
		t.Fatal(err)
	}
	if messages != 1 || bodies != 1 {
		t.Fatalf("%d message rows and %d bodies for one broadcast — the body is being copied per recipient, "+
			"which multiplies every quota by the audience", messages, bodies)
	}
	if deliveries != 2 {
		t.Fatalf("%d delivery rows, want 2 — one per member EXCLUDING the sender", deliveries)
	}
	if m.Seq != 1 {
		t.Errorf("scope_seq = %d, want 1", m.Seq)
	}

	// The sender is not among its own recipients. Without this a broadcast would put
	// its author's own message back in their inbox on the next poll.
	var selfRows int
	if err := st.R.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM delivery WHERE party_key='s:st-alpha'`).Scan(&selfRows); err != nil {
		t.Fatal(err)
	}
	if selfRows != 0 {
		t.Error("the sender was delivered its own broadcast")
	}
}

// Each recipient's state is its OWN. One member reading or acking must not settle the
// message for anybody else — the property that makes N recipients meaningful rather
// than decorative.
func TestOneMemberActingDoesNotSettleTheMessageForOthers(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	sender := stationEndpoint(t, st, "tok-a", "st-alpha")
	beta := stationEndpoint(t, st, "tok-b", "st-beta")
	roomFixture(t, st, "room1", "s:st-alpha", "s:st-beta", "s:st-gamma")

	m, err := st.SendToRoom(ctx, sender, "room1", "please look", SendOpts{RequiresResponse: true})
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.Poll(ctx, beta, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Body != "please look" {
		t.Fatalf("beta polled %+v, want the room message", got)
	}
	if _, err := st.Ack(ctx, beta, m.MessageID); err != nil {
		t.Fatal(err)
	}

	var betaState, gammaState string
	if err := st.R.QueryRowContext(ctx,
		`SELECT state FROM delivery WHERE party_key='s:st-beta'`).Scan(&betaState); err != nil {
		t.Fatal(err)
	}
	if err := st.R.QueryRowContext(ctx,
		`SELECT state FROM delivery WHERE party_key='s:st-gamma'`).Scan(&gammaState); err != nil {
		t.Fatal(err)
	}
	if betaState != "acked" {
		t.Errorf("the reader's own delivery is %q, want acked", betaState)
	}
	if gammaState != "queued" {
		t.Fatalf("a second member's delivery is %q after somebody ELSE acked — one recipient "+
			"settled the message for a station that has not seen it", gammaState)
	}

	// And the body survives, because somebody is still owed it. Blanking on the first
	// ack is the 97%-of-bodies defect rebuilt from a new cause.
	var body *string
	if err := st.R.QueryRowContext(ctx, `SELECT body FROM message WHERE message_id=?`, m.MessageID).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if body == nil {
		t.Fatal("the body was destroyed when the FIRST recipient acked — every member who had not read it yet lost the text")
	}
}

// Membership IS the authorization, and the two ways to fail it must be told apart: a
// station removed from a room needs to learn it was removed, not that the room is
// missing, or it goes looking for a typo.
func TestOnlyMembersMaySendAndTheRefusalsAreDistinct(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	outsider := stationEndpoint(t, st, "tok-x", "st-outsider")
	roomFixture(t, st, "room1", "s:st-alpha", "s:st-beta")

	_, err := st.SendToRoom(ctx, outsider, "room1", "let me in", SendOpts{})
	if !errors.Is(err, ErrNotInRoom) {
		t.Fatalf("a non-member sending to a populated room got %v, want ErrNotInRoom", err)
	}
	_, err = st.SendToRoom(ctx, outsider, "no-such-room", "hello?", SendOpts{})
	if !errors.Is(err, ErrRoomEmpty) {
		t.Fatalf("sending to an unknown room got %v, want ErrRoomEmpty", err)
	}

	// CONTROL: a member CAN send, so the refusals above are about membership rather
	// than about room sends being broken.
	alpha := stationEndpoint(t, st, "tok-a", "st-alpha")
	if _, err := st.SendToRoom(ctx, alpha, "room1", "hello", SendOpts{}); err != nil {
		t.Fatalf("a member could not send to its own room: %v", err)
	}
}

// A room with one member other than the sender is fine; a room with NONE is refused
// rather than silently delivered to nobody. A send that succeeds and reaches no one is
// the outcome hardest to notice: the sender gets a message_id and nothing says the
// audience was zero.
func TestSendingToARoomWithNoOtherMembersIsRefused(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	alone := stationEndpoint(t, st, "tok-a", "st-alpha")
	roomFixture(t, st, "empty", "s:st-alpha")

	if _, err := st.SendToRoom(ctx, alone, "empty", "anyone?", SendOpts{}); !errors.Is(err, ErrRoomEmpty) {
		t.Fatalf("sending into a room with no other members got %v, want ErrRoomEmpty", err)
	}
	var n int
	if err := st.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM message`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d message rows written for a refused send — the refusal is not atomic", n)
	}
}

// The mirror is a CACHE and must be replaceable wholesale without drifting. An
// incremental sync that missed a removal would leave a station able to send to a room a
// human took it out of — the failure that fails OPEN.
func TestReplacingTheMirrorRemovesMembershipsThatAreGone(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	beta := stationEndpoint(t, st, "tok-b", "st-beta")
	roomFixture(t, st, "room1", "s:st-alpha", "s:st-beta")

	if _, err := st.SendToRoom(ctx, beta, "room1", "still here", SendOpts{}); err != nil {
		t.Fatal(err)
	}
	// The human removes beta. The generation is stamped SEPARATELY now — a rebuild half no
	// longer records it for both projections, because whichever half survived a partial
	// rebuild used to mark the whole mirror current over stale data.
	if err := st.ReplaceRoomMirror(ctx, map[string][]string{"room1": {"s:st-alpha"}}, 2); err != nil {
		t.Fatal(err)
	}
	if err := st.StampMirrorEpoch(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SendToRoom(ctx, beta, "room1", "and now?", SendOpts{}); !errors.Is(err, ErrNotInRoom) {
		t.Fatalf("a removed station could still send: %v", err)
	}
	epoch, err := st.MirrorEpoch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if epoch != 2 {
		t.Errorf("mirror epoch = %d, want 2 — a projection that cannot say which generation it holds cannot be known to be stale", epoch)
	}
}

// A BROADCAST REACHES EVERY STATION YOU SHARE A ROOM WITH, EXACTLY ONCE.
//
// The "exactly once" is the part worth testing rather than assuming: a station in three
// of your rooms must get ONE copy. Iterating rooms and appending would deliver three,
// and at-least-once delivery makes that indistinguishable from a redelivery on the
// receiving side — the sender would never learn they had said it three times.
func TestABroadcastReachesEveryoneOnceAcrossOverlappingRooms(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	alpha := stationEndpoint(t, st, "tok-a", "st-alpha")

	// beta is in all three of alpha's rooms; gamma in one; delta in none of them.
	if err := st.ReplaceRoomMirror(ctx, map[string][]string{
		"ops":     {"s:st-alpha", "s:st-beta", "s:st-gamma"},
		"deploys": {"s:st-alpha", "s:st-beta"},
		"oncall":  {"s:st-alpha", "s:st-beta"},
		"other":   {"s:st-delta", "s:st-epsilon"},
	}, 1); err != nil {
		t.Fatal(err)
	}

	m, err := st.Broadcast(ctx, alpha, "deploying in ten", SendOpts{})
	if err != nil {
		t.Fatal(err)
	}

	var total, betaRows, deltaRows int
	if err := st.R.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM delivery WHERE message_row=(SELECT id FROM message WHERE message_id=?)`,
		m.MessageID).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if err := st.R.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM delivery WHERE party_key='s:st-beta'`).Scan(&betaRows); err != nil {
		t.Fatal(err)
	}
	if err := st.R.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM delivery WHERE party_key='s:st-delta'`).Scan(&deltaRows); err != nil {
		t.Fatal(err)
	}
	if betaRows != 1 {
		t.Fatalf("a station in THREE of the sender's rooms got %d deliveries, want 1 — "+
			"the recipient cannot tell three copies from a redelivery, so the sender never learns they said it three times", betaRows)
	}
	if total != 2 {
		t.Fatalf("%d deliveries, want 2 (beta and gamma)", total)
	}
	// A station in none of the sender's rooms is NOT reachable. Broadcast adds reach,
	// never permission — the audience is exactly the set already addressable one room
	// at a time.
	if deltaRows != 0 {
		t.Fatal("a broadcast reached a station the sender shares no room with — broadcast granted permission rather than reach")
	}
}

// A station in no shared room is told so, and told something it can act on. "Join a
// room" and "add someone to yours" are different sentences, which is why this is not
// ErrRoomEmpty.
func TestBroadcastingWithNoAudienceIsRefusedDistinctly(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	lonely := stationEndpoint(t, st, "tok-a", "st-alpha")
	if err := st.ReplaceRoomMirror(ctx, map[string][]string{"solo": {"s:st-alpha"}}, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Broadcast(ctx, lonely, "anyone?", SendOpts{}); !errors.Is(err, ErrNoAudience) {
		t.Fatalf("broadcasting into an empty estate got %v, want ErrNoAudience", err)
	}
	var n int
	if err := st.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM message`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d message rows written for a refused broadcast", n)
	}
}

// A SESSION MUST BE ABLE TO DISCOVER ITS OWN ROOMS. Without this, being in a room is
// only useful when a human pastes the id into the conversation, which is a workaround
// wearing a feature's clothes.
func TestAStationCanDiscoverTheRoomsItIsInAndWhatIsWaiting(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	alpha := stationEndpoint(t, st, "tok-a", "st-alpha")
	beta := stationEndpoint(t, st, "tok-b", "st-beta")
	if err := st.ReplaceRoomMirror(ctx, map[string][]string{
		"ops":   {"s:st-alpha", "s:st-beta"},
		"other": {"s:st-gamma", "s:st-delta"},
	}, 3); err != nil {
		t.Fatal(err)
	}

	rooms, err := st.RoomsFor(ctx, "s:st-alpha")
	if err != nil {
		t.Fatal(err)
	}
	if len(rooms) != 1 || rooms[0].RoomID != "ops" {
		t.Fatalf("alpha sees %+v, want only the room it is in", rooms)
	}
	if len(rooms[0].Members) != 2 {
		t.Fatalf("room members = %v, want both", rooms[0].Members)
	}
	if rooms[0].Pending != 0 {
		t.Errorf("pending = %d on an empty room", rooms[0].Pending)
	}

	// Something arrives, and the count sees it WITHOUT delivering it — the property
	// that makes "check before you speak" free rather than a delivery.
	if _, err := st.SendToRoom(ctx, beta, "ops", "before you send", SendOpts{}); err != nil {
		t.Fatal(err)
	}
	rooms, err = st.RoomsFor(ctx, "s:st-alpha")
	if err != nil {
		t.Fatal(err)
	}
	if rooms[0].Pending != 1 {
		t.Fatalf("pending = %d after one message, want 1", rooms[0].Pending)
	}
	var state string
	if err := st.R.QueryRowContext(ctx,
		`SELECT state FROM delivery WHERE party_key='s:st-alpha'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "queued" {
		t.Fatalf("counting moved the message to %q — the directory delivered mail it was only asked to count", state)
	}

	// CONTROL: a real poll DOES deliver it, so the assertion above is about counting
	// rather than about the message being unreachable.
	got, err := st.Poll(ctx, alpha, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("polled %d messages, want 1", len(got))
	}

	// AND IT COUNTS QUEUED ONLY. A delivered-but-unacked message has already been shown
	// to this session, so counting it would tell them to go and read something they are
	// holding — the exact mistake waiting_for_you made when it fired on mail its
	// recipient had already replied to.
	//
	// Added after a mutation that widened the count to ('queued','delivered') survived:
	// the test checked pending only while the message was still queued, so both versions
	// agreed at the one moment it looked.
	rooms, err = st.RoomsFor(ctx, "s:st-alpha")
	if err != nil {
		t.Fatal(err)
	}
	if rooms[0].Pending != 0 {
		t.Fatalf("pending = %d after the message was DELIVERED and not yet acked, want 0 — "+
			"the directory is telling a session to go and read mail it is already holding", rooms[0].Pending)
	}
}

// The reach a session is SHOWN must be the reach a broadcast would actually get, or the
// directory promises something the send refuses.
func TestTheAdvertisedBroadcastReachMatchesWhatSendingWouldDo(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	alpha := stationEndpoint(t, st, "tok-a", "st-alpha")
	if err := st.ReplaceRoomMirror(ctx, map[string][]string{
		"ops":     {"s:st-alpha", "s:st-beta", "s:st-gamma"},
		"deploys": {"s:st-alpha", "s:st-beta"},
	}, 1); err != nil {
		t.Fatal(err)
	}

	advertised, err := st.BroadcastAudience(ctx, "s:st-alpha")
	if err != nil {
		t.Fatal(err)
	}
	m, err := st.Broadcast(ctx, alpha, "hello everyone", SendOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if advertised != m.Recipients {
		t.Fatalf("the directory advertised a reach of %d and the broadcast delivered %d — "+
			"a session cannot plan against a number that is not the one it gets", advertised, m.Recipients)
	}
	if advertised != 2 {
		t.Errorf("reach = %d, want 2 (beta counted once despite two shared rooms)", advertised)
	}
}

// AN EXPIRED ROOM MESSAGE MUST NOT BREAK THE SWEEP.
//
// It did, in 3.0.0 and 3.0.1. `collectForNotice` scanned `message.channel_id` into an
// int64, and a room message belongs to no channel — so the first room message to expire
// unread aborted Sweep with a scan error, and Sweep is where expiry, body retention, the
// metadata purge, file cleanup and idle-endpoint removal all live. Every one of them
// stops, and retention fails silently from that moment on.
//
// Nothing caught it because rooms shipped in one release and the sweep was written for a
// world with one recipient and always a channel. The ordinary case — a room message
// nobody reads — was the trigger.
func TestAnExpiredRoomMessageDoesNotBreakTheSweep(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	alpha := stationEndpoint(t, st, "tok-a", "st-alpha")
	if err := st.ReplaceRoomMirror(ctx, map[string][]string{
		"ops": {"s:st-alpha", "s:st-beta"},
	}, 1); err != nil {
		t.Fatal(err)
	}
	m, err := st.SendToRoom(ctx, alpha, "ops", "will expire unread", SendOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.W.Exec(
		`UPDATE message SET expires_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','-1 second') WHERE message_id=?`,
		m.MessageID); err != nil {
		t.Fatal(err)
	}

	expired, _, err := st.Sweep(ctx)
	if err != nil {
		t.Fatalf("the sweep failed on an expired room message: %v.\n"+
			"Sweep carries expiry, body retention, the metadata purge, file cleanup and idle-endpoint "+
			"removal — one unread room message stops all of them.", err)
	}
	if expired == 0 {
		t.Fatal("the sweep expired nothing, so it is not exercising the path this test exists for")
	}

	// The sender is TOLD, in the room's own scope — DERIVED now, not written. A room
	// message that dies unread is exactly the case where silence leaves the sender
	// believing it arrived, and it is also the case that used to take the sweep down:
	// the notice writer scanned channel_id and recipient_endpoint as int64, and both are
	// NULL for room mail.
	n, err := st.NoticesFor(ctx, stationParty("st-alpha"), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(n) != 1 || n[0].Scope != "r:ops" {
		t.Fatalf("notices for an expired room message = %+v, want one on r:ops — the sender is not told", n)
	}

	// AND THE SWEEP WROTE NOTHING AT ALL. This is the invariant the slice exists for,
	// and it is stronger than the "only once" it replaces: there is no insert to repeat.
	var written int
	if err := st.R.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM message WHERE kind='status'`).Scan(&written); err != nil {
		t.Fatal(err)
	}
	if written != 0 {
		t.Fatalf("the sweep wrote %d status message(s) — a pass whose job is deleting is inserting again", written)
	}

	// Repeated sweeps stay idempotent, which used to depend on a notified_at stamp the
	// writer could forget.
	if _, _, err := st.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := st.NoticesFor(ctx, stationParty("st-alpha"), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 {
		t.Fatalf("%d notices after three sweeps, want a stable 1", len(after))
	}
}

// THE SAME QUESTION, ASKED OF EVERY OTHER SHAPE THE SWEEP MEETS.
//
// One NULL column aborted the sweep; the fix was two columns in one query, and there is
// a second collect query and a third scope kind. Asserting the class rather than the
// instance, because "I fixed the one I found" is how the next one ships.
func TestTheSweepSurvivesEveryScopeKindAndBothNoticeReasons(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	alpha := stationEndpoint(t, st, "tok-a", "st-alpha")
	if err := st.ReplaceRoomMirror(ctx, map[string][]string{
		"ops": {"s:st-alpha", "s:st-beta"},
	}, 1); err != nil {
		t.Fatal(err)
	}

	// A ROOM message that asks for a reply and never gets one — the reply_overdue
	// collect query, which is a different statement from the expiry one.
	rm, err := st.SendToRoom(ctx, alpha, "ops", "answer me", SendOpts{RequiresResponse: true})
	if err != nil {
		t.Fatal(err)
	}
	// A BROADCAST, whose scope is neither a channel nor a room.
	bm, err := st.Broadcast(ctx, alpha, "everyone", SendOpts{})
	if err != nil {
		t.Fatal(err)
	}
	// Deliver them so the reply deadline arms, then push every clock into the past.
	beta := stationEndpoint(t, st, "tok-b", "st-beta")
	if _, err := st.Poll(ctx, beta, 10); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{rm.MessageID, bm.MessageID} {
		age(t, st, id, "-30 days")
	}

	if _, _, err := st.Sweep(ctx); err != nil {
		t.Fatalf("the sweep failed with a room message and a broadcast in flight: %v", err)
	}
	// Run it again: a sweep that succeeds once and fails on the state it just created
	// is the same outage one tick later.
	if _, _, err := st.Sweep(ctx); err != nil {
		t.Fatalf("the second sweep failed on state the first one produced: %v", err)
	}
}

// D2. A ROOM ID PASSED AS channel_id MUST NAME THE RIGHT PARAMETER.
//
// ken-promo, cold: passed a room id as channel_id because that is the only addressing
// parameter their captured schema has, got a bare "not found", concluded rooms were
// receive-only, and reported that to their human. They are the promotion station.
//
// The same call answers precisely once you already know the answer — passing both
// parameters returns "pass exactly one of channel_id or to_room". The good error is
// unreachable from the state a new caller is in, which is the defect.
func TestARoomIDPassedAsAChannelSaysSo(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	alpha := stationEndpoint(t, st, "tok-a", "st-alpha")
	roomFixture(t, st, "room1", "s:st-alpha", "s:st-beta")

	_, _, err := st.ChannelFor(ctx, alpha, "room1")
	if err == nil {
		t.Fatal("a room id was accepted as a channel id")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("error is not ErrNotFound: %v", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "to_room") {
		t.Fatalf("the refusal does not name the parameter that WOULD work: %q\n"+
			"That omission cost a working station twenty minutes and a wrong report to its human.", msg)
	}
	if !strings.Contains(strings.ToLower(msg), "room") {
		t.Errorf("the refusal never says the word room: %q", msg)
	}

	// AN UNKNOWN ID STILL MENTIONS ROOMS AS A CONCEPT, so a caller who mistyped a
	// channel id also learns the parameter exists — that caller is equally stuck.
	_, _, err = st.ChannelFor(ctx, alpha, "no-such-thing-at-all")
	if err == nil || !strings.Contains(err.Error(), "to_room") {
		t.Errorf("an unknown id gives no hint that rooms exist: %v", err)
	}

	// AND IT MUST NOT BECOME AN ORACLE. A room the caller is NOT in must not be
	// confirmed as existing — that is the probing comm_open_channel's uniform refusal
	// closes, and a helpful error is exactly how it would be reopened.
	beta := stationEndpoint(t, st, "tok-x", "st-outsider")
	_, _, err = st.ChannelFor(ctx, beta, "room1")
	if err == nil {
		t.Fatal("a non-member got a channel")
	}
	if strings.Contains(err.Error(), "is a ROOM") {
		t.Fatal("the refusal CONFIRMS a room exists to a station that is not in it — " +
			"a helpful message reopened the oracle the uniform refusal was built to close")
	}
}

// D3 — A RECEIVED ROOM MESSAGE MUST SAY WHERE IT CAME FROM.
//
// The highest-severity defect of the rooms debugging, confirmed independently by two
// stations against real received messages rather than fixtures. A room message arrived
// with `channel_id: ""`, an opaque sender id, and no scope, room, broadcast flag or
// audience. Both readers inferred the room from "I am only in one" — with two rooms
// neither could have, and neither could tell who had written to them.
//
// dev's diagnosis, which prod put in the changelog: slice 5 built SEND and DISCOVERY and
// left RECEIVE alone. The sender knows what they did; the receiver cannot see it.
func TestAReceivedRoomMessageCarriesItsScopeAndSender(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	alpha := stationEndpoint(t, st, "tok-a", "st-alpha")
	beta := stationEndpoint(t, st, "tok-b", "st-beta")
	roomFixture(t, st, "ops", "s:st-alpha", "s:st-beta", "s:st-gamma")

	if _, err := st.SendToRoom(ctx, alpha, "ops", "morning", SendOpts{}); err != nil {
		t.Fatal(err)
	}
	got, err := st.Poll(ctx, beta, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("polled %d messages, want 1", len(got))
	}
	m := got[0]

	if m.Scope != "r:ops" {
		t.Fatalf("scope = %q, want r:ops — the reader cannot tell WHERE this came from, "+
			"which is what forced two stations to guess the room from being in only one", m.Scope)
	}
	if m.SenderStationID != "st-alpha" {
		t.Fatalf("sender station = %q, want st-alpha — the reader has only an opaque endpoint id "+
			"and no way to name who wrote to them", m.SenderStationID)
	}
	if m.AudienceSize != 2 {
		t.Errorf("audience = %d, want 2 — a reply to a broadcast reaches the scope rather than a "+
			"person, and a reader cannot know that without the number", m.AudienceSize)
	}
	// A room message belongs to no channel, and saying so is the point: the field being
	// empty is information, not an omission.
	if m.ChannelID != "" {
		t.Errorf("channel_id = %q on a room message, want empty", m.ChannelID)
	}

	// CONTROL: a CHANNEL message still carries its channel and is not marked broadcast.
	// Without this, everything above could be satisfied by a change that broke channels.
	c1, c2, chID := pair(t, st)
	if _, err := st.Send(ctx, c1, chID, "just you", SendOpts{}); err != nil {
		t.Fatal(err)
	}
	cgot, err := st.Poll(ctx, c2, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(cgot) != 1 {
		t.Fatalf("polled %d channel messages, want 1", len(cgot))
	}
	if cgot[0].ChannelID != chID {
		t.Errorf("a channel message lost its channel_id: %q", cgot[0].ChannelID)
	}
	if cgot[0].Scope != "ch:"+chID {
		t.Errorf("channel scope = %q, want ch:%s", cgot[0].Scope, chID)
	}
	if cgot[0].AudienceSize != 1 {
		t.Errorf("a channel message reports an audience of %d, want 1 — it would be badged "+
			"as a broadcast", cgot[0].AudienceSize)
	}
}
