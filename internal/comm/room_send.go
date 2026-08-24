package comm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Room-addressed send — the fan-out half of slice 5.
//
// It is a separate entry point from Send rather than a flag on it, and the reason is
// not style. A channel send authorises by MEMBERSHIP OF A PAIR that a pairing code
// created, and refuses on a channel that is pending or revoked; a room send authorises
// by membership of a set a human filled, and has no notion of open or pending. Folding
// both into one function would mean a chain of conditionals in which the wrong branch
// is a security answer given for the wrong reason.
//
// What they DO share is everything after addressing: scope numbering, idempotency,
// backpressure, the message insert and one delivery row per recipient. Those live in
// insertMessageWithDeliveries below and are called by both, so there is exactly one
// place where a message becomes rows.

// ErrRoomEmpty distinguishes "delivered to nobody" from "no such room".
//
// Both would otherwise surface as a send that succeeded and reached no one, which is
// the outcome hardest to notice and most expensive to debug — the sender has a
// message_id, the result looks ordinary, and nothing anywhere says the audience was
// zero.
//
// SAYS "CANNOT CURRENTLY RECEIVE", NOT "HAS NO MEMBERS", because those stopped being the
// same thing when archived stations began dropping out of the roster. A room whose other
// members are all retired shows two members on the console and delivers to none — and an
// error saying the room is empty sends its reader to look for a membership problem that the
// console will flatly contradict.
var ErrRoomEmpty = CallerSafe(errors.New("no member of that room can currently receive mail — nothing was sent, and nothing was lost. " +
	"The room may genuinely be empty, or every other member may be an ARCHIVED station: a retired post keeps its " +
	"membership and stops receiving, so the console still lists it. Ask your human to check the /stations console"))

