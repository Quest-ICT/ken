package store

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestSnapshotAndVerify(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	if _, err := st.Save(ctx, SaveInput{
		Kind:       "project",
		Content:    Content{Title: "Snapshot me", Summary: "a row to back up"},
		AuthorKind: "ai",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	dest := filepath.Join(t.TempDir(), "snap.db")
	if err := st.Snapshot(ctx, dest); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	n, err := VerifySnapshot(ctx, dest)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 entry in snapshot, got %d", n)
	}
}

// TestVerifySnapshotEmbeddingParity checks that VerifySnapshot passes with a
// well-formed embedding present and rejects one whose stored dim no longer
// matches the vector byte length (length(vec) != dim*4) — the parity guard.
func TestVerifySnapshotEmbeddingParity(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	if _, err := st.Save(ctx, SaveInput{
		Kind:       "project",
		Content:    Content{Title: "Embed me", Summary: "a vectorized row"},
		AuthorKind: "ai",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	targets, err := st.VersionsNeedingEmbedding(ctx, "test-model", 0)
	if err != nil || len(targets) == 0 {
		t.Fatalf("targets: err=%v n=%d", err, len(targets))
	}
	if err := st.UpsertEmbedding(ctx, targets[0].VersionID, "test-model", []float32{0.1, 0.2, 0.3, 0.4}); err != nil {
		t.Fatalf("embed: %v", err)
	}

	good := filepath.Join(t.TempDir(), "good.db")
	if err := st.Snapshot(ctx, good); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if _, err := VerifySnapshot(ctx, good); err != nil {
		t.Fatalf("verify healthy snapshot with embedding: %v", err)
	}

	// Corrupt the recorded dimension so length(vec) != dim*4.
	if _, err := st.W.ExecContext(ctx, `UPDATE entry_embedding SET dim = dim + 1`); err != nil {
		t.Fatalf("corrupt dim: %v", err)
	}
	bad := filepath.Join(t.TempDir(), "bad.db")
	if err := st.Snapshot(ctx, bad); err != nil {
		t.Fatalf("snapshot bad: %v", err)
	}
	if _, err := VerifySnapshot(ctx, bad); err == nil {
		t.Fatal("expected VerifySnapshot to reject an inconsistent vector length")
	}
}

// A snapshot is a byte-complete copy of the knowledge base, so its mode is part of
// writing it correctly — not something the caller may forget. The bug this pins: the
// mode used to come from the ambient umask (0644 under the usual 0022), so every
// hand-run `ken backup snapshot --out /tmp/…` documented in the runbooks produced a
// world-readable full dump in a 1777 directory.
func TestSnapshotIsOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	st := newStore(t)

	// A permissive umask must NOT be able to widen the result.
	old := syscall.Umask(0o000)
	defer syscall.Umask(old)

	dest := filepath.Join(dir, "snap.db")
	if err := st.Snapshot(context.Background(), dest); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Fatalf("snapshot mode = %04o, want 0600 (a full copy of the KB must be owner-only)", got)
	}
}
