package commserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/Quest-ICT/ken/internal/comm"
)

// fileFixture is everything the relay tests need: a KB store for auth, a comm
// store with files enabled, two paired endpoints, and the handler mounted the
// way main.go mounts it.
type fileFixture struct {
	deps    Deps
	srv     *httptest.Server
	cs      *comm.Store
	a, b    *comm.Endpoint
	channel string
	tok     string // comm,comm-file scoped token — the endpoints' owner
	commTok string // comm-only token, for the scope-refusal case
}

func newFileFixture(t *testing.T) *fileFixture {
	t.Helper()
	ctx := context.Background()
	kb := newKB(t)

	tok := mintToken(t, kb, "file-agent", "comm", "comm-file")
	commTok := mintToken(t, kb, "msg-agent", "comm")

	l := comm.DefaultLimits()
	l.FilesEnabled = true
	cs, err := comm.Open(filepath.Join(t.TempDir(), "comm.db"), l)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	if err := cs.Migrate(); err != nil {
		t.Fatal(err)
	}

	// Pair two endpoints owned by the file-agent token, directly at the store.
	p, err := authenticate(ctx, kb, tok, ScopeComm)
	if err != nil {
		t.Fatal(err)
	}
	owner := comm.Owner{TokenID: p.TokenID, ActorID: p.ActorID, SpaceID: p.SpaceID}
	a, _, err := cs.RegisterEndpoint(ctx, owner, "sender", "")
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := cs.RegisterEndpoint(ctx, owner, "receiver", "")
	if err != nil {
		t.Fatal(err)
	}
	code, err := cs.MintPairingCode(ctx, 1, 1, "t")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cs.JoinChannel(ctx, a, code); err != nil {
		t.Fatal(err)
	}
	ch, err := cs.JoinChannel(ctx, b, code)
	if err != nil {
		t.Fatal(err)
	}

	deps := Deps{Comm: cs, Store: kb}
	h := NewHTTPHandler(deps)
	mux := http.NewServeMux()
	mux.Handle("/comm/files/", NewFileHandler(deps, h))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return &fileFixture{deps: deps, srv: srv, cs: cs, a: a, b: b, channel: ch.ChannelID, tok: tok, commTok: commTok}
}

func (f *fileFixture) do(t *testing.T, method, urlPath, bearer string, body []byte) *http.Response {
	t.Helper()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, f.srv.URL+urlPath, rd)
	if err != nil {
		t.Fatal(err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func hexSHA(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// The full relay round trip, exactly as the agents would drive it: offer →
// PUT → poll shows the file → grant → GET → bytes and headers verified.
func TestFileRelayRoundTrip(t *testing.T) {
	ctx := context.Background()
	f := newFileFixture(t)
	content := []byte("the payload bytes: \x00\x01\x02 binary is fine")

	res, err := f.cs.OfferFile(ctx, f.a, comm.FileAddr{ChannelID: f.channel}, comm.FileOffer{
		Name: "handout.md", SizeBytes: int64(len(content)), SHA256: hexSHA(content),
		Transfer: "upload", Note: "over to you",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Upload with the wrong token first: the grant alone must not be enough.
	resp := f.do(t, "PUT", "/comm/files/"+res.UploadGrant, f.commTok, content)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("comm-only token uploaded: HTTP %d", resp.StatusCode)
	}
	// The failed attempt consumed nothing server-side? It DID consume the grant?
	// No: scope refusal happens before grant resolution, so the grant survives.
	resp = f.do(t, "PUT", "/comm/files/"+res.UploadGrant, f.tok, content)
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("upload: HTTP %d: %s", resp.StatusCode, b)
	}

	// Grant reuse must 404.
	resp = f.do(t, "PUT", "/comm/files/"+res.UploadGrant, f.tok, content)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("grant reuse: HTTP %d, want 404", resp.StatusCode)
	}

	// The receiver polls the offer.
	msgs, err := f.cs.Poll(ctx, f.b, 10)
	if err != nil || len(msgs) != 1 || msgs[0].File == nil {
		t.Fatalf("poll after upload: %v %+v", err, msgs)
	}
	if msgs[0].File.SHA256 != hexSHA(content) {
		t.Fatalf("descriptor sha mismatch")
	}

	// Download via a fresh grant.
	g, _, err := f.cs.GrantDownload(ctx, f.b, msgs[0].File.AttachmentID)
	if err != nil {
		t.Fatal(err)
	}
	resp = f.do(t, "GET", "/comm/files/"+g, f.tok, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("download: HTTP %d", resp.StatusCode)
	}
	got, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(got, content) {
		t.Fatalf("downloaded bytes differ: %d vs %d", len(got), len(content))
	}
	// The mandatory serving posture.
	if ct := resp.Header.Get("Content-Type"); ct != "application/octet-stream" {
		t.Fatalf("Content-Type = %q", ct)
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("missing nosniff")
	}
	if cd := resp.Header.Get("Content-Disposition"); cd != `attachment; filename="handout.md"` {
		t.Fatalf("Content-Disposition = %q", cd)
	}
}

// Corrupt bytes are refused, the attachment fails, and nothing is delivered.
func TestUploadRejectsChecksumMismatch(t *testing.T) {
	ctx := context.Background()
	f := newFileFixture(t)
	content := []byte("declared-content")

	res, err := f.cs.OfferFile(ctx, f.a, comm.FileAddr{ChannelID: f.channel}, comm.FileOffer{
		Name: "x.bin", SizeBytes: int64(len(content)), SHA256: hexSHA(content), Transfer: "upload",
	})
	if err != nil {
		t.Fatal(err)
	}
	corrupt := []byte("corrupted-conten") // same length, different bytes
	resp := f.do(t, "PUT", "/comm/files/"+res.UploadGrant, f.tok, corrupt)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("corrupt upload: HTTP %d, want 422", resp.StatusCode)
	}
	if msgs, _ := f.cs.Poll(ctx, f.b, 10); len(msgs) != 0 {
		t.Fatalf("corrupt upload delivered %d messages", len(msgs))
	}
}

// A stream that does not match the declared size is refused in both directions.
func TestUploadRejectsSizeMismatch(t *testing.T) {
	ctx := context.Background()
	f := newFileFixture(t)
	content := []byte("exactly-16-bytes")

	for _, c := range [][]byte{content[:10], append(append([]byte{}, content...), 'x')} {
		res, err := f.cs.OfferFile(ctx, f.a, comm.FileAddr{ChannelID: f.channel}, comm.FileOffer{
			Name: "s.bin", SizeBytes: int64(len(content)), SHA256: hexSHA(content), Transfer: "upload",
		})
		if err != nil {
			t.Fatal(err)
		}
		resp := f.do(t, "PUT", "/comm/files/"+res.UploadGrant, f.tok, c)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("size-mismatch upload (%d bytes): HTTP %d, want 400", len(c), resp.StatusCode)
		}
	}
}

