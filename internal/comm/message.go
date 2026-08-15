package comm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
)

// Message is one atomic transfer over a channel.
//
// Body is empty once the message has been acked (or expired) — the content is
// deleted while the metadata row survives. See the schema comment for why that
// split is load-bearing rather than an optimization.
type Message struct {
	MessageID string
	// ChannelID is EMPTY for a room or broadcast message — those belong to no channel.
	// Scope is the address that always exists.
	ChannelID string
	// Scope is where this message lives: 'ch:<channel>', 'r:<room>' or 'b:<party>'.
	//
	// D3 of the rooms debugging. Without it a recipient could not tell that a message
	// came from a room, WHICH room, or how to answer — both agents that received one
	// inferred the room from "I am only in one", and with two rooms neither could have.
	Scope string
	// SenderStationID is the sending station when the sender is staffed, empty when not.
	// The receive side previously carried only an opaque endpoint id, so a reader could
	// not name who wrote to them without a second lookup they had no tool for.
	SenderStationID string
	// AudienceSize is how many parties this went to. Above 1 means the reader is one of
	// several and a reply reaches the scope rather than a person.
	AudienceSize     int
	Seq              int64
	SenderEndpointID string
	Body             string
	RequiresResponse bool
	ReplyToMessageID string
	State            string
	DeliveryCount    int
	BodyBytes        int
	CreatedAt        string
	ExpiresAt        string
	ReplyDeadlineAt  string
	// File is the attachment descriptor when this message carries a file offer
	// (docs/COMM.md §11); nil for ordinary messages.
	File *FileInfo
	// Kind is "message" for peer traffic and "status" for a server-authored notice
	// about an earlier message's fate (expired, reply_overdue). A peer cannot author
	// a status row, so a receiver may trust the distinction.
	Kind string

	// The two fields below are TRANSIENT: Send populates them, and Poll and
	// MessageByID leave them zero because they describe the act of sending rather
	// than the row. They are here rather than in a separate result type so Send's
	// signature — and every caller of it — stays unchanged.

	// TTLClampedFrom is the ttl_seconds the caller ASKED for when the server gave it
	// something shorter, and zero when nothing was overridden.
	//
	// Silent clamping is how a sender ends up believing a message will outlive an
	// absence it will not: the result reported the resulting expires_at and never
	// mentioned that the request had been overruled, so noticing required diffing a
	// timestamp against a number you had to remember passing.
	TTLClampedFrom int

	// Recipients is how many parties this message was delivered to. 1 for a channel,
	// the room's size minus the sender for a room, the union of the sender's rooms for
	// a broadcast.
	//
	// Reported because a sender who cannot see the audience cannot tell a broadcast
	// that reached nine stations from one that reached none — and "reached none" is
	// refused rather than returned, so a number here is always a real audience.
	Recipients int

	// WaitingForYou is how many messages were already QUEUED for the sender — anywhere,
	// in any scope — at the moment of sending.
	//
	// A session that sends without reading what is already waiting answers a question
	// its peer has often moved past — measured on this project: a reply that
	// re-argued a point the peer had already conceded. The value of checking is not
	// the read, it is the pause before sending; a non-zero count here is the prompt
	// to take it.
	//
	// It was scope-local until it was found to be structurally zero on the broadcast
	// path and blind to room mail on every path — including for the channel sender, who
	// would be told "nothing is waiting" while a room message sat queued. It now costs
	// its own small query rather than riding the backpressure aggregate, which is the
	// price of it meaning what its description has always said.
	WaitingForYou int
}

// Redelivered reports whether the receiver has seen this message before. At-least-
// once delivery makes this normal, not exceptional: a receiver should treat a
// redelivery as "you may not have finished processing this", not as a duplicate to
// discard blindly.
func (m *Message) Redelivered() bool { return m.DeliveryCount > 1 }

// SendOpts carries the optional parts of a send.
type SendOpts struct {
	// IdempotencyKey makes a resend safe. A repeat with the same key returns the
	// ORIGINAL message instead of delivering a second copy. This matters because a
	// response lost after the server committed is the ordinary failure here — a
	// harness timeout, a reset connection, a restart inside the shutdown grace.
	IdempotencyKey string
	// RequiresResponse marks the message as owing a reply, and arms a server-clock
	// reply deadline. Without a deadline, full-duplex would move the hang from the
	// channel to the requester: a responder that dies leaves the sender waiting
	// forever with no signal.
	RequiresResponse bool
	// ReplyToMessageID correlates this message with an earlier request on the same
	// channel.
	ReplyToMessageID string
	// TTLSeconds overrides the default un-acked lifetime. Relative on purpose:
	// clients never supply absolute timestamps, so clock skew between agent
	// machines cannot silently shorten or extend a lifetime.
	TTLSeconds int
}

