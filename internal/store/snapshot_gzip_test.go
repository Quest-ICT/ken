package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A .gz snapshot must round-trip: written compressed, verified by reading the
// COMPRESSED artifact rather than the database that went into it.
//
// That distinction is the whole test. Verifying before compression would leave the
// compression unchecked, which is precisely where a silent truncation lives — gzip
// buffers, and the footer carrying the CRC32 and the length is only emitted on Close.
// A file whose Close error was dropped is the right sort of size, opens as a file, and
// fails its own checksum on the day someone restores it.
//
// Asked for by ken-prod-ops, who measured 68% off every artifact on the live
// deployment (4,521,984 raw vs 1,484,578 gzipped) and pointed out that nothing in the
// path compressed while age — which does not compress at all — made the archive
// undedupable as well.
func TestGzippedSnapshotRoundTrips(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if _, err := s.SeedDemo(ctx); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	gzPath := filepath.Join(dir, "snap.db.gz")
	if err := s.Snapshot(ctx, gzPath); err != nil {
		t.Fatalf("gzipped snapshot: %v", err)
	}

	// It is really gzip, by content and not merely by name.
	head, err := os.ReadFile(gzPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(head) < 2 || head[0] != 0x1f || head[1] != 0x8b {
		t.Fatalf("the artifact does not begin with the gzip magic — file(1) and a restorer would disagree about what it is")
	}

	// And it verifies THROUGH the compression.
	n, err := VerifySnapshot(ctx, gzPath)
	if err != nil {
		t.Fatalf("verifying the compressed artifact failed: %v", err)
	}
	if n <= 0 {
		t.Fatalf("verified %d entries — a snapshot of a seeded store should not be empty, so this passed for the wrong reason", n)
	}

	// The uncompressed intermediate must not survive. It is a full copy of the
	// knowledge base; leaving one beside every nightly would quietly undo the saving
	// and double the exposure.
	if _, err := os.Stat(gzPath + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("the intermediate %s.tmp still exists after a successful snapshot", gzPath)
	}
	// Nor may a verify leave its scratch behind.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".ken-verify-") {
			t.Errorf("verify left %s behind — a decompressed knowledge base in the backup directory", e.Name())
		}
	}
}

// Compression is decided by the extension, so a caller always gets the path it asked
// for. Two scripts pass a path and then operate on it (ken-snapshot.sh, install.sh);
// a snapshot that landed somewhere else would break both silently.
func TestSnapshotWithoutGzSuffixIsUncompressedAndUnchanged(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if _, err := s.SeedDemo(ctx); err != nil {
		t.Fatal(err)
	}

	plain := filepath.Join(t.TempDir(), "snap.db")
	if err := s.Snapshot(ctx, plain); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(plain)
	if err != nil {
		t.Fatalf("the snapshot is not at the path that was asked for: %v", err)
	}
	if len(b) > 2 && b[0] == 0x1f && b[1] == 0x8b {
		t.Fatal("a snapshot requested without .gz came back compressed — the caller's path no longer describes its contents")
	}
	if _, err := VerifySnapshot(ctx, plain); err != nil {
		t.Fatalf("the uncompressed path regressed: %v", err)
	}
}

// A snapshot renamed away from .gz must still verify, because detection is by content.
// This is the property that makes the artifact self-describing rather than
// self-labelling — a restorer who receives a file with no extension, or the wrong one,
// still gets a correct answer instead of a confusing failure.
func TestVerifyDetectsCompressionByContentNotByName(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if _, err := s.SeedDemo(ctx); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	gzPath := filepath.Join(dir, "snap.db.gz")
	if err := s.Snapshot(ctx, gzPath); err != nil {
		t.Fatal(err)
	}
	misnamed := filepath.Join(dir, "handed-over-without-an-extension")
	if err := os.Rename(gzPath, misnamed); err != nil {
		t.Fatal(err)
	}

	if _, err := VerifySnapshot(ctx, misnamed); err != nil {
		t.Fatalf("a compressed snapshot with no .gz in its name failed to verify: %v", err)
	}
}

// A truncated artifact must FAIL. Without this the round-trip test above proves only
// that the happy path works, and the reason gzip's Close error is checked at all is
// that its absence produces a file which passes every casual inspection.
func TestATruncatedSnapshotFailsVerification(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if _, err := s.SeedDemo(ctx); err != nil {
		t.Fatal(err)
	}

	gzPath := filepath.Join(t.TempDir(), "snap.db.gz")
	if err := s.Snapshot(ctx, gzPath); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(gzPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) < 64 {
		t.Fatalf("artifact is %d bytes — too small to truncate meaningfully", len(b))
	}
	// Drop the tail, which is where the CRC and length live.
	if err := os.WriteFile(gzPath, b[:len(b)-32], 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := VerifySnapshot(ctx, gzPath); err == nil {
		t.Fatal("a truncated snapshot verified clean — the check cannot see the failure it exists to catch")
	}
}
