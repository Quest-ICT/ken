package commserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/Quest-ICT/ken/internal/comm"
)

// The byte-relay HTTP surface: PUT and GET /comm/files/{grant}.
//
// This exists because payload bytes must never travel as tool-call tokens (C8):
// tool arguments are generated token by token by a model, so the model mints a
// one-time grant over MCP and then drives the actual bytes with a shell tool —
// curl does resumable HTTP correctly, and the tokens spent are one command line.
//
// Two credentials are required on every request, deliberately: the bearer token
// (which must carry `comm-file` and must OWN the endpoint the grant was minted
// for) and the grant itself (single-use, minutes-lived, bound to one attachment
// and one direction). A leaked URL is useless without the token; a leaked token
// cannot touch bytes it was never granted. This is what "no HTTP path serves an
// attachment without authentication" means in practice.
type FileHandler struct {
	d Deps
	// notify wakes the recipient's parked long-poll when an upload completes and
	// its message becomes deliverable.
	notify func(endpointRow int64)
}

// NewFileHandler builds the relay surface. h supplies the long-poll wakeup.
func NewFileHandler(d Deps, h *Handler) *FileHandler {
	return &FileHandler{d: d, notify: func(id int64) { h.w.notify(id) }}
}

func (f *FileHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// No CORS, same as the comm MCP endpoint: there is no browser client, so
	// permissive headers would be pure attack surface.
	grant := strings.TrimPrefix(r.URL.Path, "/comm/files/")
	if grant == "" || strings.ContainsRune(grant, '/') {
		http.NotFound(w, r)
		return
	}

	tok := bearerToken(r)
	if tok == "" {
		authFail(w, f.d.Metrics, "missing bearer token")
		return
	}
	p, err := authenticate(r.Context(), f.d.Store, tok, ScopeCommFile)
	if err != nil {
		authFail(w, f.d.Metrics, "invalid token")
		return
	}
	if !f.d.Comm.Limits().FilesEnabled {
		// Live kill switch: grants may exist, bytes stop moving.
		httpError(w, http.StatusForbidden, "file exchange is disabled")
		return
	}

	switch r.Method {
	case http.MethodPut:
		f.upload(w, r, p, grant)
	case http.MethodGet:
		f.download(w, r, p, grant)
	default:
		httpError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// upload streams the sender's bytes against an upload grant. The message the
// receiver will poll is enqueued only after the streamed checksum matches the
// offer, so partial or corrupt state is never observable.
func (f *FileHandler) upload(w http.ResponseWriter, r *http.Request, p *principal, grant string) {
	ctx := r.Context()
	gi, err := f.d.Comm.ConsumeGrant(ctx, grant, "upload")
	if err != nil {
		// Unknown, expired, consumed and wrong-kind are indistinguishable (404):
		// a probing caller learns nothing about which grants exist.
		http.NotFound(w, r)
		return
	}
	if gi.EndpointToken != p.TokenID {
		httpError(w, http.StatusForbidden, "grant does not belong to this token")
		return
	}
	if gi.State != "offered" {
		httpError(w, http.StatusConflict, "attachment is not awaiting an upload")
		return
	}
	// Bytes-level quota re-check: the offer-time check bounded the grant; this
	// bounds the disk at the moment the bytes actually arrive.
	if err := f.d.Comm.CheckFileQuota(ctx, gi.SizeBytes); err != nil {
		f.failUpload(ctx, gi, w, http.StatusInsufficientStorage, "storage quota exceeded — retry later or re-offer smaller")
		return
	}
	if err := f.d.Comm.EnsureFilesDir(); err != nil {
		log.Printf("comm: files dir: %v", err)
		f.failUpload(ctx, gi, w, http.StatusInternalServerError, "internal error")
		return
	}

	part := f.d.Comm.PartPath(gi.AttachmentID)
	dst, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		// A live .part means another PUT for this attachment is already streaming.
		// Refuse THIS request, and deliberately do NOT fail the attachment: the
		// other upload is the winner and may be nearly complete, so marking the
		// attachment failed here would destroy a good transfer because a duplicate
		// arrived. The loser just retries with a fresh grant.
		httpError(w, http.StatusConflict, "an upload for this attachment is already in progress")
		return
	}

	n, sum, copyErr := hashCopy(dst, io.LimitReader(r.Body, gi.SizeBytes+1))
	closeErr := dst.Close()

	switch {
	case copyErr != nil || closeErr != nil:
		_ = os.Remove(part)
		f.failUpload(ctx, gi, w, http.StatusBadRequest, "upload stream failed — re-offer to retry")
		return
	case n != gi.SizeBytes:
		// Short or over-long both fail: the declared size is part of the offer's
		// contract, and the +1 headroom above exists purely to detect the overrun.
		_ = os.Remove(part)
		f.failUpload(ctx, gi, w, http.StatusBadRequest, "uploaded size does not match the declared size_bytes — re-offer to retry")
		return
	case sum != gi.SHA256:
		_ = os.Remove(part)
		f.failUpload(ctx, gi, w, http.StatusUnprocessableEntity, "uploaded sha256 does not match the offer — re-offer to retry")
		return
	}

	if err := os.Rename(part, f.d.Comm.FilePath(gi.AttachmentID)); err != nil {
		log.Printf("comm: finalize upload: %v", err)
		_ = os.Remove(part)
		f.failUpload(ctx, gi, w, http.StatusInternalServerError, "internal error")
		return
	}
	msg, err := f.d.Comm.CompleteUpload(context.Background(), gi.AttachmentRow, n)
	if err != nil {
		// The bytes are on disk and verified; only the envelope failed. Keep them:
		// the attachment stays 'offered' and its TTL still governs, so the sender can
		// retry completion rather than re-uploading a large file because the peer's
		// channel was momentarily full. Discarding verified bytes here was the
		// original behaviour and it made backpressure destructive.
		log.Printf("comm: complete upload: %v", err)
		code, text := http.StatusInternalServerError, "internal error"
		if errors.Is(err, comm.ErrBackpressure) {
			code, text = http.StatusConflict,
				"the peer has too many unacknowledged messages — the uploaded bytes are kept; retry the offer once they catch up"
		}
		httpError(w, code, text)
		return
	}
	// WAKE THE PARTY, NOT THE STAMPED ROWID. `recipient` is the attachment's frozen
	// recipient_endpoint, and the offer→completion interval is exactly when the receiver
	// has been told to sit in a long poll — so a successor waited it out in full.
	if targets, wErr := f.d.Comm.WakeTargetsFor(r.Context(), msg.MessageID); wErr != nil {
		log.Printf("comm: wake targets for %s: %v", msg.MessageID, wErr)
	} else {
		for _, id := range targets {
			f.notify(id)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"attachment_id": gi.AttachmentID,
		"message_id":    msg.MessageID,
		"size_bytes":    n,
	})
}

// download streams stored bytes against a download grant.
func (f *FileHandler) download(w http.ResponseWriter, r *http.Request, p *principal, grant string) {
	gi, err := f.d.Comm.ConsumeGrant(r.Context(), grant, "download")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if gi.EndpointToken != p.TokenID {
		httpError(w, http.StatusForbidden, "grant does not belong to this token")
		return
	}
	if gi.State != "ready" {
		httpError(w, http.StatusConflict, "attachment has no downloadable bytes")
		return
	}
	src, err := os.Open(f.d.Comm.FilePath(gi.AttachmentID))
	if err != nil {
		log.Printf("comm: open attachment: %v", err)
		httpError(w, http.StatusNotFound, "attachment bytes are gone — ask the sender to re-offer")
		return
	}
	defer src.Close()

	h := w.Header()
	// The mandatory serving posture for relayed files: never sniffed, never
	// rendered, always a download, size known up front.
	h.Set("Content-Type", "application/octet-stream")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Disposition", `attachment; filename="`+sanitizeFilename(gi.Name)+`"`)
	h.Set("Content-Length", strconv.FormatInt(gi.SizeBytes, 10))
	_, _ = io.Copy(w, src)
}

// failUpload marks the attachment failed and writes the error. Failing (rather
// than leaving 'offered') keeps the concurrent-upload slot from leaking; the
// sender's recovery is always a fresh offer.
//
// The DB write runs under a fresh context, not the request's: the common way to
// arrive here is a client that vanished mid-stream, whose context is already
// cancelled — and the state transition must land precisely then, or the
// attachment stays 'offered' and holds its concurrent-upload slot forever.
func (f *FileHandler) failUpload(_ context.Context, gi *comm.GrantInfo, w http.ResponseWriter, code int, msg string) {
	if err := f.d.Comm.FailUpload(context.Background(), gi.AttachmentRow); err != nil {
		log.Printf("comm: fail upload: %v", err)
	}
	httpError(w, code, msg)
}

// hashCopy copies src to dst, returning bytes written and the hex sha256.
// hash.Hash is itself an io.Writer, so the tee needs no wrapper.
func hashCopy(dst io.Writer, src io.Reader) (int64, string, error) {
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(dst, h), src)
	return n, hex.EncodeToString(h.Sum(nil)), err
}

// sanitizeFilename bounds a name for a Content-Disposition header. Names were
// already validated at offer time; this only strips the quote/control characters
// a header cannot carry.
func sanitizeFilename(name string) string {
	var b strings.Builder
	for i := 0; i < len(name) && b.Len() < 200; i++ {
		c := name[i]
		if c == '"' || c == '\\' || c < 0x20 || c == 0x7f {
			continue
		}
		b.WriteByte(c)
	}
	if b.Len() == 0 {
		return "attachment"
	}
	return b.String()
}
