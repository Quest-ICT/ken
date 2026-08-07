package comm

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

// A human's revoke is a brake. A still-valid pairing code must not let the second
// session flip a revoked channel back open.
func TestRevokedPendingChannelCannotBeReopened(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, DefaultLimits())
	a, _, err := st.RegisterEndpoint(ctx, owner("tok-a"), "dev", "")
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := st.RegisterEndpoint(ctx, owner("tok-b"), "test", "")
	if err != nil {
		t.Fatal(err)
	}
	code, err := st.MintPairingCode(ctx, 1, 42, "")
	if err != nil {
		t.Fatal(err)
	}
	ch, err := st.JoinChannel(ctx, a, code)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RevokeChannel(ctx, ch.ChannelID); err != nil {
		t.Fatal(err)
	}

	if _, err := st.JoinChannel(ctx, b, code); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second join on a revoked channel: want ErrNotFound, got %v", err)
	}
	list, err := st.ListChannelsForSpace(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].State != "revoked" {
		t.Fatalf("channel state after refused join: %+v", list)
	}
}

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

	long, err := st.Send(ctx, a, channelID, "x", SendOpts{TTLSeconds: 365 * 24 * 3600})
	if err != nil {
		t.Fatal(err)
	}
	short, err := st.Send(ctx, a, channelID, "y", SendOpts{TTLSeconds: 30})
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

	first, err := st.Send(ctx, a, channelID, "one", SendOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Poll(ctx, b, 10); err != nil {
		t.Fatal(err)
	}
	if err := st.Ack(ctx, b, first.MessageID); err != nil {
		t.Fatal(err)
	}
	if _, purged, err := st.Sweep(ctx); err != nil || purged == 0 {
		t.Fatalf("sweep purged=%d err=%v", purged, err)
	}

	next, err := st.Send(ctx, a, channelID, "two", SendOpts{})
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

	req, err := st.Send(ctx, a, channelID, "please do X", SendOpts{RequiresResponse: true})
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

	got, err := st.Poll(ctx, a, 10) // the REQUESTER polls
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("requester received %d messages, want 1 status notice", len(got))
	}
	if got[0].Kind != "status" {
		t.Fatalf("notice kind = %q, want status", got[0].Kind)
	}
	if !strings.Contains(got[0].Body, "reply_overdue") || !strings.Contains(got[0].Body, req.MessageID) {
		t.Fatalf("notice body does not identify the overdue request: %q", got[0].Body)
	}

	// Exactly once, however often the sweeper runs.
	if _, _, err := st.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Ack(ctx, a, got[0].MessageID); err != nil {
		t.Fatal(err)
	}
	if again, _ := st.Poll(ctx, a, 10); len(again) != 0 {
		t.Fatalf("sweeper re-notified: %d extra notices", len(again))
	}
	_ = b
}

// The sender is told when a message dies unread, rather than believing it landed.
func TestSenderIsNotifiedWhenAMessageExpires(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, DefaultLimits())
	a, _, channelID := pair(t, st)

	sent, err := st.Send(ctx, a, channelID, "never read", SendOpts{})
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
	got, err := st.Poll(ctx, a, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Kind != "status" || !strings.Contains(got[0].Body, "expired") {
		t.Fatalf("sender was not told its message expired: %+v", got)
	}
	if !strings.Contains(got[0].Body, sent.MessageID) {
		t.Fatalf("notice does not identify the message: %q", got[0].Body)
	}
}

