// Package store owns Ken's embedded SQLite database: schema migrations plus the
// search/get queries. Writes serialize through a single-writer pool; reads use a
// separate pool. WAL lets readers run concurrently with the writer.
package store

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/ncruces/go-sqlite3/driver"
	"github.com/ncruces/go-sqlite3/ext/fts5" // FTS5 is a per-connection extension in ncruces, not in the default WASM

	"github.com/Quest-ICT/ken/internal/lang"
)

// Store holds the writer and reader connection pools over one SQLite file.
type Store struct {
	W *sql.DB // single-writer pool (MaxOpenConns == 1) — serializes all writes
	R *sql.DB // reader pool

	// detect auto-tags each new version's content_lang (curation-language
	// guardrail). Set to the real detector by Open; SetDetector overrides it (tests).
	// Never nil after Open — a nil detector would panic detectLang, so callers that
	// build a Store literal directly must set it.
	detect lang.Detector
}

// detectLang returns the detected primary-subtag of the concatenated prose, or
// lang.Und when the detector is unset or undecided. Detection runs over PROSE
// only (never code/triggers/tags — those are language-neutral retrieval keys).
func (s *Store) detectLang(prose ...string) string {
	if s.detect == nil {
		return lang.Und
	}
	return s.detect.Detect(strings.Join(prose, "\n"))
}

// commonPragmas are applied to every connection via the DSN. Order matters:
// busy timeout and locking mode should come first (see the ncruces driver docs).
const commonPragmas = "_pragma=busy_timeout(10000)&_pragma=journal_mode(wal)&_pragma=synchronous(normal)&_pragma=foreign_keys(on)"

// Open opens (creating if needed) the SQLite database at path.
func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?" + commonPragmas
	// The writer uses BEGIN IMMEDIATE (grab the write lock up front) to avoid the
	// "upgrade mid-transaction -> deadlock despite busy_timeout" trap.
	w, err := driver.Open(dsn+"&_txlock=immediate", fts5.Register)
	if err != nil {
		return nil, fmt.Errorf("open writer: %w", err)
	}
	w.SetMaxOpenConns(1)

	r, err := driver.Open(dsn, fts5.Register)
	if err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("open reader: %w", err)
	}
	r.SetMaxOpenConns(4)
	r.SetMaxIdleConns(4)

	return &Store{W: w, R: r, detect: lang.New()}, nil
}

// Close closes both connection pools.
func (s *Store) Close() error {
	e1 := s.W.Close()
	if e2 := s.R.Close(); e2 != nil && e1 == nil {
		e1 = e2
	}
	return e1
}
