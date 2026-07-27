// Package comm owns the inter-session communication subsystem: authenticated
// message passing between AI sessions on the same or different machines
// (design decision D9; full contract in docs/COMM.md).
//
// It is OPT-IN and OFF BY DEFAULT. Nothing in this package runs, and no table is
// created, unless the operator enables COMM — a default Ken install stays exactly
// the curated knowledge base it advertises.
//
// # Boundaries
//
// This package owns its OWN SQLite file (data/comm/comm.db) and never touches
// ken.db. That separation is the point: message traffic is high-churn and
// EXPENDABLE, knowledge is low-churn and DURABLE, so keeping the files apart
// keeps ephemeral WAL churn out of the replicated database and out of the KB's
// single writer. Losing this file costs an in-flight conversation, never
// knowledge, and it is deliberately outside both backup tiers.
//
// Ownership columns (actor, space, token) identify rows in ken.db and are plain
// values here — SQLite foreign keys cannot span database files, so the CALLER
// (which holds both handles) is responsible for supplying identities it has
// already authenticated. See migrations/0001_init.sql.
//
// # What this package does not do
//
// It does not decide whether a receiving session should act on a message. COMM
// authenticates WHO may talk to whom — structurally, via a human-minted pairing
// code — but the handling of message content is the receiving harness's
// responsibility, and instruction text is not a control. docs/COMM.md §8 states
// that boundary rather than implying a guarantee this code cannot make.
package comm

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/ncruces/go-sqlite3/driver"
	"github.com/ncruces/go-sqlite3/ext/fts5"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Sentinel errors. Callers map these to their own surface (an MCP tool error, an
// HTTP status); they are matched with errors.Is, never by string.
var (
	// ErrNotFound covers an unknown endpoint, channel, or pairing code.
	ErrNotFound = errors.New("not found")
	// ErrDenied means authenticated but not entitled: a wrong endpoint secret, a
	// revoked endpoint, or an endpoint acting on a channel it does not belong to.
	ErrDenied = errors.New("denied")
	// ErrBackpressure means the channel's un-acked depth is at its cap. Callers
	// must surface this as "stop and wait", never as a retryable transport error:
	// full-duplex has no turn-taking, so two auto-processing sessions can
	// otherwise enter a reply loop that grows the database without bound.
	ErrBackpressure = errors.New("channel backpressure: too many unacknowledged messages")
	// ErrTooLarge means the body exceeds the configured cap.
	ErrTooLarge = errors.New("message body too large")
	// ErrChannelClosed means the channel is not open (still pending a second join,
	// or revoked).
	ErrChannelClosed = errors.New("channel is not open")
)

// Limits are the operator-tunable bounds enforced by this package. They are
// enforced in SQL inside the writing transaction rather than by keying the shared
// rate-limiter bucket: that bucket fails OPEN when saturated, which is correct for
// IP and token keys (an attacker cannot mint those cheaply) and wrong for
// identifiers a caller can create in a loop. A fail-open quota is a disk-full
// outage; a refused message is a retry.
type Limits struct {
	// MaxBodyBytes caps one message body. Kept modest on purpose: tool arguments
	// are generated token-by-token by a model, so even a 64 KiB message is a
	// five-figure output-token count.
	MaxBodyBytes int
	// MaxUnackedPerChannel caps un-acked depth per channel (backpressure).
	MaxUnackedPerChannel int
	// MessageTTLSeconds is how long an un-acked message survives before expiring.
	MessageTTLSeconds int
	// MetadataTTLSeconds is how long a settled (acked/expired) message's metadata
	// row survives after creation. Bodies are dropped at ack; this governs only
	// the audit shell.
	MetadataTTLSeconds int
	// ReplyDeadlineSeconds is the default deadline applied to a message that
	// requires a response.
	ReplyDeadlineSeconds int
	// PairingCodeTTLSeconds is how long a human-minted pairing code stays valid.
	PairingCodeTTLSeconds int

	// FilesEnabled gates file exchange INDEPENDENTLY of COMM itself, and defaults
	// off: the relay is the bulk of the subsystem's risk (disk, quotas, orphan
	// sweeping), so an operator opts into it separately. Live-togglable — checked
	// per operation, so it doubles as a kill switch.
	FilesEnabled bool
	// FileMaxBytes caps one attachment.
	FileMaxBytes int64
	// FileTTLSeconds bounds how long an offered/undelivered attachment survives.
	FileTTLSeconds int
	// FileBudgetBytes is the GLOBAL cap on bytes held in the relay at once. This is
	// the rule that makes C1's isolation honest: the relay shares a volume with the
	// knowledge base, and filling it would fail durable KB writes over chat traffic.
	FileBudgetBytes int64
	// FileMinFreeBytes is the free-space floor: even under budget, an upload is
	// refused when the volume has less than this left, so the KB writer always has
	// headroom.
	FileMinFreeBytes int64
	// GrantTTLSeconds bounds a transfer grant. Short on purpose: it only has to
	// survive being handed to curl.
	GrantTTLSeconds int
	// EndpointIdleTTLSeconds is how long an endpoint with no traffic and no live
	// attachment survives before the sweeper removes it (its channels cascade).
	// Sessions register once and never unregister, so without this the row set
	// grows forever under ordinary use.
	EndpointIdleTTLSeconds int
}

// DefaultLimits are deliberately conservative: COMM shares a disk with the
// knowledge base, and the failure this guards against is ephemeral traffic
// filling the volume and failing durable KB writes.
func DefaultLimits() Limits {
	return Limits{
		MaxBodyBytes:          64 * 1024,
		MaxUnackedPerChannel:  64,
		MessageTTLSeconds:     24 * 3600,
		MetadataTTLSeconds:    7 * 24 * 3600,
		ReplyDeadlineSeconds:  3600,
		PairingCodeTTLSeconds: 900,

		FilesEnabled:     false,
		FileMaxBytes:     16 << 20,
		FileTTLSeconds:   24 * 3600,
		FileBudgetBytes:  256 << 20,
		FileMinFreeBytes: 512 << 20,
		GrantTTLSeconds:  300,

		EndpointIdleTTLSeconds: 7 * 24 * 3600,
	}
}

// Store holds the writer and reader pools over comm.db.
//
// The single-writer discipline mirrors the knowledge base (D6): one writer
// connection with BEGIN IMMEDIATE turns contention into an in-process queue
// instead of SQLITE_BUSY races, and avoids the upgrade-mid-transaction deadlock.
// It also makes the per-(channel,sender) sequence assignment in Send a plain
// MAX+1 rather than a contended counter.
type Store struct {
	W *sql.DB // single-writer pool (MaxOpenConns == 1)
	R *sql.DB // reader pool

	// limits is swapped atomically because the operator edits these live from the
	// settings page while requests are in flight. A plain field would be a data
	// race — one that the race detector catches only if a test happens to write
	// and read concurrently, which is exactly the kind of bug that ships.
	limits atomic.Pointer[Limits]

	path string // the file this store was opened from (logging only)
}

// commonPragmas mirrors internal/store: busy timeout and journal mode first.
const commonPragmas = "_pragma=busy_timeout(10000)&_pragma=journal_mode(wal)&_pragma=synchronous(normal)&_pragma=foreign_keys(on)"

// Open opens (creating if needed) the COMM database at path.
//
// fts5.Register is passed on both pools even though no table here uses FTS5. In
// this driver FTS5 is a PER-CONNECTION extension, not part of the default WASM
// build, so a pool opened without it fails with "no such module: fts5" the moment
// anything touches an FTS table. Registering costs nothing measurable and means a
// future migration that adds message search cannot reintroduce that trap.
func Open(path string, limits Limits) (*Store, error) {
	dsn := "file:" + path + "?" + commonPragmas
	w, err := driver.Open(dsn+"&_txlock=immediate", fts5.Register)
	if err != nil {
		return nil, fmt.Errorf("open comm writer: %w", err)
	}
	w.SetMaxOpenConns(1)

	r, err := driver.Open(dsn, fts5.Register)
	if err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("open comm reader: %w", err)
	}
	r.SetMaxOpenConns(4)
	r.SetMaxIdleConns(4)

	st := &Store{W: w, R: r, path: path}
	st.limits.Store(&limits)
	return st, nil
}

