package comm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
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

	ttl := clampTTL(opts.TTLSeconds, s.lim().MessageTTLSeconds)

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
		var unacked int
		if err := t.QueryRowContext(ctx, `
SELECT COUNT(*) FROM message WHERE channel_id=? AND state IN ('queued','delivered')`, ch.ID).
			Scan(&unacked); err != nil {
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

		var deadline any
		if opts.RequiresResponse {
			deadline = nowExpr(s.lim().ReplyDeadlineSeconds)
		}

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
			// kept its body (Ack retains it precisely so a recovered responder can
			// re-read what it owes), and Sweep's later pass only considers rows with
			// replied_by IS NULL. Without this the answered request's body survived
			// until the metadata purge, days past the point it was needed.
			if _, err := t.ExecContext(ctx, `
UPDATE message SET replied_by=?,
       body = CASE WHEN state='acked' THEN NULL ELSE body END
WHERE id=? AND replied_by IS NULL`, newRow, replyToRow); err != nil {
				return err
			}
		}

		out, err = messageByID(ctx, t, messageID)
		return err
	})
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
	if limit <= 0 || limit > 100 {
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
			if _, err := t.ExecContext(ctx, `
UPDATE message
SET state='delivered',
    delivery_count = delivery_count + 1,
    first_delivered_at = COALESCE(first_delivered_at, strftime('%Y-%m-%dT%H:%M:%fZ','now'))
WHERE message_id=?`, id); err != nil {
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
    body = CASE WHEN requires_response=1 AND replied_by IS NULL THEN body ELSE NULL END
WHERE message_id=?
  AND recipient_endpoint IN (SELECT id FROM endpoint WHERE station_id=?)
  AND state IN ('queued','delivered')`, messageID, ep.StationID)
		return err
	}
	_, err := s.W.ExecContext(ctx, `
UPDATE message
SET state='acked',
    acked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),
    body = CASE WHEN requires_response=1 AND replied_by IS NULL THEN body ELSE NULL END
WHERE message_id=? AND recipient_endpoint=? AND state IN ('queued','delivered')`,
		messageID, ep.ID)
	return err
}

// AckUpTo cumulatively acks every message from one sender on a channel up to and
// including seq. Cumulative acking collapses ack chatter and is idempotent by
// construction, which is why it exists alongside per-message Ack.
func (s *Store) AckUpTo(ctx context.Context, ep *Endpoint, channelID string, seq int64) error {
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
    body = CASE WHEN requires_response=1 AND replied_by IS NULL THEN body ELSE NULL END
WHERE channel_id=? AND sender_endpoint=? AND seq<=? AND state IN ('queued','delivered')
  AND recipient_endpoint IN (SELECT id FROM endpoint WHERE station_id=?)`,
			ch.ID, peer, seq, ep.StationID)
		return err
	}
	_, err = s.W.ExecContext(ctx, `
UPDATE message
SET state='acked',
    acked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),
    body = CASE WHEN requires_response=1 AND replied_by IS NULL THEN body ELSE NULL END
WHERE channel_id=? AND sender_endpoint=? AND recipient_endpoint=? AND seq<=? AND state IN ('queued','delivered')`,
		ch.ID, peer, ep.ID, seq)
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
		res, err := t.ExecContext(ctx, `
UPDATE message SET state='expired', body=NULL
WHERE state IN ('queued','delivered') AND expires_at <= strftime('%Y-%m-%dT%H:%M:%fZ','now')`)
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
		if _, err := t.ExecContext(ctx, `
UPDATE message SET body=NULL
WHERE state='acked' AND body IS NOT NULL AND replied_by IS NULL
  AND reply_deadline_at IS NOT NULL
  AND reply_deadline_at <= strftime('%Y-%m-%dT%H:%M:%fZ','now')`); err != nil {
			return err
		}

		// 3. Deliver the failure notices through the ordinary poll path, so a client
		//    needs no new mechanism to learn about them. notified_at makes this
		//    exactly-once across repeating sweeps.
		for _, n := range append(expiring, overdue...) {
			if err := s.notifySender(ctx, t, n); err != nil {
				return err
			}
		}

		// 3. Purge settled metadata past the retention window. Bodies are long gone;
		//    this is the audit shell, bounded so comm.db cannot grow without limit.
		res, err = t.ExecContext(ctx, `
DELETE FROM message
WHERE state IN ('acked','expired')
  AND created_at <= strftime('%Y-%m-%dT%H:%M:%fZ','now',?)`, nowExpr(-s.lim().MetadataTTLSeconds))
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
	ttl := s.lim().MessageTTLSeconds
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
