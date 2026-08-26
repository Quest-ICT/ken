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
	"math/big"
	"sync/atomic"

	"github.com/ncruces/go-sqlite3/driver"
	"github.com/ncruces/go-sqlite3/ext/fts5"

	"github.com/Quest-ICT/ken/internal/dbmigrate"
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

// CallerSafe marks an error whose TEXT may be shown to the caller verbatim.
//
// WHY THIS EXISTS, and it is a defect that shipped. Callers map sentinels to their own
// surface by FLATTENING them: commserver's mapper answers every errors.Is(ErrNotFound)
// with the literal string "not found". That is deliberate — uniform refusals are what
// stop an error message becoming an existence oracle. But it also means a raise site
// that wraps a sentinel with guidance has its guidance silently discarded one layer up.
//
// That is exactly what happened to the room-vs-channel message in 3.3.0: the text was in
// the shipped binary, a test at the raise site asserted it and passed, and a caller
// passing a room id as channel_id still got a bare "not found" — byte-identical to the
// error that made a working station conclude rooms did not exist. Two correct layers,
// one untested composition.
//
// The marker is OPT-IN so the default stays uniform: an error only carries its text
// across the boundary when its author decided the caller is entitled to it. Wrapping
// preserves the chain, so errors.Is against the sentinel is unaffected — nothing that
// matched before stops matching.
//
// The bar for marking one: the text must reveal nothing the caller could not already
// establish. The room message clears it because it is raised only for a MEMBER of that
// room, who necessarily knows it exists.
func CallerSafe(err error) error {
	if err == nil {
		return nil
	}
	return callerSafeError{err: err}
}

type callerSafeError struct{ err error }

func (e callerSafeError) Error() string { return e.err.Error() }
func (e callerSafeError) Unwrap() error { return e.err }

