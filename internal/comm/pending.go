package comm

import (
	"context"
	"strings"
)

// Pending counters — "how much mail is waiting for me, without delivering any of it".
//
// FOUR COUNTERS, ONE JOB, AND THEY MUST NOT DISAGREE. They live together in this file
// because the failure they exist to prevent is drift between them:
//
//   PendingForEndpoint (channel.go)  per CHANNEL, keyed by channel_id
//   RoomsFor           (room_mirror) per ROOM, alongside that room's members
//   PendingTotalFor    (here)        every scope at once — channels, rooms, broadcast
//   BroadcastPendingFor(here)        the broadcast subset, which has nowhere else to live
//
// The comment said THREE and listed four, and two of the four were meanwhile answering a
// different question — the drift this file exists to prevent, in the file that says so.
// Two more counts outside this file share the clause below for the same reason:
// queuedForEndpoint (message.go), which tells a SENDER what was already waiting, and
// PairsFor (pair_send.go), which counts one pair conversation.
//
// WHY A TOTAL EXISTS AT ALL. `comm_channels` is the survey every session is instructed to
// call before it sends, and until now it could only answer for channels — a room's mail
// was not undercounted, it was structurally absent, and so was broadcast's. The per-scope
// numbers say where; the total is the one number that cannot be wrong by omission, and it
// is the number a session whose captured instructions only ever mentioned channels can
// still act on, because the remedy those instructions prescribe — poll — is scope-blind
// and already returns everything.
//
// WHAT "PENDING" MEANS, identically in all four: QUEUED FOR YOU AND STILL DELIVERABLE.
// Not delivered-but-unacked — that is mail you have already been handed, and counting it
// would be telling you to go and read something you have. Reading any of these delivers
// nothing, stamps nothing, and starts no clock.

// pendingScopeSQL is the shared body of the two counters below, so they cannot drift from
// each other or from Poll. It is a fragment rather than a function returning rows because
// the two differ only by an extra scope filter.
//
// The predicates mirror Poll (message.go) deliberately: a count that includes mail a poll
// would refuse is a count that sends a session to look for something it cannot have.
//
//	%NOTEXPIRED%             — an expired message is not deliverable, and the sweeper may
//	                           not have flipped its delivery to 'expired' yet. Without this
//	                           the number is right only immediately after a sweep. Spliced
//	                           from pendingNotExpiredSQL, which is now shared by every
//	                           counter rather than by this fragment's two.
//	channel open, or none    — a revoked channel's mail is unreachable. Room and broadcast
//	                           messages have channel_id NULL and must pass, which is what
//	                           the `c.id IS NULL` half is for; an INNER JOIN here is the
//	                           exact mistake that made the console blind to room traffic.
//
// NOT mirrored: Poll's claim predicate. A claim is a transient lease between two endpoints
// of the SAME station racing the same inbox, so excluding claimed rows would make the count
// flicker for reasons that have nothing to do with what is waiting for the station.
const pendingScopeSQL = `
  FROM delivery d
  JOIN message m ON m.id = d.message_row
 WHERE %PARTY%
   AND d.state = 'queued'
   AND %NOTEXPIRED%
`

// pendingNotExpiredSQL is THE deliverability-by-clock clause, and it belongs to every
// counter in this package rather than to the fragment above.
//
// It lived in pendingScopeSQL alone, so the two counters that do not use that fragment
// answered a different question: PendingForEndpoint and RoomsFor counted mail that had
// expired and had not yet been swept. The sweep runs on an interval, so that window is
// ordinary rather than rare, and inside it ONE comm_channels result said pending_total=0
// beside a per-channel row saying 1 — while the instruction block frozen into every
// running session says to read pending_total FIRST. Two further counts each held their own
// byte-identical copy of the clause, which is the same defect one edit away.
//
// A CLAUSE AND NOT A QUERY, because these counters share no shape: two count over
// pendingScopeSQL, PendingForEndpoint is a LEFT JOIN aggregate that must keep emitting a
// zero row for every open channel, RoomsFor is a correlated subquery per room, and
// PairsFor counts one scope at a time. They can share this; they cannot share the query.
//
// WHERE IT GOES MATTERS in the aggregate: PendingForEndpoint carries it in the delivery
// JOIN's ON clause and never in the WHERE. In the WHERE it eliminates the channel row
// itself — m.expires_at is NULL for a channel with no mail — turning "nothing waiting
// here" into a missing row, which that function's own comment calls the difference
// between a silence and an assertion.
//
// The message table must be aliased `m` at every call site.
const pendingNotExpiredSQL = `m.expires_at > strftime('%Y-%m-%dT%H:%M:%fZ','now')`

