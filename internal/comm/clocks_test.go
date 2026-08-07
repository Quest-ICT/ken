package comm

import (
	"context"
	"testing"
)

// age moves a message's clocks backwards to simulate wall time passing, because a
// test cannot wait 64 hours and a sleep would only prove the scheduler works.
func age(t *testing.T, st *Store, messageID, modifier string) {
	t.Helper()
	res, err := st.W.Exec(`
UPDATE message
   SET created_at  = strftime('%Y-%m-%dT%H:%M:%fZ', created_at,  ?),
       expires_at  = strftime('%Y-%m-%dT%H:%M:%fZ', expires_at,  ?),
       first_delivered_at = CASE WHEN first_delivered_at IS NULL THEN NULL
            ELSE strftime('%Y-%m-%dT%H:%M:%fZ', first_delivered_at, ?) END,
       reply_deadline_at  = CASE WHEN reply_deadline_at IS NULL THEN NULL
            ELSE strftime('%Y-%m-%dT%H:%M:%fZ', reply_deadline_at, ?) END
 WHERE message_id=?`, modifier, modifier, modifier, modifier, messageID)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("age(%s) matched %d rows, want 1 — the test aged nothing", messageID, n)
	}
}

// A MESSAGE MUST SURVIVE A WEEKEND.
//
// A human works roughly eight hours a day, Monday to Friday. A session therefore
// goes 16 h between pulls on a weeknight, 64 h across a weekend and weeks across
// annual leave. Against the shipped 24 h TTL, anchored at SEND, that meant every
// message sent during a Friday shift was dead before Monday — 2.67x the TTL — and
// it is what killed a real 4 661-byte message sent on Sunday 2026-08-02.
//
// The fix is not a bigger number. It is that an undelivered message has not had its
// chance yet, so its clock has not started.
func TestAMessageSurvivesAWeekendUnpolled(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, DefaultLimits())
	a, b, channelID := pair(t, st)

	sent, err := st.Send(ctx, a, channelID, "sent Friday afternoon", SendOpts{})
	if err != nil {
		t.Fatal(err)
	}
	age(t, st, sent.MessageID, "-64 hours") // Friday 17:00 -> Monday 09:00

	if _, _, err := st.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := st.Poll(ctx, b, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("Monday's poll found %d messages, want 1 — the weekend killed it", len(got))
	}
	if got[0].Body != "sent Friday afternoon" {
		t.Fatalf("body did not survive the weekend: %q", got[0].Body)
	}
}

// CONTROL for the test above. With the undelivered backstop set BELOW the gap, the
// same message dies — which proves the survival above comes from the delivery
// anchor and not from a sweep that has quietly stopped expiring anything.
func TestTheUndeliveredBackstopStillExpiresAbandonedMail(t *testing.T) {
	ctx := context.Background()
	l := DefaultLimits()
	// Both clocks must be set: a backstop SHORTER than the post-delivery TTL is
	// nonsense (mail would die before its real clock could start) and the store
	// deliberately refuses it, substituting the default.
	l.MessageTTLSeconds = 60
	l.UndeliveredTTLSeconds = 2 * 3600 // a backstop far shorter than the gap
	st := newStore(t, l)
	a, b, channelID := pair(t, st)

	sent, err := st.Send(ctx, a, channelID, "nobody is ever coming back", SendOpts{})
	if err != nil {
		t.Fatal(err)
	}
	age(t, st, sent.MessageID, "-64 hours")

	expired, _, err := st.Sweep(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if expired != 1 {
		t.Fatalf("sweep expired %d, want 1 — the backstop is not bounding abandoned mail", expired)
	}
	if got, _ := st.Poll(ctx, b, 10); len(got) != 0 {
		t.Fatalf("an expired message was delivered: %d", len(got))
	}
}

// Delivery starts the clock, and only the FIRST delivery does. A peer that polls
// repeatedly without acking must not be able to hold a message open forever.
func TestDeliveryArmsTheClocksOnceAndOnlyOnce(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, DefaultLimits())
	a, b, channelID := pair(t, st)

	sent, err := st.Send(ctx, a, channelID, "q", SendOpts{RequiresResponse: true})
	if err != nil {
		t.Fatal(err)
	}
	if sent.ReplyDeadlineAt != "" {
		t.Fatalf("a deadline was armed at send: %q", sent.ReplyDeadlineAt)
	}
	sentExpiry := sent.ExpiresAt

	first, err := st.Poll(ctx, b, 10)
	if err != nil || len(first) != 1 {
		t.Fatalf("first poll: %d %v", len(first), err)
	}
	if first[0].ReplyDeadlineAt == "" {
		t.Fatal("delivery did not arm the reply deadline")
	}
	// The delivered clock is much shorter than the undelivered backstop, so the
	// expiry must have moved EARLIER when the message landed.
	if first[0].ExpiresAt >= sentExpiry {
		t.Fatalf("delivery did not re-anchor the expiry: send %s, delivered %s", sentExpiry, first[0].ExpiresAt)
	}
	armed, armedDeadline := first[0].ExpiresAt, first[0].ReplyDeadlineAt

	// Redeliver: the message is un-acked, so a second poll returns it again.
	second, err := st.Poll(ctx, b, 10)
	if err != nil || len(second) != 1 {
		t.Fatalf("second poll: %d %v", len(second), err)
	}
	if second[0].ExpiresAt != armed || second[0].ReplyDeadlineAt != armedDeadline {
		t.Fatalf("a redelivery restarted the clocks: expiry %s->%s deadline %s->%s",
			armed, second[0].ExpiresAt, armedDeadline, second[0].ReplyDeadlineAt)
	}
	if second[0].DeliveryCount < 2 {
		t.Fatalf("delivery_count = %d, want >= 2", second[0].DeliveryCount)
	}
}

