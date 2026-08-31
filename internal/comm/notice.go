package comm

import "context"

// Notices — computed from the sender's own rows, never stored (migration 0011).
//
// A notice tells a sender what became of something they sent: it expired without being
// read, or it asked for a reply and the deadline passed. Until slice 4 that was a real
// MESSAGE Ken wrote back to them, which made a failure signal into a second thing that
// could fail — and did: the notice path scanned a column that is NULL for room mail, and
// took the entire sweep down with it in 3.0.0 and 3.0.1.
//
// Everything a notice carries was already in `message` and `delivery`. This is that
// query, run per poll, so there is nothing to store, nothing to expire, and nothing that
// can disagree with its source.

// Notice is one thing that happened to a message you sent.
type Notice struct {
	MessageID string
	Scope     string
	// Reason is "expired" (nobody read it in time) or "reply_overdue" (you asked for an
	// answer and the deadline passed). Both are about YOUR message, never a peer's.
	Reason string
	// At is when it became true, and it is what the watermark is compared against.
	At string
	// IdempotencyKey is echoed because it may be the only surviving description of what
	// the message was about: retention blanks bodies and keeps metadata, so a notice
	// about a swept message would otherwise name an opaque id and nothing else.
	IdempotencyKey string
	// Recipients names the parties it concerns — the ones who did not read it, or did
	// not answer. For a room message that is a subset of the audience, and saying which
	// is the difference between "nobody engaged" and "one station is quiet".
	Recipients []string
}

