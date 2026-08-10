package store

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ncruces/go-sqlite3/driver"
	"github.com/ncruces/go-sqlite3/ext/fts5"
)

// Snapshot writes a consistent copy of the database to dest via VACUUM INTO —
// safe on a live WAL database (no torn file). dest must not already exist.
//
// The result is chmod'd 0600 HERE, not left to the caller. A snapshot is a
// byte-complete copy of the knowledge base — every entry, the full curation history,
// curator accounts and token records — so its mode is part of writing it correctly,
// not a courtesy the caller may forget. SQLite creates the file at the process umask
// (0644 under the usual 0022), and callers that only *document* a safe umask leave
// every hand-run `ken backup snapshot --out …` world-readable: the shell wrappers
// cannot protect an operator following the runbook by hand.
//
// The umask is also narrowed for the duration of the write (see the CLI), so the file
// is 0600 from creation rather than only once VACUUM INTO returns; this chmod is the
// backstop that holds no matter which caller invoked us.
// A dest ending in ".gz" is written GZIPPED, and that is decided by the extension
// rather than by a flag so a caller always gets the path it asked for. Two scripts pass
// a path and then operate on it (scripts/ken-snapshot.sh, scripts/install.sh); a
// snapshot that silently landed somewhere else would break both.
//
// Measured on the live deployment by ken-prod-ops: 4,521,984 bytes raw against
// 1,484,578 gzipped — 68% off every artifact, every night. Nothing in this path
// compressed before, and age does not: ciphertext is incompressible, which is also why
// the archive could never dedupe. The nightly pull over their tunnel drops from about
// 4.4 MB to 1.5 MB, which matters more than the disk does on a single-path link.
func (s *Store) Snapshot(ctx context.Context, dest string) error {
	if !strings.HasSuffix(dest, ".gz") {
		return s.snapshotRaw(ctx, dest)
	}

	// VACUUM INTO needs a real file — it cannot write to a pipe — so the uncompressed
	// database exists briefly. It is written BESIDE the destination rather than in
	// os.TempDir(): the backup directory is the one the operator has already secured,
	// and /tmp is 1777. Same filesystem also means no cross-device copy.
	tmp := dest + ".tmp"
	_ = os.Remove(tmp) // VACUUM INTO refuses an existing file; a previous crash may have left one
	defer os.Remove(tmp)

	if err := s.snapshotRaw(ctx, tmp); err != nil {
		return err
	}
	if err := gzipFile(tmp, dest); err != nil {
		return fmt.Errorf("compressing snapshot %s: %w", dest, err)
	}
	return nil
}

// snapshotRaw is the original behaviour: VACUUM INTO, then secure.
func (s *Store) snapshotRaw(ctx context.Context, dest string) error {
	if _, err := s.W.ExecContext(ctx, `VACUUM INTO ?`, dest); err != nil {
		return fmt.Errorf("vacuum into %s: %w", dest, err)
	}
	if err := os.Chmod(dest, 0o600); err != nil {
		return fmt.Errorf("securing snapshot %s: %w", dest, err)
	}
	return nil
}

