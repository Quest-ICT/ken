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
// It keys on delivery, not on arrival: `first_delivered_at` is when the receiving
// session actually polled the message and could have been influenced by it, whereas
// a message sitting un-polled in the queue has influenced nothing.
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
	if actorID == 0 || windowSeconds <= 0 {
		return false, nil
	}
	var found int
	err := s.R.QueryRowContext(ctx, `
SELECT EXISTS(
  SELECT 1 FROM message m
  JOIN endpoint e ON e.id = m.recipient_endpoint
  WHERE e.actor_id = ?
    AND m.first_delivered_at IS NOT NULL
    AND m.first_delivered_at > strftime('%Y-%m-%dT%H:%M:%fZ','now',?))`,
		actorID, nowExpr(-windowSeconds)).Scan(&found)
	return found == 1, err
}