// SendToRoom delivers one body to every member of a room except the sender.
//
// ONE message row, N delivery rows. That is the whole shape of broadcast: the body is
// stored once, charged once against every size bound, and retained on one clock, while
// each recipient gets its own state, its own redelivery counter and its own reply
// deadline. A fan-out that copied the body per recipient would multiply every quota by
// the audience and make a five-station room five times the database of a pair.
func (s *Store) SendToRoom(ctx context.Context, ep *Endpoint, roomID, body string, opts SendOpts) (*Message, error) {
	if len(body) > s.lim().MaxBodyBytes {
		return nil, ErrTooLarge
	}
	scope := roomScope(roomID)

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
	err := s.tx(ctx, func(t *sql.Tx) error {
		senderParty, err := endpointParty(ctx, t, ep.ID)
		if err != nil {
			return err
		}

		members, err := membersOfScope(ctx, t, scope)
		if err != nil {
			return err
		}
		// AUTHORIZATION IS MEMBERSHIP, checked here inside the writing transaction
		// rather than by the caller. A check that happens anywhere else is advisory:
		// between an outer check and this insert a human can remove a station, and the
		// window is exactly as long as the rest of the request.
		var sender bool
		recipients := make([]scopeMember, 0, len(members))
		for _, m := range members {
			if m.Party == senderParty {
				sender = true
				continue
			}
			recipients = append(recipients, m)
		}
		if !sender {
			// *** BOTH NON-MEMBER BRANCHES ANSWER THE SAME WAY, AND THAT IS THE POINT. ***
			//
			// An unknown room and an empty one are already indistinguishable from the
			// mirror alone, and both are "you cannot send here". A room with members you
			// are not in is a THIRD case — and answering it differently would confirm
			// "this room exists and has at least one member" to a non-member, which is an
			// existence oracle over every room on the deployment.
			//
			// There WAS a separate ErrNotInRoom sentinel here until 2026-08-24. It was
			// deleted rather than made caller-safe: once both branches must answer alike,
			// a second sentinel nothing returns is the shape this project spent a week
			// removing. ErrRoomEmpty's wording is the more useful of the two for the case
			// that actually happens — a human made the room and has not filled it yet.
			return ErrRoomEmpty
		}
		if len(recipients) == 0 {
			return ErrRoomEmpty
		}

		out, err = s.insertMessageWithDeliveries(ctx, t, insertSpec{
			Scope:       scope,
			ChannelRow:  nil,
			Sender:      ep.ID,
			SenderParty: senderParty,
			Recipients:  recipients,
			Body:        body,
			TTLSeconds:  ttl,
			Opts:        opts,
			Kind:        "message",
			Endpoint:    ep,
		})
		if out != nil {
			out.TTLClampedFrom, out.Recipients = clampedFrom, len(recipients)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// insertSpec is what both send paths have already decided by the time a message becomes
// rows: where it lives, who wrote it, and who it is for.
type insertSpec struct {
	Scope       string
	ChannelRow  any // the channel rowid for a 'ch:' scope; nil for a room
	Sender      int64
	SenderParty string
	Recipients  []scopeMember
	Body        string
	TTLSeconds  int
	Opts        SendOpts
	Kind        string
	// Endpoint is the SENDING endpoint, carried so the waiting_for_you warning can be
	// computed here rather than at each call site. Room and broadcast sends never set that
	// field at all before this: the result simply omitted it, and `omitempty` then deleted
	// the key — so a sender with mail waiting read an absence as all-clear, on the two
	// paths where a hasty reply reaches the most people.
	Endpoint *Endpoint
}

// insertMessageWithDeliveries writes one message and one delivery row per recipient.
//
// The single place a message becomes rows, so the two addressing paths cannot drift on
// numbering, idempotency, or the shape of a delivery. They drifted before — the shipped
// AckUpTo and Ack carried different recipient predicates for months — and the fix was
// to state the rule once. This is the same move applied to writing.
func (s *Store) insertMessageWithDeliveries(ctx context.Context, t *sql.Tx, in insertSpec) (*Message, error) {
	if in.Opts.IdempotencyKey != "" {
		var existing string
		err := t.QueryRowContext(ctx, `
SELECT message_id FROM message
WHERE scope_id=? AND sender_party=? AND idempotency_key=?`,
			in.Scope, in.SenderParty, in.Opts.IdempotencyKey).Scan(&existing)
		if err == nil {
			return messageByID(ctx, t, existing)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}

	// Backpressure counts the SCOPE's open deliveries, so a busy room throttles the
	// same way a busy channel does. Counting per recipient instead would let an
	// N-member room carry N times the backlog before anything pushed back, which is
	// precisely backwards: the wider the audience, the more a runaway costs.
	// UNCONDITIONAL, where it used to exempt kind='status'. The exemption existed because
	// the sweeper authored notices into this path and a full scope is exactly when a
	// failure signal matters most. Notices are derived at poll time since 3.4.0, so both
	// call sites pass 'message' and the exemption was unreachable — removed here for the
	// same reason as its twin in message.go, and consistently with it.
	var unacked int
	if err := t.QueryRowContext(ctx, `
SELECT COUNT(*) FROM delivery d JOIN message m ON m.id = d.message_row
 WHERE m.scope_id = ? AND d.state IN ('queued','delivered')`, in.Scope).Scan(&unacked); err != nil {
		return nil, err
	}
	if unacked >= s.lim().MaxUnackedPerChannel {
		return nil, ErrBackpressure
	}

	var replyTo any
	var replyToRow int64
	if in.Opts.ReplyToMessageID != "" {
		err := t.QueryRowContext(ctx, `
SELECT m.id FROM message m
  JOIN delivery d ON d.message_row = m.id AND d.party_key = ?
 WHERE m.message_id = ? AND m.scope_id = ?`,
			in.SenderParty, in.Opts.ReplyToMessageID, in.Scope).Scan(&replyToRow)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		replyTo = replyToRow
	}

	seq, err := nextScopeSeq(ctx, t, in.Scope)
	if err != nil {
		return nil, err
	}
	messageID, err := randBase62(22)
	if err != nil {
		return nil, err
	}

	if _, err := t.ExecContext(ctx, `
INSERT INTO message(message_id, scope_id, scope_seq, channel_id, sender_endpoint, sender_party,
                    idempotency_key, body, body_sha256, body_bytes, requires_response, reply_to,
                    audience_size, kind, expires_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,strftime('%Y-%m-%dT%H:%M:%fZ','now',?))`,
		messageID, in.Scope, seq, in.ChannelRow, in.Sender, in.SenderParty,
		nullStr(in.Opts.IdempotencyKey), in.Body, sha256Hex(in.Body), len(in.Body),
		boolInt(in.Opts.RequiresResponse), replyTo, len(in.Recipients), in.Kind,
		nowExpr(in.TTLSeconds)); err != nil {
		return nil, err
	}

	var msgRow int64
	if err := t.QueryRowContext(ctx, `SELECT id FROM message WHERE message_id=?`, messageID).Scan(&msgRow); err != nil {
		return nil, err
	}
	for _, r := range in.Recipients {
		// NO reply deadline at insert. It is armed per recipient at first delivery, so
		// a station that polls tomorrow gets its full window rather than the remains of
		// one that started while it had no way to know the message existed.
		if _, err := t.ExecContext(ctx, `
INSERT INTO delivery(message_row, party_key, recipient_endpoint) VALUES(?,?,?)`,
			msgRow, r.Party, nullInt(r.Endpoint)); err != nil {
			return nil, fmt.Errorf("deliver to %s: %w", r.Party, err)
		}
	}

	if replyTo != nil {
		if _, err := t.ExecContext(ctx, `
UPDATE delivery SET replied_by=? WHERE message_row=? AND party_key=? AND replied_by IS NULL`,
			msgRow, replyToRow, in.SenderParty); err != nil {
			return nil, err
		}
		if _, err := t.ExecContext(ctx, `
UPDATE message SET answered_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
WHERE id=? AND answered_at IS NULL`, replyToRow); err != nil {
			return nil, err
		}
	}
	out, err := messageByID(ctx, t, messageID)
	if err != nil || out == nil {
		return out, err
	}
	// WHAT WAS ALREADY WAITING FOR THE SENDER, computed here so both addressing paths get
	// it from one place. Counted AFTER the insert, which is correct and worth stating: the
	// message just written is addressed to the recipients, never to the sender, so it
	// cannot count itself — and on the broadcast path the sender is excluded from its own
	// audience by construction.
	if in.Endpoint != nil {
		n, wErr := queuedForEndpoint(ctx, t, in.Endpoint)
		if wErr != nil {
			return nil, wErr
		}
		out.WaitingForYou = n
	}
	return out, nil
}

// nullInt keeps a room delivery's recipient_endpoint NULL rather than zero. Zero would
// be a rowid that never existed, and a foreign key that points at nothing reads as data.
func nullInt(v sql.NullInt64) any {
	if !v.Valid {
		return nil
	}
	return v.Int64
}

// ErrNoAudience refuses a broadcast that would reach nobody.
//
// Distinct from ErrRoomEmpty because the remedy differs: an empty room is one the human
// has not filled, while an empty broadcast means this station is in no room with anyone
// else. "Join a room" and "add someone to yours" are different sentences to hear.
var ErrNoAudience = CallerSafe(errors.New("you share no room with any other station, so a broadcast would reach nobody"))

// Broadcast sends one body to every station the sender shares a room with.
//
// The audience is DERIVED, never stored: it is the union of the memberships of the rooms
// this sender is in, minus the sender. That is deliberately the same authorization as a
// room send — you may broadcast to exactly the set you could already have addressed one
// room at a time — so broadcast adds reach, never permission.
//
// One message, N deliveries, exactly like a room send, so a broadcast to nine stations
// is one body and nine rows rather than nine copies.
func (s *Store) Broadcast(ctx context.Context, ep *Endpoint, body string, opts SendOpts) (*Message, error) {
	if len(body) > s.lim().MaxBodyBytes {
		return nil, ErrTooLarge
	}
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
	err := s.tx(ctx, func(t *sql.Tx) error {
		senderParty, err := endpointParty(ctx, t, ep.ID)
		if err != nil {
			return err
		}
		// The union, computed in one query rather than room by room, because a station
		// in three rooms with one shared member must receive ONE copy. Iterating rooms
		// and appending would deliver it three times, and at-least-once delivery makes
		// that indistinguishable from a redelivery on the receiving side.
		rows, err := t.QueryContext(ctx, `
SELECT DISTINCT other.party_key
  FROM room_member_mirror mine
  JOIN room_member_mirror other ON other.room_id = mine.room_id
 WHERE mine.party_key = ? AND other.party_key <> ?
 ORDER BY other.party_key`, senderParty, senderParty)
		if err != nil {
			return err
		}
		var recipients []scopeMember
		for rows.Next() {
			var party string
			if err := rows.Scan(&party); err != nil {
				rows.Close()
				return err
			}
			recipients = append(recipients, scopeMember{Party: party})
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(recipients) == 0 {
			return ErrNoAudience
		}

		out, err = s.insertMessageWithDeliveries(ctx, t, insertSpec{
			Scope:       broadcastScope(senderParty),
			Sender:      ep.ID,
			SenderParty: senderParty,
			Recipients:  recipients,
			Body:        body,
			TTLSeconds:  ttl,
			Opts:        opts,
			Kind:        "message",
			Endpoint:    ep,
		})
		if out != nil {
			out.TTLClampedFrom, out.Recipients = clampedFrom, len(recipients)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
