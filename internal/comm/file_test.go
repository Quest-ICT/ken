package comm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"
)

func fileLimits() Limits {
	l := DefaultLimits()
	l.FilesEnabled = true
	return l
}

func shaOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// The name contract is the C9 security boundary: a name that fails here could
// steer a receiving session toward an arbitrary local path.
func TestValidateAttachmentName(t *testing.T) {
	ok := []string{"HANDOUT.md", "report-v2.tar.gz", "ARCHIVO Ñ.txt", ".env.example", "a"}
	for _, n := range ok {
		if err := ValidateAttachmentName(n); err != nil {
			t.Errorf("valid name %q rejected: %v", n, err)
		}
	}
	bad := []string{
		"", ".", "..", "~", "~/.ssh/id_ed25519",
		"/etc/passwd", "a/b", `a\b`, "..\\up",
		"nul\x00byte", "tab\tname", "esc\x1b[0m", "del\x7f",
		strings.Repeat("x", 256),
	}
	for _, n := range bad {
		if err := ValidateAttachmentName(n); err == nil {
			t.Errorf("dangerous name %q was accepted", n)
		}
	}
}

func TestOfferValidation(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, fileLimits())
	a, _, channelID := pair(t, st)
	good := FileOffer{Name: "f.txt", SizeBytes: 5, SHA256: shaOf([]byte("hello")), Transfer: "upload"}

	// Disabled ⇒ refused, regardless of anything else.
	st.SetLimits(DefaultLimits())
	if _, err := st.OfferFile(ctx, a, channelID, good); !errors.Is(err, ErrFilesDisabled) {
		t.Fatalf("disabled: want ErrFilesDisabled, got %v", err)
	}
	st.SetLimits(fileLimits())

	cases := []struct {
		name string
		mut  func(*FileOffer)
	}{
		{"bad name", func(o *FileOffer) { o.Name = "../../etc/passwd" }},
		{"bad sha", func(o *FileOffer) { o.SHA256 = "nothex" }},
		{"zero size", func(o *FileOffer) { o.SizeBytes = 0 }},
		{"oversize", func(o *FileOffer) { o.SizeBytes = fileLimits().FileMaxBytes + 1 }},
		{"bad transfer", func(o *FileOffer) { o.Transfer = "carrier-pigeon" }},
		{"path without nonce", func(o *FileOffer) { o.Transfer = "path"; o.NonceSHA256 = "" }},
	}
	for _, c := range cases {
		o := good
		c.mut(&o)
		if _, err := st.OfferFile(ctx, a, channelID, o); err == nil {
			t.Errorf("%s: offer was accepted", c.name)
		}
	}
}

// A path offer delivers immediately: the peer polls a message whose File
// descriptor carries the validated name and both hashes.
func TestPathOfferDeliversFileDescriptor(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, fileLimits())
	a, b, channelID := pair(t, st)

	nonce := shaOf([]byte("the-nonce"))
	res, err := st.OfferFile(ctx, a, channelID, FileOffer{
		Name: "HANDOUT.md", SizeBytes: 9, SHA256: shaOf([]byte("the-bytes")),
		Transfer: "path", NonceSHA256: nonce, Note: "read this and follow up",
	})
	if err != nil {
		t.Fatalf("offer: %v", err)
	}
	if res.Message == nil {
		t.Fatal("path offer did not enqueue a message")
	}
	if res.UploadGrant != "" {
		t.Fatal("path offer minted an upload grant — no bytes should move through the server")
	}

	got, err := st.Poll(ctx, b, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].File == nil {
		t.Fatalf("peer did not receive the file descriptor: %+v", got)
	}
	f := got[0].File
	if f.Name != "HANDOUT.md" || f.Transfer != "path" || f.NonceSHA256 != nonce {
		t.Fatalf("descriptor mismatch: %+v", f)
	}
	if got[0].Body != "read this and follow up" {
		t.Fatalf("note lost: %q", got[0].Body)
	}
}

