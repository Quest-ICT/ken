package comm

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// A session must be able to see what is waiting WITHOUT taking delivery.
//
// This is what makes "check before you send" followable. comm_poll is the only other
// way to find out and it hands you the messages: it stamps first_delivered_at, arms the
// expiry and reply-deadline clocks, and leaves you on the hook to handle what you were
// only trying to peek at. An instruction that costs a delivery every time you send gets
// skipped, and a skipped instruction is worse than none because it still looks like a
// control.
//
// The property under test is the ABSENCE of a side effect, which is the kind that
// silently stops holding — so the message state is read before and after and required
// to be identical.
func TestPendingCountDoesNotDeliverAnything(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	a, b, ch := pair(t, st)

	if _, err := st.Send(ctx, b, ch, "waiting for you", SendOpts{}); err != nil {
		t.Fatal(err)
	}

	var beforeState string
	var beforeDelivered any
	if err := st.R.QueryRow(
		`SELECT d.state, d.first_delivered_at FROM delivery d ORDER BY d.id DESC LIMIT 1`).Scan(&beforeState, &beforeDelivered); err != nil {
		t.Fatal(err)
	}
	if beforeState != "queued" {
		t.Fatalf("setup: message is %q, want queued", beforeState)
	}

	pending, err := st.PendingForEndpoint(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if pending[ch] != 1 {
		t.Fatalf("pending on %s = %d, want 1 — the count is the whole reason this exists", ch, pending[ch])
	}

	var afterState string
	var afterDelivered any
	if err := st.R.QueryRow(
		`SELECT d.state, d.first_delivered_at FROM delivery d ORDER BY d.id DESC LIMIT 1`).Scan(&afterState, &afterDelivered); err != nil {
		t.Fatal(err)
	}
	if afterState != beforeState {
		t.Errorf("counting changed the message state from %q to %q — the check took delivery, which is exactly what it exists to avoid", beforeState, afterState)
	}
	if afterDelivered != nil {
		t.Error("counting stamped first_delivered_at — the expiry and reply-deadline clocks are now running on a message nobody has read")
	}
}

// Counts QUEUED only. A delivered-but-unacked message has already been shown to this
// session, so counting it would tell them to go and read something they have — the same
// mistake waiting_for_you made when it fired on mail its recipient had already replied
// to.
func TestPendingIgnoresMailAlreadyShownToYou(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	a, b, ch := pair(t, st)

	if _, err := st.Send(ctx, b, ch, "one", SendOpts{}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Poll(ctx, a, 10); err != nil { // delivered, deliberately not acked
		t.Fatal(err)
	}

	pending, err := st.PendingForEndpoint(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if pending[ch] != 0 {
		t.Fatalf("pending = %d after polling — a message this session has already been handed is not waiting to be read", pending[ch])
	}

	// CONTROL: a genuinely new message still counts, so the zero above is about the
	// delivered state and not about the count being broken.
	if _, err := st.Send(ctx, b, ch, "two", SendOpts{}); err != nil {
		t.Fatal(err)
	}
	pending, err = st.PendingForEndpoint(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if pending[ch] != 1 {
		t.Fatalf("pending = %d after a fresh send — the count is not seeing new mail at all", pending[ch])
	}
}

// Your own outgoing mail is not waiting for you. Without this the count would tell a
// sender to go and read what they just wrote.
func TestPendingDoesNotCountYourOwnSends(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	a, _, ch := pair(t, st)

	if _, err := st.Send(ctx, a, ch, "mine", SendOpts{}); err != nil {
		t.Fatal(err)
	}
	pending, err := st.PendingForEndpoint(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if pending[ch] != 0 {
		t.Fatalf("pending = %d on a channel where the only message is my own — the sender is being told to read their own mail", pending[ch])
	}
}

// A SUCCESSOR ENDPOINT MUST SEE THE COUNT ON THE CHANNEL IT INHERITED.
//
// The channel LIST was widened to station scope so a replacement session could enumerate
// what its predecessor joined — that is the takeover case stations exist for. The COUNT
// was left endpoint-scoped, so the successor got the channel listed with `pending: 0`
// beside it while mail sat queued for the station.
//
// A missing row is a silence somebody can notice; a row that says zero is an ASSERTION,
// and this one was false exactly where it mattered most.
func TestASuccessorEndpointSeesTheCountOnTheChannelItInherited(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()

	first := stationEndpoint(t, st, "tok-1", "st-ops")
	peer, _, err := st.RegisterEndpoint(ctx, owner("tok-peer"), "peer", "")
	if err != nil {
		t.Fatal(err)
	}
	code, err := st.MintPairingCode(ctx, 1, 42, "ops<->peer")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.JoinChannel(ctx, first, code); err != nil {
		t.Fatal(err)
	}
	ch, err := st.JoinChannel(ctx, peer, code)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Send(ctx, peer, ch.ChannelID, "for whoever is staffing ops", SendOpts{}); err != nil {
		t.Fatal(err)
	}

	// The predecessor is gone; a NEW endpoint takes the same station.
	successor := stationEndpoint(t, st, "tok-2", "st-ops")

	m, err := st.PendingForEndpoint(ctx, successor)
	if err != nil {
		t.Fatal(err)
	}
	// THE TWO-VALUE FORM IS THE POINT. `m[ch] == 1` alone cannot distinguish "counted
	// zero" from "no row at all" — Go returns the zero value for a missing key, which is
	// the very confusion this whole change is about.
	n, ok := m[ch.ChannelID]
	if !ok {
		t.Fatalf("the inherited channel is absent from the count map entirely: %+v", m)
	}
	if n != 1 {
		t.Fatalf("the successor sees pending=%d on the channel it inherited, want 1.\n"+
			"The list shows the channel and the count says nothing is waiting — a false assertion "+
			"in exactly the takeover case stations exist for.", n)
	}
}

// AND THE WIDENING MUST NOT LEAK. A station-scoped seat predicate that lost its seat
// clause entirely would pass the test above and report every channel in the database.
func TestThePendingCountDoesNotLeakChannelsTheStationIsNotOn(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()

	mine := stationEndpoint(t, st, "tok-mine", "st-mine")
	peer, _, err := st.RegisterEndpoint(ctx, owner("tok-peer"), "peer", "")
	if err != nil {
		t.Fatal(err)
	}
	code, err := st.MintPairingCode(ctx, 1, 42, "mine<->peer")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.JoinChannel(ctx, mine, code); err != nil {
		t.Fatal(err)
	}
	if _, err := st.JoinChannel(ctx, peer, code); err != nil {
		t.Fatal(err)
	}

	// A channel between two OTHER endpoints, with mail on it.
	x, _, err := st.RegisterEndpoint(ctx, owner("tok-x"), "x", "")
	if err != nil {
		t.Fatal(err)
	}
	y, _, err := st.RegisterEndpoint(ctx, owner("tok-y"), "y", "")
	if err != nil {
		t.Fatal(err)
	}
	code2, err := st.MintPairingCode(ctx, 1, 42, "x<->y")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.JoinChannel(ctx, x, code2); err != nil {
		t.Fatal(err)
	}
	other, err := st.JoinChannel(ctx, y, code2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Send(ctx, y, other.ChannelID, "none of your business", SendOpts{}); err != nil {
		t.Fatal(err)
	}

	m, err := st.PendingForEndpoint(ctx, mine)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m[other.ChannelID]; ok {
		t.Fatalf("a channel this station has no seat on appears in its count map: %+v.\n"+
			"The seat predicate was widened past the station.", m)
	}
}

// BROADCAST MAIL IS COUNTED BY NOTHING ELSE. A channel has a channel row and a room has a
// room row; `b:<sender>` is synthesised per sender and appears in no list a recipient can
// enumerate, so without this counter broadcast mail is invisible to every survey.
func TestBroadcastMailIsCounted(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	alpha := stationEndpoint(t, st, "tok-a", "st-alpha")
	beta := stationEndpoint(t, st, "tok-b", "st-beta")
	roomFixture(t, st, "ops", "s:st-alpha", "s:st-beta")

	if _, err := st.Broadcast(ctx, beta, "everyone", SendOpts{}); err != nil {
		t.Fatal(err)
	}
	// ROOM MAIL ALONGSIDE IT, so the scope filter has something to exclude. Without this
	// the test passes against a counter with no filter at all — it would be counting
	// everything and happening to be right, which is the shape of a test that cannot fail.
	if _, err := st.SendToRoom(ctx, beta, "ops", "not a broadcast", SendOpts{}); err != nil {
		t.Fatal(err)
	}

	n, err := st.BroadcastPendingFor(ctx, alpha)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("broadcast pending = %d, want 1 — broadcast mail is counted by nothing at all", n)
	}
	// CONTROL: the channel counter genuinely cannot see it, so the number above is new
	// information rather than a second view of something already reported.
	m, err := st.PendingForEndpoint(ctx, alpha)
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 0 {
		t.Fatalf("the channel counter reported %+v for broadcast-only mail — the fixture is not testing what it claims", m)
	}
}

// THE TOTAL SPANS EVERY SCOPE, which is the number a session can act on when its captured
// instructions only ever mentioned channels.
func TestPendingTotalSpansChannelRoomAndBroadcast(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	alpha := stationEndpoint(t, st, "tok-a", "st-alpha")
	beta := stationEndpoint(t, st, "tok-b", "st-beta")
	roomFixture(t, st, "ops", "s:st-alpha", "s:st-beta")

	// One channel message...
	code, err := st.MintPairingCode(ctx, 1, 42, "a<->b")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.JoinChannel(ctx, alpha, code); err != nil {
		t.Fatal(err)
	}
	ch, err := st.JoinChannel(ctx, beta, code)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Send(ctx, beta, ch.ChannelID, "channel", SendOpts{}); err != nil {
		t.Fatal(err)
	}
	// ...one room message, one broadcast.
	if _, err := st.SendToRoom(ctx, beta, "ops", "room", SendOpts{}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Broadcast(ctx, beta, "broadcast", SendOpts{}); err != nil {
		t.Fatal(err)
	}

	total, err := st.PendingTotalFor(ctx, alpha)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 {
		t.Fatalf("pending_total = %d, want 3 (one channel, one room, one broadcast).\n"+
			"A total computed as the sum of the per-channel counts reports 1 and looks entirely reasonable.", total)
	}
}

// QUEUED ONLY — ASSERTED AFTER A POLL, because that is the only moment the assertion can
// discriminate. Before the poll every row is 'queued', so a count over ('queued',
// 'delivered') is indistinguishable from a count over 'queued'.
func TestPendingTotalIsQueuedOnlyCheckedAfterAPoll(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	alpha := stationEndpoint(t, st, "tok-a", "st-alpha")
	beta := stationEndpoint(t, st, "tok-b", "st-beta")
	roomFixture(t, st, "ops", "s:st-alpha", "s:st-beta")

	if _, err := st.SendToRoom(ctx, beta, "ops", "read me", SendOpts{}); err != nil {
		t.Fatal(err)
	}
	if n, err := st.PendingTotalFor(ctx, alpha); err != nil || n != 1 {
		t.Fatalf("setup: total %d, err %v", n, err)
	}

	if _, err := st.Poll(ctx, alpha, 10); err != nil {
		t.Fatal(err)
	}
	n, err := st.PendingTotalFor(ctx, alpha)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("total = %d after polling it, want 0 — the count includes mail already handed over, "+
			"so it tells a session to go and read what it is holding", n)
	}
}

// BOTH PARTY FORMS. A station-bound endpoint can hold deliveries filed under its own
// e:<rowid> from before it bound; a counter that binds one form returns a confident,
// smaller, wrong number.
func TestPendingCountersMatchBothPartyForms(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	alpha := stationEndpoint(t, st, "tok-a", "st-alpha")
	beta := stationEndpoint(t, st, "tok-b", "st-beta")
	roomFixture(t, st, "ops", "s:st-alpha", "s:st-beta")

	if _, err := st.SendToRoom(ctx, beta, "ops", "filed under the station", SendOpts{}); err != nil {
		t.Fatal(err)
	}
	// Re-file it under the endpoint's OWN party, the shape a backwards ken.db restore
	// leaves behind.
	if _, err := st.W.ExecContext(ctx,
		`UPDATE delivery SET party_key=? WHERE party_key=?`,
		endpointPartyKey(alpha.ID), stationParty("st-alpha")); err != nil {
		t.Fatal(err)
	}

	n, err := st.PendingTotalFor(ctx, alpha)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("total = %d for a delivery filed under e:<rowid>, want 1 — "+
			"the counter binds one party form and reads zero, which is indistinguishable from an empty inbox", n)
	}
}

// COUNTING DELIVERS NOTHING — asserted for the new counters too, because the cheapest
// wrong implementation of any of them is a call to Poll.
func TestTheNewCountersDeliverNothing(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	alpha := stationEndpoint(t, st, "tok-a", "st-alpha")
	beta := stationEndpoint(t, st, "tok-b", "st-beta")
	roomFixture(t, st, "ops", "s:st-alpha", "s:st-beta")

	if _, err := st.SendToRoom(ctx, beta, "ops", "room", SendOpts{}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Broadcast(ctx, beta, "broadcast", SendOpts{}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PendingTotalFor(ctx, alpha); err != nil {
		t.Fatal(err)
	}
	if _, err := st.BroadcastPendingFor(ctx, alpha); err != nil {
		t.Fatal(err)
	}

	var queued, delivered int
	if err := st.R.QueryRowContext(ctx, `
SELECT COUNT(*) FILTER (WHERE state='queued'), COUNT(*) FILTER (WHERE first_delivered_at IS NOT NULL)
  FROM delivery WHERE party_key=?`, stationParty("st-alpha")).Scan(&queued, &delivered); err != nil {
		t.Fatal(err)
	}
	if queued != 2 || delivered != 0 {
		t.Fatalf("after counting: %d queued, %d delivered — counting took delivery, so "+
			"the pre-send check now costs exactly what it exists to avoid", queued, delivered)
	}
}

// waiting_for_you IS THE FIELD WITH THE WIDEST REACH, because it is the one a session
// already holding an old tool description is instructed to act on: "IF THE RESULT CARRIES
// waiting_for_you, mail was already waiting for you when this went out."
//
// That sentence is scope-agnostic and has been since 1.6.0. The implementation was not.
func TestWaitingForYouSeesRoomMailOnAChannelSend(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	alpha := stationEndpoint(t, st, "tok-a", "st-alpha")
	beta := stationEndpoint(t, st, "tok-b", "st-beta")
	roomFixture(t, st, "ops", "s:st-alpha", "s:st-beta")

	// A channel for alpha to send on, and room mail waiting for alpha meanwhile.
	code, err := st.MintPairingCode(ctx, 1, 42, "a<->b")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.JoinChannel(ctx, alpha, code); err != nil {
		t.Fatal(err)
	}
	ch, err := st.JoinChannel(ctx, beta, code)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SendToRoom(ctx, beta, "ops", "you have not read this", SendOpts{}); err != nil {
		t.Fatal(err)
	}

	m, err := st.Send(ctx, alpha, ch.ChannelID, "replying on the channel", SendOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if m.WaitingForYou != 1 {
		t.Fatalf("waiting_for_you = %d while a room message sits unread, want 1.\n"+
			"A scope-local count tells a sender their inbox is clear because the mail is in a "+
			"different scope — which is the warning failing in the one moment it exists for.", m.WaitingForYou)
	}
}

// ON A ROOM SEND. The result never carried the field at all, and `omitempty` deleted the
// key — so an absence read as all-clear on the path that reaches the most people.
func TestARoomSendReportsWaitingForYou(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	alpha := stationEndpoint(t, st, "tok-a", "st-alpha")
	beta := stationEndpoint(t, st, "tok-b", "st-beta")
	roomFixture(t, st, "ops", "s:st-alpha", "s:st-beta")

	if _, err := st.SendToRoom(ctx, beta, "ops", "read me first", SendOpts{}); err != nil {
		t.Fatal(err)
	}
	m, err := st.SendToRoom(ctx, alpha, "ops", "talking over you", SendOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if m.WaitingForYou != 1 {
		t.Fatalf("a room send reports waiting_for_you = %d, want 1", m.WaitingForYou)
	}
}

// ON A BROADCAST, AND THIS IS A SEPARATE TEST ON PURPOSE.
//
// A scope-local implementation passes the room test above and fails only here: a
// broadcast's scope is b:<sender> and the sender is excluded from its own audience, so no
// delivery addressed to the sender can EVER exist in that scope. Scope-local is not a
// weaker answer on this path — it is a field that is structurally always zero.
func TestABroadcastReportsWaitingForYou(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	alpha := stationEndpoint(t, st, "tok-a", "st-alpha")
	beta := stationEndpoint(t, st, "tok-b", "st-beta")
	roomFixture(t, st, "ops", "s:st-alpha", "s:st-beta")

	if _, err := st.SendToRoom(ctx, beta, "ops", "waiting", SendOpts{}); err != nil {
		t.Fatal(err)
	}
	m, err := st.Broadcast(ctx, alpha, "shouting past it", SendOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if m.WaitingForYou != 1 {
		t.Fatalf("a broadcast reports waiting_for_you = %d, want 1.\n"+
			"On this path a scope-local count can only ever be zero, so the warning was decoration.", m.WaitingForYou)
	}
}

// QUEUED, NOT DELIVERED — asserted AFTER the sender polls, which is the only moment the
// two differ. Before the poll every row is queued and the assertion proves nothing.
func TestWaitingForYouDoesNotCountMailAlreadyHandedOver(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	alpha := stationEndpoint(t, st, "tok-a", "st-alpha")
	beta := stationEndpoint(t, st, "tok-b", "st-beta")
	roomFixture(t, st, "ops", "s:st-alpha", "s:st-beta")

	if _, err := st.SendToRoom(ctx, beta, "ops", "you will read this", SendOpts{}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Poll(ctx, alpha, 10); err != nil {
		t.Fatal(err)
	}
	m, err := st.SendToRoom(ctx, alpha, "ops", "answering it", SendOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if m.WaitingForYou != 0 {
		t.Fatalf("waiting_for_you = %d after the sender polled the mail, want 0.\n"+
			"Warning a session about something it is holding is how the warning gets ignored — "+
			"it fired on ME while I replied to the very message it was counting.", m.WaitingForYou)
	}
}

// BACKPRESSURE MUST STAY SCOPE-LOCAL. Splitting the sender's count out of the aggregate
// must not widen the CAP, or one busy room throttles every unrelated conversation.
func TestBackpressureStillCountsOnlyItsOwnScope(t *testing.T) {
	l := DefaultLimits()
	l.MaxUnackedPerChannel = 2
	st := newStore(t, l)
	ctx := context.Background()
	alpha := stationEndpoint(t, st, "tok-a", "st-alpha")
	beta := stationEndpoint(t, st, "tok-b", "st-beta")
	if err := st.ReplaceRoomMirror(ctx, map[string][]string{
		"ops":  {"s:st-alpha", "s:st-beta"},
		"side": {"s:st-alpha", "s:st-beta"},
	}, 1); err != nil {
		t.Fatal(err)
	}
	_ = alpha

	// Fill ONE room to its cap.
	for i := 0; i < 2; i++ {
		if _, err := st.SendToRoom(ctx, beta, "ops", "filling", SendOpts{}); err != nil {
			t.Fatalf("room send %d: %v", i, err)
		}
	}
	if _, err := st.SendToRoom(ctx, beta, "ops", "over", SendOpts{}); !errors.Is(err, ErrBackpressure) {
		t.Fatalf("the room did not reach its cap: %v", err)
	}

	// A SECOND ROOM must still accept mail. This has to be another ROOM, not a channel:
	// the channel path has its own backpressure query, so a channel send never touches
	// the one under test and a test using it passes against a globally-counted cap.
	// Mutation testing caught exactly that — the first version of this test used a
	// channel and survived widening the room cap to every delivery in the database.
	if _, err := st.SendToRoom(ctx, beta, "side", "unrelated conversation", SendOpts{}); err != nil {
		t.Fatalf("a full room blocked an unrelated ROOM: %v.\n"+
			"The cap counts the scope's own backlog; widening it makes one noisy room a global brake.", err)
	}
}

// AN ACK THAT CANNOT FAIL IS NOT AN ACK.
//
// This call ran an UPDATE and discarded the row count, so a fabricated message id, an empty
// string, and acking a message addressed to somebody else ALL returned success — in the one
// call whose entire contract is "I have PROCESSED this", and the one the instructions most
// insist a session trust.
//
// Found the expensive way: a session ran with the WRONG endpoint's credentials, acked, got
// ok:true, and had no signal at all. Nothing was lost — ack-means-processed plus redelivery
// meant the bogus ack settled nothing and the message came back on the right endpoint — but
// the session believed it was finished.
func TestAckReportsThatItSettledNothing(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	a, b, ch := pair(t, st)

	m, err := st.Send(ctx, a, ch, "for b only", SendOpts{})
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		label string
		ep    *Endpoint
		id    string
	}{
		{"a fabricated id", b, "ThisMessageIdDoesNotExist99"},
		{"an empty id", b, ""},
		{"a real message addressed to somebody else", a, m.MessageID},
	} {
		n, err := st.Ack(ctx, c.ep, c.id)
		// STILL SUCCEEDS, deliberately. Making a bad ack fail hard would break the
		// legitimate no-op — acking something already settled or already swept — and
		// redelivery is the safety net that made the real incident recoverable.
		if err != nil {
			t.Errorf("%s returned an error (%v); the no-op must stay harmless", c.label, err)
		}
		if n != 0 {
			t.Errorf("%s reported settling %d deliveries, want 0", c.label, n)
		}
	}

	// CONTROL: the same call on the right endpoint settles exactly one, so a zero above is
	// evidence about the ack rather than about a fixture in which nothing was ackable.
	if _, err := st.Poll(ctx, b, 10); err != nil {
		t.Fatal(err)
	}
	n, err := st.Ack(ctx, b, m.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("the legitimate ack settled %d, want 1 — the count is not measuring anything", n)
	}
	// And acking it AGAIN settles nothing, which is the case that must not become an error.
	if again, err := st.Ack(ctx, b, m.MessageID); err != nil || again != 0 {
		t.Fatalf("re-acking a settled message returned (%d, %v), want (0, nil)", again, err)
	}
}

// A ROOM CAN BE ACKED CUMULATIVELY, in the parameter a session actually holds.
//
// AckUpTo gated on ChannelFor, so a room id came back with the caller-safe text written for
// comm_send — "address it with to_room instead of channel_id" — and comm_ack has no to_room.
// A session holding a room id was told to use a parameter that does not exist on the call it
// was making. Measured on this project: acking eight room messages took eight calls.
func TestARoomCanBeAckedCumulatively(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	alpha := stationEndpoint(t, st, "tok-a", "st-alpha")
	beta := stationEndpoint(t, st, "tok-b", "st-beta")
	roomFixture(t, st, "ops", "s:st-alpha", "s:st-beta")

	var last int64
	for i := 0; i < 3; i++ {
		m, err := st.SendToRoom(ctx, beta, "ops", "one of three", SendOpts{})
		if err != nil {
			t.Fatal(err)
		}
		last = m.Seq
	}
	// Cumulative ack only settles DELIVERED mail, so poll first — as a real session would.
	if _, err := st.Poll(ctx, alpha, 10); err != nil {
		t.Fatal(err)
	}

	n, err := st.AckUpTo(ctx, alpha, "ops", last)
	if err != nil {
		t.Fatalf("cumulative ack on a room failed: %v.\n"+
			"A session holding a room id has one addressing parameter and this is the call it makes.", err)
	}
	if n != 3 {
		t.Fatalf("cumulative room ack settled %d of 3", n)
	}
	// CONTROL: they really are settled, not merely reported.
	var open int
	if err := st.R.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM delivery WHERE party_key=? AND state IN ('queued','delivered')`,
		stationParty("st-alpha")).Scan(&open); err != nil {
		t.Fatal(err)
	}
	if open != 0 {
		t.Fatalf("%d deliveries still open after a cumulative ack that reported 3", open)
	}
}

// AND THE RELAXED GATE MUST NOT ADMIT A NON-MEMBER — the defect this fix could create.
//
// Accepting a room id in channel_id means the gate now consults room membership. If it
// consulted room EXISTENCE instead, a caller could learn a room exists by acking into it,
// which is precisely the oracle comm_open_channel's uniform refusal is built to close.
func TestCumulativeAckRefusesARoomYouAreNotIn(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	outsider := stationEndpoint(t, st, "tok-x", "st-outsider")
	beta := stationEndpoint(t, st, "tok-b", "st-beta")
	roomFixture(t, st, "ops", "s:st-alpha", "s:st-beta")

	if _, err := st.SendToRoom(ctx, beta, "ops", "not for you", SendOpts{}); err != nil {
		t.Fatal(err)
	}
	n, err := st.AckUpTo(ctx, outsider, "ops", 99)
	if err == nil {
		t.Fatalf("a non-member cumulatively acked room 'ops' and settled %d — "+
			"the gate checks existence rather than membership", n)
	}
	if strings.Contains(err.Error(), "is a ROOM") {
		t.Fatalf("the refusal confirms the room exists: %v", err)
	}
}