// CallerSafeText is the interface a surface checks for. Declared as a method rather than
// matched by concrete type so a mapper in another package needs no import beyond this one.
func (e callerSafeError) CallerSafeText() string { return e.err.Error() }

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
	// MessageTTLSeconds is how long a DELIVERED but un-acked message survives
	// before expiring. The clock starts at FIRST DELIVERY, not at send.
	//
	// It used to start at send, and that was wrong in a way no measurement inside
	// the system could reveal: a human works ~8 h a day, so a session goes 16 h
	// between pulls on a weeknight, 64 h over a weekend and weeks over annual
	// leave. Against the shipped 24 h default that made every message sent during
	// a Friday shift dead before Monday — 2.67x the TTL — and it is what killed a
	// real 4 661-byte message sent on Sunday 2026-08-02. The clock was running
	// during exactly the window in which nobody could possibly poll.
	//
	// Anchoring at delivery asks the right question: not "how long has this
	// existed" but "how long has the recipient had it and done nothing".
	MessageTTLSeconds int
	// UndeliveredTTLSeconds bounds a message NOBODY HAS EVER SEEN. It exists only
	// as a backstop against a permanently dead endpoint, so it is generous by
	// design: undelivered mail is bounded primarily by MaxUnackedPerChannel, which
	// is a volume bound and therefore immune to how long a human is away.
	//
	// Must be comfortably longer than the longest absence an operator expects. A
	// value shorter than MessageTTLSeconds is nonsense — it would kill mail before
	// the delivered clock could even start — and is treated as "use the default".
	UndeliveredTTLSeconds int
	// BodyRetentionSeconds is how long a body survives AFTER the message settles.
	//
	// Zero restores the historical behaviour: blank on ack. That behaviour
	// destroyed 97% of one live deployment's message bodies (153 of 159) through
	// the ordinary, instructed path — poll, act, ack — because ack blanked the
	// body unless the message happened to require a response. The un-acked inbox
	// was not a safety net; it was the archive, and acking was the instruction to
	// destroy the only copy.
	BodyRetentionSeconds int
	// MetadataTTLSeconds is how long a settled (acked/expired) message's row
	// survives after creation.
	//
	// It is no longer only the audit shell. Bodies survive acknowledgement now, and
	// a body that was NEVER DELIVERED cannot be reclaimed by BodyRetentionSeconds —
	// retention measures from the settle time and an unread message has none — so
	// this is the sole bound on that population. An operator raising it for a longer
	// audit trail is also raising retained bytes.
	MetadataTTLSeconds int
	// ORDERING INVARIANT, AND THE ONE PAIR NOTHING GUARDS: this must be SHORTER than
	// MessageTTLSeconds. Longer, and the body is destroyed before its own reply deadline
	// arrives, so the reply_overdue notice points at text nobody can read any more — it
	// asks a sender to chase an answer to a question that no longer exists.
	//
	// Found on the live deployment 2026-08-20 by ken-prod-ops, tracing a notice I could not
	// explain: comm_reply_deadline_sec 604800 (7 d) against comm_message_ttl_sec 259200
	// (3 d), so EVERY unanswered requires_response message there generates a notice about a
	// body that expired four days earlier. The shipped defaults have it right — 3600 inside
	// 86400 — which is exactly why nothing caught it.
	//
	// NOT CLAMPED, deliberately, unlike UndeliveredTTLSeconds above. Clamping would silently
	// convert an operator's considered 7-day reply window into 3 days and tell nobody; the
	// operator's intent is legitimate and the honest remedy — raise the TTL, or lower the
	// deadline — is theirs to choose. It is LOGGED at startup instead, loudly, naming both
	// values. See CheckDeadlineOrdering.
	//
	// ReplyDeadlineSeconds is the default deadline applied to a message that
	// requires a response.
	ReplyDeadlineSeconds int
	// PairingCodeTTLSeconds is how long a human-minted pairing code stays valid.
	PairingCodeTTLSeconds int
	// ClaimLeaseSeconds is how long a station-bound reader holds a claimed message
	// before it returns to the unclaimed tail (docs/STATIONS.md S4).
	//
	// It bounds how long a session that claimed a message and then DIED can strand
	// it from its station's other readers. Too short and a reader still working is
	// undercut by a second reader picking the message up; too long and a dead
	// session's mail sits invisible. Sized against a turn rather than a request,
	// because "claimed" means "a model is reasoning about it", not "a query ran".
	//
	// Applies ONLY to bound endpoints. An unbound endpoint is the sole reader of
	// its own mail, so there is nothing to claim it against.
	ClaimLeaseSeconds int

	// FilesEnabled gates file exchange. It defaults ON as of 2026-08-24 and remains
	// live-togglable, which is now a KILL SWITCH rather than an opt-in — the distinction
	// Vlad drew when he ruled that no Ken feature is optional or off by default.
	//
	// It shipped defaulting OFF because the relay is the bulk of the subsystem's risk
	// (disk, quotas, orphan sweeping) and an operator was expected to opt in. That is the
	// same reasoning main.go:337 already rejected for COMM itself: "a feature an operator
	// can be missing is a feature every doc, every instruction and every session has to
	// hedge about". Withholding the switch was never the point; withholding the DEFAULT was.
	//
	// What stays is the ability to turn the relay off in an incident, checked per operation.
	// What goes is a shipped answer of "no" to every file offer on every deployment.
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
// CheckDeadlineOrdering reports the one settings pair whose bad ordering is invisible:
// a reply deadline that outlives the message it is about.
//
// Returns "" when the ordering is sound. The caller logs the string; nothing refuses,
// because a deployment configured this way is running and working, and the only thing
// wrong is that a class of notice points at destroyed text. Taking the process down over
// that would be a worse outcome than the defect.
func CheckDeadlineOrdering(l Limits) string {
	if l.ReplyDeadlineSeconds <= 0 || l.MessageTTLSeconds <= 0 {
		return ""
	}
	if l.ReplyDeadlineSeconds <= l.MessageTTLSeconds {
		return ""
	}
	return fmt.Sprintf(
		"COMM: reply deadline (%ds) OUTLIVES the message TTL (%ds) — a body is destroyed %ds "+
			"before its own reply deadline arrives, so every unanswered requires_response message "+
			"produces a reply_overdue notice about text nobody can read. Raise comm_message_ttl_sec "+
			"above comm_reply_deadline_sec, or lower the deadline.",
		l.ReplyDeadlineSeconds, l.MessageTTLSeconds, l.ReplyDeadlineSeconds-l.MessageTTLSeconds)
}

func DefaultLimits() Limits {
	return Limits{
		MaxBodyBytes:          64 * 1024,
		MaxUnackedPerChannel:  64,
		MessageTTLSeconds:     24 * 3600,
		UndeliveredTTLSeconds: 30 * 24 * 3600,
		BodyRetentionSeconds:  24 * 3600,
		MetadataTTLSeconds:    7 * 24 * 3600,
		ReplyDeadlineSeconds:  3600,
		PairingCodeTTLSeconds: 900,
		// 900 = 15 minutes, matching docs/STATIONS.md and what every configured
		// deployment actually runs. This said 300 while internal/settings said 900 — and
		// settings' own comment names THIS as the source of truth that it mirrors, so the
		// declared authority was the one that had drifted. Nothing in production was
		// affected (boot takes the settings value), but the test suite exercised a lease
		// production has never used.
		ClaimLeaseSeconds: 900,

		FilesEnabled:     true,
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
//
// The runner itself is internal/dbmigrate, shared with ken.db. It used to live
// here and ONLY here, which is why ken.db spent nineteen migrations without the
// foreign-key handling a table rebuild depends on. The comment carrying the
// measurement that bought it moved with the code.
func (s *Store) Migrate() error {
	return dbmigrate.Run(context.Background(), s.W, s.R, migrationFS, "migrations/*.sql")
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
