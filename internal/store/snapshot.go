package store

import (
	"context"
	"errors"
	"fmt"
	"os"

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
func (s *Store) Snapshot(ctx context.Context, dest string) error {
	if _, err := s.W.ExecContext(ctx, `VACUUM INTO ?`, dest); err != nil {
		return fmt.Errorf("vacuum into %s: %w", dest, err)
	}
	if err := os.Chmod(dest, 0o600); err != nil {
		return fmt.Errorf("securing snapshot %s: %w", dest, err)
	}
	return nil
}

// VerifySnapshot opens a database file and runs the backup's mandatory checks:
// PRAGMA integrity_check, foreign-key integrity, the FTS5 internal
// integrity-check on both indexes, a functional MATCH canary, embedding
// vector-length parity, and returns the entry count (for the caller to reconcile
// against the source). Opened read-write so the FTS integrity-check can run; a
// VACUUM-INTO snapshot is in rollback-journal mode, so no -wal/-shm persists.
func VerifySnapshot(ctx context.Context, path string) (int, error) {
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