// An upload offer enqueues NOTHING until the bytes arrive and verify: the
// receiver never observes partial state.
func TestUploadOfferDeliversOnlyAfterCompletion(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, fileLimits())
	a, b, channelID := pair(t, st)

	content := []byte("file-content")
	res, err := st.OfferFile(ctx, a, channelID, FileOffer{
		Name: "data.bin", SizeBytes: int64(len(content)), SHA256: shaOf(content),
		Transfer: "upload", Note: "here you go",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.UploadGrant == "" {
		t.Fatal("upload offer returned no grant")
	}
	if res.Message != nil {
		t.Fatal("upload offer enqueued a message before any bytes arrived")
	}
	if got, _ := st.Poll(ctx, b, 10); len(got) != 0 {
		t.Fatalf("peer polled %d messages before upload completion", len(got))
	}

	gi, err := st.ConsumeGrant(ctx, res.UploadGrant, "upload")
	if err != nil {
		t.Fatalf("consume grant: %v", err)
	}
	if gi.EndpointToken != a.Owner.TokenID || gi.SHA256 != shaOf(content) {
		t.Fatalf("grant info mismatch: %+v", gi)
	}
	msg, err := st.CompleteUpload(ctx, gi.AttachmentRow, int64(len(content)))
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	// The delivery is filed against the recipient's PARTY. This asserted the returned
	// endpoint rowid instead — the frozen seat, which is the thing that must NOT be
	// relied upon; the Poll below already proves the message reaches b.
	var party string
	if err := st.R.QueryRow(
		`SELECT d.party_key FROM delivery d JOIN message m ON m.id=d.message_row WHERE m.message_id=?`,
		msg.MessageID).Scan(&party); err != nil {
		t.Fatal(err)
	}
	if want := endpointPartyKey(b.ID); party != want {
		t.Fatalf("delivery party = %q, want %q", party, want)
	}

	got, err := st.Poll(ctx, b, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].MessageID != msg.MessageID || got[0].File == nil {
		t.Fatalf("completed upload not delivered: %+v", got)
	}
	if got[0].File.Transfer != "upload" || got[0].Body != "here you go" {
		t.Fatalf("descriptor/note mismatch: %+v body=%q", got[0].File, got[0].Body)
	}
}

// Grants are single-use, kind-bound, and indistinguishable when dead.
func TestGrantIsSingleUseAndKindBound(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, fileLimits())
	a, _, channelID := pair(t, st)

	res, err := st.OfferFile(ctx, a, channelID, FileOffer{
		Name: "x", SizeBytes: 1, SHA256: shaOf([]byte("x")), Transfer: "upload",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Wrong kind first: must not consume.
	if _, err := st.ConsumeGrant(ctx, res.UploadGrant, "download"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong kind: want ErrNotFound, got %v", err)
	}
	if _, err := st.ConsumeGrant(ctx, res.UploadGrant, "upload"); err != nil {
		t.Fatalf("first use: %v", err)
	}
	if _, err := st.ConsumeGrant(ctx, res.UploadGrant, "upload"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second use: want ErrNotFound, got %v", err)
	}
	if _, err := st.ConsumeGrant(ctx, "nosuchgrant", "upload"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown grant: want ErrNotFound, got %v", err)
	}
}

// Download grants belong to the recipient alone, and only once bytes exist.
func TestGrantDownloadIsRecipientOnly(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, fileLimits())
	a, b, channelID := pair(t, st)

	content := []byte("payload")
	res, err := st.OfferFile(ctx, a, channelID, FileOffer{
		Name: "p.bin", SizeBytes: int64(len(content)), SHA256: shaOf(content), Transfer: "upload",
	})
	if err != nil {
		t.Fatal(err)
	}
	attID := res.Attachment.AttachmentID

	// Not ready yet — no bytes.
	if _, _, err := st.GrantDownload(ctx, b, attID); err == nil {
		t.Fatal("download granted before the upload completed")
	}
	gi, _ := st.ConsumeGrant(ctx, res.UploadGrant, "upload")
	if _, err := st.CompleteUpload(ctx, gi.AttachmentRow, int64(len(content))); err != nil {
		t.Fatal(err)
	}

	// The SENDER must not be able to mint a download for its own offer.
	if _, _, err := st.GrantDownload(ctx, a, attID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("sender minted a download grant: %v", err)
	}
	g, att, err := st.GrantDownload(ctx, b, attID)
	if err != nil || g == "" {
		t.Fatalf("recipient grant failed: %v", err)
	}
	if att.Name != "p.bin" {
		t.Fatalf("attachment mismatch: %+v", att)
	}
	// Repeatable: a failed curl needs a fresh grant.
	if g2, _, err := st.GrantDownload(ctx, b, attID); err != nil || g2 == g {
		t.Fatalf("second grant failed or was identical: %v", err)
	}
}

// Idempotent re-offer returns the original attachment with a fresh grant.
func TestOfferIsIdempotentPerKey(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, fileLimits())
	a, _, channelID := pair(t, st)

	o := FileOffer{Name: "f", SizeBytes: 1, SHA256: shaOf([]byte("f")), Transfer: "upload", IdempotencyKey: "k1"}
	r1, err := st.OfferFile(ctx, a, channelID, o)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := st.OfferFile(ctx, a, channelID, o)
	if err != nil {
		t.Fatal(err)
	}
	if r1.Attachment.AttachmentID != r2.Attachment.AttachmentID {
		t.Fatal("re-offer minted a second attachment")
	}
	if r2.UploadGrant == "" || r2.UploadGrant == r1.UploadGrant {
		t.Fatal("re-offer must mint a FRESH grant while the upload is pending")
	}
}

