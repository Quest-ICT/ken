package comm

import (
	"context"
	"database/sql"
	"errors"
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
	if len(body) > s.limits.MaxBodyBytes {
		return nil, ErrTooLarge
	}
	ch, peer, err := s.ChannelFor(ctx, ep, channelID)
	if err != nil {
		return nil, err
	}

	ttl := opts.TTLSeconds
	if ttl <= 0 {
		ttl = s.limits.MessageTTLSeconds
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
		var unacked int
		if err := t.QueryRowContext(ctx, `
SELECT COUNT(*) FROM message WHERE channel_id=? AND state IN ('queued','delivered')`, ch.ID).
			Scan(&unacked); err != nil {
			return err
		}
		if unacked >= s.limits.MaxUnackedPerChannel {
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

		// Sequence is per (channel, sender) — i.e. per direction, which is the only
		// ordering COMM promises. MAX+1 is safe under the single-writer discipline.
		var seq int64
		if err := t.QueryRowContext(ctx, `
SELECT COALESCE(MAX(seq),0)+1 FROM message WHERE channel_id=? AND sender_endpoint=?`,
			ch.ID, ep.ID).Scan(&seq); err != nil {
			return err
		}

		messageID, err := randBase62(22)
		if err != nil {
			return err
		}

		var deadline any
		if opts.RequiresResponse {
			deadline = nowExpr(s.limits.ReplyDeadlineSeconds)
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
			if _, err := t.ExecContext(ctx,
				`UPDATE message SET replied_by=? WHERE id=? AND replied_by IS NULL`, newRow, replyToRow); err != nil {
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
	err = s.tx(ctx, func(t *sql.Tx) error {
		// 1. Expire un-acked messages past their TTL and drop their bodies. This
		//    covers BOTH queued and delivered-but-never-acked: a message polled by a
		//    session that then died must not live forever.
		res, err := t.ExecContext(ctx, `
UPDATE message SET state='expired', body=NULL
WHERE state IN ('queued','delivered') AND expires_at <= strftime('%Y-%m-%dT%H:%M:%fZ','now')`)
		if err != nil {
			return err
		}
		expired, _ = res.RowsAffected()

		// 2. Drop the retained body of a requires_response message whose reply
		//    deadline has passed unanswered — it is no longer owed anything.
		if _, err := t.ExecContext(ctx, `
UPDATE message SET body=NULL
WHERE state='acked' AND body IS NOT NULL AND replied_by IS NULL
  AND reply_deadline_at IS NOT NULL
  AND reply_deadline_at <= strftime('%Y-%m-%dT%H:%M:%fZ','now')`); err != nil {
			return err
		}

		// 3. Purge settled metadata past the retention window. Bodies are long gone;
		//    this is the audit shell, bounded so comm.db cannot grow without limit.
		res, err = t.ExecContext(ctx, `
DELETE FROM message
WHERE state IN ('acked','expired')
  AND created_at <= strftime('%Y-%m-%dT%H:%M:%fZ','now',?)`, nowExpr(-s.limits.MetadataTTLSeconds))
		if err != nil {
			return err
		}
		purged, _ = res.RowsAffected()

		// 4. Drop pairing codes that were never redeemed.
		_, err = t.ExecContext(ctx, `
DELETE FROM pairing_code
WHERE consumed_at IS NULL AND expires_at <= strftime('%Y-%m-%dT%H:%M:%fZ','now')`)
		return err
	})
	return expired, purged, err
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
	err := q.QueryRowContext(ctx, `
SELECT m.message_id, c.channel_id, m.seq, se.endpoint_id, m.body, m.requires_response,
       (SELECT r.message_id FROM message r WHERE r.id = m.reply_to),
       m.state, m.delivery_count, m.body_bytes, m.created_at, m.expires_at, m.reply_deadline_at
FROM message m
JOIN channel  c  ON c.id  = m.channel_id
JOIN endpoint se ON se.id = m.sender_endpoint
WHERE m.message_id=?`, messageID).
		Scan(&m.MessageID, &m.ChannelID, &m.Seq, &m.SenderEndpointID, &body, &reqResp,
			&replyTo, &m.State, &m.DeliveryCount, &m.BodyBytes, &m.CreatedAt, &m.ExpiresAt, &deadline)
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
	return &m, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
