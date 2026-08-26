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
	a, _, err := st.RegisterEndpoint(ctx, owner("tok-a"), "dev", "")
	if err != nil {
		t.Fatalf("register a: %v", err)
	}
	b, _, err := st.RegisterEndpoint(ctx, owner("tok-b"), "test", "")
	if err != nil {
		t.Fatalf("register b: %v", err)
	}
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

func TestEndpointAuthentication(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, DefaultLimits())

	ep, secret, err := st.RegisterEndpoint(ctx, owner("tok"), "dev", "hint")
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	got, err := st.AuthenticateEndpoint(ctx, ep.EndpointID, secret)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if got.ID != ep.ID || got.Label != "dev" || got.HostHint != "hint" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}

	if _, err := st.AuthenticateEndpoint(ctx, ep.EndpointID, "wrong"); !errors.Is(err, ErrDenied) {
		t.Fatalf("wrong secret: want ErrDenied, got %v", err)
	}
	if _, err := st.AuthenticateEndpoint(ctx, "nosuchendpoint", secret); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown endpoint: want ErrNotFound, got %v", err)
	}

	if err := st.RevokeEndpoint(ctx, ep.EndpointID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := st.AuthenticateEndpoint(ctx, ep.EndpointID, secret); !errors.Is(err, ErrDenied) {
		t.Fatalf("revoked endpoint: want ErrDenied, got %v", err)
	}
}

