package comm

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func newStore(t *testing.T, l Limits) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "comm.db"), l)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return st
}

func owner(token string) Owner { return Owner{TokenID: token, ActorID: 7} }

// pair registers two endpoints and joins them through one human-minted code,
// returning both endpoints and the open channel id.
func pair(t *testing.T, st *Store) (*Endpoint, *Endpoint, string) {
	t.Helper()
	ctx := context.Background()
	a := mailbox(t, st, "dev", "tok-a")
	b := mailbox(t, st, "test", "tok-b")
	code, err := st.MintPairingCode(ctx, 42, "dev<->test")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	ch1, err := st.JoinChannel(ctx, a, code)
	if err != nil {
		t.Fatalf("join a: %v", err)
	}
	if ch1.Open() {
		t.Fatal("channel opened after only ONE join — establishment must be two-sided")
	}
	ch2, err := st.JoinChannel(ctx, b, code)
	if err != nil {
		t.Fatalf("join b: %v", err)
	}
	if !ch2.Open() {
		t.Fatalf("channel not open after both joins: state=%q", ch2.State)
	}
	return a, b, ch2.ChannelID
}

func TestMigrateIsIdempotent(t *testing.T) {
	st := newStore(t, DefaultLimits())
	if err := st.Migrate(); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}

// TestEndpointAuthentication IS DELETED. There is no endpoint credential to authenticate: a
// caller proves an OAuth grant and names a station, and the mailbox follows from the station.

// *** ONE STATION, ONE MAILBOX — AND THIS TEST ASSERTED THE OPPOSITE UNTIL TODAY. ***
//
// It was TestRegisterNeverReusesAnEndpoint, and it was right under the old model: registering
// twice with the same label had to mint DISTINCT endpoints, because attaching to the existing one
// would hand a second session the first session's inbox. That accident is what the endpoint secret
// existed to prevent.
//
// The accident cannot occur now. station.session_key is UNIQUE, so one station is held by exactly
// one conversation, and the mailbox belongs to the station rather than to whoever asked for it. So
// the property inverts: asking twice for the same station's mailbox MUST return the same one, or
// nothing is intrinsic and a session would accumulate inboxes it cannot read.
func TestAStationHasExactlyOneMailbox(t *testing.T) {
	st := newStore(t, DefaultLimits())

	a := mailbox(t, st, "stn-same", "tok")
	b := mailbox(t, st, "stn-same", "tok")
	if a.EndpointID != b.EndpointID {
		t.Fatalf("a station got two mailboxes (%s and %s) — mail would land in one and be read "+
			"from the other", a.EndpointID, b.EndpointID)
	}
	// CONTROL: two stations get two mailboxes. Without this the test would pass against an
	// implementation that returned one mailbox to everybody.
	c := mailbox(t, st, "stn-other", "tok")
	if c.EndpointID == a.EndpointID {
		t.Fatal("two stations share one mailbox — they would read each other's inboxes, which is " +
			"precisely what the endpoint secret used to exist to prevent")
	}
}

func TestPairingCodeIsSingleUseByTwoEndpoints(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, DefaultLimits())
	a, b, channelID := pair(t, st)

	// Re-joining from a member is idempotent, so a retried call after a lost
	// response cannot wedge the pairing.
	code, err := st.MintPairingCode(ctx, 42, "second")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.JoinChannel(ctx, a, code); err != nil {
		t.Fatal(err)
	}
	again, err := st.JoinChannel(ctx, a, code)
	if err != nil {
		t.Fatalf("re-join by the same endpoint must be idempotent, got %v", err)
	}
	if again.Open() {
		t.Fatal("re-join by the SAME endpoint must not open the channel")
	}

	// A third endpoint cannot take a seat on the already-open channel.
	c := mailbox(t, st, "intruder", "tok-c")
	if _, _, err := st.ChannelFor(ctx, c, channelID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("non-member must not resolve the channel: got %v", err)
	}
	_ = b
}

func TestUnknownOrExpiredCodeIsIndistinguishable(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, DefaultLimits())
	a := mailbox(t, st, "dev", "tok")
	if _, err := st.JoinChannel(ctx, a, "nosuchcode"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}

	// An expired code must behave exactly like an unknown one.
	l := DefaultLimits()
	l.PairingCodeTTLSeconds = -1
	st2 := newStore(t, l)
	a2 := mailbox(t, st2, "dev", "tok")
	code, err := st2.MintPairingCode(ctx, 42, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st2.JoinChannel(ctx, a2, code); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired code: want ErrNotFound, got %v", err)
	}
}

func TestSendPollAckRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, DefaultLimits())
	a, b, channelID := pair(t, st)

	sent, err := st.Send(ctx, a, channelID, "hello", SendOpts{})
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	// The sender must not receive its own message.
	if got, err := st.Poll(ctx, a, 10); err != nil || len(got) != 0 {
		t.Fatalf("sender polled its own message: %d, %v", len(got), err)
	}

	got, err := st.Poll(ctx, b, 10)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if len(got) != 1 || got[0].Body != "hello" || got[0].MessageID != sent.MessageID {
		t.Fatalf("unexpected poll result: %+v", got)
	}
	if got[0].DeliveryCount != 1 || got[0].Redelivered() {
		t.Fatalf("first delivery should not be marked redelivered: %+v", got[0])
	}

	if _, err := st.Ack(ctx, b, sent.MessageID); err != nil {
		t.Fatalf("ack: %v", err)
	}
	after, err := st.Poll(ctx, b, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 0 {
		t.Fatalf("acked message was redelivered: %+v", after)
	}
}

// Being polled must never hide a message from the next poll — only Ack advances
// state. This is what makes a lost poll response harmless.
func TestUnackedMessageIsRedelivered(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, DefaultLimits())
	a, b, channelID := pair(t, st)

	if _, err := st.Send(ctx, a, channelID, "again", SendOpts{}); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		got, err := st.Poll(ctx, b, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 {
			t.Fatalf("poll %d: want the message back, got %d", i, len(got))
		}
		if got[0].DeliveryCount != i {
			t.Fatalf("poll %d: delivery_count=%d, want %d", i, got[0].DeliveryCount, i)
		}
		if i > 1 && !got[0].Redelivered() {
			t.Fatalf("poll %d: should be flagged as redelivered", i)
		}
	}
}

