package comm

import (
	"context"
	"errors"
	"os"
	"testing"
)

// A caller may ask for a SHORTER lifetime, never a longer one — otherwise a
// session mints messages no sweep can settle.
func TestTTLIsClampedToTheOperatorSetting(t *testing.T) {
	ctx := context.Background()
	l := DefaultLimits()
	// A sender's ttl_seconds governs how long UNDELIVERED mail stays deliverable,
	// so that is the ceiling it is clamped against. MessageTTLSeconds bounds the
	// window AFTER delivery and a sender cannot choose it at all.
	l.MessageTTLSeconds = 30
	l.UndeliveredTTLSeconds = 60
	st := newStore(t, l)
	a, b, channelID := pair(t, st)

	long, err := st.SendToStation(ctx, a, channelID, "x", SendOpts{TTLSeconds: 365 * 24 * 3600})
	if err != nil {
		t.Fatal(err)
	}
	short, err := st.SendToStation(ctx, a, channelID, "y", SendOpts{TTLSeconds: 30})
	if err != nil {
		t.Fatal(err)
	}
	if long.ExpiresAt >= addSeconds(t, st, 3600) {
		t.Fatalf("an over-long ttl was honoured: %s", long.ExpiresAt)
	}
	if short.ExpiresAt >= long.ExpiresAt {
		t.Fatal("a shorter requested ttl was not honoured")
	}
	_ = b
}