// The global budget counts bytes actually held; offers beyond it are refused.
func TestFileBudgetFailsClosed(t *testing.T) {
	ctx := context.Background()
	l := fileLimits()
	l.FileBudgetBytes = 10
	l.FileMinFreeBytes = 0 // isolate the budget check from the machine's real disk
	st := newStore(t, l)
	a, _, channelID := pair(t, st)

	content := []byte("12345678") // 8 bytes
	res, err := st.OfferFile(ctx, a, channelID, FileOffer{
		Name: "a", SizeBytes: 8, SHA256: shaOf(content), Transfer: "upload",
	})
	if err != nil {
		t.Fatal(err)
	}
	gi, _ := st.ConsumeGrant(ctx, res.UploadGrant, "upload")
	if _, err := st.CompleteUpload(ctx, gi.AttachmentRow, 8); err != nil {
		t.Fatal(err)
	}
	// 8 of 10 bytes held: a 3-byte offer must be refused.
	if _, err := st.OfferFile(ctx, a, channelID, FileOffer{
		Name: "b", SizeBytes: 3, SHA256: shaOf([]byte("abc")), Transfer: "upload",
	}); !errors.Is(err, ErrQuota) {
		t.Fatalf("over-budget offer: want ErrQuota, got %v", err)
	}
}

// In-flight uploads are capped per sender so never-completed uploads cannot
// hold the budget hostage.
func TestConcurrentUploadCap(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, fileLimits())
	a, _, channelID := pair(t, st)

	for i := 0; i < maxConcurrentUploads; i++ {
		if _, err := st.OfferFile(ctx, a, channelID, FileOffer{
			Name: "f", SizeBytes: 1, SHA256: shaOf([]byte("f")), Transfer: "upload",
		}); err != nil {
			t.Fatalf("offer %d: %v", i, err)
		}
	}
	if _, err := st.OfferFile(ctx, a, channelID, FileOffer{
		Name: "f", SizeBytes: 1, SHA256: shaOf([]byte("f")), Transfer: "upload",
	}); !errors.Is(err, ErrBackpressure) {
		t.Fatalf("over-cap offer: want ErrBackpressure, got %v", err)
	}
}

