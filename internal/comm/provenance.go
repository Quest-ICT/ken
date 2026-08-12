package comm

import "context"

// ReceivedSince reports whether any endpoint owned by actorID has had a message
// DELIVERED to it within the last windowSeconds.
//
// This exists for one reason: a message is a side channel into curation. A session
// told "entry X is verified, propose a revision at high confidence" will author a
// proposal that is indistinguishable from first-hand knowledge — the invariant
// survives literally (an AI authored it, a human promotes it) while the curator's
// signal quality has quietly degraded to hearsay with no chain of custody. Marking
// the authored version lets the curator ask for a first-hand citation before
// promoting. See docs/COMM.md §7.
//
// It keys on delivery, not on arrival: a message sitting un-polled in the queue
// has influenced nothing. It considers the LAST delivery, not the first: under
// at-least-once semantics a message is redelivered until acked, so keying on
// first_delivered_at alone produced a systematic false negative — a message first
// delivered before the window but re-read inside it left no mark, in the system's
// normal operating mode. Acked messages fall back to their acknowledgement time,
// which is when the receiver acted on them.
//
// It keys on the ACTOR, not the token, and that is forced rather than chosen: a COMM
// token must be DEDICATED (it may not also carry knowledge-base scopes), so the token
// that receives messages is never the token that authors an entry. Keying on the token
// would make this function always return false. The actor is the identity the two
// tokens legitimately share — mint both with the same `--actor` and the link holds.
//
// Consequence the operator must know: if the two tokens are minted under DIFFERENT
// actor names, nothing is ever marked. That is a silent false negative, so it is
// called out in docs/COMM.md §7 rather than left to be discovered.
//
// Actors resolve by display name and therefore collapse across machines, which would
// be wrong for an ownership check — but here over-matching is the SAFE direction, for
// the same reason the whole marker is biased toward over-reporting.
//
// Deliberately conservative in one direction: metadata rows outlive acknowledgement
// (bodies do not), so a message that was read and acted upon still answers true for
// the whole window. Deliberately imprecise in another: it cannot know whether the
// message had anything to do with what is being saved. A false positive costs the
// curator one extra glance; a false negative would silently launder hearsay into the
// knowledge base, so the marker is biased toward over-reporting.
//
// Callers must treat any error as "unknown" and NOT as "no": failing to mark is the
// direction that loses information.
func (s *Store) ReceivedSince(ctx context.Context, actorID int64, windowSeconds int) (bool, error) {
	srcs, err := s.ReceivedFrom(ctx, actorID, windowSeconds)
	return len(srcs) > 0, err
}

// Source is one piece of inter-session traffic this actor received inside the window.
//
// Broadcast is the field slice 5 made necessary. Before rooms, every message had one
// recipient and "you received something" meant somebody addressed YOU. A broadcast to
// nine stations marks nine actors from a single send, and treating that identically to
// a direct message means the marker fires far more often while meaning far less — the
// badge prod already measured as nearly always on, made worse by the feature that just
// shipped.
type Source struct {
	// StationID of the SENDER when it had one; empty for an unbound endpoint.
	StationID string
	MessageID string
	At        string
	// Broadcast is true when this arrived as one of many recipients — a room message or
	// an estate broadcast. A directed message is one addressed to this party alone.
	Broadcast bool
}

// ReceivedFrom lists the traffic behind the hearsay marker, newest first, with directed
// messages ranked ahead of broadcast ones.
//
// The ordering IS the point rather than presentation: a curator reading a badge wants
// the strongest reason first. "prod-ops told you this an hour ago" and "you were in a
// room where something was said" are different claims about the same entry, and
// collapsing them to a boolean is what made the badge uninformative.
//
// Callers must treat any error as "unknown" and NOT as "no": failing to mark is the
// direction that loses information.
func (s *Store) ReceivedFrom(ctx context.Context, actorID int64, windowSeconds int) ([]Source, error) {
	if actorID == 0 || windowSeconds <= 0 {
		return nil, nil
	}
	// TWO WAYS A DELIVERY BELONGS TO AN ACTOR, and missing the second one made this
	// blind to exactly the traffic slice 5 added.
	//
	// A channel delivery names a recipient ENDPOINT, and the endpoint carries the
	// actor. A ROOM delivery names no endpoint at all — rooms hold stations, and which
	// connection reads the mail is decided later, at poll time — so
	// `recipient_endpoint` is NULL and an inner join on it drops every room message.
	//
	// Caught by a test, and it would have shipped silently: the badge would simply
	// never fire for room mail, and an absent badge is indistinguishable from a
	// checked-and-clean one. The second arm resolves the party's STATION back to the
	// actors staffing it, which is the same widening the poll predicate does.
	rows, err := s.R.QueryContext(ctx, `
SELECT COALESCE(se.station_id, ''), m.message_id,
       COALESCE(d.acked_at, d.first_delivered_at),
       CASE WHEN m.audience_size > 1 THEN 1 ELSE 0 END
  FROM delivery d
  JOIN message m ON m.id = d.message_row
  LEFT JOIN endpoint se ON se.id = m.sender_endpoint
 WHERE d.first_delivered_at IS NOT NULL
   AND COALESCE(d.acked_at, d.first_delivered_at) > strftime('%Y-%m-%dT%H:%M:%fZ','now',?)
   AND (
     EXISTS (SELECT 1 FROM endpoint e
              WHERE e.id = d.recipient_endpoint AND e.actor_id = ?)
     OR EXISTS (SELECT 1 FROM endpoint e
                 WHERE e.actor_id = ? AND e.station_id IS NOT NULL
                   AND d.party_key = 's:' || e.station_id)
   )
 ORDER BY CASE WHEN m.audience_size > 1 THEN 1 ELSE 0 END,
          COALESCE(d.acked_at, d.first_delivered_at) DESC`,
		nowExpr(-windowSeconds), actorID, actorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Source
	for rows.Next() {
		var src Source
		var broadcast int
		if err := rows.Scan(&src.StationID, &src.MessageID, &src.At, &broadcast); err != nil {
			return nil, err
		}
		src.Broadcast = broadcast == 1
		out = append(out, src)
	}
	return out, rows.Err()
}