// A second registration under the same token and label must mint a DISTINCT
// endpoint. Attaching to the existing one would hand a second session the first
// session's inbox — the accident this guards against.
func TestRegisterNeverReusesAnEndpoint(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, DefaultLimits())

	a, _, err := st.RegisterEndpoint(ctx, owner("tok"), "same-label", "")
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := st.RegisterEndpoint(ctx, owner("tok"), "same-label", "")
	if err != nil {
		t.Fatal(err)
	}
	if a.EndpointID == b.EndpointID {
		t.Fatal("same endpoint returned for a duplicate label — inbox collision")
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
	c, _, err := st.RegisterEndpoint(ctx, owner("tok-c"), "intruder", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.ChannelFor(ctx, c, channelID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("non-member must not resolve the channel: got %v", err)
	}
	_ = b
}

func TestUnknownOrExpiredCodeIsIndistinguishable(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, DefaultLimits())
	a, _, err := st.RegisterEndpoint(ctx, owner("tok"), "dev", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.JoinChannel(ctx, a, "nosuchcode"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}

	// An expired code must behave exactly like an unknown one.
	l := DefaultLimits()
	l.PairingCodeTTLSeconds = -1
	st2 := newStore(t, l)
	a2, _, err := st2.RegisterEndpoint(ctx, owner("tok"), "dev", "")
	if err != nil {
		t.Fatal(err)
	}
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
	if _, _, err := st.RegisterEndpoint(ctx, owner("tok-x"), "solo", ""); err != nil {
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

// Rotation is the incident-response primitive COMM was missing: until it existed,
// the only remedy for a LEAKED endpoint secret was revoking the endpoint and
// re-pairing every channel from scratch.
//
// The property that makes it SAFE is structural rather than testable here — no tool
// reaches this function, only the authenticated console does, because one bearer
// token covers a machine and anything a session could trigger, every session on that
// machine could trigger. What is tested is what makes it WORTH having: the identity
// and the channel memberships survive, so no peer has to re-pair.
func TestRotatingASecretKeepsTheEndpointAndItsChannels(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()

	a, _, err := st.RegisterEndpoint(ctx, owner("tok-a"), "dev", "")
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := st.RegisterEndpoint(ctx, owner("tok-b"), "prod", "")
	if err != nil {
		t.Fatal(err)
	}
	code, err := st.MintPairingCode(ctx, 42, "dev<->prod")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.JoinChannel(ctx, a, code); err != nil {
		t.Fatal(err)
	}
	ch, err := st.JoinChannel(ctx, b, code)
	if err != nil {
		t.Fatal(err)
	}

	// Re-register to learn a secret we can prove stops working: RegisterEndpoint is
	// the only path that ever reveals one.
	c, cSecret, err := st.RegisterEndpoint(ctx, owner("tok-c"), "solo", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AuthenticateEndpoint(ctx, c.EndpointID, cSecret); err != nil {
		t.Fatalf("baseline authentication failed before rotating: %v", err)
	}
	newSecret, err := st.RotateEndpointSecret(ctx, c.EndpointID)
	if err != nil {
		t.Fatal(err)
	}
	if newSecret == cSecret {
		t.Fatal("rotation returned the same secret")
	}
	// Containment: the leaked value must stop working the moment it is rotated.
	if _, err := st.AuthenticateEndpoint(ctx, c.EndpointID, cSecret); err == nil {
		t.Fatal("the OLD secret still authenticates after rotation — a leaked secret would remain usable, which is the whole thing this prevents")
	}
	got, err := st.AuthenticateEndpoint(ctx, c.EndpointID, newSecret)
	if err != nil {
		t.Fatalf("the new secret does not authenticate: %v", err)
	}
	if got.EndpointID != c.EndpointID {
		t.Fatalf("rotation changed the endpoint id from %s to %s", c.EndpointID, got.EndpointID)
	}

	// The point of rotating rather than revoking: membership survives.
	aNew, err := st.RotateEndpointSecret(ctx, a.EndpointID)
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := st.AuthenticateEndpoint(ctx, a.EndpointID, aNew)
	if err != nil {
		t.Fatal(err)
	}
	chans, err := st.ListChannels(ctx, rotated)
	if err != nil {
		t.Fatal(err)
	}
	if len(chans) != 1 || chans[0].ChannelID != ch.ChannelID {
		t.Fatalf("channels after rotation = %+v, want the original %s — if membership is lost, rotation is only a slower revoke",
			chans, ch.ChannelID)
	}
}

// A revoked endpoint must not be rotatable: rotating one would resurrect a
// capability an operator deliberately destroyed. Revoke is what a leak response
// escalates TO, never back from.
func TestRotatingARevokedEndpointIsRefused(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	ep, _, err := st.RegisterEndpoint(ctx, owner("tok-a"), "dev", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RevokeEndpoint(ctx, ep.EndpointID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RotateEndpointSecret(ctx, ep.EndpointID); err == nil {
		t.Fatal("rotated a REVOKED endpoint — that would undo a deliberate destruction")
	}
}

// bindEndpoint attaches an endpoint to a station id directly, standing in for the
// voucher round-trip (which lives in the knowledge-base store and is covered there).
func bindEndpoint(t *testing.T, st *Store, ep *Endpoint, stationID, keyID string) *Endpoint {
	t.Helper()
	if err := st.BindEndpointToStation(context.Background(), ep.EndpointID, stationID, keyID); err != nil {
		t.Fatalf("bind: %v", err)
	}
	out, err := st.endpointByRowID(context.Background(), ep.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	return out
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
	a, _, err := st.RegisterEndpoint(ctx, owner("tok-a"), "dev-v1", "")
	if err != nil {
		t.Fatal(err)
	}
	peer, _, err := st.RegisterEndpoint(ctx, owner("tok-b"), "peer", "")
	if err != nil {
		t.Fatal(err)
	}
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
	b, _, err := st.RegisterEndpoint(ctx, owner("tok-a"), "dev-v2", "")
	if err != nil {
		t.Fatal(err)
	}
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

// Claim-once: two readers of ONE station must not both act on the same message.
// That is the shared-inbox accident the per-endpoint secret was invented to prevent,
// and letting a station have several readers would re-create it without this.
func TestTwoReadersOfOneStationDoNotBothGetTheSameMessage(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	const station = "stn_shared"

	a, _, err := st.RegisterEndpoint(ctx, owner("tok-a"), "reader-1", "")
	if err != nil {
		t.Fatal(err)
	}
	peer, _, err := st.RegisterEndpoint(ctx, owner("tok-b"), "peer", "")
	if err != nil {
		t.Fatal(err)
	}
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
	if _, err := st.Send(ctx, peer, ch.ChannelID, "only one of you should do this", SendOpts{}); err != nil {
		t.Fatal(err)
	}

	// A second session joins the same station.
	b, _, err := st.RegisterEndpoint(ctx, owner("tok-a"), "reader-2", "")
	if err != nil {
		t.Fatal(err)
	}
	b = bindEndpoint(t, st, b, station, "kens_key1")

	first, err := st.Poll(ctx, a, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 {
		t.Fatalf("first reader got %d, want 1", len(first))
	}
	second, err := st.Poll(ctx, b, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 0 {
		t.Fatalf("the SECOND reader also received the claimed message (%d) — both sessions would act on it, which is exactly the shared-inbox failure this design refuses", len(second))
	}
}

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

// Revoking a station key severs every endpoint it bound (S6) and releases their
// claims. A revocation that leaves the leaked capability running until an idle sweep
// notices is theatre — and traffic keeps an endpoint alive indefinitely.
func TestRevokingAStationKeySeversTheEndpointsItBound(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()

	a, aSecret, err := st.RegisterEndpoint(ctx, owner("tok-a"), "laptop", "")
	if err != nil {
		t.Fatal(err)
	}
	other, otherSecret, err := st.RegisterEndpoint(ctx, owner("tok-a"), "vps", "")
	if err != nil {
		t.Fatal(err)
	}
	bindEndpoint(t, st, a, "stn_x", "kens_leaked")
	bindEndpoint(t, st, other, "stn_x", "kens_safe")

	n, err := st.CountEndpointsBoundBy(ctx, "kens_leaked")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("count before severing = %d, want 1 — the console states this number before the click", n)
	}
	severed, err := st.SeverEndpointsBoundBy(ctx, "kens_leaked")
	if err != nil {
		t.Fatal(err)
	}
	if severed != 1 {
		t.Fatalf("severed %d, want 1", severed)
	}
	if _, err := st.AuthenticateEndpoint(ctx, a.EndpointID, aSecret); err == nil {
		t.Fatal("an endpoint bound by the REVOKED key still authenticates — revocation that leaves the capability running is theatre")
	}
	// A different key's endpoint is untouched: revocation is targeted, which is the
	// whole reason keys are minted per machine rather than copied.
	if _, err := st.AuthenticateEndpoint(ctx, other.EndpointID, otherSecret); err != nil {
		t.Fatalf("severing one key's endpoints also killed another key's: %v", err)
	}
}

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

	a, _, err := st.RegisterEndpoint(ctx, owner("tok-a"), "dev-v1", "")
	if err != nil {
		t.Fatal(err)
	}
	peer, _, err := st.RegisterEndpoint(ctx, owner("tok-b"), "peer", "")
	if err != nil {
		t.Fatal(err)
	}
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
	b, _, err := st.RegisterEndpoint(ctx, owner("tok-a"), "dev-v2", "")
	if err != nil {
		t.Fatal(err)
	}
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

// Revoking an endpoint must release its claims (S4). An operator revokes a WEDGED
// session precisely so another reader can take over; holding its mail for the rest
// of the lease defeats the reason they clicked.
func TestRevokingAnEndpointReleasesItsClaims(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	const station = "stn_x"

	a, _, err := st.RegisterEndpoint(ctx, owner("tok-a"), "wedged", "")
	if err != nil {
		t.Fatal(err)
	}
	peer, _, err := st.RegisterEndpoint(ctx, owner("tok-b"), "peer", "")
	if err != nil {
		t.Fatal(err)
	}
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
	if _, err := st.Send(ctx, peer, ch.ChannelID, "work to do", SendOpts{}); err != nil {
		t.Fatal(err)
	}
	if got, err := st.Poll(ctx, a, 10); err != nil || len(got) != 1 {
		t.Fatalf("setup poll: %v, %d messages", err, len(got))
	}

	// A second reader must NOT see it while A holds the claim.
	b, _, err := st.RegisterEndpoint(ctx, owner("tok-a"), "helper", "")
	if err != nil {
		t.Fatal(err)
	}
	b = bindEndpoint(t, st, b, station, "kens_key1")
	if got, err := st.Poll(ctx, b, 10); err != nil || len(got) != 0 {
		t.Fatalf("the claim is not holding: %v, %d messages", err, len(got))
	}

	// Revoke the wedged reader — the claim must go with it.
	if err := st.RevokeEndpoint(ctx, a.EndpointID); err != nil {
		t.Fatal(err)
	}
	got, err := st.Poll(ctx, b, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("after revoking the claim holder, the other reader saw %d message(s), want 1 — the operator revoked it so someone else could take over", len(got))
	}
}

// The payoff of a link: two stations open a channel with NO pairing code, because a
// human approved the relationship once instead of approving each conversation.
//
// The gate is not removed, only moved: the channel exists because a person said these
// two posts may talk. What is removed is the per-conversation step.
func TestLinkedStationsOpenAChannelWithoutAPairingCode(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()

	a, _, err := st.RegisterEndpoint(ctx, owner("tok-a"), "dev", "")
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := st.RegisterEndpoint(ctx, owner("tok-b"), "prod", "")
	if err != nil {
		t.Fatal(err)
	}
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
	a2, _, err := st.RegisterEndpoint(ctx, owner("tok-a"), "dev-v2", "")
	if err != nil {
		t.Fatal(err)
	}
	a2 = bindEndpoint(t, st, a2, "stn_dev", "kens_a")
	rejoined, err := st.OpenLinkedChannel(ctx, a2, b, 42, "dev <-> prod")
	if err != nil {
		t.Fatal(err)
	}
	if rejoined.ChannelID != ch.ChannelID {
		t.Fatalf("a replacement session started a PARALLEL channel (%s vs %s) instead of finding its predecessor's", rejoined.ChannelID, ch.ChannelID)
	}
}

// An unbound endpoint has no station and therefore no relationships to spend. It must
// be refused rather than silently opening an unauthorized channel.
func TestOpenLinkedChannelRefusesUnboundEndpoints(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	a, _, err := st.RegisterEndpoint(ctx, owner("tok-a"), "dev", "")
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := st.RegisterEndpoint(ctx, owner("tok-b"), "prod", "")
	if err != nil {
		t.Fatal(err)
	}
	b = bindEndpoint(t, st, b, "stn_prod", "kens_b")

	if _, err := st.OpenLinkedChannel(ctx, a, b, 42, "x"); err == nil {
		t.Fatal("an UNBOUND endpoint opened a linked channel — it belongs to no station, so no human ever approved a relationship for it")
	}
}

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

	a, _, err := st.RegisterEndpoint(ctx, owner("tok-a"), "dev-v1", "")
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := st.RegisterEndpoint(ctx, owner("tok-b"), "peer", "")
	if err != nil {
		t.Fatal(err)
	}
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
	a2, _, err := st.RegisterEndpoint(ctx, owner("tok-a"), "dev-v2", "")
	if err != nil {
		t.Fatal(err)
	}
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

// NUMBERING DOES NOT DEPEND ON WHO IS SENDING, bound or not. This replaces
// TestUnboundSendersKeepTheirOwnSequence, whose second half asserted a per-direction
// counter the delivery split retires.
//
// The property that survives, and the one that mattered: a sequence is strictly
// ascending and never reissued within a conversation. The old design achieved that
// per direction and needed a counter carried between the 'e:' and 's:' namespaces
// every time an endpoint bound or unbound, or a replacement session restarted at 1
// while its predecessor had reached 20. One counter per scope removes the namespace,
// the carry-over, and the class of bug — there is nothing to migrate because there is
// nothing keyed on the sender.
func TestNumberingIsIndependentOfWhoIsSending(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()

	a, aSecret, err := st.RegisterEndpoint(ctx, owner("tok-a"), "sender", "")
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := st.RegisterEndpoint(ctx, owner("tok-b"), "peer", "")
	if err != nil {
		t.Fatal(err)
	}
	code, err := st.MintPairingCode(ctx, 42, "x")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.JoinChannel(ctx, a, code); err != nil {
		t.Fatal(err)
	}
	ch, err := st.JoinChannel(ctx, b, code)
	if err != nil {
		t.Fatal(err)
	}
	chID := ch.ChannelID

	var seqs []int64
	for i := 0; i < 3; i++ {
		m, err := st.Send(ctx, a, chID, "x", SendOpts{})
		if err != nil {
			t.Fatal(err)
		}
		seqs = append(seqs, m.Seq)
	}
	if seqs[0] != 1 || seqs[1] != 2 || seqs[2] != 3 {
		t.Fatalf("sequence = %v, want strictly ascending from 1", seqs)
	}
	// The reply CONTINUES the conversation rather than opening a parallel stream.
	m, err := st.Send(ctx, b, chID, "reply", SendOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if m.Seq != 4 {
		t.Fatalf("the reply took seq %d, want 4 — the conversation has one stream", m.Seq)
	}

	// CONTROL: binding a sender mid-conversation must not disturb the numbering. This
	// is the scenario the deleted carry-over existed to protect, asserted directly so
	// its removal cannot silently reintroduce the restart it prevented — a live
	// deployment measured 'e:1 -> 50' and 's:… -> 50' under the old mechanism, and
	// the new one has to hold the same line without one.
	if err := st.BindEndpointToStation(ctx, a.EndpointID, "stn_numbering", "kens_k"); err != nil {
		t.Fatal(err)
	}
	rebound, err := st.AuthenticateEndpoint(ctx, a.EndpointID, aSecret)
	if err != nil {
		t.Fatal(err)
	}
	after, err := st.Send(ctx, rebound, chID, "after binding", SendOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if after.Seq != 5 {
		t.Fatalf("after binding, the next message took seq %d, want 5 — binding restarted the numbering, "+
			"which is exactly what the removed carry-over used to prevent", after.Seq)
	}
}

// A session that was ALREADY RUNNING when its human set stations up must be able to
// adopt one in place. Without this, adoption means re-registering — a new endpoint,
// a new secret, and every channel abandoned — which is the cost stations exist to
// remove, charged at the moment of adopting them.
func TestAnAlreadyRunningEndpointCanAdoptAStationAndKeepItsChannels(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()

	a, aSecret, err := st.RegisterEndpoint(ctx, owner("tok-a"), "long-running", "")
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := st.RegisterEndpoint(ctx, owner("tok-b"), "peer", "")
	if err != nil {
		t.Fatal(err)
	}
	// It has been talking to a peer for a while, over an ordinary pairing code.
	code, err := st.MintPairingCode(ctx, 42, "old channel")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.JoinChannel(ctx, a, code); err != nil {
		t.Fatal(err)
	}
	ch, err := st.JoinChannel(ctx, b, code)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Send(ctx, b, ch.ChannelID, "mail that arrived before stations existed", SendOpts{}); err != nil {
		t.Fatal(err)
	}

	// The human sets stations up. The session adopts one WITHOUT re-registering.
	if err := st.BindEndpointToStation(ctx, a.EndpointID, "stn_adopted", "kens_k"); err != nil {
		t.Fatal(err)
	}

	// Its credential still works — nothing was reissued.
	bound, err := st.AuthenticateEndpoint(ctx, a.EndpointID, aSecret)
	if err != nil {
		t.Fatalf("the endpoint's own secret stopped working after binding: %v", err)
	}
	if bound.StationID != "stn_adopted" {
		t.Fatalf("station = %q after binding", bound.StationID)
	}
	// Its channel survived.
	chans, err := st.ListChannels(ctx, bound)
	if err != nil {
		t.Fatal(err)
	}
	if len(chans) != 1 || chans[0].ChannelID != ch.ChannelID {
		t.Fatalf("channels after adoption = %+v, want the original %s — adoption must not cost a re-pair", chans, ch.ChannelID)
	}
	// And the mail it had not read is still readable, now as the station's.
	got, err := st.Poll(ctx, bound, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("polled %d messages after adopting a station, want the 1 that predates it — binding must not strand mail already addressed to the endpoint", len(got))
	}

	// A SECOND session can now take over, which is the point of adopting at all.
	successor, _, err := st.RegisterEndpoint(ctx, owner("tok-a"), "successor", "")
	if err != nil {
		t.Fatal(err)
	}
	successor = bindEndpoint(t, st, successor, "stn_adopted", "kens_k")
	if _, err := st.Send(ctx, b, ch.ChannelID, "sent after the handover", SendOpts{}); err != nil {
		t.Fatal(err)
	}
	inherited, err := st.Poll(ctx, successor, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(inherited) == 0 {
		t.Fatal("the successor inherited nothing — adoption did not actually move the inbox to the station")
	}
}

// Binding was a one-way door, and an operator weighing adoption asked the right
// question before stepping through it: is it reversible? It was not. That is a bad
// property for a step whose entire purpose is to make things cheaper.
//
// Unbinding must cost nothing: the endpoint keeps its id, its secret and every
// channel, and mail addressed to IT is still readable afterwards.
func TestUnbindReturnsAnEndpointToStandingAloneWithoutLosingAnything(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	const station = "stn_x"

	a, aSecret, err := st.RegisterEndpoint(ctx, owner("tok-a"), "mine", "")
	if err != nil {
		t.Fatal(err)
	}
	peer, _, err := st.RegisterEndpoint(ctx, owner("tok-b"), "peer", "")
	if err != nil {
		t.Fatal(err)
	}
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
	if err := st.BindEndpointToStation(ctx, a.EndpointID, station, "kens_k"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Send(ctx, peer, ch.ChannelID, "addressed to me", SendOpts{}); err != nil {
		t.Fatal(err)
	}

	if err := st.UnbindEndpointFromStation(ctx, a.EndpointID); err != nil {
		t.Fatal(err)
	}
	back, err := st.AuthenticateEndpoint(ctx, a.EndpointID, aSecret)
	if err != nil {
		t.Fatalf("the endpoint's secret stopped working after unbinding: %v", err)
	}
	if back.StationID != "" {
		t.Fatalf("still bound to %q after unbinding", back.StationID)
	}
	chans, err := st.ListChannels(ctx, back)
	if err != nil {
		t.Fatal(err)
	}
	if len(chans) != 1 || chans[0].ChannelID != ch.ChannelID {
		t.Fatalf("channels after unbinding = %+v, want the original %s — unbinding must cost nothing", chans, ch.ChannelID)
	}
	// MAIL SENT WHILE BOUND BELONGS TO THE STATION, AND STAYS THERE.
	//
	// This reverses what this test asserted before the delivery split, and the reversal
	// is the point rather than a casualty. S4 says the station owns the inbox; storing
	// a recipient ENDPOINT made that true only in the poll query, which is why an
	// unbound endpoint used to keep reading mail that had been sent to a post it no
	// longer staffs. Filing the delivery against the party makes the rule true at the
	// storage layer, and then this follows.
	//
	// Nothing is lost or unreachable: the message waits for whoever staffs the station
	// next, and it is visible in the console meanwhile. The cost, stated plainly rather
	// than discovered: if nobody ever binds to that station again, nobody reads it.
	got, err := st.Poll(ctx, back, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("polled %+v after unbinding — mail addressed to the STATION followed the endpoint out of it, "+
			"which means the station does not own its inbox after all", got)
	}

	// CONTROL, and the half that must not regress: mail sent to this endpoint while it
	// is UNBOUND is its own and arrives normally. Without this the assertion above
	// would also pass on an endpoint that had simply stopped receiving anything.
	if _, err := st.Send(ctx, peer, ch.ChannelID, "sent after unbinding", SendOpts{}); err != nil {
		t.Fatal(err)
	}
	own, err := st.Poll(ctx, back, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(own) != 1 || own[0].Body != "sent after unbinding" {
		t.Fatalf("an unbound endpoint polled %+v, want the message addressed to it — unbinding cost it its own mail", own)
	}
	// And it can bind again — the door swings both ways, which is the point.
	if err := st.BindEndpointToStation(ctx, a.EndpointID, station, "kens_k"); err != nil {
		t.Fatalf("could not re-bind after unbinding: %v", err)
	}
}

// ADOPTION MUST NOT BREAK A CHANNEL THE ENDPOINT ALREADY USED. Found the hard way: I
// bound this project's own session to its new station, tried to send on a channel it
// had been talking on all day, and got an internal error.
//
// The mechanism is two correct pieces colliding. `message` has a UNIQUE index on
// (channel_id, sender_endpoint, seq). The per-channel counter keys on the sending
// STATION once bound and on the endpoint rowid otherwise — which is what stops a
// REPLACEMENT session restarting at 1. So binding MID-CONVERSATION moved the endpoint
// to a fresh counter beginning at 1 while its own messages 1, 2, 3 were already on the
// channel, and the next send violated the constraint. The endpoint could not talk
// again until it unbound.
func TestAdoptingAStationDoesNotBreakAnExistingChannel(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	a, b, chID := pair(t, st)

	// A conversation that predates the station.
	for _, body := range []string{"first", "second", "third"} {
		if _, err := st.Send(ctx, a, chID, body, SendOpts{}); err != nil {
			t.Fatal(err)
		}
	}

	// The human sets stations up; this session adopts one in place.
	a = bindEndpoint(t, st, a, "stn_adopted", "kens_k")

	m, err := st.Send(ctx, a, chID, "sent after adopting a station", SendOpts{})
	if err != nil {
		t.Fatalf("SENDING BROKE after adopting a station: %v — the counter restarted and collided "+
			"with this endpoint's own earlier messages on the channel", err)
	}
	if m.Seq <= 3 {
		t.Fatalf("post-adoption seq = %d, want > 3 — it must continue the endpoint's own numbering, "+
			"not restart under the station key", m.Seq)
	}
	// The peer still receives it, so the channel genuinely still works.
	got, err := st.Poll(ctx, b, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("peer polled %d messages, want all 4", len(got))
	}

	// And unbinding must not break it either — the counter has to come back forward.
	if err := st.UnbindEndpointFromStation(ctx, a.EndpointID); err != nil {
		t.Fatal(err)
	}
	back, err := st.endpointByRowID(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	m2, err := st.Send(ctx, back, chID, "sent after unbinding again", SendOpts{})
	if err != nil {
		t.Fatalf("SENDING BROKE after unbinding: %v", err)
	}
	if m2.Seq <= m.Seq {
		t.Fatalf("post-unbind seq = %d, not past the pre-unbind %d — the counter went backwards", m2.Seq, m.Seq)
	}
}

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