// gzipFile compresses src to dst at 0600.
//
// THE Close() ERROR IS CHECKED AND THAT IS NOT PEDANTRY. gzip.Writer buffers, and the
// footer — including the CRC32 and the length — is only emitted by Close. Ignoring it
// yields a file that looks complete, is the right sort of size, and fails its own
// checksum on the day someone needs it. Same family as a serializer that closes the
// stream it was handed: the failure is invisible at write time and total at read time.
func gzipFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	zw, err := gzip.NewWriterLevel(out, gzip.BestCompression)
	if err != nil {
		out.Close()
		return err
	}
	if _, err := io.Copy(zw, in); err != nil {
		zw.Close()
		out.Close()
		return err
	}
	if err := zw.Close(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// gzipMagic is the two-byte header every gzip stream starts with.
//
// Detection is by CONTENT, not by extension, so a snapshot that was renamed — or handed
// over without one — still verifies. The extension is for the human and file(1); the
// magic is for the tool. ken-prod-ops asked for both, on the grounds that a snapshot
// whose format you have to infer from the release that wrote it is a bad thing to meet
// during a restore.
var gzipMagic = []byte{0x1f, 0x8b}

// isGzip reports whether a file begins with the gzip magic.
func isGzip(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	var head [2]byte
	n, err := io.ReadFull(f, head[:])
	if err != nil && n < 2 {
		// Too short to be a gzip stream; let the caller's real open report why.
		return false, nil
	}
	return bytes.Equal(head[:], gzipMagic), nil
}

// gunzipToTemp decompresses path into a sibling temp file and returns its name. The
// caller must remove it.
func gunzipToTemp(path string) (string, error) {
	in, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer in.Close()
	zr, err := gzip.NewReader(in)
	if err != nil {
		return "", fmt.Errorf("reading gzip %s: %w", path, err)
	}
	defer zr.Close()

	out, err := os.CreateTemp(filepath.Dir(path), ".ken-verify-*.db")
	if err != nil {
		return "", err
	}
	name := out.Name()
	if err := os.Chmod(name, 0o600); err != nil {
		out.Close()
		os.Remove(name)
		return "", err
	}
	if _, err := io.Copy(out, zr); err != nil {
		out.Close()
		os.Remove(name)
		return "", fmt.Errorf("decompressing %s: %w", path, err)
	}
	if err := out.Close(); err != nil {
		os.Remove(name)
		return "", err
	}
	return name, nil
}

// VerifySnapshot opens a database file and runs the backup's mandatory checks:
// PRAGMA integrity_check, foreign-key integrity, the FTS5 internal
// integrity-check on both indexes, a functional MATCH canary, embedding
// vector-length parity, and returns the entry count (for the caller to reconcile
// against the source). Opened read-write so the FTS integrity-check can run; a
// VACUUM-INTO snapshot is in rollback-journal mode, so no -wal/-shm persists.
func VerifySnapshot(ctx context.Context, path string) (int, error) {
	// A gzipped snapshot is decompressed to a sibling temp first. SQLite needs a real
	// file, so there is no streaming shortcut — and verifying the COMPRESSED artifact
	// rather than the database that went into it is the point: it proves the thing that
	// will actually be restored, including that the gzip footer is intact. Verifying
	// before compression would leave the compression itself unchecked, which is exactly
	// where a silent truncation lives.
	if gz, err := isGzip(path); err != nil {
		return 0, err
	} else if gz {
		plain, err := gunzipToTemp(path)
		if err != nil {
			return 0, err
		}
		defer os.Remove(plain)
		path = plain
	}
	db, err := driver.Open("file:"+path+"?_pragma=busy_timeout(5000)", fts5.Register)
	if err != nil {
		return 0, err
	}
	defer db.Close()

	var res string
	if err := db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&res); err != nil {
		return 0, err
	}
	if res != "ok" {
		return 0, fmt.Errorf("integrity_check failed: %s", res)
	}

	// Foreign-key integrity: any row means a broken reference.
	fkRows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return 0, err
	}
	viol := fkRows.Next()
	_ = fkRows.Close()
	if viol {
		return 0, errors.New("foreign key violations found in snapshot")
	}

	// FTS5 internal integrity-check (raises on a corrupt index).
	for _, ft := range []string{"entry_fts", "entry_code_fts"} {
		if _, err := db.ExecContext(ctx, fmt.Sprintf("INSERT INTO %s(%s) VALUES('integrity-check')", ft, ft)); err != nil {
			return 0, fmt.Errorf("%s integrity-check failed: %w", ft, err)
		}
	}
	// Functional MATCH canary — proves FTS is queryable.
	var canary int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM entry_fts WHERE entry_fts MATCH '"zzznomatchzzz"'`).Scan(&canary); err != nil {
		return 0, fmt.Errorf("FTS match canary failed: %w", err)
	}

	// Embedding parity: every stored vector's byte length must equal dim*4.
	var badVec int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM entry_embedding WHERE length(vec) != dim*4`).Scan(&badVec); err != nil {
		return 0, err
	}
	if badVec > 0 {
		return 0, fmt.Errorf("%d embedding(s) with an inconsistent vector length", badVec)
	}

	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM entry`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}