// Acking the message settles the attachment and the sweeper deletes its bytes.
func TestSweepDeletesDeliveredFileBytes(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, fileLimits())
	a, b, channelID := pair(t, st)

	content := []byte("bytes-to-delete")
	res, err := st.OfferFile(ctx, a, channelID, FileOffer{
		Name: "d.bin", SizeBytes: int64(len(content)), SHA256: shaOf(content), Transfer: "upload",
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
	if _, err := os.Stat(st.FilePath(gi.AttachmentID)); !os.IsNotExist(err) {
		t.Fatal("delivered attachment bytes survived the sweep")
	}
}

// Expiry covers attachments that were never uploaded or never fetched.
func TestSweepExpiresStaleAttachments(t *testing.T) {
	ctx := context.Background()
	l := fileLimits()
	l.FileTTLSeconds = -1 // already expired at insert (a per-call TTL <= 0 is clamped to this default)
	st := newStore(t, l)
	a, _, channelID := pair(t, st)

	res, err := st.OfferFile(ctx, a, channelID, FileOffer{
		Name: "stale", SizeBytes: 1, SHA256: shaOf([]byte("x")), Transfer: "upload",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	// The expired attachment no longer accepts a completion.
	gi, err := st.ConsumeGrant(ctx, res.UploadGrant, "upload")
	if err == nil {
		if _, err := st.CompleteUpload(ctx, gi.AttachmentRow, 1); err == nil {
			t.Fatal("an expired attachment accepted an upload completion")
		}
	}
}

// A SUCCESSOR SESSION MUST BE ABLE TO DOWNLOAD ITS STATION'S FILE.
//
// GrantDownload authorised with `a.recipientRow != ep.ID` — an ENDPOINT ROWID comparison —
// so a replacement session staffing the same station was refused an attachment its station
// legitimately owns. The offer message is delivered to the station's PARTY, so the successor
// polls it normally, sees the descriptor, calls comm_file_grant, and is told the attachment
// does not exist. Worst in exactly the case stations exist for: a takeover.
//
// It is the endpoint-versus-party mistake migration 0010 names explicitly, left standing in
// the one call that mints bytes. Found by a survey that was looking at something else.
func TestAReplacementSessionCanDownloadItsStationsAttachment(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, fileLimits())

	sender := stationEndpoint(t, st, "tok-send", "st-sender")
	first := stationEndpoint(t, st, "tok-1", "st-recv")
	code, err := st.MintPairingCode(ctx, 1, 42, "sender<->recv")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.JoinChannel(ctx, sender, code); err != nil {
		t.Fatal(err)
	}
	ch, err := st.JoinChannel(ctx, first, code)
	if err != nil {
		t.Fatal(err)
	}

	content := []byte("bytes for whoever is staffing st-recv")
	res, err := st.OfferFile(ctx, sender, ch.ChannelID, FileOffer{
		Name: "data.bin", SizeBytes: int64(len(content)), SHA256: shaOf(content),
		Transfer: "upload", Note: "for the station",
	})
	if err != nil {
		t.Fatal(err)
	}
	gi, err := st.ConsumeGrant(ctx, res.UploadGrant, "upload")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CompleteUpload(ctx, gi.AttachmentRow, int64(len(content))); err != nil {
		t.Fatal(err)
	}

	// CONTROL: the original recipient can download it, so a refusal below is about the
	// successor rather than about an attachment that was never grantable.
	msgs, err := st.Poll(ctx, first, 10)
	if err != nil || len(msgs) != 1 || msgs[0].File == nil {
		t.Fatalf("setup: the original recipient polled %d messages", len(msgs))
	}
	attID := msgs[0].File.AttachmentID
	if _, _, err := st.GrantDownload(ctx, first, attID); err != nil {
		t.Fatalf("the ORIGINAL recipient cannot download: %v", err)
	}

	// The predecessor goes away; a new session takes the same station.
	successor := stationEndpoint(t, st, "tok-2", "st-recv")
	if _, _, err := st.GrantDownload(ctx, successor, attID); err != nil {
		t.Fatalf("a replacement session on the same station cannot download its station's "+
			"attachment: %v.\nIt polled the offer legitimately and is then told the file does "+
			"not exist — the endpoint-versus-party mistake, in the one call that mints bytes.", err)
	}
}

// AND A STRANGER STILL LEARNS NOTHING. Widening from endpoint to party must not widen past
// the station — the refusal is ErrNotFound precisely so a non-recipient cannot confirm the
// id exists, and that property is the reason this check is written the way it is.
func TestAnUnrelatedStationStillCannotDownload(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, fileLimits())

	sender := stationEndpoint(t, st, "tok-send", "st-sender")
	recv := stationEndpoint(t, st, "tok-r", "st-recv")
	code, err := st.MintPairingCode(ctx, 1, 42, "sender<->recv")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.JoinChannel(ctx, sender, code); err != nil {
		t.Fatal(err)
	}
	ch, err := st.JoinChannel(ctx, recv, code)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("not for you")
	res, err := st.OfferFile(ctx, sender, ch.ChannelID, FileOffer{
		Name: "x.bin", SizeBytes: int64(len(content)), SHA256: shaOf(content), Transfer: "upload",
	})
	if err != nil {
		t.Fatal(err)
	}
	gi, _ := st.ConsumeGrant(ctx, res.UploadGrant, "upload")
	if _, err := st.CompleteUpload(ctx, gi.AttachmentRow, int64(len(content))); err != nil {
		t.Fatal(err)
	}
	msgs, _ := st.Poll(ctx, recv, 10)
	if len(msgs) != 1 || msgs[0].File == nil {
		t.Fatal("setup: recipient did not receive the offer")
	}

	outsider := stationEndpoint(t, st, "tok-x", "st-outsider")
	_, _, err = st.GrantDownload(ctx, outsider, msgs[0].File.AttachmentID)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("an unrelated station got %v, want ErrNotFound — anything else confirms the "+
			"attachment id exists to somebody who should not know", err)
	}
}