// pendingSQL splices that clause into a counter's query, and PANICS on a query that does
// not ask for it.
//
// A MARKER RATHER THAN A FORMAT VERB, for the reason replacePartyPlaceholder records
// below: the clause carries strftime's own `%Y-%m-%d`, which fmt.Sprintf would mangle at
// runtime instead of failing at compile time. The panic is the cheap half of the guard —
// every one of these queries is a constant, so it fires on the first call rather than
// hiding behind a rare input. The other half is a source-reading test that fails when a
// new COUNT over queued deliveries does not ask for the clause
// (TestEveryQueuedDeliveryCountCarriesTheSharedExpiryClause).
func pendingSQL(q string) string {
	if !strings.Contains(q, "%NOTEXPIRED%") {
		panic("comm: a pending-count query must carry %NOTEXPIRED% (pending.go)")
	}
	return strings.ReplaceAll(q, "%NOTEXPIRED%", pendingNotExpiredSQL)
}

// PendingTotalFor counts every message queued for this endpoint's party, in every scope.
//
// This is the number `comm_channels` reports as `pending_total`, and it is deliberately
// NOT the sum of the per-channel counts: that sum is zero for a session whose only mail is
// in a room, which is the whole defect. A caller comparing the two and finding them
// different is seeing room or broadcast mail, not a bug.
func (s *Store) PendingTotalFor(ctx context.Context, ep *Endpoint) (int, error) {
	return s.countPending(ctx, ep, "")
}

// BroadcastPendingFor counts messages queued for this party in a BROADCAST scope.
//
// Broadcast is the one address with nowhere to hang a count: a channel has a channel row
// and a room has a room row, but `b:<sender>` is synthesised per sender and appears in no
// list a recipient can enumerate. Without this number, broadcast mail is visible only in
// the total — present, but with no way to tell what kind of thing is waiting.
func (s *Store) BroadcastPendingFor(ctx context.Context, ep *Endpoint) (int, error) {
	return s.countPending(ctx, ep, ` AND m.scope_id LIKE 'b:%'`)
}

// countPending is the one implementation. `extra` narrows the scope; empty counts all.
func (s *Store) countPending(ctx context.Context, ep *Endpoint, extra string) (int, error) {
	// BOTH PARTY FORMS, via the shared predicate. A station-bound endpoint can hold
	// deliveries filed under 's:<station>' AND under its own 'e:<rowid>' — the second
	// happens when rows were written while it was unbound, and any counter that binds one
	// form reports a confident, wrong, smaller number. This is why the predicate is a
	// shared function and not a party string: RoomsFor takes a single party and that is
	// precisely its blind spot.
	pred, args := partyPredicate(ep, "d")
	q := `SELECT COUNT(*)` + pendingSQL(replacePartyPlaceholder(pendingScopeSQL, pred)) + extra
	var n int
	if err := s.R.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// replacePartyPlaceholder splices the party predicate into the shared SQL.
//
// A named placeholder rather than concatenation at each call site, so the two counters
// cannot accidentally build different WHERE clauses — and `%PARTY%` rather than a format
// verb because the fragment contains strftime's own `%Y-%m-%d`, which fmt.Sprintf would
// mangle into nonsense at runtime instead of failing at compile time.
func replacePartyPlaceholder(sql, pred string) string {
	return strings.Replace(sql, "%PARTY%", pred, 1)
}
