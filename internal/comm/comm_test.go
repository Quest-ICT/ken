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

func owner(token string) Owner { return Owner{TokenID: token, ActorID: 7, SpaceID: 1} }

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
	code, err := st.MintPairingCode(ctx, 1, 42, "dev<->test")
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
	code, err := st.MintPairingCode(ctx, 1, 42, "second")
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
	code, err := st2.MintPairingCode(ctx, 1, 42, "")
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

	if err := st.Ack(ctx, b, sent.MessageID); err != nil {
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
	if err := st.Ack(ctx, b, sent.MessageID); err != nil {
		t.Fatal(err)
	}

	m, err := st.MessageByID(ctx, sent.MessageID)
	if err != nil {
		t.Fatalf("metadata row must survive ack: %v", err)
	}
	if m.Body != "" {
		t.Fatalf("body must be dropped on ack, got %q", m.Body)
	}
	if m.State != "acked" || m.BodyBytes != len("body-here") {
		t.Fatalf("metadata not preserved: %+v", m)
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
		if err := st.Ack(ctx, b, sent.MessageID); err != nil {
			t.Fatalf("ack %d: %v", i, err)
		}
	}
	if err := st.Ack(ctx, b, "nosuchmessage"); err != nil {
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

// Sequence numbers are per (channel, sender) — i.e. per direction — which is the
// only ordering COMM promises.
func TestSequenceIsPerDirection(t *testing.T) {
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
		t.Fatalf("sender A sequence: got %d,%d want 1,2", a1.Seq, a2.Seq)
	}
	if b1.Seq != 1 {
		t.Fatalf("sender B must start its own sequence at 1, got %d", b1.Seq)
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
	if req.ReplyDeadlineAt == "" {
		t.Fatal("requires_response must arm a reply deadline")
	}

	pending, err := st.PendingReplies(ctx, a)
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

	pending, err = st.PendingReplies(ctx, a)
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
	if err := st.Ack(ctx, b, req.MessageID); err != nil {
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
	if err := st.Ack(ctx, b, got[0].MessageID); err != nil {
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
	l.MessageTTLSeconds = -1 // already expired at insert
	st := newStore(t, l)
	a, b, channelID := pair(t, st)

	sent, err := st.Send(ctx, a, channelID, "doomed", SendOpts{})
	if err != nil {
		t.Fatal(err)
	}
	// An expired message is never delivered.
	if got, err := st.Poll(ctx, b, 10); err != nil || len(got) != 0 {
		t.Fatalf("expired message was polled: %d %v", len(got), err)
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
	if err := st.Ack(ctx, b, sent.MessageID); err != nil {
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

// Ownership is scoped by space from day 1, even though only one space exists —
// scoping it later would be a behavioural break.
func TestListEndpointsIsScopedBySpace(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, DefaultLimits())

	if _, _, err := st.RegisterEndpoint(ctx, Owner{TokenID: "t1", ActorID: 1, SpaceID: 1}, "mine", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.RegisterEndpoint(ctx, Owner{TokenID: "t2", ActorID: 2, SpaceID: 2}, "theirs", ""); err != nil {
		t.Fatal(err)
	}

	got, err := st.ListEndpoints(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Label != "mine" {
		t.Fatalf("space scoping leaked: %+v", got)
	}
}

// A pairing code may not join endpoints from another space.
func TestPairingCodeIsSpaceScoped(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, DefaultLimits())

	other, _, err := st.RegisterEndpoint(ctx, Owner{TokenID: "t", ActorID: 9, SpaceID: 2}, "other-space", "")
	if err != nil {
		t.Fatal(err)
	}
	code, err := st.MintPairingCode(ctx, 1, 42, "space-1 code")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.JoinChannel(ctx, other, code); !errors.Is(err, ErrDenied) {
		t.Fatalf("cross-space join: want ErrDenied, got %v", err)
	}
}

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
	if err := st.Ack(ctx, b, m.MessageID); err != nil {
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
