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

// The console fingerprint backs the /comm page's live auto-refresh: the page
// reloads when it diverges. So it must be 0 for an empty space, STABLE across
// repeat reads (or the page would reload in a loop), MOVE on every console-visible
// change, and stay isolated per space.
func TestConsoleFingerprint(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()

	base, err := st.ConsoleFingerprint(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if base != 0 {
		t.Fatalf("empty space fingerprint = %d, want 0", base)
	}
	if again, _ := st.ConsoleFingerprint(ctx, 1); again != base {
		t.Fatalf("fingerprint not stable across reads: %d != %d (would loop-reload)", again, base)
	}

	// Registering an endpoint is console-visible → the number must move.
	if _, _, err := st.RegisterEndpoint(ctx, owner("tok-x"), "solo", ""); err != nil {
		t.Fatal(err)
	}
	afterReg, _ := st.ConsoleFingerprint(ctx, 1)
	if afterReg == base {
		t.Fatal("registering an endpoint did not move the fingerprint")
	}

	// A full pairing (two more endpoints + an open channel) moves it again.
	pair(t, st)
	afterPair, _ := st.ConsoleFingerprint(ctx, 1)
	if afterPair == afterReg {
		t.Fatal("pairing did not move the fingerprint")
	}

	// Another space is untouched — the fingerprint is space-scoped, like the console.
	if other, _ := st.ConsoleFingerprint(ctx, 2); other != 0 {
		t.Fatalf("space 2 fingerprint = %d, want 0 (space isolation)", other)
	}
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
	code, err := st.MintPairingCode(ctx, 1, 42, "dev<->prod")
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
	code, err := st.MintPairingCode(ctx, 1, 42, "dev<->peer")
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
	if err := st.Ack(ctx, b, got[0].MessageID); err != nil {
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
	code, err := st.MintPairingCode(ctx, 1, 42, "x")
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
	code, err := st.MintPairingCode(ctx, 1, 42, "x")
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
	if err := st.AckUpTo(ctx, b, ch.ChannelID, got[len(got)-1].Seq); err != nil {
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
	code, err := st.MintPairingCode(ctx, 1, 42, "x")
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