// Ack drops the BODY but keeps the metadata row. Deleting the whole record is
// mutually exclusive with request/response correlation, so this split is a
// design invariant, not an optimization.
func TestAckDropsBodyButKeepsMetadata(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, DefaultLimits())
	a, b, channelID := pair(t, st)

	sent, err := st.Send(ctx, a, channelID, "body-here", SendOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Poll(ctx, b, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Ack(ctx, b, sent.MessageID); err != nil {
		t.Fatal(err)
	}

	m, err := st.MessageByID(ctx, sent.MessageID)
	if err != nil {
		t.Fatalf("metadata row must survive ack: %v", err)
	}
	// THE BODY SURVIVES THE ACK. This inverts the behaviour this test used to
	// assert, deliberately: destroying the body at ack destroyed 97% of one live
	// deployment's message bodies (153 of 159) through the ordinary, instructed
	// path. The un-acked inbox was not a safety net, it was the archive, and ack
	// was the instruction to burn the only copy.
	if m.Body != "body-here" {
		t.Fatalf("body was destroyed on ack, got %q — retention is %ds", m.Body, DefaultLimits().BodyRetentionSeconds)
	}
	if m.State != "acked" || m.BodyBytes != len("body-here") {
		t.Fatalf("metadata not preserved: %+v", m)
	}
}

// The historical behaviour is still reachable, exactly, by setting retention to 0.
// An operator who wants comm to be a pure conduit keeps that, and the CONTROL
// matters as much as the feature: without this, "bodies survive" could be true
// because the blanking code is dead rather than because retention governs it.
func TestZeroRetentionRestoresBlankOnAck(t *testing.T) {
	ctx := context.Background()
	l := DefaultLimits()
	l.BodyRetentionSeconds = 0
	st := newStore(t, l)
	a, b, channelID := pair(t, st)

	sent, err := st.Send(ctx, a, channelID, "body-here", SendOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Poll(ctx, b, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Ack(ctx, b, sent.MessageID); err != nil {
		t.Fatal(err)
	}
	m, err := st.MessageByID(ctx, sent.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	if m.Body != "" {
		t.Fatalf("retention 0 must blank on ack, got %q", m.Body)
	}
	if m.BodyBytes != len("body-here") {
		t.Fatalf("body_bytes must survive for accounting: %+v", m)
	}
}

// Acking is idempotent: an unknown id and a repeat ack both succeed, because a
// retried ack after a lost response must not surface as an error.
func TestAckIsIdempotent(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, DefaultLimits())
	a, b, channelID := pair(t, st)

	sent, err := st.Send(ctx, a, channelID, "x", SendOpts{})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := st.Ack(ctx, b, sent.MessageID); err != nil {
			t.Fatalf("ack %d: %v", i, err)
		}
	}
	if _, err := st.Ack(ctx, b, "nosuchmessage"); err != nil {
		t.Fatalf("acking an unknown id must succeed, got %v", err)
	}
}

// A resend with the same idempotency key returns the ORIGINAL message rather
// than delivering a second copy.
func TestSendIsIdempotentPerKey(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, DefaultLimits())
	a, b, channelID := pair(t, st)

	first, err := st.Send(ctx, a, channelID, "once", SendOpts{IdempotencyKey: "k1"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.Send(ctx, a, channelID, "once", SendOpts{IdempotencyKey: "k1"})
	if err != nil {
		t.Fatalf("resend: %v", err)
	}
	if first.MessageID != second.MessageID {
		t.Fatalf("resend created a new message: %s vs %s", first.MessageID, second.MessageID)
	}
	got, err := st.Poll(ctx, b, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("idempotent resend delivered %d copies, want 1", len(got))
	}
}

// SEQUENCE NUMBERS ARE PER CONVERSATION, NOT PER DIRECTION. This replaces
// TestSequenceIsPerDirection, which asserted the opposite and was correct until the
// delivery split retired the rule.
//
// The old scheme gave each SENDER its own counter in a channel, so two interleaved
// sequences shared one stream and both reused the same low numbers. Ordering was the
// visible cost; the real one is that `ack_up_to_seq` is a RANGE — "ack up to 2" could
// not distinguish the two 2s, so a cumulative ack could settle mail from the other
// direction that nobody had read.
//
// One counter per scope makes the range mean one thing. It is also the only scheme
// that survives a third participant: "per direction" has no meaning among five
// stations.
func TestSequenceIsPerConversationNotPerSender(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, DefaultLimits())
	a, b, channelID := pair(t, st)

	a1, _ := st.Send(ctx, a, channelID, "a1", SendOpts{})
	a2, _ := st.Send(ctx, a, channelID, "a2", SendOpts{})
	b1, err := st.Send(ctx, b, channelID, "b1", SendOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if a1.Seq != 1 || a2.Seq != 2 {
		t.Fatalf("first sender's numbering: got %d,%d want 1,2", a1.Seq, a2.Seq)
	}
	if b1.Seq != 3 {
		t.Fatalf("the reply took seq %d, want 3 — a second sender must CONTINUE the "+
			"conversation's numbering, not start a parallel one. Two messages sharing a "+
			"number is what made a cumulative ack able to settle mail nobody read.", b1.Seq)
	}

	// CONTROL: the numbers are unique across the whole scope, which is the property
	// the range ack actually depends on. Asserting 1,2,3 alone would pass on a scheme
	// that numbered per sender and happened to interleave.
	seen := map[int64]bool{}
	for _, m := range []*Message{a1, a2, b1} {
		if seen[m.Seq] {
			t.Fatalf("sequence %d was issued twice in one conversation", m.Seq)
		}
		seen[m.Seq] = true
	}
}

func TestRequestResponseCorrelation(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, DefaultLimits())
	a, b, channelID := pair(t, st)

	req, err := st.Send(ctx, a, channelID, "please do X", SendOpts{RequiresResponse: true})
	if err != nil {
		t.Fatal(err)
	}
	// NOT armed at send: a deadline that starts before the recipient can know the
	// message exists is a deadline against the transport, not against the peer.
	if req.ReplyDeadlineAt != "" {
		t.Fatalf("a deadline was armed at SEND: %q", req.ReplyDeadlineAt)
	}
	delivered, err := st.Poll(ctx, b, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(delivered) != 1 || delivered[0].ReplyDeadlineAt == "" {
		t.Fatalf("delivery must arm the reply deadline: %+v", delivered)
	}

	pending, err := st.PendingReplies(ctx, a, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].MessageID != req.MessageID {
		t.Fatalf("request should be outstanding: %+v", pending)
	}

	if _, err := st.Poll(ctx, b, 10); err != nil {
		t.Fatal(err)
	}
	reply, err := st.Send(ctx, b, channelID, "done", SendOpts{ReplyToMessageID: req.MessageID})
	if err != nil {
		t.Fatalf("reply: %v", err)
	}
	if reply.ReplyToMessageID != req.MessageID {
		t.Fatalf("reply correlation lost: %+v", reply)
	}

	pending, err = st.PendingReplies(ctx, a, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("answered request still outstanding: %+v", pending)
	}
}

// A reply may only reference a message on the same channel addressed TO the
// replier — otherwise a reply could be pinned to an unrelated message.
func TestReplyToForeignMessageIsRejected(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, DefaultLimits())
	a, b, channelID := pair(t, st)

	own, err := st.Send(ctx, a, channelID, "mine", SendOpts{})
	if err != nil {
		t.Fatal(err)
	}
	// A tries to reply to its OWN message (it is not the recipient).
	if _, err := st.Send(ctx, a, channelID, "bogus", SendOpts{ReplyToMessageID: own.MessageID}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	_ = b
}

// A message owing a response keeps its body past ack, so a responder that
// crashed and recovered can re-read what it owes.
func TestRequiresResponseRetainsBodyUntilReplied(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, DefaultLimits())
	a, b, channelID := pair(t, st)

	req, err := st.Send(ctx, a, channelID, "the request text", SendOpts{RequiresResponse: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Poll(ctx, b, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Ack(ctx, b, req.MessageID); err != nil {
		t.Fatal(err)
	}

	m, err := st.MessageByID(ctx, req.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	if m.Body != "the request text" {
		t.Fatalf("unanswered request lost its body on ack: %q", m.Body)
	}

	if _, err := st.Send(ctx, b, channelID, "answer", SendOpts{ReplyToMessageID: req.MessageID}); err != nil {
		t.Fatal(err)
	}
	// Once answered, the sweeper is free to drop it; the reply link is what the
	// correlation needs, not the body.
	if _, _, err := st.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestBackpressureCapsUnackedDepth(t *testing.T) {
	ctx := context.Background()
	l := DefaultLimits()
	l.MaxUnackedPerChannel = 3
	st := newStore(t, l)
	a, b, channelID := pair(t, st)

	for i := 0; i < 3; i++ {
		if _, err := st.Send(ctx, a, channelID, "m", SendOpts{}); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	if _, err := st.Send(ctx, a, channelID, "overflow", SendOpts{}); !errors.Is(err, ErrBackpressure) {
		t.Fatalf("want ErrBackpressure, got %v", err)
	}

	// Acking frees capacity again.
	got, err := st.Poll(ctx, b, 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Ack(ctx, b, got[0].MessageID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Send(ctx, a, channelID, "now-ok", SendOpts{}); err != nil {
		t.Fatalf("send after ack should succeed: %v", err)
	}
}

func TestBodyCapRejectsOversizeMessage(t *testing.T) {
	ctx := context.Background()
	l := DefaultLimits()
	l.MaxBodyBytes = 16
	st := newStore(t, l)
	a, _, channelID := pair(t, st)

	if _, err := st.Send(ctx, a, channelID, "0123456789abcdefX", SendOpts{}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("want ErrTooLarge, got %v", err)
	}
}

func TestRevokedChannelStopsTraffic(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, DefaultLimits())
	a, _, channelID := pair(t, st)

	if err := st.RevokeChannel(ctx, channelID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Send(ctx, a, channelID, "after revoke", SendOpts{}); !errors.Is(err, ErrChannelClosed) {
		t.Fatalf("want ErrChannelClosed, got %v", err)
	}
}

// Expiry must cover delivered-but-never-acked as well as queued: a message polled
// by a session that then died must not live forever.
func TestSweepExpiresDeliveredButUnackedMessages(t *testing.T) {
	ctx := context.Background()
	l := DefaultLimits()
	l.MessageTTLSeconds = -1 // the DELIVERED clock: expired the instant it lands
	st := newStore(t, l)
	a, b, channelID := pair(t, st)

	sent, err := st.Send(ctx, a, channelID, "doomed", SendOpts{})
	if err != nil {
		t.Fatal(err)
	}
	// It must be DELIVERED first — which is what this test has always been named
	// for and never actually did. Under the old send-anchored clock the message
	// was already expired at insert, so the poll below returned nothing and the
	// "delivered but unacked" path was never exercised at all.
	if got, err := st.Poll(ctx, b, 10); err != nil || len(got) != 1 {
		t.Fatalf("message was not delivered: %d %v", len(got), err)
	}

	expired, _, err := st.Sweep(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if expired != 1 {
		t.Fatalf("sweep expired %d messages, want 1", expired)
	}
	m, err := st.MessageByID(ctx, sent.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	// Delivered-then-expired DOES drop the body: the recipient had it and did
	// nothing. Only a message nobody ever saw keeps its text (see the undelivered
	// case), because there "expired" would otherwise mean permanently unknowable.
	if m.State != "expired" || m.Body != "" {
		t.Fatalf("expired message not cleaned: %+v", m)
	}
}

func TestSweepPurgesSettledMetadata(t *testing.T) {
	ctx := context.Background()
	l := DefaultLimits()
	l.MetadataTTLSeconds = -1 // retention already elapsed
	st := newStore(t, l)
	a, b, channelID := pair(t, st)

	sent, err := st.Send(ctx, a, channelID, "transient", SendOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Poll(ctx, b, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Ack(ctx, b, sent.MessageID); err != nil {
		t.Fatal(err)
	}

	_, purged, err := st.Sweep(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if purged != 1 {
		t.Fatalf("sweep purged %d rows, want 1", purged)
	}
	if _, err := st.MessageByID(ctx, sent.MessageID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("purged message still present: %v", err)
	}
}

// *** TWO SPACE-SCOPING TESTS WERE DELETED HERE, DELIBERATELY, WITH `space_id` ITSELF. ***
//
// `TestListEndpointsIsScopedBySpace` and `TestPairingCodeIsSpaceScoped`. IDENTITY.md §9.1
// required this: "Whoever removes it deletes that test in the same commit, deliberately and
// reviewably, and says why." This is the why.
//
// THEY WERE REAL CHECKS, NOT DEAD ONES — that is the whole reason the rule exists. A refuter
// deleted the three-line predicate in JoinChannel and CI went red in seconds. But the state
// they asserted was unreachable: only this instance has ever existed, `0001_init.sql` inserts it
// and nothing anywhere creates a second, so both tests had to FABRICATE a second space by
// hand to have anything to test. A control exercised solely by a fixture that manufactures
// its own precondition is a control over a hypothetical.
//
// Removing the predicate and leaving the tests would have left them failing; removing the
// tests first and the predicate later would have left a live control nothing exercises,
// which is the exact condition this project keeps paying for. Both halves left together.
//
// §9.1 named ONE test. There were FOUR — the other two were `TestStatsStillScopeToOneSpace`
// (deleted, same reasoning) and `TestStationNameUniquePerSpace` (KEPT and renamed: station
// names really are unique, and CreateStationAutoNamed's collision retry depends on
// ErrStationNameTaken; only its "another space may reuse it" clause was a space claim).
// A plan that names one instance is a sample, not an inventory.

// The hearsay guard keys on DELIVERY, not arrival: a message sitting un-polled in
// the queue has influenced nothing, so it must not mark an authored version.
func TestReceivedSinceKeysOnDelivery(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, DefaultLimits())
	a, b, channelID := pair(t, st)

	// Both endpoints belong to actor 7 (see owner()); actor 99 is a stranger.
	got, err := st.ReceivedSince(ctx, 7, 3600)
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Fatal("reported comm traffic before anything was sent")
	}

	if _, err := st.Send(ctx, a, channelID, "hearsay", SendOpts{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.ReceivedSince(ctx, 7, 3600); got {
		t.Fatal("a queued but never-polled message marked the recipient — it has influenced nothing")
	}

	if _, err := st.Poll(ctx, b, 10); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.ReceivedSince(ctx, 7, 3600); !got {
		t.Fatal("a delivered message did not mark the recipient")
	}

	// An unrelated actor is never marked.
	if got, _ := st.ReceivedSince(ctx, 99, 3600); got {
		t.Fatal("an unrelated actor was marked as having received comm traffic")
	}
}

// The mark survives acknowledgement: bodies are deleted on ack, metadata is not,
// and a session that read and acted on a message is exactly the case to flag.
func TestReceivedSinceSurvivesAck(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, DefaultLimits())
	a, b, channelID := pair(t, st)

	m, err := st.Send(ctx, a, channelID, "x", SendOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Poll(ctx, b, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Ack(ctx, b, m.MessageID); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.ReceivedSince(ctx, 7, 3600); !got {
		t.Fatal("acking cleared the provenance signal")
	}
}

// Outside the window, and for unknown or empty tokens, the answer is false.
func TestReceivedSinceWindowAndUnknownActors(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, DefaultLimits())
	a, b, channelID := pair(t, st)
	if _, err := st.Send(ctx, a, channelID, "x", SendOpts{}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Poll(ctx, b, 10); err != nil {
		t.Fatal(err)
	}

	if got, _ := st.ReceivedSince(ctx, 7, -1); got {
		t.Fatal("a non-positive window must report false")
	}
	if got, _ := st.ReceivedSince(ctx, 0, 3600); got {
		t.Fatal("a zero actor id must report false")
	}
	if got, _ := st.ReceivedSince(ctx, 12345, 3600); got {
		t.Fatal("an unknown actor must report false")
	}
}

// The console fingerprint backs the /comm page's live auto-refresh: the page
// reloads when it diverges. So it must be 0 for an empty space, STABLE across
// repeat reads (or the page would reload in a loop), MOVE on every console-visible
// change, and stay isolated per instance.
func TestConsoleFingerprint(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()

	base, err := st.ConsoleFingerprint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if base != 0 {
		t.Fatalf("empty space fingerprint = %d, want 0", base)
	}
	if again, _ := st.ConsoleFingerprint(ctx); again != base {
		t.Fatalf("fingerprint not stable across reads: %d != %d (would loop-reload)", again, base)
	}

	// Registering an endpoint is console-visible → the number must move.
	if _, err := st.MailboxFor(ctx, "solo", owner("tok-x")); err != nil {
		t.Fatal(err)
	}
	afterReg, _ := st.ConsoleFingerprint(ctx)
	if afterReg == base {
		t.Fatal("registering an endpoint did not move the fingerprint")
	}

	// A full pairing (two more endpoints + an open channel) moves it again.
	pair(t, st)
	afterPair, _ := st.ConsoleFingerprint(ctx)
	if afterPair == afterReg {
		t.Fatal("pairing did not move the fingerprint")
	}

	// The space-isolation clause that stood here went with space_id (§9.1). It asserted a
	// SECOND space's fingerprint was 0 — a fixture-manufactured state no deployment reached.
	// What the test is actually for, that console-visible changes move the number, is above
	// and untouched.
}

// TestRotatingASecretKeepsTheEndpointAndItsChannels IS DELETED WITH ROTATION. It proved the
// property that made rotation worth having — the identity and the channels survive, so no peer
// re-pairs — for a recovery that only existed because a lost secret was terminal. There is no
// secret to lose.

// TestRotatingARevokedEndpointIsRefused IS DELETED with rotation. Revocation itself survives and
// is still one-way; what is gone is the operation that could have undone it.

// bindEndpoint IS NOW A LOOKUP, NOT AN ACTION. Binding does not exist — a station comes with a
// mailbox — but the helper's shape is kept so the many fixtures that said "give me this station's
// endpoint" still read the same. The keyID argument is ignored: nothing authorises a binding any
// more, because there is no binding to authorise.
func bindEndpoint(t *testing.T, st *Store, _ *Endpoint, stationID, _ string) *Endpoint {
	t.Helper()
	return mailbox(t, st, stationID, "tok")
}

// THE PROPERTY SLICE 4 EXISTS FOR: a replacement session inherits the mail addressed
// to the session it replaced, with no new pairing code and no peer involvement.
//
// This is the fix for the outage that started the whole design — a session lost its
// endpoint secret to context compaction and recovery cost a human minting a fresh
// pairing code per channel, which stalled for a day until they were available.
func TestAReplacementReaderInheritsTheStationsUnreadMail(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	const station = "stn_prod_ops"

	// The original reader, bound to the station, paired with a peer.
	a := mailbox(t, st, "dev-v1", "tok-a")
	peer := mailbox(t, st, "peer", "tok-b")
	a = bindEndpoint(t, st, a, station, "kens_key1")
	code, err := st.MintPairingCode(ctx, 42, "dev<->peer")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.JoinChannel(ctx, a, code); err != nil {
		t.Fatal(err)
	}
	ch, err := st.JoinChannel(ctx, peer, code)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Send(ctx, peer, ch.ChannelID, "the archive host is refusing the pull", SendOpts{}); err != nil {
		t.Fatal(err)
	}

	// The original session dies WITHOUT polling. Its secret is gone; nothing can ever
	// authenticate as it again.
	b := mailbox(t, st, "dev-v2", "tok-a")
	b = bindEndpoint(t, st, b, station, "kens_key1")

	got, err := st.Poll(ctx, b, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("the replacement reader saw %d message(s), want 1 — inheriting the station's unread mail is the entire point of binding", len(got))
	}
	if got[0].Body != "the archive host is refusing the pull" {
		t.Fatalf("wrong message inherited: %q", got[0].Body)
	}
	// And it can settle it for the station: one ack, not one per reader.
	if _, err := st.Ack(ctx, b, got[0].MessageID); err != nil {
		t.Fatal(err)
	}
	again, err := st.Poll(ctx, b, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("acked message came back to the station (%d)", len(again))
	}
}

// TestTwoReadersOfOneStationDoNotBothGetTheSameMessage IS DELETED, AND SO IS THE CLAIM-ONCE LEASE
// IT COVERED. A station has exactly one mailbox, so there is never a second reader to exclude —
// the code itself already described the sole-reader case as "bookkeeping with no reader to
// exclude", and that is now every case.

// An UNBOUND endpoint must behave exactly as it did before stations existed. This is
// the compatibility promise that lets the shipped path stay valid indefinitely: a
// session that never heard of stations is unaffected, and never claims anything.
func TestUnboundEndpointsAreUnaffectedByClaiming(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	a, b, chID := pair(t, st)

	if _, err := st.Send(ctx, b, chID, "hello", SendOpts{}); err != nil {
		t.Fatal(err)
	}
	// Polling twice must return it twice: for an unbound endpoint, polling is a pure
	// read of deliverability and only Ack advances state.
	for i := 1; i <= 2; i++ {
		got, err := st.Poll(ctx, a, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 {
			t.Fatalf("poll %d returned %d messages, want 1 — an unbound endpoint must not have gained claim semantics", i, len(got))
		}
	}
}

// TestRevokingAStationKeySeversTheEndpointsItBound IS DELETED. Nothing binds an endpoint to a
// station any more — a station comes with a mailbox — so there is no binding to sever and no
// station key doing the binding. Revoking a station key stops the station surface; comm follows
// the station, and an archived station is refused at station.Resolve.

// The review found that a replacement reader could POLL inherited mail but not act
// on it: ChannelFor resolved membership by endpoint rowid alone, so the reader was
// not a member of the channel its predecessor joined. It could neither reply nor ack
// cumulatively — it would loop on mail it had already handled while the sender
// waited for an answer that could not be sent. Inheriting mail you cannot answer is
// not a feature.
func TestAReplacementReaderCanReplyAndAckCumulatively(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	const station = "stn_prod_ops"

	a := mailbox(t, st, "dev-v1", "tok-a")
	peer := mailbox(t, st, "peer", "tok-b")
	a = bindEndpoint(t, st, a, station, "kens_key1")
	code, err := st.MintPairingCode(ctx, 42, "x")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.JoinChannel(ctx, a, code); err != nil {
		t.Fatal(err)
	}
	ch, err := st.JoinChannel(ctx, peer, code)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"first", "second"} {
		if _, err := st.Send(ctx, peer, ch.ChannelID, body, SendOpts{}); err != nil {
			t.Fatal(err)
		}
	}

	// A replaces itself. B is NOT a member of the channel — that is the point.
	b := mailbox(t, st, "dev-v2", "tok-a")
	b = bindEndpoint(t, st, b, station, "kens_key1")

	got, err := st.Poll(ctx, b, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("replacement polled %d messages, want 2", len(got))
	}
	// It must be able to ANSWER what it inherited.
	if _, err := st.Send(ctx, b, ch.ChannelID, "picking this up from my predecessor", SendOpts{}); err != nil {
		t.Fatalf("the replacement cannot reply on the inherited channel: %v — polling mail it cannot answer is a half-feature", err)
	}
	// And settle it cumulatively, the form the tool description advertises.
	if _, err := st.AckUpTo(ctx, b, ch.ChannelID, got[len(got)-1].Seq); err != nil {
		t.Fatalf("cumulative ack failed for the replacement: %v", err)
	}
	again, err := st.Poll(ctx, b, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("%d message(s) redelivered after a cumulative ack — this is the infinite-redelivery loop the fix exists to prevent", len(again))
	}
}

// TestRevokingAnEndpointReleasesItsClaims IS DELETED with the claim lease — see above.

// The payoff of a link: two stations open a channel with NO pairing code, because a
// human approved the relationship once instead of approving each conversation.
//
// The gate is not removed, only moved: the channel exists because a person said these
// two posts may talk. What is removed is the per-conversation step.
func TestLinkedStationsOpenAChannelWithoutAPairingCode(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()

	a := mailbox(t, st, "dev", "tok-a")
	b := mailbox(t, st, "prod", "tok-b")
	a = bindEndpoint(t, st, a, "stn_dev", "kens_a")
	b = bindEndpoint(t, st, b, "stn_prod", "kens_b")

	ch, err := st.OpenLinkedChannel(ctx, a, b, 42, "dev <-> prod")
	if err != nil {
		t.Fatal(err)
	}
	if !ch.Open() {
		t.Fatalf("a linked channel opened in state %q — there is no rendezvous to wait for, both stations are already approved", ch.State)
	}
	// It must carry real traffic, not merely exist.
	if _, err := st.Send(ctx, a, ch.ChannelID, "opened from a link", SendOpts{}); err != nil {
		t.Fatal(err)
	}
	got, err := st.Poll(ctx, b, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Body != "opened from a link" {
		t.Fatalf("the peer received %d message(s): %+v", len(got), got)
	}

	// Idempotent: a retry after a lost response must not fragment one conversation
	// into two channels.
	again, err := st.OpenLinkedChannel(ctx, a, b, 42, "dev <-> prod")
	if err != nil {
		t.Fatal(err)
	}
	if again.ChannelID != ch.ChannelID {
		t.Fatalf("a second open produced a NEW channel (%s vs %s) — a retry would split the conversation", again.ChannelID, ch.ChannelID)
	}

	// And a REPLACEMENT session on one side rejoins the SAME conversation, because
	// the match is on stations rather than endpoints.
	a2 := mailbox(t, st, "dev-v2", "tok-a")
	a2 = bindEndpoint(t, st, a2, "stn_dev", "kens_a")
	rejoined, err := st.OpenLinkedChannel(ctx, a2, b, 42, "dev <-> prod")
	if err != nil {
		t.Fatal(err)
	}
	if rejoined.ChannelID != ch.ChannelID {
		t.Fatalf("a replacement session started a PARALLEL channel (%s vs %s) instead of finding its predecessor's", rejoined.ChannelID, ch.ChannelID)
	}
}

// TestOpenLinkedChannelRefusesUnboundEndpoints IS DELETED. There is no unbound mailbox to refuse.

// S4 says the per-channel sequence must key on the sending STATION, "or outbound
// numbering restarts every reconnect". It restarted: keyed on the endpoint rowid, a
// replacement session got a fresh counter beginning at 1 while its predecessor had
// already reached 2.
//
// The damage is not cosmetic ordering. ack_up_to_seq is a RANGE, so acking up to 2
// after a takeover settles the replacement's messages AND the predecessor's —
// including ones nobody ever read. Silent mail loss from an ordinary documented call.
func TestSequenceDoesNotRestartWhenASessionIsReplaced(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()

	a := mailbox(t, st, "dev-v1", "tok-a")
	b := mailbox(t, st, "peer", "tok-b")
	a = bindEndpoint(t, st, a, "stn_dev", "k")
	b = bindEndpoint(t, st, b, "stn_peer", "k")
	ch, err := st.OpenLinkedChannel(ctx, a, b, 1, "x")
	if err != nil {
		t.Fatal(err)
	}

	var last int64
	for _, body := range []string{"first", "second"} {
		m, err := st.Send(ctx, a, ch.ChannelID, body, SendOpts{})
		if err != nil {
			t.Fatal(err)
		}
		last = m.Seq
	}

	// The session dies; a replacement binds to the SAME station and sends.
	a2 := mailbox(t, st, "dev-v2", "tok-a")
	a2 = bindEndpoint(t, st, a2, "stn_dev", "k")
	m, err := st.Send(ctx, a2, ch.ChannelID, "first from the replacement", SendOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if m.Seq <= last {
		t.Fatalf("the replacement sent seq %d after its predecessor reached %d — duplicate sequence "+
			"numbers in one channel and direction, so a cumulative ack settles messages nobody read", m.Seq, last)
	}
}

// TestNumberingIsIndependentOfWhoIsSending IS DELETED. It proved that BINDING an endpoint mid-life
// did not restart a channel's sequence numbering. Nothing binds; a mailbox has its station from
// the moment it exists, so the event whose side effect was under test cannot occur.

// TestAnAlreadyRunningEndpointCanAdoptAStationAndKeepItsChannels IS DELETED. Adoption was how a
// standing endpoint acquired a station; a mailbox now has one from the moment it exists.

// TestUnbindReturnsAnEndpointToStandingAloneWithoutLosingAnything IS DELETED with comm_unbind.
// "Standing alone" is not a state a mailbox can be in.

// TestAdoptingAStationDoesNotBreakAnExistingChannel IS DELETED. Adoption and unbinding are both
// gone; a mailbox has its station from the moment it exists and cannot change it.

// RETIRED: the collided state this recovered from can no longer occur.
//
// The original, kept because the reasoning still matters: a production operator bound
// on 1.5.2, hit a sequence collision, and unbound to recover. The test simulated the
// broken state directly — a station-keyed counter behind the endpoint's own history —
// so that no later change could make unbind depend on sending, or on the sequence
// table being consistent, and strand the one person who needed the way out.
//
// The delivery split removes the state rather than the recovery. Sequence numbers key
// on the SCOPE now, so binding does not move a sender between counters and there is no
// pair of counters to fall out of step. The carry-forward that used to reconcile them
// is deleted, and this test's own failure said so before I did: "the simulated
// collided state did not actually break sending — the test is not testing what it
// claims."
//
// It is retired rather than repaired, because repairing it would mean inventing a
// failure to keep a test alive. What survives is the property, asserted in
// TestNumberingIsIndependentOfWhoIsSending: binding a sender mid-conversation does not
// disturb the numbering — measured on a live endpoint at 'e:1 -> 50' and 's:… -> 50'
// under the old mechanism, and held by construction under the new one.

// mailbox is the test fixture for "a station and its mailbox", replacing RegisterEndpoint.
//
// A station comes with a mailbox, so there is nothing to register — but this package has no handle
// on ken.db and therefore no stations of its own. That is fine and it is the S7 rule working:
// station ids are opaque text here with no foreign key, so a test names one and MailboxFor mints
// the mailbox for it. The label doubles as the station id, which keeps fixtures readable.
func mailbox(t *testing.T, st *Store, station, token string) *Endpoint {
	t.Helper()
	ep, err := st.MailboxFor(context.Background(), station, owner(token))
	if err != nil {
		t.Fatalf("mailbox for %s: %v", station, err)
	}
	return ep
}