// Send enqueues one message from ep to its peer on the named channel.
//
// Enforced inside the writing transaction: channel membership and openness, the
// body cap, the per-channel un-acked cap (backpressure), and sequence assignment.
// Quotas are checked here rather than in the shared rate-limiter bucket because
// that bucket fails OPEN when saturated — correct for keys an attacker cannot mint
// cheaply, wrong for identifiers a caller creates in a loop.
func (s *Store) Send(ctx context.Context, ep *Endpoint, channelID, body string, opts SendOpts) (*Message, error) {
	if len(body) > s.lim().MaxBodyBytes {
		return nil, ErrTooLarge
	}
	ch, peer, err := s.ChannelFor(ctx, ep, channelID)
	if err != nil {
		return nil, err
	}

	// The send-time stamp is the UNDELIVERED backstop, not the message's real
	// lifetime: the delivered clock is armed at first delivery (see Poll). A
	// sender's ttl_seconds therefore means "how long should this stay deliverable
	// while nobody has picked it up", which is the only lifetime a sender can
	// meaningfully choose.
	undelivered := s.lim().UndeliveredTTLSeconds
	if undelivered <= 0 || undelivered < s.lim().MessageTTLSeconds {
		undelivered = DefaultLimits().UndeliveredTTLSeconds
	}
	ttl := clampTTL(opts.TTLSeconds, undelivered)
	clampedFrom := 0
	if opts.TTLSeconds > 0 && opts.TTLSeconds != ttl {
		clampedFrom = opts.TTLSeconds
	}

	scope := channelScope(channelID)

	var out *Message
	err = s.tx(ctx, func(t *sql.Tx) error {
		// Resolved inside the transaction so a binding that lands mid-send cannot
		// file the message under one identity and its deliveries under another.
		senderParty, err := endpointParty(ctx, t, ep.ID)
		if err != nil {
			return err
		}

		// Idempotent resend: return the original rather than sending a second copy.
		if opts.IdempotencyKey != "" {
			var existing string
			// Keyed on the SENDER PARTY, not the sending endpoint: a session that
			// reconnects under a new endpoint and retries the same key must get its
			// original message back rather than send a second copy.
			err := t.QueryRowContext(ctx, `
SELECT message_id FROM message
WHERE scope_id=? AND sender_party=? AND idempotency_key=?`,
				scope, senderParty, opts.IdempotencyKey).Scan(&existing)
			if err == nil {
				out, err = messageByID(ctx, t, existing)
				return err
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}

		// Backpressure: cap un-acked depth per channel. Full-duplex has no
		// turn-taking, so two auto-processing sessions could otherwise enter a
		// reply loop that grows the database without bound.
		// One scan, two aggregates over DIFFERENT state sets, and the difference is
		// the point.
		//
		// Backpressure counts queued AND delivered, because both occupy the channel.
		// The sender's prompt counts ONLY 'queued' — mail that has never been handed
		// to them. 'delivered' means they have already been shown it; telling them to
		// "poll it and reconsider" would be advice they have already taken, and the
		// prompt fires on a session that read its mail, is mid-reply, and simply has
		// not acked yet.
		//
		// Found within minutes of shipping, by the field firing on ME while I was
		// replying to the very message it was counting. It is the same queued-versus-
		// delivered distinction I argued for on the refusal design and then failed to
		// apply to the warning I built alongside it.
		// BACKPRESSURE STAYS SCOPE-LOCAL. The cap is about this conversation's backlog,
		// and widening it would let one busy room throttle unrelated traffic.
		var unacked int
		if err := t.QueryRowContext(ctx, `
SELECT COUNT(*)
  FROM delivery d JOIN message m ON m.id = d.message_row
 WHERE m.scope_id = ? AND d.state IN ('queued','delivered')`, scope).Scan(&unacked); err != nil {
			return err
		}
		if unacked >= s.lim().MaxUnackedPerChannel {
			return ErrBackpressure
		}
		// The sender's own waiting mail is a SEPARATE, party-wide question — see
		// queuedForEndpoint. It used to be the second half of the aggregate above, which
		// is why it could only ever see this one scope.
		waitingForSender, err := queuedForEndpoint(ctx, t, ep)
		if err != nil {
			return err
		}

		// Correlate a reply, if any. The referenced request must be on this channel
		// and addressed TO this sender — otherwise a reply could be pinned to an
		// unrelated message.
		var replyTo any
		var replyToRow int64
		if opts.ReplyToMessageID != "" {
			err := t.QueryRowContext(ctx, `
SELECT m.id FROM message m
  JOIN delivery d ON d.message_row = m.id AND d.party_key = ?
 WHERE m.message_id = ? AND m.scope_id = ?`,
				senderParty, opts.ReplyToMessageID, scope).Scan(&replyToRow)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			if err != nil {
				return err
			}
			replyTo = replyToRow
		}

		seq, err := nextScopeSeq(ctx, t, scope)
		if err != nil {
			return err
		}

		messageID, err := randBase62(22)
		if err != nil {
			return err
		}

		// NO deadline at send. It is armed at FIRST DELIVERY (see Poll), because a
		// deadline anchored at send starts running while the recipient has no way
		// to know the message exists: measured median delivery latency is 11 min,
		// p90 144 min, max 23.7 h, against a 1 h default — 18% of messages used to
		// arrive with the deadline already blown, which made the retention the
		// deadline governs dead on arrival for nearly a fifth of all traffic.
		// Every placeholder is a plain `?`: mixing `?` with explicit `?N` in one
		// statement renumbers the auto-assigned ones and silently binds the wrong
		// values.
		if _, err := t.ExecContext(ctx, `
INSERT INTO message(message_id, scope_id, scope_seq, channel_id, sender_endpoint, sender_party,
                    idempotency_key, body, body_sha256, body_bytes, requires_response, reply_to,
                    audience_size, expires_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,strftime('%Y-%m-%dT%H:%M:%fZ','now',?))`,
			messageID, scope, seq, ch.ID, ep.ID, senderParty, nullStr(opts.IdempotencyKey),
			body, sha256Hex(body), len(body), boolInt(opts.RequiresResponse), replyTo,
			1, nowExpr(ttl)); err != nil {
			return err
		}

		// One delivery row per recipient party. `peer` is still the addressee for a
		// channel, but it is resolved to a PARTY first, so a message sent to a staffed
		// peer is filed against the station rather than the connection that happens to
		// be reading it — the whole point of the split, and what makes N of these
		// rows a room.
		//
		// NO reply deadline here. It is armed at FIRST DELIVERY (see Poll), because a
		// deadline anchored at send runs while the recipient has no way to know the
		// message exists: measured median delivery latency 11 min, p90 144 min, max
		// 23.7 h against a 1 h default — 18% of messages used to arrive already
		// expired, which made the retention that deadline governs dead on arrival for
		// nearly a fifth of all traffic.
		recipientParty, err := endpointParty(ctx, t, peer)
		if err != nil {
			return err
		}
		if _, err := t.ExecContext(ctx, `
INSERT INTO delivery(message_row, party_key, recipient_endpoint)
SELECT id, ?, ? FROM message WHERE message_id = ?`,
			recipientParty, peer, messageID); err != nil {
			return err
		}

		// Link the request to its reply so a sender can tell what was answered.
		if replyTo != nil {
			var newRow int64
			if err := t.QueryRowContext(ctx, `SELECT id FROM message WHERE message_id=?`, messageID).Scan(&newRow); err != nil {
				return err
			}
			// Clearing the body here matters: a request acked BEFORE its reply arrived
			// The body is NOT blanked here any more, and the comment that used to
			// justify blanking is why: it said Sweep's later pass "only considers
			// rows with replied_by IS NULL", which was true of the OLD pass and is
			// false of the retention pass that replaced it — that one has no
			// replied_by predicate at all. So this line was silently bypassing
			// BodyRetentionSeconds for exactly the messages a curator is most likely
			// to want: the ones that got an answer.
			//
			// A rewritten pass left its dependant untouched, which is the same shape
			// as a stale comment naming an enforcer that never existed. Retention now
			// governs every settled body, with no side door.
			// Recorded against the DELIVERY that owed the answer, not the message: with
			// several recipients "was this answered" is a different question per
			// party, and `answered_at` on the message is the any-recipient roll-up.
			if _, err := t.ExecContext(ctx, `
UPDATE delivery SET replied_by=?
WHERE message_row=? AND party_key=? AND replied_by IS NULL`,
				newRow, replyToRow, senderParty); err != nil {
				return err
			}
			if _, err := t.ExecContext(ctx, `
UPDATE message SET answered_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
WHERE id=? AND answered_at IS NULL`, replyToRow); err != nil {
				return err
			}
		}

		out, err = messageByID(ctx, t, messageID)
		if out != nil {
			out.TTLClampedFrom, out.WaitingForYou = clampedFrom, waitingForSender
			out.Recipients = 1
		}
		return err
	})
	if isSeqCollision(err) {
		return nil, ErrSequenceCollision
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}

// nextSeq allocates the next sequence number for one direction of a channel.
//
// The high-water mark lives in its own table rather than being derived as
// MAX(seq)+1 over the message rows, because those rows are swept: once a
// direction's history was purged the derived counter RESET to 1, breaking the
// strictly-ascending promise and — the real damage — letting a retried cumulative
// ack computed against the old numbering settle brand-new messages that had been
// reissued the same low numbers.
//
// Safe as an upsert-then-read under the single-writer discipline.
//
// The counter is keyed on the SENDER'S STATION when it has one, and on its endpoint
// rowid otherwise (docs/STATIONS.md S4). Keyed on the endpoint alone, a replacement
// session — a different endpoint of the same station — starts a fresh counter at 1
// while its predecessor already reached 20, and two messages in one channel and
// direction then share a sequence number. Ordering breaks, but the real damage is
// that `ack_up_to_seq` is a RANGE: acking up to 2 after a takeover settles the new
// session's messages AND old ones nobody read.
func nextSeq(ctx context.Context, t *sql.Tx, chRow, sender int64) (int64, error) {
	key, err := senderKey(ctx, t, sender)
	if err != nil {
		return 0, err
	}
	if _, err := t.ExecContext(ctx, `
INSERT INTO channel_seq(channel_id, sender_key, next_seq) VALUES(?,?,2)
ON CONFLICT(channel_id, sender_key) DO UPDATE SET next_seq = next_seq + 1`,
		chRow, key); err != nil {
		return 0, err
	}
	var next int64
	if err := t.QueryRowContext(ctx,
		`SELECT next_seq FROM channel_seq WHERE channel_id=? AND sender_key=?`,
		chRow, key).Scan(&next); err != nil {
		return 0, err
	}
	return next - 1, nil
}

// ErrSequenceCollision reports that a message could not be numbered because this
// endpoint's per-channel counter has fallen behind its own history on that channel.
//
// It exists because the failure it names is otherwise a bare "internal error", and an
// operator who has just adopted a station has no path from that string to a sequence
// counter — they will suspect the network, the token, the peer, or the restart they
// happened to do. A production operator hit exactly this and said so: they only knew
// where to look because a report arrived first.
//
// The condition is repaired by comm_unbind, which is the remediation path for anyone
// who bound on a release before the counter was carried across.
var ErrSequenceCollision = errors.New("sequence collision")

// isSeqCollision recognises the UNIQUE index on (channel_id, sender_endpoint, seq).
// Matched on the index's own columns rather than a driver code, so it cannot quietly
// start matching some other constraint.
func isSeqCollision(err error) bool {
	if err == nil {
		return false
	}
	e := err.Error()
	return strings.Contains(e, "UNIQUE") &&
		strings.Contains(e, "message.seq") &&
		strings.Contains(e, "message.sender_endpoint")
}

// senderKey resolves the counter key for a sender, INSIDE the send transaction so it
// cannot observe a binding that changes underneath it.
//
// The prefix tags the two namespaces apart. Without it, a station whose id happened to
// be the decimal string of some endpoint's rowid would silently share that endpoint's
// counter — a collision that never appears in testing and is unrecoverable once it
// does.
func senderKey(ctx context.Context, t *sql.Tx, sender int64) (string, error) {
	var station sql.NullString
	err := t.QueryRowContext(ctx, `SELECT station_id FROM endpoint WHERE id=?`, sender).Scan(&station)
	if errors.Is(err, sql.ErrNoRows) {
		// The endpoint is gone mid-send. Fall back to the rowid form rather than
		// failing: the message is already authorized, and an unbound key is the
		// conservative choice — it can only ever under-share a counter.
		return "e:" + strconv.FormatInt(sender, 10), nil
	}
	if err != nil {
		return "", err
	}
	if station.Valid && station.String != "" {
		return "s:" + station.String, nil
	}
	return "e:" + strconv.FormatInt(sender, 10), nil
}

// enqueueLocked inserts one plain message inside an open write transaction:
// backpressure, sequence assignment, insert, read-back. The FILE surface uses it
// (offers and completed uploads become ordinary poll-able messages).
//
// Send does NOT call this, on purpose: its insert carries five more columns and
// is wrapped in idempotency and reply-correlation logic, and a shared helper
// with that many knobs would be harder to hold to account than these few
// shared-shape lines. If the backpressure or sequencing RULES ever change, both
// places change — they are the same rule stated twice, and each points here.
//
// EVERY ROW IT WRITES IS kind='message'. It used to take a kind, because the sweeper
// authored 'status' rows here and those bypassed backpressure — a channel that is full
// being exactly when a failure signal matters most. Notices are derived at poll time
// since 3.4.0 (notice.go), so nothing authors a status row and the parameter, the branch
// and the exemption were all unreachable. They are removed rather than left with a
// corrected comment: dead code behind an explanation is how the next reader rebuilds a
// mechanism that was deliberately deleted.
func (s *Store) enqueueLocked(ctx context.Context, t *sql.Tx, chRow, sender, recipient int64, body string, ttlSec int) (*Message, error) {
	// The scope is derived from the channel rather than passed in, so the two can
	// never disagree: a caller holding a rowid cannot accidentally file a message
	// into another channel's stream.
	var chPublicID string
	if err := t.QueryRowContext(ctx, `SELECT channel_id FROM channel WHERE id=?`, chRow).Scan(&chPublicID); err != nil {
		return nil, err
	}
	scope := channelScope(chPublicID)

	var unacked int
	if err := t.QueryRowContext(ctx, `
SELECT COUNT(*) FROM delivery d JOIN message m ON m.id = d.message_row
 WHERE m.scope_id = ? AND d.state IN ('queued','delivered')`, scope).
		Scan(&unacked); err != nil {
		return nil, err
	}
	if unacked >= s.lim().MaxUnackedPerChannel {
		return nil, ErrBackpressure
	}
	seq, err := nextScopeSeq(ctx, t, scope)
	if err != nil {
		return nil, err
	}
	messageID, err := randBase62(22)
	if err != nil {
		return nil, err
	}
	senderParty, err := endpointParty(ctx, t, sender)
	if err != nil {
		return nil, err
	}
	recipientParty, err := endpointParty(ctx, t, recipient)
	if err != nil {
		return nil, err
	}
	if _, err := t.ExecContext(ctx, `
INSERT INTO message(message_id, scope_id, scope_seq, channel_id, sender_endpoint, sender_party,
                    body, body_sha256, body_bytes, expires_at, kind)
VALUES(?,?,?,?,?,?,?,?,?, strftime('%Y-%m-%dT%H:%M:%fZ','now',?), ?)`,
		messageID, scope, seq, chRow, sender, senderParty,
		body, sha256Hex(body), len(body), nowExpr(ttlSec), "message"); err != nil {
		return nil, err
	}
	if _, err := t.ExecContext(ctx, `
INSERT INTO delivery(message_row, party_key, recipient_endpoint)
SELECT id, ?, ? FROM message WHERE message_id = ?`,
		recipientParty, recipient, messageID); err != nil {
		return nil, err
	}
	return messageByID(ctx, t, messageID)
}

// Poll returns the un-acknowledged messages this endpoint may read, oldest first,
// and counts a delivery attempt for each.
//
// TWO REGIMES, and which one applies depends on whether the endpoint is bound to a
// station (docs/STATIONS.md S4).
//
// UNBOUND — the shipped behaviour, unchanged. The endpoint is the sole reader of
// its own mail. Poll is a pure read of DELIVERABILITY: being polled never hides a
// message from the next poll, only Ack advances state. That is what makes a lost
// poll response harmless — the messages simply come back — and it is why
// "delivered" is an informational timestamp rather than a gate.
//
// BOUND — the STATION owns the inbox and this endpoint is one of possibly several
// credentialed readers, so delivery becomes CLAIM-ONCE. The first reader to poll a
// message claims it, and while the claim holds, that message is hidden from the
// station's other readers. This deliberately weakens the "polling never hides
// anything" property above, and it has to: without it, two sessions staffing one
// station would both act on the same message, which is precisely the shared-inbox
// accident the per-endpoint secret exists to prevent.
//
// The claim is a LEASE, not a transfer of ownership. When it expires unacknowledged
// the message returns to the unclaimed tail and may reach a DIFFERENT reader than
// first saw it. Without the lease, a session that claims and then dies strands its
// messages permanently and COMM's C6 promise — a message delivered but never acted
// upon comes back — would be false.
//
// The ordering promise weakens accordingly, and the tool description says so: from
// "per channel and direction" to "per channel and direction, across the station's
// readers". Two sessions polling one station see a PARTITIONED stream and neither
// sees the whole order. That is the price of letting a second session help without
// severing the first.
func (s *Store) Poll(ctx context.Context, ep *Endpoint, limit int) ([]Message, error) {
	// Asking for MORE than the ceiling must give you the CEILING, not less than it.
	// This used to collapse both cases to 50: measured with 64 queued, limit=100
	// returned 64 but limit=101 returned 50, so a hub asking for everything got
	// half — a trap that punishes exactly the caller trying to drain its backlog.
	if limit > 100 {
		limit = 100
	}
	if limit <= 0 {
		limit = 50
	}
	var out []Message
	err := s.tx(ctx, func(t *sql.Tx) error {
		// The recipient predicate is the whole difference between the two regimes.
		// Unbound: my own rowid, exactly as before. Bound: any endpoint of my
		// station, so a replacement session inherits the mail addressed to the
		// endpoint it replaced.
		//
		// The bound form deliberately still includes this endpoint's OWN rowid. S7
		// names one skew direction that actually occurs — ken.db restored backwards
		// while comm.db stays current — and after it, comm.db rows carry a station_id
		// that resolves to nothing. Without the `OR`, such a reader would poll a
		// station that no longer exists and see none of its own mail. With it, it
		// degrades to exactly the unbound behaviour, which is what "treated as
		// unbound rather than as an error" has to mean in practice.
		// The inbox is now the set of DELIVERY rows for my party. The old query
		// asked "is this message addressed to my endpoint, or to any endpoint of my
		// station" at read time; the party key answers that at write time, so the
		// widening is storage rather than a predicate.
		//
		// The `OR d.party_key = ?` on the endpoint form survives for the skew case S7
		// names — ken.db restored backwards while comm.db stays current, leaving rows
		// whose station_id resolves to nothing. Such a reader still finds mail filed
		// under its own rowid, degrading to exactly the unbound behaviour, which is
		// what "treated as unbound rather than as an error" has to mean in practice.
		party := `d.party_key = ?`
		args := []any{endpointPartyKey(ep.ID)}
		if ep.StationID != "" {
			party = `(d.party_key = ? OR d.party_key = ?)`
			args = []any{stationParty(ep.StationID), endpointPartyKey(ep.ID)}
		}
		rows, err := t.QueryContext(ctx, `
SELECT m.message_id, d.party_key
FROM delivery d
JOIN message m ON m.id = d.message_row
LEFT JOIN channel c ON c.id = m.channel_id
WHERE `+party+`
  AND d.state IN ('queued','delivered')
  AND m.expires_at > strftime('%Y-%m-%dT%H:%M:%fZ','now')
  AND (c.id IS NULL OR c.state='open')
  AND (d.claimed_by_endpoint IS NULL
       OR d.claimed_by_endpoint = ?
       OR d.claim_expires_at <= strftime('%Y-%m-%dT%H:%M:%fZ','now'))
ORDER BY m.scope_seq, m.id
LIMIT ?`, append(args, ep.ID, limit)...)
		if err != nil {
			return err
		}
		// The party is carried per row rather than recomputed. The predicate above
		// deliberately matches BOTH forms for a bound endpoint (the S7 skew case), so
		// assuming the station form here would silently fail to settle any row filed
		// under the endpoint's own rowid — re-polled forever, no error, no signal.
		type pollRow struct{ id, party string }
		var ids []pollRow
		for rows.Next() {
			var r pollRow
			if err := rows.Scan(&r.id, &r.party); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, r)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()

		for _, pr := range ids {
			id, deliveryParty := pr.id, pr.party
			// Claiming is conditional on the message still being claimable, so two
			// readers of one station racing on the same row cannot both take it:
			// exactly one UPDATE matches. The read above is advisory; THIS is the
			// arbiter. An unbound endpoint writes no claim at all — it is the sole
			// reader of its own mail, so a claim would be bookkeeping with no reader
			// to exclude.
			if ep.StationID != "" {
				res, err := t.ExecContext(ctx, `
UPDATE delivery
SET claimed_by_endpoint=?,
    claim_expires_at=strftime('%Y-%m-%dT%H:%M:%fZ','now', ?)
WHERE message_row = (SELECT id FROM message WHERE message_id=?)
  AND party_key = ?
  AND (claimed_by_endpoint IS NULL
       OR claimed_by_endpoint = ?
       OR claim_expires_at <= strftime('%Y-%m-%dT%H:%M:%fZ','now'))`,
					ep.ID, fmt.Sprintf("+%d seconds", s.lim().ClaimLeaseSeconds), id,
					deliveryParty, ep.ID)
				if err != nil {
					return err
				}
				// Lost the race: another reader of this station claimed it between the
				// SELECT and here. Skip it rather than returning a message this reader
				// does not hold.
				if n, _ := res.RowsAffected(); n == 0 {
					continue
				}
			}
			// FIRST DELIVERY ARMS BOTH CLOCKS, and only the first: the COALESCE
			// guards on first_delivered_at mean a redelivery does not restart
			// either one, or a peer could hold a message open indefinitely by
			// polling without acking.
			//
			//   expires_at        -> now + MessageTTL. Until this moment the row
			//                        carried the UNDELIVERED backstop, because a
			//                        message nobody has seen has not had its
			//                        chance yet and must survive a weekend.
			//   reply_deadline_at -> now + ReplyDeadline, when a response is owed.
			//
			// Both were previously stamped at SEND, which ran them during exactly
			// the window in which the recipient could not act.
			// expires_at stays on MESSAGE: there is one body with one lifetime, and it
			// switches from the undelivered backstop to the delivered TTL on first
			// delivery to ANYONE.
			//
			// RUN BEFORE the delivery stamp below, and guarded on nothing having been
			// delivered yet. Running it after and counting delivered rows looks
			// equivalent and is not: on a REDELIVERY the count is unchanged, so the
			// expiry was restamped on every poll and a peer could hold a message open
			// forever by polling without acking — precisely what
			// TestDeliveryArmsTheClocksOnceAndOnlyOnce forbids.
			//
			// It failed 2 runs in 5. Both statements usually land in the same
			// millisecond, so the restamp was invisible unless the clock ticked between
			// them: a real defect wearing a flaky test.
			if _, err := t.ExecContext(ctx, `
UPDATE message
SET expires_at = strftime('%Y-%m-%dT%H:%M:%fZ','now',?)
WHERE message_id=?
  AND NOT EXISTS (SELECT 1 FROM delivery d
                   WHERE d.message_row = message.id AND d.first_delivered_at IS NOT NULL)`,
				nowExpr(s.lim().MessageTTLSeconds), id); err != nil {
				return err
			}

			// The per-recipient half: state, the redelivery counter and the reply
			// deadline all live on THIS party's delivery row now, so one recipient
			// reading does not advance another's clock.
			if _, err := t.ExecContext(ctx, `
UPDATE delivery
SET state='delivered',
    delivery_count = delivery_count + 1,
    reply_deadline_at = CASE WHEN first_delivered_at IS NULL
                              AND (SELECT requires_response FROM message WHERE id = message_row) = 1
                             THEN strftime('%Y-%m-%dT%H:%M:%fZ','now',?) ELSE reply_deadline_at END,
    first_delivered_at = COALESCE(first_delivered_at, strftime('%Y-%m-%dT%H:%M:%fZ','now'))
WHERE message_row = (SELECT id FROM message WHERE message_id=?)
  AND party_key = ?`,
				nowExpr(s.lim().ReplyDeadlineSeconds), id, deliveryParty); err != nil {
				return err
			}
			m, err := messageByID(ctx, t, id)
			if err != nil {
				return err
			}
			out = append(out, *m)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Ack marks a message PROCESSED — not merely received — and drops its body.
//
// The distinction is deliberate and belongs in the instruction text too: a model
// should ack after acting, so that a turn truncated mid-processing leaves the
// message to be redelivered rather than silently lost.
//
// Acking an unknown or already-acked message succeeds. Idempotency is required
// because the transport is at-least-once: a retried ack after a lost response must
// not surface as an error the model then tries to "fix".
//
// A message that requires a response keeps its body until the reply arrives or its
// deadline passes: a responder that crashed and recovered plausibly needs to
// re-read what it owes.
// RETURNS HOW MANY DELIVERIES IT ACTUALLY SETTLED, and that number is the whole point.
//
// This call could not fail. It ran an UPDATE and discarded the row count, so a fabricated
// message id, an empty string, and acking a message addressed to somebody else ALL returned
// success — in the one call whose entire contract is "I have PROCESSED this", and which the
// instructions most insist a session trust.
//
// It was found the expensive way: a session ran with the WRONG endpoint's credentials, acked
// on it, got ok:true, and had no signal at all. Nothing was lost, because ack-means-processed
// plus redelivery meant the bogus ack settled nothing and the message came back on the correct
// endpoint — but the session believed it was done.
//
// THE FIX IS NOT TO MAKE A BAD ACK FAIL HARD. That redelivery is the safety net, and an ack
// that errors on an unknown id would break the legitimate case it exists for: acking something
// already settled, or already swept, must stay harmless. The no-op stays; it stops being
// SILENT. A caller reading acked=0 knows it settled nothing and can ask why; a caller reading
// ok:true knows nothing at all.
func (s *Store) Ack(ctx context.Context, ep *Endpoint, messageID string) (int, error) {
	pred, pargs := partyPredicate(ep, "")
	args := append([]any{ep.ID, messageID}, pargs...)
	res, err := s.W.ExecContext(ctx, `
UPDATE delivery
SET state='acked',
    acked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),
    acked_by_endpoint=?,
    claimed_by_endpoint=NULL, claim_expires_at=NULL
WHERE message_row = (SELECT id FROM message WHERE message_id=?)
  AND `+pred+`
  AND state IN ('queued','delivered')`, args...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		// Nothing settled, so there is nothing to blank and no reason to spend the query.
		return 0, nil
	}
	return int(n), s.blankIfFullySettled(ctx, messageID)
}

// partyPredicate is Poll's recipient rule, stated ONCE so Ack and AckUpTo cannot
// drift from it — and the drift that matters has a direction: an ack scoped to the
// acking endpoint's own rowid silently matches nothing when the message was addressed
// to a different endpoint of the same station, so it comes back on the next poll and
// a replacement session loops on mail it has already acted upon. One ack settles it
// for the STATION (S4).
//
// Both forms are matched for a bound endpoint, for the reason Poll gives: after a
// backwards ken.db restore a station_id can resolve to nothing, and such a reader must
// still settle the mail filed under its own rowid.
// The alias is taken as an argument rather than pasted on by the caller: prefixing
// `d.` onto a parenthesised OR produces `d.(party_key = ? OR …)`, which is not SQL.
// Caught by TestAReplacementReaderCanReplyAndAckCumulatively rather than by reading.
func partyPredicate(ep *Endpoint, alias string) (string, []any) {
	if alias != "" {
		alias += "."
	}
	if ep.StationID != "" {
		return `(` + alias + `party_key = ? OR ` + alias + `party_key = ?)`,
			[]any{stationParty(ep.StationID), endpointPartyKey(ep.ID)}
	}
	return alias + `party_key = ?`, []any{endpointPartyKey(ep.ID)}
}

// queuedForEndpoint counts what is waiting for this endpoint EVERYWHERE, for the
// waiting_for_you warning on a send.
//
// PARTY-WIDE, NOT SCOPE-LOCAL, and the second reason is not optional:
//
//  1. The instruction a session holds is scope-agnostic — "mail was already waiting for
//     you when this went out". It has said that since 1.6.0, and a session that captured
//     it will never see a corrected version, because tool descriptions pin at conversation
//     start. Broadening the number makes text already in the field MORE true.
//  2. Scope-local is a permanent zero on the broadcast path. A broadcast's scope is
//     b:<senderParty> and Broadcast excludes the sender from its own audience, so no
//     delivery addressed to the sender can ever exist there. Scope-local is not a weaker
//     answer on that path; it is a dead field.
//
// Shares Poll's predicates for the same reason pending.go does: a warning about mail a
// poll would refuse sends a session looking for something it cannot have.
func queuedForEndpoint(ctx context.Context, t *sql.Tx, ep *Endpoint) (int, error) {
	pred, args := partyPredicate(ep, "d")
	var n int
	err := t.QueryRowContext(ctx, `
SELECT COUNT(*)
  FROM delivery d
  JOIN message m ON m.id = d.message_row
  LEFT JOIN channel c ON c.id = m.channel_id
 WHERE `+pred+`
   AND d.state = 'queued'
   AND m.expires_at > strftime('%Y-%m-%dT%H:%M:%fZ','now')
   AND (c.id IS NULL OR c.state='open')`, args...).Scan(&n)
	return n, err
}

// blankIfFullySettled drops a body once NO recipient still owes anything.
//
// With one recipient this is exactly the old inline behaviour. With several it is the
// only correct rule: blanking when the FIRST recipient acks would destroy the text for
// everyone who has not read it yet — the retention defect that cost 97% of bodies
// before 1.6.0, rebuilt from a new cause.
//
// Retention above 0 means the sweep governs and this does nothing; at 0 it means
// "destroy on settling", which is the documented way to ask for the old behaviour
// deliberately.
func (s *Store) blankIfFullySettled(ctx context.Context, messageID string) error {
	if s.lim().BodyRetentionSeconds > 0 {
		return nil
	}
	_, err := s.W.ExecContext(ctx, `
UPDATE message SET body=NULL
WHERE message_id=? AND body IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM delivery d
                   WHERE d.message_row = message.id
                     AND d.state IN ('queued','delivered'))`, messageID)
	return err
}

// AckUpTo settles everything up to `seq` in one scope — a channel OR A ROOM.
//
// IT USED TO GATE ON ChannelFor, so passing a room id returned the caller-safe text written
// for comm_send: "address it with to_room instead of channel_id". comm_ack HAS NO to_room, so
// a session holding a room id was told to use a parameter that does not exist on the call it
// was making, and looped. Measured: acking eight room messages took eight separate calls.
//
// The room id is accepted in `channel_id` deliberately, rather than adding a to_room parameter.
// A session that has just polled room mail holds a room id and one addressing parameter, and
// that is what it will try; a new parameter would also have to be discovered, and tool schemas
// pin at conversation start.
func (s *Store) AckUpTo(ctx context.Context, ep *Endpoint, scopeID string, seq int64) (int, error) {
	scope, err := s.cumulativeAckScope(ctx, ep, scopeID)
	if err != nil {
		return 0, err
	}
	// The range is over the SCOPE sequence — one ascending stream across every sender
	// rather than two interleaved ones. That is what makes a cumulative ack safe:
	// under the old per-(channel, sender) numbering both directions reused the same
	// low numbers, so acking up to 2 could settle mail from the other direction that
	// nobody had read.
	//
	// Collected then acked one at a time, through Ack, so the body-blanking rule lives
	// in exactly one place. A range that settled rows with its own UPDATE is how the
	// two paths drifted apart the first time.
	pred, pargs := partyPredicate(ep, "d")
	args := append([]any{scope, seq}, pargs...)
	rows, err := s.W.QueryContext(ctx, `
SELECT m.message_id FROM delivery d JOIN message m ON m.id = d.message_row
 WHERE m.scope_id=? AND m.scope_seq<=? AND d.state='delivered' AND `+pred, args...)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	// SUMS WHAT EACH ACK ACTUALLY SETTLED, not len(ids).
	//
	// The two differ only when a row selected as a candidate is settled by somebody else
	// before this loop reaches it — another endpoint of the SAME station racing the same
	// inbox, which is the one case stations make ordinary. NO TEST COVERS THAT: the select
	// and the acks happen inside this function, so a single-threaded test cannot interleave
	// anything between them, and the two expressions are indistinguishable from outside.
	// Stated rather than papered over, because a mutation swapping them survives the suite
	// and someone will eventually "simplify" it.
	total := 0
	for _, id := range ids {
		n, err := s.Ack(ctx, ep, id)
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

// cumulativeAckScope resolves an id the caller passed as `channel_id` to the scope it may
// cumulatively ack in — refusing anything it does not belong to.
//
// The room branch keys on MEMBERSHIP, not existence, for the same reason the room-vs-channel
// error does: telling a non-member "that is a room" confirms the room exists, which is exactly
// the oracle comm_open_channel's uniform refusal is built to close.
func (s *Store) cumulativeAckScope(ctx context.Context, ep *Endpoint, id string) (string, error) {
	if _, _, err := s.ChannelFor(ctx, ep, id); err == nil {
		return channelScope(id), nil
	} else if !errors.Is(err, ErrNotFound) {
		// A real failure — a revoked channel, a database error — is not a licence to go
		// looking for a room with the same id.
		return "", err
	}
	if s.callerIsInRoom(ctx, ep, id) {
		return roomScope(id), nil
	}
	return "", CallerSafe(fmt.Errorf("%w: no channel or room %q that you belong to. "+
		"comm_ack takes the SAME id you polled the message from — a room_id works here, in channel_id", ErrNotFound, id))
}

// PendingReplies lists this endpoint's sent messages that still owe a response.
// Exposed as a query so a sender can ask what is outstanding rather than inferring
// it from a reference message that may already have been superseded.
func (s *Store) PendingReplies(ctx context.Context, ep *Endpoint) ([]Message, error) {
	rows, err := s.R.QueryContext(ctx, `
SELECT m.message_id FROM message m
WHERE m.sender_endpoint=? AND m.requires_response=1 AND m.answered_at IS NULL
  AND EXISTS (SELECT 1 FROM delivery d WHERE d.message_row = m.id AND d.state <> 'expired')
ORDER BY m.created_at`, ep.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Message, 0, len(ids))
	for _, id := range ids {
		m, err := s.MessageByID(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, nil
}

// MessageByID loads one message by its public id.
func (s *Store) MessageByID(ctx context.Context, messageID string) (*Message, error) {
	return messageByID(ctx, s.R, messageID)
}

// rowQuerier is satisfied by both *sql.DB and *sql.Tx, so the same loader serves
// a plain read and a read inside an open write transaction.
type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Sweep enforces every time-based rule in one pass and returns what it changed.
//
// Runs on a cadence of a minute or less, deliberately NOT folded into the hourly
// housekeeping loop: at a sustained send rate a single sender writes hundreds of
// megabytes before an hourly sweep first runs, which is why a TTL is not a quota
// and must not be mistaken for one.
func (s *Store) Sweep(ctx context.Context) (expired, purged int64, err error) {
	var unlink []string
	err = s.tx(ctx, func(t *sql.Tx) error {
		// 1. Expire un-acked messages past their TTL and drop their bodies. This
		//    covers BOTH queued and delivered-but-never-acked: a message polled by a
		//    session that then died must not live forever.
		//
		//    NOTHING IS COLLECTED FOR NOTIFICATION HERE ANY MORE (slice 4). This step
		//    used to read the rows it was about to change so the sweep could WRITE each
		//    sender a status message. The sender is still told — comm_poll now DERIVES
		//    the notice from these very rows (notice.go) — but the sweep no longer
		//    inserts, which is what makes it safe: a pass whose job is deleting rolls
		//    back its deletions when an insert fails, and that is exactly how one unread
		//    ROOM message stopped expiry, body retention, the metadata purge, file
		//    cleanup and idle-endpoint removal in 3.0.0 and 3.0.1.
		// Expiry marks the state but does NOT blank a body nobody ever read.
		//
		// It used to blank unconditionally, and that is how a 4 661-byte message
		// requiring a response — sent 2026-08-02, expired undelivered a day later —
		// became permanently unknowable to both parties. The sender is told it
		// expired (step 3); keeping the text means "expired" is a fact they can act
		// on rather than a hole. A DELIVERED message is different: the recipient had
		// it and did nothing, so it follows the ordinary retention rule.
		res, err := t.ExecContext(ctx, `
UPDATE delivery
   SET state='expired'
 WHERE state IN ('queued','delivered')
   AND message_row IN (SELECT id FROM message
                        WHERE expires_at <= strftime('%Y-%m-%dT%H:%M:%fZ','now'))`)
		if err != nil {
			return err
		}
		expired, _ = res.RowsAffected()

		// The BODY is one object with one lifetime, so it is blanked on the message
		// and only once NO recipient is still owed it. `first_delivered_at IS NOT
		// NULL` becomes "somebody actually received this", asked of the audience.
		if _, err := t.ExecContext(ctx, `
UPDATE message SET body=NULL
 WHERE body IS NOT NULL
   AND expires_at <= strftime('%Y-%m-%dT%H:%M:%fZ','now')
   AND NOT EXISTS (SELECT 1 FROM delivery d WHERE d.message_row = message.id
                                              AND d.state IN ('queued','delivered'))
   AND (? OR EXISTS (SELECT 1 FROM delivery d WHERE d.message_row = message.id
                                                AND d.first_delivered_at IS NOT NULL))`,
			s.lim().BodyRetentionSeconds <= 0); err != nil {
			return err
		}

		// 2. A requires_response message whose deadline passed unanswered: drop the
		//    retained body, because nothing is owed any more. The requester is told by
		//    the same derived path as an expiry — without it a session whose peer died
		//    waits forever and reply_deadline_at would be decoration — but the telling
		//    happens at poll time, not here.
		// RETENTION decides when a settled body goes — not the reply deadline.
		//
		// The old pass keyed on reply_deadline_at, so an acked message's text died
		// one hour after it was SENT regardless of when it was read, and a message
		// that asked no question was blanked at ack with no window at all. Between
		// them those two rules destroyed 97% of one deployment's bodies through the
		// documented poll/act/ack path.
		//
		// With retention at 0 the body is already gone at ack, so this pass finds
		// nothing and the historical behaviour is preserved exactly.
		//
		// SCOPED TO DELIVERED MAIL. Retention runs from the moment a message
		// SETTLED, and a message nobody ever received has no settle moment — there
		// is no expired_at column and created_at is the wrong clock, because it
		// would blank an unread body purely for having waited a long time, which is
		// precisely the wait the delivery anchor exists to permit. An unread
		// expired message therefore keeps its text until the metadata purge removes
		// the whole row, which is bounded by MetadataTTLSeconds.
		// Runs at EVERY retention value including zero. Guarding on r > 0 meant an
		// operator setting retention to 0 during a growth incident — the first remedy
		// its own help text offers — got "blank at ack from now on" for new mail and
		// "keep forever" for everything already retained, so the remedy provided no
		// relief on the bytes that prompted it. At zero the window is `now`, which
		// reclaims the backlog on the next sweep.
		{
			r := s.lim().BodyRetentionSeconds
			if r < 0 {
				r = 0
			}
			// The retention window runs from when the message settled for its LAST
			// recipient, not its first. With one recipient that is the same instant
			// and this is unchanged; with several, keying on the earliest would
			// destroy the text while somebody was still entitled to it.
			if _, err := t.ExecContext(ctx, `
UPDATE message SET body=NULL
WHERE body IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM delivery d WHERE d.message_row = message.id
                                             AND d.state IN ('queued','delivered'))
  AND EXISTS (SELECT 1 FROM delivery d WHERE d.message_row = message.id
                                         AND d.first_delivered_at IS NOT NULL)
  AND (SELECT MAX(COALESCE(d.acked_at, d.first_delivered_at)) FROM delivery d
        WHERE d.message_row = message.id)
      <= strftime('%Y-%m-%dT%H:%M:%fZ','now',?)`,
				nowExpr(-r)); err != nil {
				return err
			}
		}

		// 3. Purge settled metadata past the retention window.
		//
		//    ANCHORED AT SETTLE TIME, not at creation. Keyed on created_at, the window
		//    was already spent before a long-lived message ever settled: with the
		//    shipped 30-day undelivered backstop and 7-day metadata TTL, a message
		//    that expired unread was 23 days past the horizon the instant it got
		//    there, so the very next sweep deleted it. That made "expiry keeps the
		//    body of a message nobody read" — the whole point of not blanking on the
		//    expiry path — a no-op under the default configuration. The property was
		//    real; the defaults made it unreachable.
		//
		//    acked_at is the settle moment for an acked message; expires_at is the
		//    settle moment for an expired one, since that is precisely when it
		//    expired. Both columns already exist, so no migration.
		//
		//    This also RETIRES the ordering rule an operator previously had to know —
		//    that metadata TTL must exceed the message TTL or the audit row vanishes
		//    on settling. Measured from settle, the two are independent.
		res, err = t.ExecContext(ctx, `
DELETE FROM message
WHERE NOT EXISTS (SELECT 1 FROM delivery d WHERE d.message_row = message.id
                                              AND d.state IN ('queued','delivered'))
  AND COALESCE((SELECT MAX(d.acked_at) FROM delivery d WHERE d.message_row = message.id),
               expires_at) <= strftime('%Y-%m-%dT%H:%M:%fZ','now',?)`,
			nowExpr(-s.lim().MetadataTTLSeconds))
		if err != nil {
			return err
		}
		purged, _ = res.RowsAffected()

		// 4. Drop pairing codes that were never redeemed.
		if _, err := t.ExecContext(ctx, `
DELETE FROM pairing_code
WHERE consumed_at IS NULL AND expires_at <= strftime('%Y-%m-%dT%H:%M:%fZ','now')`); err != nil {
			return err
		}

		// 5. Idle endpoints and their settled channels. Sessions register once each
		//    and never unregister, so under NORMAL usage these rows accumulate
		//    forever — the operator console and comm.db grow without bound, and an
		//    agent loop could add rows freely. An endpoint unseen for the retention
		//    window has no live session behind it; its channels cascade.
		//
		//    The guard is load-bearing: a threshold of 0 would make "idle for 0
		//    seconds" = "idle now" and delete EVERY endpoint with no message traffic
		//    yet — including one that just registered and is mid-handshake. A retention
		//    sweep must fail SAFE (do nothing) on a non-positive window, never sweep
		//    everything. This is exactly the failure a dropped settings mapping caused
		//    in 1.2.0, so the sweep now refuses to run without a positive window
		//    regardless of how the window was configured.
		//
		//    A CHANNEL SEAT IS NOT AN IDLE ROW, however quiet its endpoint is.
		//    `channel.endpoint_a/endpoint_b` are ON DELETE CASCADE, and the sentence
		//    above — "its channels cascade" — was written when an endpoint WAS the party.
		//    Under stations it is a disposable reader of a relationship a human
		//    authorised, and the successor is promised it inherits that relationship
		//    without re-pairing. So a seat whose messages had merely aged out took the
		//    CHANNEL with it, plus any of the successor's own queued mail on it, plus the
		//    attachment rows that are the only record of which bytes to unlink — silently,
		//    since Sweep reports only expired and purged counts.
		//
		//    The endpoint rows are still bounded: the channel-deletion pass below releases
		//    a seat as soon as its channel is gone, and the next sweep collects it.
		//
		//    Both guards must exclude NULL explicitly. `id NOT IN (…, NULL)` is NULL, not
		//    true, so a single NULL in either set would silently stop the sweep deleting
		//    anything at all — a retention leak that presents as no error and no log line.
		if idle := s.lim().EndpointIdleTTLSeconds; idle > 0 {
			if _, err := t.ExecContext(ctx, `
DELETE FROM endpoint
WHERE last_seen_at <= strftime('%Y-%m-%dT%H:%M:%fZ','now',?)
  AND id NOT IN (SELECT sender_endpoint FROM message
                 UNION SELECT recipient_endpoint FROM delivery WHERE recipient_endpoint IS NOT NULL)
  AND id NOT IN (SELECT sender_endpoint FROM attachment WHERE stored_bytes > 0
                 UNION SELECT recipient_endpoint FROM attachment WHERE stored_bytes > 0)
  AND id NOT IN (SELECT endpoint_a FROM channel
                 UNION SELECT endpoint_b FROM channel WHERE endpoint_b IS NOT NULL)`,
				nowExpr(-idle)); err != nil {
				return err
			}
		}
		// Revoked channels with nothing left referencing them.
		if _, err := t.ExecContext(ctx, `
DELETE FROM channel
WHERE state='revoked' AND revoked_at <= strftime('%Y-%m-%dT%H:%M:%fZ','now',?)
  AND id NOT IN (SELECT channel_id FROM message)
  AND id NOT IN (SELECT channel_id FROM attachment)`,
			nowExpr(-s.lim().MetadataTTLSeconds)); err != nil {
			return err
		}

		// 6. The file-exchange half: attachment expiry, done-marking, grant purge.
		//    Byte deletion happens after commit — filesystem calls stay out of the
		//    write transaction.
		unlink, err = s.sweepFiles(ctx, t)
		return err
	})
	if err == nil {
		for _, id := range unlink {
			// Free the budget only for bytes that are really gone: a failed unlink
			// leaves stored_bytes intact so the next sweep retries the same row.
			// ENOENT counts as gone — an earlier sweep already removed it.
			if rmErr := os.Remove(s.FilePath(id)); rmErr == nil || os.IsNotExist(rmErr) {
				if cErr := s.ClearStoredBytes(ctx, id); cErr != nil {
					log.Printf("comm: clear stored bytes for %s: %v", id, cErr)
				}
			} else {
				log.Printf("comm: could not remove attachment bytes (will retry next sweep): %v", rmErr)
			}
		}
		s.sweepPartFiles()
	}
	return expired, purged, err
}

// messageByID loads a message from any querier (pool or open transaction).
func messageByID(ctx context.Context, q rowQuerier, messageID string) (*Message, error) {
	var (
		m        Message
		body     sql.NullString
		chID     sql.NullString
		replyTo  sql.NullString
		deadline sql.NullString
		reqResp  int
	)
	var (
		attID    sql.NullString
		attName  sql.NullString
		attSize  sql.NullInt64
		attSHA   sql.NullString
		attMode  sql.NullString
		attNonce sql.NullString
	)
	// State, delivery_count and the reply deadline are now PER RECIPIENT, so a
	// single-valued view of a message has to say which recipient it means. This one
	// aggregates: the LEAST settled state across the audience, the highest delivery
	// count, and the earliest live deadline.
	//
	// For every message that exists today the audience is one and this is exactly the
	// old behaviour. For a room it answers the question a SENDER asks — "has anybody
	// still not dealt with this" — which is the only question a single state can
	// honestly answer about many recipients. A caller needing per-recipient truth
	// reads `delivery` (slice 4's comm_outbox is that surface).
	err := q.QueryRowContext(ctx, `
SELECT m.message_id, c.channel_id, m.scope_id, COALESCE(se.station_id,''), m.audience_size,
       m.scope_seq, se.endpoint_id, m.body, m.requires_response,
       (SELECT r.message_id FROM message r WHERE r.id = m.reply_to),
       COALESCE((SELECT d.state FROM delivery d WHERE d.message_row = m.id
                  ORDER BY CASE d.state WHEN 'queued' THEN 0 WHEN 'delivered' THEN 1
                                        WHEN 'expired' THEN 2 ELSE 3 END LIMIT 1), 'queued'),
       COALESCE((SELECT MAX(d.delivery_count) FROM delivery d WHERE d.message_row = m.id), 0),
       m.body_bytes, m.created_at, m.expires_at,
       (SELECT MIN(d.reply_deadline_at) FROM delivery d WHERE d.message_row = m.id AND d.acked_at IS NULL),
       m.kind,
       a.attachment_id, a.name, a.size_bytes, a.sha256, a.transfer, a.nonce_sha256
FROM message m
LEFT JOIN channel c  ON c.id  = m.channel_id
JOIN endpoint se ON se.id = m.sender_endpoint
LEFT JOIN attachment a ON a.message_id = m.id
WHERE m.message_id=?`, messageID).
		Scan(&m.MessageID, &chID, &m.Scope, &m.SenderStationID, &m.AudienceSize,
			&m.Seq, &m.SenderEndpointID, &body, &reqResp,
			&replyTo, &m.State, &m.DeliveryCount, &m.BodyBytes, &m.CreatedAt, &m.ExpiresAt, &deadline, &m.Kind,
			&attID, &attName, &attSize, &attSHA, &attMode, &attNonce)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	m.Body = body.String
	m.ChannelID = chID.String
	m.RequiresResponse = reqResp == 1
	m.ReplyToMessageID = replyTo.String
	m.ReplyDeadlineAt = deadline.String
	if attID.Valid {
		m.File = &FileInfo{
			AttachmentID: attID.String, Name: attName.String, SizeBytes: attSize.Int64,
			SHA256: attSHA.String, Transfer: attMode.String, NonceSHA256: attNonce.String,
		}
	}
	return &m, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