// Close closes both pools.
func (s *Store) Close() error {
	e1 := s.W.Close()
	if e2 := s.R.Close(); e2 != nil && e1 == nil {
		e1 = e2
	}
	return e1
}

// Limits returns the bounds this store currently enforces.
func (s *Store) Limits() Limits { return *s.limits.Load() }

// SetLimits replaces the enforced bounds. Safe to call at any time, including
// while requests are in flight: the settings page applies changes live, so an
// operator can tighten a limit during a runaway rather than after a restart.
//
// An operation that already read the old limits completes under them; there is no
// attempt to make a single request see a consistent snapshot across several reads,
// because every enforcement point reads once.
func (s *Store) SetLimits(l Limits) { s.limits.Store(&l) }

// lim reads the current limits. Every enforcement point goes through this rather
// than touching the field, so a live swap is picked up on the next operation.
func (s *Store) lim() Limits { return *s.limits.Load() }

// Migrate applies embedded COMM migrations in lexical order, skipping versions
// already recorded. Idempotent, forward-only, and independent of the knowledge
// base's migration state — the two databases version separately on purpose, so a
// COMM schema change never touches ken.db.
func (s *Store) Migrate() error {
	files, err := fs.Glob(migrationFS, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(files)

	applied, err := s.appliedVersions()
	if err != nil {
		return err
	}

	for _, f := range files {
		v := versionOf(f)
		if v == 0 || applied[v] {
			continue
		}
		body, err := migrationFS.ReadFile(f)
		if err != nil {
			return err
		}
		if _, err := s.W.Exec(string(body)); err != nil {
			return fmt.Errorf("apply %s: %w", f, err)
		}
	}
	return nil
}

// appliedVersions reads schema_migration; a missing table (fresh db) yields an
// empty set rather than an error, but a real error is never swallowed as
// "nothing applied".
func (s *Store) appliedVersions() (map[int]bool, error) {
	out := map[int]bool{}
	rows, err := s.R.Query(`SELECT version FROM schema_migration`)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return out, nil
		}
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out[v] = true
	}
	return out, rows.Err()
}