// A message NOBODY EVER READ keeps its text when it expires.
//
// Expiry used to blank unconditionally, which is how a 4 661-byte message requiring
// a response became permanently unknowable to both parties. The sender is told it
// expired; keeping the text makes "expired" a fact they can act on — resend it —
// rather than a hole where their own words used to be.
func TestExpiryKeepsTheBodyOfAMessageNobodyRead(t *testing.T) {
	ctx := context.Background()
	l := DefaultLimits()
	l.MessageTTLSeconds = 60
	l.UndeliveredTTLSeconds = 3600
	st := newStore(t, l)
	a, _, channelID := pair(t, st)

	sent, err := st.Send(ctx, a, channelID, "the thing nobody got to read", SendOpts{})
	if err != nil {
		t.Fatal(err)
	}
	age(t, st, sent.MessageID, "-64 hours")
	if _, _, err := st.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	m, err := st.MessageByID(ctx, sent.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	if m.State != "expired" {
		t.Fatalf("state = %q, want expired", m.State)
	}
	if m.Body != "the thing nobody got to read" {
		t.Fatalf("the body of an unread message was destroyed on expiry: %q", m.Body)
	}
}

// A cumulative ack must not settle mail the reader has never been shown.
//
// AckUpTo matched state IN ('queued','delivered'), so `ack_up_to_seq N` could mark
// a message that had never been delivered — you cannot have PROCESSED something you
// were never handed, and ack means processed.
func TestAckUpToCannotSettleUndeliveredMail(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, DefaultLimits())
	a, b, channelID := pair(t, st)

	first, err := st.Send(ctx, a, channelID, "read this one", SendOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Poll(ctx, b, 10); err != nil { // delivers only the first
		t.Fatal(err)
	}
	second, err := st.Send(ctx, a, channelID, "never shown to anyone", SendOpts{})
	if err != nil {
		t.Fatal(err)
	}

	// A cumulative ack covering BOTH sequence numbers.
	if err := st.AckUpTo(ctx, b, channelID, second.Seq); err != nil {
		t.Fatal(err)
	}
	m1, err := st.MessageByID(ctx, first.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	if m1.State != "acked" {
		t.Fatalf("the DELIVERED message was not acked: %q", m1.State)
	}
	m2, err := st.MessageByID(ctx, second.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	if m2.State == "acked" {
		t.Fatal("a cumulative ack settled a message that was never delivered — it is now unreadable and nobody knows it existed")
	}
	// And it is still there to be read.
	got, err := st.Poll(ctx, b, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].MessageID != second.MessageID {
		t.Fatalf("the undelivered message was not still deliverable: %+v", got)
	}
}

// Asking for more than the ceiling must yield the ceiling.
//
// `if limit <= 0 || limit > 100 { limit = 50 }` collapsed "no preference" and
// "give me everything" into the same answer, so a caller asking for 1000 got 50
// while a caller asking for 100 got 100. The failure punishes precisely the hub
// trying to drain a backlog.
func TestPollLimitAboveTheCeilingYieldsTheCeiling(t *testing.T) {
	ctx := context.Background()
	l := DefaultLimits()
	l.MaxUnackedPerChannel = 200
	st := newStore(t, l)
	a, b, channelID := pair(t, st)

	for i := 0; i < 120; i++ {
		if _, err := st.Send(ctx, a, channelID, "m", SendOpts{}); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	got, err := st.Poll(ctx, b, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 100 {
		t.Fatalf("poll(limit=1000) returned %d, want the ceiling of 100", len(got))
	}
}