// THE PREDECESSOR IS REVOKED, WHICH IS THE ORDINARY REASON A SUCCESSOR EXISTS.
//
// The test above says "the predecessor goes away" and then does nothing to it — a death
// leaves the row untouched, so it exercises the mildest version of a takeover. An operator
// revoking a wedged endpoint is the case RevokeEndpoint's own comment describes ("revoked a
// wedged session precisely so someone else could take over"), and it was refused: the
// download re-check joined `endpoint` on the attachment's frozen recipient rowid, which is
// the predecessor's.
//
// The second offer matters as much as the first. OfferFile stamps recipient_endpoint with
// the seat rowid ChannelFor returns, and the seat never moves, so a file offered AFTER the
// revocation carried the same dead rowid — the denial belonged to the channel, not to one
// stale attachment, and re-offering could not clear it.
func TestARevokedPredecessorDoesNotStrandItsStationsFiles(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, fileLimits())

	sender := stationEndpoint(t, st, "tok-send", "st-sender")
	first := stationEndpoint(t, st, "tok-1", "st-recv")
	code, err := st.MintPairingCode(ctx, 1, 42, "sender<->recv")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.JoinChannel(ctx, sender, code); err != nil {
		t.Fatal(err)
	}
	ch, err := st.JoinChannel(ctx, first, code)
	if err != nil {
		t.Fatal(err)
	}

	offer := func(name string) string {
		t.Helper()
		content := []byte("bytes for whoever is staffing st-recv: " + name)
		res, err := st.OfferFile(ctx, sender, ch.ChannelID, FileOffer{
			Name: name, SizeBytes: int64(len(content)), SHA256: shaOf(content),
			Transfer: "upload", Note: "for the station",
		})
		if err != nil {
			t.Fatalf("offer %s: %v", name, err)
		}
		gi, err := st.ConsumeGrant(ctx, res.UploadGrant, "upload")
		if err != nil {
			t.Fatalf("consume %s: %v", name, err)
		}
		if _, err := st.CompleteUpload(ctx, gi.AttachmentRow, int64(len(content))); err != nil {
			t.Fatalf("complete %s: %v", name, err)
		}
		return res.Attachment.AttachmentID
	}

	before := offer("before-revocation.bin")

	// CONTROL: grantable while the predecessor is live, so a refusal below is about the
	// revocation and not about an attachment that was never grantable.
	if _, _, err := st.GrantDownload(ctx, first, before); err != nil {
		t.Fatalf("setup: the original recipient cannot download: %v", err)
	}

	if err := st.RevokeEndpoint(ctx, first.EndpointID); err != nil {
		t.Fatal(err)
	}
	successor := stationEndpoint(t, st, "tok-2", "st-recv")

	if _, _, err := st.GrantDownload(ctx, successor, before); err != nil {
		t.Errorf("a successor cannot download a file offered BEFORE its predecessor was "+
			"revoked: %v.\nThe channel is open and the file belongs to its station; the only "+
			"revoked thing is the connection that is gone.", err)
	}

	after := offer("after-revocation.bin")
	if _, _, err := st.GrantDownload(ctx, successor, after); err != nil {
		t.Errorf("a successor cannot download a file offered AFTER its predecessor was "+
			"revoked: %v.\nThis is the half that makes the defect permanent: the seat rowid "+
			"never moves, so every later offer is stamped with the dead endpoint and "+
			"re-offering cannot clear it.", err)
	}
}