// addSeconds returns the server clock plus n seconds, for expiry comparisons.
func addSeconds(t *testing.T, st *Store, n int) string {
	t.Helper()
	var out string
	if err := st.R.QueryRow(`SELECT strftime('%Y-%m-%dT%H:%M:%fZ','now',?)`, nowExpr(n)).Scan(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

// Sequence numbers must never go backwards, even after the metadata sweep has
// purged a direction's history — a reused number lets a retried cumulative ack
// settle brand-new messages.
func TestSequenceSurvivesMetadataPurge(t *testing.T) {
	ctx := context.Background()
	l := DefaultLimits()
	l.MetadataTTLSeconds = -1 // purge settled rows on the next sweep
	st := newStore(t, l)
	a, b, channelID := pair(t, st)

	first, err := st.SendToStation(ctx, a, channelID, "one", SendOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Poll(ctx, b, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Ack(ctx, b, first.MessageID); err != nil {
		t.Fatal(err)
	}
	if _, purged, err := st.Sweep(ctx); err != nil || purged == 0 {
		t.Fatalf("sweep purged=%d err=%v", purged, err)
	}

	next, err := st.SendToStation(ctx, a, channelID, "two", SendOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if next.Seq <= first.Seq {
		t.Fatalf("sequence went backwards after a purge: %d then %d", first.Seq, next.Seq)
	}
}

// The sender is told when a required reply never arrives — the reason reply
// deadlines exist. Without this the requester waits forever.
func TestSenderIsNotifiedWhenAReplyIsOverdue(t *testing.T) {
	ctx := context.Background()
	l := DefaultLimits()
	l.ReplyDeadlineSeconds = -1 // deadline already passed at insert
	st := newStore(t, l)
	a, b, channelID := pair(t, st)

	req, err := st.SendToStation(ctx, a, channelID, "please do X", SendOpts{RequiresResponse: true})
	if err != nil {
		t.Fatal(err)
	}
	// The deadline is armed at DELIVERY, so nothing is overdue until the peer has
	// been handed the message. Before this the clock ran from send, which is why
	// 18% of real messages arrived with the deadline already blown.
	if _, err := st.Poll(ctx, b, 10); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Sweep(ctx); err != nil {
		t.Fatal(err)
	}

	// DERIVED, not delivered (slice 4). The property is unchanged — a requester whose
	// peer went silent must not wait forever — but the sweep no longer writes them a
	// message to say so, because a pass that deletes must not also insert.
	n, err := st.NoticesForPoll(ctx, PartyOf(a), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(n) != 1 {
		t.Fatalf("requester sees %d notices, want 1 — the deadline passed and nothing tells them", len(n))
	}
	if n[0].Reason != "reply_overdue" || n[0].MessageID != req.MessageID {
		t.Fatalf("notice = %+v, want reply_overdue for %s", n[0], req.MessageID)
	}

	// Exactly once, however often the sweeper runs. Enforced by the WATERMARK now
	// rather than by a notified_at stamp: the old exactly-once lived in the writer, so
	// a notice enqueued without stamping repeated every minute forever. A query cannot
	// have that bug — but it CAN repeat what the reader already saw, so the watermark
	// is where the property moved, and this is the assertion that follows it there.
	if _, _, err := st.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	// A SECOND POLL is what confirms the first one's display — that is where the
	// exactly-once property lives, and it is the path a session actually takes.
	again, err := st.NoticesForPoll(ctx, PartyOf(a), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("the sender is told again after marking it seen: %+v", again)
	}
	_ = b
}

// The sender is told when a message dies unread, rather than believing it landed.
func TestSenderIsNotifiedWhenAMessageExpires(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, DefaultLimits())
	a, _, channelID := pair(t, st)

	sent, err := st.SendToStation(ctx, a, channelID, "never read", SendOpts{})
	if err != nil {
		t.Fatal(err)
	}
	// Backdate the UNDELIVERED backstop rather than setting a negative limit: a
	// non-positive retention window must fail safe and do nothing, so the store
	// refuses it, and reaching expiry through the real column is the honest route.
	if _, err := st.W.Exec(`UPDATE message SET expires_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','-1 second') WHERE message_id=?`, sent.MessageID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	n, err := st.NoticesFor(ctx, PartyOf(a), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(n) != 1 || n[0].Reason != "expired" || n[0].MessageID != sent.MessageID {
		t.Fatalf("sender was not told its message expired: %+v", n)
	}
	// AND THE SENDER'S INBOX IS UNTOUCHED. The notice used to be a real message in the
	// channel, so it consumed the sender's own poll and their ack. Nothing arrives now.
	got, err := st.Poll(ctx, a, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("the sweep delivered %d message(s) to the sender: %+v", len(got), got)
	}
}

// A status notice must reach the sender even when the channel is at its
// backpressure cap — that is exactly when the failure signal matters most.
// A SENDER AT THE BACKPRESSURE CAP STILL LEARNS ITS MESSAGES DIED.
//
// This test used to be called TestStatusNoticeBypassesBackpressure, and the name records
// what the old design needed: the notice was a real message into a channel that was, by
// construction, already full — so it had to be granted an exemption from the cap it was
// reporting. An exemption is a rule with a hole in it, and the hole was in the writer
// that a failing sweep rolls back.
//
// Derived notices need no exemption because they are not messages. The property is the
// one that mattered all along and it is now structural: being at the cap has nothing to
// do with being told.
func TestASenderAtTheBackpressureCapIsStillTold(t *testing.T) {
	ctx := context.Background()
	l := DefaultLimits()
	l.MaxUnackedPerChannel = 2
	st := newStore(t, l)
	a, _, channelID := pair(t, st)

	for i := 0; i < 2; i++ {
		if _, err := st.SendToStation(ctx, a, channelID, "m", SendOpts{}); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	// CONTROL: the channel really is at its cap, so the assertion below is about
	// notices surviving backpressure rather than about backpressure never happening.
	if _, err := st.SendToStation(ctx, a, channelID, "one too many", SendOpts{}); !errors.Is(err, ErrBackpressure) {
		t.Fatalf("a third send past a cap of 2 returned %v, want ErrBackpressure — the fixture is not at the cap", err)
	}
	if _, err := st.W.Exec(`UPDATE message SET expires_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','-1 second')`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Sweep(ctx); err != nil {
		t.Fatalf("sweep with a full channel: %v", err)
	}
	n, err := st.NoticesFor(ctx, PartyOf(a), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(n) != 2 {
		t.Fatalf("want 2 notices past the cap, got %d", len(n))
	}
}

// An answered request must not keep its body until the metadata purge.
func TestAnsweredRequestBodyIsDropped(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, DefaultLimits())
	a, b, channelID := pair(t, st)

	req, err := st.SendToStation(ctx, a, channelID, "the request text", SendOpts{RequiresResponse: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Poll(ctx, b, 10); err != nil {
		t.Fatal(err)
	}
	// Ack BEFORE replying — the case that used to leak.
	if _, err := st.Ack(ctx, b, req.MessageID); err != nil {
		t.Fatal(err)
	}
	if m, _ := st.MessageByID(ctx, req.MessageID); m.Body == "" {
		t.Fatal("an unanswered request lost its body on ack")
	}
	if _, err := st.SendToStation(ctx, b, a.StationID, "done", SendOpts{ReplyToMessageID: req.MessageID}); err != nil {
		t.Fatal(err)
	}
	// AN ANSWERED REQUEST NOW FOLLOWS THE SAME RETENTION RULE AS EVERYTHING ELSE.
	//
	// It used to be blanked the instant a reply arrived, because requires_response was
	// the ONLY thing that kept any body alive and an answered request no longer needed
	// the carve-out. With retention governing every settled message uniformly, an
	// early drop would make the answered request the single message type whose text
	// dies sooner than its peers — and it is the one a curator reconstructing a
	// decision is most likely to want, since it is the half that got an answer.
	m, err := st.MessageByID(ctx, req.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	if m.Body != "the request text" {
		t.Fatalf("an answered request lost its body ahead of the retention window: %q", m.Body)
	}
	// And it IS reclaimed — retention is a delay, not an exemption. This is the half
	// the old immediate-drop was really protecting, and it still holds.
	age(t, st, req.MessageID, "-25 hours") // past the 24 h default retention
	if _, _, err := st.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if m, _ := st.MessageByID(ctx, req.MessageID); m.Body != "" {
		t.Fatalf("the answered request's body outlived its retention window: %q", m.Body)
	}
}

// The budget must reserve the DECLARED size of in-flight uploads, or concurrent
// PUTs each pass the check and collectively overshoot it.
func TestBudgetReservesInFlightUploads(t *testing.T) {
	ctx := context.Background()
	l := fileLimits()
	l.FileBudgetBytes = 100
	l.FileMinFreeBytes = 0
	st := newStore(t, l)
	a, _, channelID := pair(t, st)

	if _, err := st.OfferFile(ctx, a, FileAddr{StationID: channelID}, FileOffer{
		Name: "a", SizeBytes: 80, SHA256: shaOf([]byte("a")), Transfer: "upload",
	}); err != nil {
		t.Fatal(err)
	}
	// 80 declared but not yet uploaded: a second 80-byte offer must be refused.
	if _, err := st.OfferFile(ctx, a, FileAddr{StationID: channelID}, FileOffer{
		Name: "b", SizeBytes: 80, SHA256: shaOf([]byte("b")), Transfer: "upload",
	}); !errors.Is(err, ErrQuota) {
		t.Fatalf("in-flight bytes were invisible to the budget: %v", err)
	}
}

// The relay's own errors say "re-offer to retry"; that path must actually work.
func TestReOfferAfterFailureMintsAFreshGrant(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, fileLimits())
	a, _, channelID := pair(t, st)

	o := FileOffer{Name: "f", SizeBytes: 4, SHA256: shaOf([]byte("abcd")), Transfer: "upload", IdempotencyKey: "k9"}
	first, err := st.OfferFile(ctx, a, FileAddr{StationID: channelID}, o)
	if err != nil {
		t.Fatal(err)
	}
	gi, _ := st.ConsumeGrant(ctx, first.UploadGrant, "upload")
	if err := st.FailUpload(ctx, gi.AttachmentRow); err != nil {
		t.Fatal(err)
	}

	again, err := st.OfferFile(ctx, a, FileAddr{StationID: channelID}, o)
	if err != nil {
		t.Fatalf("re-offer after failure: %v", err)
	}
	if again.UploadGrant == "" {
		t.Fatal("re-offer after a failed upload returned no grant — the documented recovery path is a dead end")
	}
	if again.Attachment.State != "offered" {
		t.Fatalf("revived attachment state = %q", again.Attachment.State)
	}
}

// The budget frees only for bytes actually gone: a row whose file could not be
// removed must keep its accounting so the next sweep retries it.
func TestSweepKeepsAccountingWhenBytesRemain(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, fileLimits())
	a, b, channelID := pair(t, st)

	content := []byte("bytes")
	res, err := st.OfferFile(ctx, a, FileAddr{StationID: channelID}, FileOffer{
		Name: "d", SizeBytes: int64(len(content)), SHA256: shaOf(content), Transfer: "upload",
	})
	if err != nil {
		t.Fatal(err)
	}
	gi, _ := st.ConsumeGrant(ctx, res.UploadGrant, "upload")
	if err := st.EnsureFilesDir(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(st.FilePath(gi.AttachmentID), content, 0o600); err != nil {
		t.Fatal(err)
	}
	msg, err := st.CompleteUpload(ctx, gi.AttachmentRow, int64(len(content)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Poll(ctx, b, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Ack(ctx, b, msg.MessageID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	// File gone AND accounting cleared.
	if _, err := os.Stat(st.FilePath(gi.AttachmentID)); !os.IsNotExist(err) {
		t.Fatal("delivered bytes survived the sweep")
	}
	var held int64
	if err := st.R.QueryRow(`SELECT COALESCE(SUM(stored_bytes),0) FROM attachment`).Scan(&held); err != nil {
		t.Fatal(err)
	}
	if held != 0 {
		t.Fatalf("accounting not freed after a successful unlink: %d", held)
	}
}

// The hearsay window must see a REdelivered message: at-least-once means a
// message first delivered before the window is commonly re-read inside it.
func TestProvenanceSeesRedelivery(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, DefaultLimits())
	a, b, channelID := pair(t, st)

	if _, err := st.SendToStation(ctx, a, channelID, "hearsay", SendOpts{}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Poll(ctx, b, 10); err != nil {
		t.Fatal(err)
	}
	// Backdate the first delivery well outside any window.
	if _, err := st.W.Exec(`UPDATE delivery SET first_delivered_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','-2 days')`); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.ReceivedSince(ctx, 7, 3600); got {
		t.Fatal("a long-stale delivery still marked the actor")
	}
	// Acking now is the receiver acting on it, inside the window.
	msgs, err := st.Poll(ctx, b, 10)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("redelivery: %v %d", err, len(msgs))
	}
	if _, err := st.Ack(ctx, b, msgs[0].MessageID); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.ReceivedSince(ctx, 7, 3600); !got {
		t.Fatal("a message acted upon inside the window did not mark the actor")
	}
}

// *** A STATION'S MAILBOX MUST SURVIVE EVERY SWEEP, AND THE CASE THAT MATTERS IS PAIR MAIL. ***
//
// Four tests stood here, covering an idle-endpoint reap: that it removed idle rows, spared rows
// with traffic, failed safe on a non-positive window, and did not collect a channel seat. All four
// were correct about the code they described, and the code is gone — the reap bounded a row set
// that grew because "sessions register once and never unregister", and there is no registration
// any more. One station, one mailbox, created on first use and reused forever.
//
// THIS REPLACES THEM WITH THE PROPERTY THAT NOW MATTERS, and it is the case the old guards did not
// cover. The reap spared any endpoint referenced by a message, an attachment or a channel seat —
// but a PAIR message is addressed to the post, not to a connection, so its delivery row carries
// recipient_endpoint NULL by design. A station whose only traffic was pair mail therefore matched
// every guard and was eligible for deletion, with mail waiting for it.
//
// The damage would have been quiet rather than loud: MailboxFor recreates a mailbox on the next
// call, so nothing errors. It comes back with a NEW endpoint_id, no last-seen history, and the
// directory reporting the station as unstaffed. A retention sweep that silently re-issues an
// identity is worse than one that fails.
func TestASweepNeverTakesAStationsMailbox(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, DefaultLimits())
	ep := mailbox(t, st, "s-quiet", "tok")

	// AS IDLE AS A ROW CAN LOOK: backdated a year, and never party to anything the old guards
	// recognised. If any reap survives anywhere in Sweep, this is the row it takes.
	if _, err := st.W.Exec(
		`UPDATE endpoint SET last_seen_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','-365 day') WHERE endpoint_id=?`,
		ep.EndpointID); err != nil {
		t.Fatal(err)
	}
	// POSITIVE CONTROL. Without it, "the mailbox is still there" would be equally true of a store
	// where the row never existed and every assertion below would be about nothing.
	before, err := st.ListEndpoints(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 1 {
		t.Fatalf("setup: %d mailboxes, want 1", len(before))
	}

	for i := 0; i < 3; i++ {
		if _, _, err := st.Sweep(ctx); err != nil {
			t.Fatal(err)
		}
	}

	after, err := st.ListEndpoints(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 {
		t.Fatalf("a station's mailbox did not survive the sweep: %d rows remain. Its mail is addressed "+
			"to the post rather than to this row, so nothing would have errored — the station would "+
			"simply have come back with a new id and no history.", len(after))
	}
	// THE SAME ROW, not a replacement. Identity is the thing at risk here: a mailbox deleted and
	// recreated satisfies a count and loses everything the count was standing in for.
	if after[0].EndpointID != ep.EndpointID {
		t.Errorf("the mailbox was replaced: %s -> %s", ep.EndpointID, after[0].EndpointID)
	}
}