// NoticesFor returns what has happened to this party's sent messages since their
// watermark, oldest first.
//
// Bounded by `limit` because a sender returning after a long absence could otherwise
// receive hundreds at once, and a notice stream that floods the poll it rides on has
// replaced one delivery problem with another.
//
// BOUNDED ALSO BY METADATA RETENTION, which is the one thing a derived notice gives up
// against a written one and is stated here because it is invisible from the call site: a
// notice is computed from `message` and `delivery`, so it exists only while those rows
// do. The metadata purge removes a settled message MetadataTTLSeconds after it settled
// (7 days by default), and the notice goes with it. A written notice was an independent
// row and could outlive its subject; this cannot.
//
// Accepted, because the alternative is what shipped: an independent row is exactly what
// made a failure signal into a second failure-prone delivery, with its own expiry, its
// own backpressure and its own ack — and it took the sweep down twice. A sender who has
// not polled in a week has a larger problem than a missing notice, and the retention
// window is the audit window for everything else in this database too.
func (s *Store) NoticesFor(ctx context.Context, party string, limit int) ([]Notice, error) {
	if limit <= 0 {
		limit = 25
	}
	rows, err := s.R.QueryContext(ctx, `
WITH mark AS (
  SELECT COALESCE((SELECT seen_at FROM notice_watermark WHERE party_key = ?1), '') AS seen_at
)
-- EXPIRED: at least one recipient's delivery expired, and nobody is STILL HOLDING it.
-- Reported once per MESSAGE rather than once per recipient: the sender asked one
-- question and wants one answer, with the silent parties named below.
--
-- "Still holding" is queued or delivered — NOT acked. An acked delivery is settled, so
-- excluding it here suppressed the notice whenever a single recipient read a room
-- message and the rest ignored it: one ack made two silences invisible. That is the
-- ordinary shape of room traffic, and it produced exactly the silence-reads-as-delivery
-- failure the slice exists to remove. Found by mutation testing — no test changed when
-- the clause did, which is what "no coverage" looks like from the outside.
SELECT m.message_id, m.scope_id, 'expired' AS reason, m.expires_at AS at,
       COALESCE(m.idempotency_key,'')
  FROM message m, mark
 WHERE m.sender_party = ?1
   AND m.kind = 'message'
   AND m.expires_at > mark.seen_at
   AND EXISTS (SELECT 1 FROM delivery d WHERE d.message_row = m.id AND d.state = 'expired')
   AND NOT EXISTS (SELECT 1 FROM delivery d WHERE d.message_row = m.id
                    AND d.state IN ('queued','delivered'))

UNION ALL

-- REPLY OVERDUE: you asked for an answer, a recipient was handed the message, the
-- deadline passed, and that recipient has not replied.
--
-- *** ONCE PER MESSAGE, NOT ONCE PER RECIPIENT — GROUP BY, exactly as the expired arm above
-- already does. *** It was keyed per DELIVERY, on the argument that "nobody answered" and "one
-- of five answered" are different facts. They are, and the recipients list below already carries that
-- distinction by NAMING the parties who went quiet. What per-delivery keying actually produced
-- was N near-identical notices for one message, each repeating the same N names: at thirteen
-- stations, one estate broadcast consumed twelve of a poll's twenty-five notice slots and
-- starved every other notice for several polls. Estate-wide broadcast is what made that
-- ordinary rather than theoretical.
SELECT m.message_id, m.scope_id, 'reply_overdue' AS reason, MIN(d.reply_deadline_at) AS at,
       COALESCE(m.idempotency_key,'')
  FROM message m
  JOIN delivery d ON d.message_row = m.id, mark
 WHERE m.sender_party = ?1
   AND m.kind = 'message'
   AND m.requires_response = 1
   AND `+replyOverdueEligible+`
   AND d.reply_deadline_at > mark.seen_at
 GROUP BY m.message_id, m.scope_id, m.idempotency_key

 ORDER BY at
 LIMIT ?2`, party, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Notice
	for rows.Next() {
		var n Notice
		if err := rows.Scan(&n.MessageID, &n.Scope, &n.Reason, &n.At, &n.IdempotencyKey); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Who each one is about, resolved after the fact so the union above stays a plain
	// two-arm query. A notice naming no one tells a sender something happened and not to
	// whom, which for a room message is most of the information.
	for i := range out {
		who, err := s.noticeRecipients(ctx, out[i].MessageID, out[i].Reason)
		if err != nil {
			return nil, err
		}
		out[i].Recipients = who
	}
	return out, nil
}

// replyOverdueEligible is the reply_overdue rule, stated ONCE because it is asked in two
// places and a second copy that drifted would name recipients for a notice the main query
// had already decided not to send.
//
// IT USED TO CARRY A FOURTH CLAUSE, and the reason it no longer does is worth keeping.
// `delivery.replied_by` existed from comm 0001 and NOTHING WROTE IT until 0009 created the
// delivery table and Send began linking replies, so on a deployment upgraded through 0009
// every earlier requires_response message had replied_by NULL forever, whatever actually
// happened. The notice query read that NULL as "nobody replied" and reported a peer as
// owing answers it had given within minutes — ken-prod-ops caught it from the other side of
// the conversation: 4 notices received, 136 rows permanently eligible, the thread reply_to-
// linked end to end. It MANUFACTURES work, which is the expensive failure direction.
//
// The fix was a lower bound read from `schema_migration.applied_at WHERE version = 9` —
// production behaviour depending on migration archaeology. That dependency is gone with the
// chain: comm.db is now created in ONE step, so a row predating 0009 cannot exist in any
// database this code will ever open, and a clause that can never exclude anything is a
// clause that only misleads. Note the shape of the old guard if it is ever needed again:
// COALESCE(..., ”) meant a MISSING boundary admitted everything, so it failed open — the
// safe direction here, and the reason removing it changes no behaviour rather than
// restoring the defect.
const replyOverdueEligible = `d.replied_by IS NULL
   AND d.reply_deadline_at IS NOT NULL
   AND d.reply_deadline_at <= strftime('%Y-%m-%dT%H:%M:%fZ','now')
`

// noticeRecipients names the parties a notice concerns.
func (s *Store) noticeRecipients(ctx context.Context, messageID, reason string) ([]string, error) {
	pred := `d.state = 'expired'`
	if reason == "reply_overdue" {
		pred = replyOverdueEligible
	}
	rows, err := s.R.QueryContext(ctx, `
SELECT d.party_key FROM delivery d JOIN message m ON m.id = d.message_row
 WHERE m.message_id = ? AND `+pred+` ORDER BY d.party_key`, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PartyOf is the address this endpoint receives as: its station when it is bound, itself
// when it is not. Exported because the poll surface needs it and must not re-derive the
// rule — a second copy that disagreed would file a session's notices under an address it
// never reads.
func PartyOf(ep *Endpoint) string {
	if ep.StationID != "" {
		return stationParty(ep.StationID)
	}
	return endpointPartyKey(ep.ID)
}

// NoticesForPoll is what comm_poll calls: it confirms the previous poll's notices, then
// returns and records this one's.
//
// A notice is cleared by the caller's NEXT poll, never by the call that showed it.
//
// WHAT THAT BUYS, stated narrowly because it is narrow: the window between this query
// and the caller having the result is not a window in which the notice can be lost. A
// crash, a rolled-back transaction or a failure later in the poll handler leaves
// shown_at unpromoted, so the next poll still carries it.
//
// WHAT IT DOES NOT BUY, and this is the accepted trade: a result lost in transit is a
// notice lost. The next poll promotes it regardless, because the server cannot tell a
// delivered result from a discarded one. Closing that would need the caller to confirm
// receipt — and the only mechanisms for that are a new tool or a new parameter, neither
// of which a session running today can use: tool lists and descriptions pin at
// conversation start. A design that clears only on confirmation would repeat every
// notice forever for exactly the sessions least able to fix it.
//
// So: shown once, to a caller that came back. The rare loss is a repeat of the silence
// this replaces; the alternative is a permanent repeat for every frozen session.
func (s *Store) NoticesForPoll(ctx context.Context, party string, limit int) ([]Notice, error) {
	// Confirm what the last poll displayed. Done BEFORE the query so this call cannot
	// clear anything it is about to show.
	if _, err := s.W.ExecContext(ctx, `
UPDATE notice_watermark SET seen_at = MAX(seen_at, shown_at) WHERE party_key = ?`, party); err != nil {
		return nil, err
	}
	out, err := s.NoticesFor(ctx, party, limit)
	if err != nil || len(out) == 0 {
		return out, err
	}
	// Record the high-water mark of what is going out. MAX, not assignment: the rows
	// are ordered oldest-first and a limit truncates the TAIL, so the last one shown is
	// the newest the caller actually received.
	high := out[len(out)-1].At
	if _, err := s.W.ExecContext(ctx, `
INSERT INTO notice_watermark(party_key, seen_at, shown_at) VALUES(?,'',?)
ON CONFLICT(party_key) DO UPDATE SET shown_at = MAX(notice_watermark.shown_at, excluded.shown_at)`,
		party, high); err != nil {
		return nil, err
	}
	return out, nil
}
