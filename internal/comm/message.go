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
	MessageID        string
	ChannelID        string
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

	// WaitingForYou is how many messages were already queued or delivered FOR THE
	// SENDER on this channel at the moment of sending.
	//
	// A session that sends without reading what is already waiting answers a question
	// its peer has often moved past — measured on this project: a reply that
	// re-argued a point the peer had already conceded. The value of checking is not
	// the read, it is the pause before sending; a non-zero count here is the prompt
	// to take it. Send already computed this number for backpressure and discarded
	// it, so it costs one extra aggregate over a scan that was happening anyway.
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

	var out *Message
	err = s.tx(ctx, func(t *sql.Tx) error {
		// Idempotent resend: return the original rather than sending a second copy.
		if opts.IdempotencyKey != "" {
			var existing string
			err := t.QueryRowContext(ctx, `
SELECT message_id FROM message
WHERE channel_id=? AND sender_endpoint=? AND idempotency_key=?`,
				ch.ID, ep.ID, opts.IdempotencyKey).Scan(&existing)
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
		// One scan, two aggregates: the channel total that bounds backpressure, and
		// the SENDER's own share, which is what "you have mail waiting" means. The
		// total was already being computed and thrown away.
		var unacked, waitingForSender int
		if err := t.QueryRowContext(ctx, `
SELECT COUNT(*), COUNT(*) FILTER (WHERE recipient_endpoint = ?)
  FROM message WHERE channel_id=? AND state IN ('queued','delivered')`, ep.ID, ch.ID).
			Scan(&unacked, &waitingForSender); err != nil {
			return err
		}
		if unacked >= s.lim().MaxUnackedPerChannel {
			return ErrBackpressure
		}

		// Correlate a reply, if any. The referenced request must be on this channel
		// and addressed TO this sender — otherwise a reply could be pinned to an
		// unrelated message.
		var replyTo any
		var replyToRow int64
		if opts.ReplyToMessageID != "" {
			err := t.QueryRowContext(ctx, `
SELECT id FROM message WHERE message_id=? AND channel_id=? AND recipient_endpoint=?`,
				opts.ReplyToMessageID, ch.ID, ep.ID).Scan(&replyToRow)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			if err != nil {
				return err
			}
			replyTo = replyToRow
		}

		seq, err := nextSeq(ctx, t, ch.ID, ep.ID)
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
		var deadline any

		// Every placeholder is a plain `?`: mixing `?` with explicit `?N` in one
		// statement renumbers the auto-assigned ones and silently binds the wrong
		// values. The deadline is therefore passed twice rather than referenced twice.
		if _, err := t.ExecContext(ctx, `
INSERT INTO message(message_id, channel_id, seq, sender_endpoint, recipient_endpoint, idempotency_key,
                    body, body_sha256, body_bytes, requires_response, reply_to,
                    expires_at, reply_deadline_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,
       strftime('%Y-%m-%dT%H:%M:%fZ','now',?),
       CASE WHEN ? IS NULL THEN NULL ELSE strftime('%Y-%m-%dT%H:%M:%fZ','now',?) END)`,
			messageID, ch.ID, seq, ep.ID, peer, nullStr(opts.IdempotencyKey),
			body, sha256Hex(body), len(body), boolInt(opts.RequiresResponse), replyTo,
			nowExpr(ttl), deadline, deadline); err != nil {
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
			if _, err := t.ExecContext(ctx, `
UPDATE message SET replied_by=?
WHERE id=? AND replied_by IS NULL`, newRow, replyToRow); err != nil {
				return err
			}
		}

		out, err = messageByID(ctx, t, messageID)
		if out != nil {
			out.TTLClampedFrom, out.WaitingForYou = clampedFrom, waitingForSender
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
func (s *Store) enqueueLocked(ctx context.Context, t *sql.Tx, chRow, sender, recipient int64, body string, ttlSec int) (*Message, error) {
	return s.enqueueKind(ctx, t, chRow, sender, recipient, body, ttlSec, "message")
}

// enqueueKind is enqueueLocked plus the message kind. A 'status' row is authored
// by the SERVER about a message's fate and deliberately BYPASSES backpressure: a
// channel that is full is exactly when a failure signal matters most, and letting
// the cap suppress it would reintroduce the indefinite wait the signal exists to
// prevent.
func (s *Store) enqueueKind(ctx context.Context, t *sql.Tx, chRow, sender, recipient int64, body string, ttlSec int, kind string) (*Message, error) {
	if kind != "status" {
		var unacked int
		if err := t.QueryRowContext(ctx, `
SELECT COUNT(*) FROM message WHERE channel_id=? AND state IN ('queued','delivered')`, chRow).
			Scan(&unacked); err != nil {
			return nil, err
		}
		if unacked >= s.lim().MaxUnackedPerChannel {
			return nil, ErrBackpressure
		}
	}
	seq, err := nextSeq(ctx, t, chRow, sender)
	if err != nil {
		return nil, err
	}
	messageID, err := randBase62(22)
	if err != nil {
		return nil, err
	}
	if _, err := t.ExecContext(ctx, `
INSERT INTO message(message_id, channel_id, seq, sender_endpoint, recipient_endpoint,
                    body, body_sha256, body_bytes, expires_at, kind)
VALUES(?,?,?,?,?,?,?,?, strftime('%Y-%m-%dT%H:%M:%fZ','now',?), ?)`,
		messageID, chRow, seq, sender, recipient,
		body, sha256Hex(body), len(body), nowExpr(ttlSec), kind); err != nil {
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
		recipient := `m.recipient_endpoint = ?`
		args := []any{ep.ID}
		if ep.StationID != "" {
			recipient = `(r.station_id = ? OR m.recipient_endpoint = ?)`
			args = []any{ep.StationID, ep.ID}
		}
		rows, err := t.QueryContext(ctx, `
SELECT m.message_id
FROM message m
JOIN channel c ON c.id = m.channel_id
LEFT JOIN endpoint r ON r.id = m.recipient_endpoint
WHERE `+recipient+`
  AND m.state IN ('queued','delivered')
  AND m.expires_at > strftime('%Y-%m-%dT%H:%M:%fZ','now')
  AND c.state='open'
  AND (m.claimed_by_endpoint IS NULL
       OR m.claimed_by_endpoint = ?
       OR m.claim_expires_at <= strftime('%Y-%m-%dT%H:%M:%fZ','now'))
ORDER BY m.seq, m.id
LIMIT ?`, append(args, ep.ID, limit)...)
		if err != nil {
			return err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()

		for _, id := range ids {
			// Claiming is conditional on the message still being claimable, so two
			// readers of one station racing on the same row cannot both take it:
			// exactly one UPDATE matches. The read above is advisory; THIS is the
			// arbiter. An unbound endpoint writes no claim at all — it is the sole
			// reader of its own mail, so a claim would be bookkeeping with no reader
			// to exclude.
			if ep.StationID != "" {
				res, err := t.ExecContext(ctx, `
UPDATE message
SET claimed_by_endpoint=?,
    claim_expires_at=strftime('%Y-%m-%dT%H:%M:%fZ','now', ?)
WHERE message_id=?
  AND (claimed_by_endpoint IS NULL
       OR claimed_by_endpoint = ?
       OR claim_expires_at <= strftime('%Y-%m-%dT%H:%M:%fZ','now'))`,
					ep.ID, fmt.Sprintf("+%d seconds", s.lim().ClaimLeaseSeconds), id, ep.ID)
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
			if _, err := t.ExecContext(ctx, `
UPDATE message
SET state='delivered',
    delivery_count = delivery_count + 1,
    expires_at = CASE WHEN first_delivered_at IS NULL
                      THEN strftime('%Y-%m-%dT%H:%M:%fZ','now',?) ELSE expires_at END,
    reply_deadline_at = CASE WHEN first_delivered_at IS NULL AND requires_response=1
                             THEN strftime('%Y-%m-%dT%H:%M:%fZ','now',?) ELSE reply_deadline_at END,
    first_delivered_at = COALESCE(first_delivered_at, strftime('%Y-%m-%dT%H:%M:%fZ','now'))
WHERE message_id=?`,
				nowExpr(s.lim().MessageTTLSeconds), nowExpr(s.lim().ReplyDeadlineSeconds), id); err != nil {
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
func (s *Store) Ack(ctx context.Context, ep *Endpoint, messageID string) error {
	blankNow := s.lim().BodyRetentionSeconds <= 0
	// The recipient predicate mirrors Poll's, and it MUST: a bound reader can be
	// handed a message addressed to a DIFFERENT endpoint of its station — that is the
	// whole point of the station owning the inbox — so an ack scoped to this
	// endpoint's own rowid would silently match nothing. The message would then come
	// back on the next poll, and a replacement session would loop on mail it had
	// already acted upon. One ack settles it for the STATION (S4).
	if ep.StationID != "" {
		_, err := s.W.ExecContext(ctx, `
UPDATE message
SET state='acked',
    acked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),
    claimed_by_endpoint=NULL, claim_expires_at=NULL,
    body = CASE WHEN ? THEN NULL ELSE body END
WHERE message_id=?
  AND recipient_endpoint IN (SELECT id FROM endpoint WHERE station_id=?)
  AND state IN ('queued','delivered')`, blankNow, messageID, ep.StationID)
		return err
	}
	_, err := s.W.ExecContext(ctx, `
UPDATE message
SET state='acked',
    acked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),
    body = CASE WHEN ? THEN NULL ELSE body END
WHERE message_id=? AND recipient_endpoint=? AND state IN ('queued','delivered')`,
		blankNow, messageID, ep.ID)
	return err
}

// AckUpTo cumulatively acks every message from one sender on a channel up to and
// including seq. Cumulative acking collapses ack chatter and is idempotent by
// construction, which is why it exists alongside per-message Ack.
func (s *Store) AckUpTo(ctx context.Context, ep *Endpoint, channelID string, seq int64) error {
	blankNow := s.lim().BodyRetentionSeconds <= 0
	ch, peer, err := s.ChannelFor(ctx, ep, channelID)
	if err != nil {
		return err
	}
	// Same station-scoping as Ack, and for the same reason: a cumulative ack from a
	// replacement reader must settle the mail addressed to the endpoint it replaced.
	if ep.StationID != "" {
		_, err = s.W.ExecContext(ctx, `
UPDATE message
SET state='acked',
    acked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),
    claimed_by_endpoint=NULL, claim_expires_at=NULL,
    body = CASE WHEN ? THEN NULL ELSE body END
WHERE channel_id=? AND sender_endpoint=? AND seq<=? AND state='delivered'
  AND recipient_endpoint IN (SELECT id FROM endpoint WHERE station_id=?)`,
			blankNow, ch.ID, peer, seq, ep.StationID)
		return err
	}
	_, err = s.W.ExecContext(ctx, `
UPDATE message
SET state='acked',
    acked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),
    body = CASE WHEN ? THEN NULL ELSE body END
WHERE channel_id=? AND sender_endpoint=? AND recipient_endpoint=? AND seq<=? AND state='delivered'`,
		blankNow, ch.ID, peer, ep.ID, seq)
	return err
}

// PendingReplies lists this endpoint's sent messages that still owe a response.
// Exposed as a query so a sender can ask what is outstanding rather than inferring
// it from a reference message that may already have been superseded.
func (s *Store) PendingReplies(ctx context.Context, ep *Endpoint) ([]Message, error) {
	rows, err := s.R.QueryContext(ctx, `
SELECT message_id FROM message
WHERE sender_endpoint=? AND requires_response=1 AND replied_by IS NULL AND state<>'expired'
ORDER BY created_at`, ep.ID)
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
		//    The senders are collected BEFORE the update so each can be told (step 3):
		//    a message that dies unread is exactly the case where silence would leave
		//    the sender believing it was delivered.
		expiring, err := collectForNotice(ctx, t, "expired", `
SELECT id, channel_id, sender_endpoint, recipient_endpoint, message_id FROM message
WHERE state IN ('queued','delivered') AND kind='message' AND notified_at IS NULL
  AND expires_at <= strftime('%Y-%m-%dT%H:%M:%fZ','now')`)
		if err != nil {
			return err
		}
		// Expiry marks the state but does NOT blank a body nobody ever read.
		//
		// It used to blank unconditionally, and that is how a 4 661-byte message
		// requiring a response — sent 2026-08-02, expired undelivered a day later —
		// became permanently unknowable to both parties. The sender is told it
		// expired (step 3); keeping the text means "expired" is a fact they can act
		// on rather than a hole. A DELIVERED message is different: the recipient had
		// it and did nothing, so it follows the ordinary retention rule.
		res, err := t.ExecContext(ctx, `
UPDATE message
   SET state='expired',
       body = CASE WHEN ? OR first_delivered_at IS NOT NULL THEN NULL ELSE body END
WHERE state IN ('queued','delivered') AND expires_at <= strftime('%Y-%m-%dT%H:%M:%fZ','now')`,
			s.lim().BodyRetentionSeconds <= 0)
		if err != nil {
			return err
		}
		expired, _ = res.RowsAffected()

		// 2. A requires_response message whose deadline passed unanswered: drop the
		//    retained body (nothing is owed any more) and tell the requester, which is
		//    the whole reason deadlines exist — without it a session whose peer died
		//    waits forever, and reply_deadline_at would be decoration.
		overdue, err := collectForNotice(ctx, t, "reply_overdue", `
SELECT id, channel_id, sender_endpoint, recipient_endpoint, message_id FROM message
WHERE requires_response=1 AND replied_by IS NULL AND kind='message' AND notified_at IS NULL
  AND state IN ('queued','delivered','acked')
  AND reply_deadline_at IS NOT NULL
  AND reply_deadline_at <= strftime('%Y-%m-%dT%H:%M:%fZ','now')`)
		if err != nil {
			return err
		}
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
			if _, err := t.ExecContext(ctx, `
UPDATE message SET body=NULL
WHERE body IS NOT NULL
  AND state IN ('acked','expired')
  AND first_delivered_at IS NOT NULL
  AND COALESCE(acked_at, first_delivered_at) <= strftime('%Y-%m-%dT%H:%M:%fZ','now',?)`,
				nowExpr(-r)); err != nil {
				return err
			}
		}

		// 3. Deliver the failure notices through the ordinary poll path, so a client
		//    needs no new mechanism to learn about them. notified_at makes this
		//    exactly-once across repeating sweeps.
		for _, n := range append(expiring, overdue...) {
			if err := s.notifySender(ctx, t, n); err != nil {
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
WHERE state IN ('acked','expired')
  AND COALESCE(acked_at, expires_at) <= strftime('%Y-%m-%dT%H:%M:%fZ','now',?)`,
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
		if idle := s.lim().EndpointIdleTTLSeconds; idle > 0 {
			if _, err := t.ExecContext(ctx, `
DELETE FROM endpoint
WHERE last_seen_at <= strftime('%Y-%m-%dT%H:%M:%fZ','now',?)
  AND id NOT IN (SELECT sender_endpoint FROM message UNION SELECT recipient_endpoint FROM message)
  AND id NOT IN (SELECT sender_endpoint FROM attachment WHERE stored_bytes > 0)`,
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

// minNoticeTTLSeconds is the floor on a status notice's lifetime.
const minNoticeTTLSeconds = 3600

// notice identifies one message whose sender must be told of its fate. `to` is
// the endpoint that will RECEIVE the status message — the original sender for an
// expiry, the original requester for an overdue reply.
type notice struct {
	row      int64
	chRow    int64
	to       int64
	from     int64
	publicID string
	reason   string // "expired" | "reply_overdue"
}

// collectForNotice reads the rows a sweep step is about to change, so their
// senders can be notified. The query MUST select
// (id, channel_id, to_endpoint, from_endpoint, message_id) in that order, where
// to_endpoint is whoever needs telling — for both an expiry and an overdue reply
// that is the message's original SENDER, since they are the party still waiting.
func collectForNotice(ctx context.Context, t *sql.Tx, reason, query string) ([]notice, error) {
	rows, err := t.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []notice
	for rows.Next() {
		var n notice
		if err := rows.Scan(&n.row, &n.chRow, &n.to, &n.from, &n.publicID); err != nil {
			return nil, err
		}
		n.reason = reason
		out = append(out, n)
	}
	return out, rows.Err()
}

// notifySender enqueues a server-authored status message so a failure arrives
// through the ordinary poll path. The body is small, structured, and stable
// enough for a model to branch on without prose parsing.
func (s *Store) notifySender(ctx context.Context, t *sql.Tx, n notice) error {
	body := `{"status":"` + n.reason + `","message_id":"` + n.publicID + `"}`
	// A notice must outlive the condition that produced it. Deriving its lifetime
	// from the configured message TTL alone would make a very short TTL delete the
	// notice in the same sweep that created it — the failure signal would vanish
	// exactly where messages die fastest.
	// Sized against the UNDELIVERED backstop, not the post-delivery TTL. A notice
	// exists to tell a sender that their message died because nobody came for it, so
	// it has to outlive the same absence — a notice stamped with the delivered window
	// can expire before the peer whose silence it reports comes back. That was
	// harmless while both numbers were one value; the delivery anchor split them, and
	// this is the half that reports failures during absence.
	ttl := s.lim().UndeliveredTTLSeconds
	if m := s.lim().MessageTTLSeconds; m > ttl {
		ttl = m
	}
	if ttl < minNoticeTTLSeconds {
		ttl = minNoticeTTLSeconds
	}
	if _, err := s.enqueueKind(ctx, t, n.chRow, n.to, n.to, body, ttl, "status"); err != nil {
		// A closed or revoked channel has nobody to tell; that is not a sweep failure.
		if errors.Is(err, ErrChannelClosed) || errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}
	_, err := t.ExecContext(ctx,
		`UPDATE message SET notified_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, n.row)
	return err
}

// messageByID loads a message from any querier (pool or open transaction).
func messageByID(ctx context.Context, q rowQuerier, messageID string) (*Message, error) {
	var (
		m        Message
		body     sql.NullString
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
	err := q.QueryRowContext(ctx, `
SELECT m.message_id, c.channel_id, m.seq, se.endpoint_id, m.body, m.requires_response,
       (SELECT r.message_id FROM message r WHERE r.id = m.reply_to),
       m.state, m.delivery_count, m.body_bytes, m.created_at, m.expires_at, m.reply_deadline_at, m.kind,
       a.attachment_id, a.name, a.size_bytes, a.sha256, a.transfer, a.nonce_sha256
FROM message m
JOIN channel  c  ON c.id  = m.channel_id
JOIN endpoint se ON se.id = m.sender_endpoint
LEFT JOIN attachment a ON a.message_id = m.id
WHERE m.message_id=?`, messageID).
		Scan(&m.MessageID, &m.ChannelID, &m.Seq, &m.SenderEndpointID, &body, &reqResp,
			&replyTo, &m.State, &m.DeliveryCount, &m.BodyBytes, &m.CreatedAt, &m.ExpiresAt, &deadline, &m.Kind,
			&attID, &attName, &attSize, &attSHA, &attMode, &attNonce)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	m.Body = body.String
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