// The live kill switch stops bytes without a restart, even for existing grants.
func TestFilesDisabledIsALiveKillSwitch(t *testing.T) {
	ctx := context.Background()
	f := newFileFixture(t)
	content := []byte("z")

	res, err := f.cs.OfferFile(ctx, f.a, comm.FileAddr{ChannelID: f.channel}, comm.FileOffer{
		Name: "z", SizeBytes: 1, SHA256: hexSHA(content), Transfer: "upload",
	})
	if err != nil {
		t.Fatal(err)
	}
	l := f.cs.Limits()
	l.FilesEnabled = false
	f.cs.SetLimits(l)

	resp := f.do(t, "PUT", "/comm/files/"+res.UploadGrant, f.tok, content)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("disabled relay accepted bytes: HTTP %d", resp.StatusCode)
	}
	// And the store-level surface refuses new offers too.
	if _, err := f.cs.OfferFile(ctx, f.a, comm.FileAddr{ChannelID: f.channel}, comm.FileOffer{
		Name: "z2", SizeBytes: 1, SHA256: hexSHA(content), Transfer: "upload",
	}); err == nil {
		t.Fatal("disabled file exchange accepted an offer")
	}
}

// No token, garbage grants, and path probing all fail closed.
func TestRelayFailsClosed(t *testing.T) {
	f := newFileFixture(t)

	if resp := f.do(t, "PUT", "/comm/files/whatever", "", []byte("x")); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: HTTP %d", resp.StatusCode)
	}
	if resp := f.do(t, "GET", "/comm/files/nosuchgrant", f.tok, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("bad grant: HTTP %d", resp.StatusCode)
	}
	if resp := f.do(t, "GET", "/comm/files/", f.tok, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("empty grant: HTTP %d", resp.StatusCode)
	}
	if resp := f.do(t, "POST", "/comm/files/whatever", f.tok, []byte("x")); resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST: HTTP %d", resp.StatusCode)
	}
}

// viewOf is the cross-layer copy between the store's message and the tool
// result. This regression exists because the file descriptor once went missing
// exactly here: the store carried it, the view dropped it, unit tests on either
// side stayed green, and only driving the real binary showed a message with no
// file. Every field the store exposes must survive the copy.
func TestViewOfCarriesTheFileDescriptor(t *testing.T) {
	m := &comm.Message{
		MessageID: "m1", ChannelID: "c1", Seq: 7, SenderEndpointID: "e1",
		Body: "note", RequiresResponse: true, ReplyToMessageID: "m0",
		DeliveryCount: 2, CreatedAt: "t0", ReplyDeadlineAt: "t1",
		File: &comm.FileInfo{
			AttachmentID: "a1", Name: "f.bin", SizeBytes: 42,
			SHA256: "aa", Transfer: "upload", NonceSHA256: "bb",
		},
	}
	v := viewOf(m)
	if v.File == nil {
		t.Fatal("file descriptor dropped in the store->view copy")
	}
	if v.File.AttachmentID != "a1" || v.File.Name != "f.bin" || v.File.SizeBytes != 42 ||
		v.File.SHA256 != "aa" || v.File.Transfer != "upload" || v.File.NonceSHA256 != "bb" {
		t.Fatalf("descriptor mangled: %+v", v.File)
	}
	if !v.Redelivered || v.MessageID != "m1" || v.Seq != 7 || v.ReplyTo != "m0" {
		t.Fatalf("envelope mangled: %+v", v)
	}
	if plain := viewOf(&comm.Message{MessageID: "m2"}); plain.File != nil {
		t.Fatal("a plain message grew a file descriptor")
	}
}
