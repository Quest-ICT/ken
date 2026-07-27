package comm

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"os"
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
func nextSeq(ctx context.Context, t *sql.Tx, chRow, sender int64) (int64, error) {
	if _, err := t.ExecContext(ctx, `
INSERT INTO channel_seq(channel_id, sender_endpoint, next_seq) VALUES(?,?,2)
ON CONFLICT(channel_id, sender_endpoint) DO UPDATE SET next_seq = next_seq + 1`,
		chRow, sender); err != nil {
		return 0, err
	}
	var next int64
	if err := t.QueryRowContext(ctx,
		`SELECT next_seq FROM channel_seq WHERE channel_id=? AND sender_endpoint=?`,
		chRow, sender).Scan(&next); err != nil {
		return 0, err
	}
	return next - 1, nil
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

// Poll returns this endpoint's un-acknowledged messages across all its channels,
// oldest first, and counts a delivery attempt for each.
//
// Poll is a pure read of DELIVERABILITY: being polled never hides a message from
// the next poll. Only Ack advances state. That is what makes a lost poll response
// harmless — the messages simply come back — and it is why "delivered" is an
// informational timestamp rather than a gate.
func (s *Store) Poll(ctx context.Context, ep *Endpoint, limit int) ([]Message, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	var out []Message
	err := s.tx(ctx, func(t *sql.Tx) error {
		rows, err := t.QueryContext(ctx, `
SELECT m.message_id
FROM message m
JOIN channel c ON c.id = m.channel_id
WHERE m.recipient_endpoint=?
  AND m.state IN ('queued','delivered')
  AND m.expires_at > strftime('%Y-%m-%dT%H:%M:%fZ','now')
  AND c.state='open'
ORDER BY m.seq, m.id
LIMIT ?`, ep.ID, limit)
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
		if _, err := t.ExecContext(ctx, `
DELETE FROM endpoint
WHERE last_seen_at <= strftime('%Y-%m-%dT%H:%M:%fZ','now',?)
  AND id NOT IN (SELECT sender_endpoint FROM message UNION SELECT recipient_endpoint FROM message)
  AND id NOT IN (SELECT sender_endpoint FROM attachment WHERE stored_bytes > 0)`,
			nowExpr(-s.lim().EndpointIdleTTLSeconds)); err != nil {
			return err
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