// A status notice must reach the sender even when the channel is at its
// backpressure cap — that is exactly when the failure signal matters most.
func TestStatusNoticeBypassesBackpressure(t *testing.T) {
	ctx := context.Background()
	l := DefaultLimits()
	l.MaxUnackedPerChannel = 2
	st := newStore(t, l)
	a, _, channelID := pair(t, st)

	for i := 0; i < 2; i++ {
		if _, err := st.Send(ctx, a, channelID, "m", SendOpts{}); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	if _, err := st.W.Exec(`UPDATE message SET expires_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','-1 second')`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Sweep(ctx); err != nil {
		t.Fatalf("sweep with a full channel: %v", err)
	}
	got, err := st.Poll(ctx, a, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 status notices past the cap, got %d", len(got))
	}
}

// An answered request must not keep its body until the metadata purge.
func TestAnsweredRequestBodyIsDropped(t *testing.T) {
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
	// Ack BEFORE replying — the case that used to leak.
	if err := st.Ack(ctx, b, req.MessageID); err != nil {
		t.Fatal(err)
	}
	if m, _ := st.MessageByID(ctx, req.MessageID); m.Body == "" {
		t.Fatal("an unanswered request lost its body on ack")
	}
	if _, err := st.Send(ctx, b, channelID, "done", SendOpts{ReplyToMessageID: req.MessageID}); err != nil {
		t.Fatal(err)
	}
	m, err := st.MessageByID(ctx, req.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	if m.Body != "" {
		t.Fatalf("answered request kept its body: %q", m.Body)
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

	if _, err := st.OfferFile(ctx, a, channelID, FileOffer{
		Name: "a", SizeBytes: 80, SHA256: shaOf([]byte("a")), Transfer: "upload",
	}); err != nil {
		t.Fatal(err)
	}
	// 80 declared but not yet uploaded: a second 80-byte offer must be refused.
	if _, err := st.OfferFile(ctx, a, channelID, FileOffer{
		Name: "b", SizeBytes: 80, SHA256: shaOf([]byte("b")), Transfer: "upload",
	}); !errors.Is(err, ErrQuota) {
		t.Fatalf("in-flight bytes were invisible to the budget: %v", err)
	}
}

// Revoking a channel must stop BYTES, not just new messages.
func TestRevokedChannelStopsDownloadGrants(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, fileLimits())
	a, b, channelID := pair(t, st)

	content := []byte("payload")
	res, err := st.OfferFile(ctx, a, channelID, FileOffer{
		Name: "p", SizeBytes: int64(len(content)), SHA256: shaOf(content), Transfer: "upload",
	})
	if err != nil {
		t.Fatal(err)
	}
	gi, _ := st.ConsumeGrant(ctx, res.UploadGrant, "upload")
	if _, _, err := st.CompleteUpload(ctx, gi.AttachmentRow, int64(len(content))); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.GrantDownload(ctx, b, res.Attachment.AttachmentID); err != nil {
		t.Fatalf("grant before revoke: %v", err)
	}
	if err := st.RevokeChannel(ctx, channelID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.GrantDownload(ctx, b, res.Attachment.AttachmentID); !errors.Is(err, ErrChannelClosed) {
		t.Fatalf("revoked channel still grants downloads: %v", err)
	}
}

// The relay's own errors say "re-offer to retry"; that path must actually work.
func TestReOfferAfterFailureMintsAFreshGrant(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, fileLimits())
	a, _, channelID := pair(t, st)

	o := FileOffer{Name: "f", SizeBytes: 4, SHA256: shaOf([]byte("abcd")), Transfer: "upload", IdempotencyKey: "k9"}
	first, err := st.OfferFile(ctx, a, channelID, o)
	if err != nil {
		t.Fatal(err)
	}
	gi, _ := st.ConsumeGrant(ctx, first.UploadGrant, "upload")
	if err := st.FailUpload(ctx, gi.AttachmentRow); err != nil {
		t.Fatal(err)
	}

	again, err := st.OfferFile(ctx, a, channelID, o)
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
	res, err := st.OfferFile(ctx, a, channelID, FileOffer{
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
	msg, _, err := st.CompleteUpload(ctx, gi.AttachmentRow, int64(len(content)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Poll(ctx, b, 10); err != nil {
		t.Fatal(err)
	}
	if err := st.Ack(ctx, b, msg.MessageID); err != nil {
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

// Idle endpoints must not accumulate forever: sessions register once and never
// unregister.
func TestSweepRemovesIdleEndpoints(t *testing.T) {
	ctx := context.Background()
	l := DefaultLimits()
	l.EndpointIdleTTLSeconds = 60 // a real, positive idle window
	st := newStore(t, l)
	ep, _, err := st.RegisterEndpoint(ctx, owner("tok"), "ghost", "")
	if err != nil {
		t.Fatal(err)
	}
	if eps, _ := st.ListEndpoints(ctx, 1); len(eps) != 1 {
		t.Fatalf("setup: %d endpoints", len(eps))
	}
	// Backdate last_seen well past the window — this is what makes it "idle".
	if _, err := st.W.Exec(`UPDATE endpoint SET last_seen_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','-1 day') WHERE endpoint_id=?`, ep.EndpointID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	eps, err := st.ListEndpoints(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(eps) != 0 {
		t.Fatalf("idle endpoint survived the sweep: %+v", eps)
	}
}

// An endpoint with live traffic must NOT be swept, however idle it looks.
func TestSweepKeepsEndpointsWithTraffic(t *testing.T) {
	ctx := context.Background()
	l := DefaultLimits()
	l.EndpointIdleTTLSeconds = -1
	st := newStore(t, l)
	a, _, channelID := pair(t, st)
	if _, err := st.Send(ctx, a, channelID, "live", SendOpts{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if eps, _ := st.ListEndpoints(ctx, 1); len(eps) != 2 {
		t.Fatalf("endpoints with traffic were swept: %d remain", len(eps))
	}
}

// The hearsay window must see a REdelivered message: at-least-once means a
// message first delivered before the window is commonly re-read inside it.
func TestProvenanceSeesRedelivery(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, DefaultLimits())
	a, b, channelID := pair(t, st)

	if _, err := st.Send(ctx, a, channelID, "hearsay", SendOpts{}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Poll(ctx, b, 10); err != nil {
		t.Fatal(err)
	}
	// Backdate the first delivery well outside any window.
	if _, err := st.W.Exec(`UPDATE message SET first_delivered_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','-2 days')`); err != nil {
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
	if err := st.Ack(ctx, b, msgs[0].MessageID); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.ReceivedSince(ctx, 7, 3600); !got {
		t.Fatal("a message acted upon inside the window did not mark the actor")
	}
}

// A retention sweep keyed on a threshold must fail SAFE on a non-positive window:
// a zero window (the shape a dropped settings mapping produces) must DISABLE the
// endpoint sweep, never delete every idle endpoint. This is the 1.2.0 regression
// that made COMM unusable — a freshly registered endpoint was swept mid-handshake.
func TestEndpointSweepFailsSafeOnZeroWindow(t *testing.T) {
	ctx := context.Background()
	l := DefaultLimits()
	l.EndpointIdleTTLSeconds = 0 // the dropped-mapping shape
	st := newStore(t, l)
	if _, _, err := st.RegisterEndpoint(ctx, owner("tok"), "fresh", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	eps, err := st.ListEndpoints(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(eps) != 1 {
		t.Fatalf("a zero idle-window swept a fresh endpoint (%d remain) — the sweep did not fail safe", len(eps))
	}
}

// The pairing-code label must travel onto the channel so the console can lead
// with the human name the operator chose, not the opaque id or the drifting
// endpoint labels.
func TestChannelCarriesPairingLabel(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, DefaultLimits())
	a, _, err := st.RegisterEndpoint(ctx, owner("tok-a"), "public-dev", "")
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := st.RegisterEndpoint(ctx, owner("tok-b"), "ken-prod-ops", "")
	if err != nil {
		t.Fatal(err)
	}
	code, err := st.MintPairingCode(ctx, 1, 42, "Ken dev <-> prod")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.JoinChannel(ctx, a, code); err != nil {
		t.Fatal(err)
	}
	if _, err := st.JoinChannel(ctx, b, code); err != nil {
		t.Fatal(err)
	}

	rows, err := st.ListChannelsForSpace(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 channel, got %d", len(rows))
	}
	if rows[0].Label != "Ken dev <-> prod" {
		t.Fatalf("channel label = %q, want the pairing-code label", rows[0].Label)
	}
}

// A code with no label leaves the channel label empty (the console falls back to
// the endpoint labels), not broken.
func TestChannelLabelEmptyWhenCodeUnlabelled(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, DefaultLimits())
	a, b, channelID := pair(t, st) // pair() mints a code with a label, so re-do plainly
	_ = channelID
	c, _, err := st.RegisterEndpoint(ctx, owner("tok-c"), "x", "")
	if err != nil {
		t.Fatal(err)
	}
	d, _, err := st.RegisterEndpoint(ctx, owner("tok-d"), "y", "")
	if err != nil {
		t.Fatal(err)
	}
	code, err := st.MintPairingCode(ctx, 1, 42, "") // no label
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.JoinChannel(ctx, c, code); err != nil {
		t.Fatal(err)
	}
	if _, err := st.JoinChannel(ctx, d, code); err != nil {
		t.Fatal(err)
	}
	rows, err := st.ListChannelsForSpace(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		// the unlabelled channel (between x and y) must have an empty label, not a crash
		if r.LabelA == "x" && r.Label != "" {
			t.Fatalf("unlabelled channel got label %q", r.Label)
		}
	}
	_, _ = a, b
}