// versionOf parses the leading integer of a migration path
// ("migrations/0001_init.sql" -> 1).
func versionOf(path string) int {
	base := path
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	if i := strings.IndexByte(base, '_'); i > 0 {
		base = base[:i]
	}
	n, _ := strconv.Atoi(base)
	return n
}

// Owner identifies who a COMM object belongs to. All three fields name rows in
// ken.db and are supplied by the authenticated caller.
//
// ActorID alone is NOT an ownership key and must never be used as one: actors
// resolve by (kind, display_name), so every token minted with the same actor name
// collapses to one actor row across machines and humans. Ownership is SpaceID
// plus the authorizing human recorded on the channel.
type Owner struct {
	TokenID string
	ActorID int64
	SpaceID int64
}

const base62Alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// randBase62 returns n cryptographically-random base62 characters.
func randBase62(n int) (string, error) {
	out := make([]byte, n)
	m := big.NewInt(int64(len(base62Alphabet)))
	for i := range out {
		x, err := rand.Int(rand.Reader, m)
		if err != nil {
			return "", err
		}
		out[i] = base62Alphabet[x.Int64()]
	}
	return string(out), nil
}

// sha256Hex is the one-way store for every secret in this package (endpoint
// secrets, pairing codes). Never Argon2: these are high-entropy server-minted
// values, where a slow KDF only taxes every call without adding strength.
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// nowExpr yields a server-clock timestamp offset by n seconds, as a SQL
// parameter for strftime's modifier slot. Clients supply RELATIVE lifetimes only,
// never absolute timestamps, so clock skew between agent machines cannot silently
// shorten or extend anything.
func nowExpr(seconds int) string { return fmt.Sprintf("%+d seconds", seconds) }

// tx runs fn inside a single writer transaction, rolling back on error. Every
// multi-statement write in this package goes through it.
func (s *Store) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	t, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(t); err != nil {
		_ = t.Rollback()
		return err
	}
	return t.Commit()
}

// Path reports the database file this store was opened from (startup logging).
func (s *Store) Path() string { return s.path }
