package comm

import (
	"context"
	"database/sql"
)

// The room membership mirror (internal/comm/migrations/0010_rooms.sql).
//
// ken.db owns rooms; this is a projection of them, kept here so Send can check
// membership inside its own writer transaction. `comm.Store` has no ken.db handle by
// construction — the two databases are opened separately and version separately — so
// there is no cross-database read available at the point the check has to happen.
//
// The rule that keeps this honest: a mirror may be STALE, never AUTHORITATIVE. Nothing
// in this file writes a membership decision; it only copies one. If comm.db is lost, the
// rooms are still in ken.db and the next rebuild restores this table exactly.

// ReplaceRoomMirror swaps the whole projection in one transaction.
//
// Wholesale rather than incremental, and that is the point: an incremental sync has to
// be right about what changed, and a missed removal leaves a station able to send to a
// room a human took it out of — the failure that fails OPEN. Replacing everything cannot
// drift, and the table is small enough that the cost is not worth reasoning about.
//
// epoch is ken.db's roster generation as of the read that produced these rows. Storing
// it lets a later caller notice this projection is behind, rather than trusting it.
func (s *Store) ReplaceRoomMirror(ctx context.Context, rooms map[string][]string, epoch int64) error {
	return s.tx(ctx, func(t *sql.Tx) error {
		if _, err := t.ExecContext(ctx, `DELETE FROM room_member_mirror`); err != nil {
			return err
		}
		for roomID, parties := range rooms {
			for _, party := range parties {
				if _, err := t.ExecContext(ctx,
					`INSERT INTO room_member_mirror(room_id, party_key) VALUES(?,?)`,
					roomID, party); err != nil {
					return err
				}
			}
		}
		// NO EPOCH STAMP HERE — see StampMirrorEpoch. The generation is written by the
		// caller, once, and only when BOTH projections have been rebuilt.
		_ = epoch
		return nil
	})
}

// StampMirrorEpoch records the roster generation both projections were built from.
//
// IT IS SEPARATE FROM THE REBUILDS BECAUSE A PARTIAL REBUILD USED TO READ AS FRESH.
// `mirror_state` is one row (`0010_rooms.sql:38`, `CHECK (id = 1)`) and both halves used to
// write its `roster_epoch` themselves — while both rebuild paths deliberately run the halves
// INDEPENDENTLY and log-and-continue (`internal/web/rooms.go`: "a failure reading rooms must
// not silently skip the link refresh"). So whichever half survived stamped the new generation
// for both, and `MirrorEpoch` reported fresh over stale data. Measured before the fix: rebuild
// both at 5, then only the link half at 6, and MirrorEpoch returns 6 while room_member_mirror
// still holds epoch-5 rows.
//
// That independence is CORRECT and stays — it exists so one projection's failure cannot take
// the other down. What was wrong was `link_mirror.go`'s stated rationale for sharing one
// counter: "the two projections are refreshed together by one caller, so one generation
// describes both". They are not refreshed together, and have not been since the
// log-and-continue paths landed. Two comments in one subsystem contradicting each other, and
// the epoch rested on the wrong one.
//
// THE COARSENESS IS DELIBERATE. One counter still covers both, so a room-only failure now
// marks the link mirror stale too. That is the safe direction — stale means "re-read from
// ken.db", never "trust this" — and it costs no schema. Per-projection epochs would be sharper
// and need a migration; if that is ever wanted, this is the seam to widen.
func (s *Store) StampMirrorEpoch(ctx context.Context, epoch int64) error {
	_, err := s.W.ExecContext(ctx, `
UPDATE mirror_state SET roster_epoch=?, refreshed_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
 WHERE id=1`, epoch)
	return err
}

// MirrorEpoch is the roster generation this projection was built from. A caller holding
// ken.db's current epoch can compare and know whether it is reading yesterday's room.
func (s *Store) MirrorEpoch(ctx context.Context) (int64, error) {
	var e int64
	err := s.R.QueryRowContext(ctx, `SELECT roster_epoch FROM mirror_state WHERE id=1`).Scan(&e)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return e, err
}

// RoomView is one room from a party's point of view, as the directory reports it.
type RoomView struct {
	RoomID  string
	Members []string // party keys; the caller resolves them to names
	Pending int
}

// RoomsFor lists the rooms a party is in, with every member and the count of messages
// waiting for that party in each.
//
// The pending count carries the shared expiry clause (pendingNotExpiredSQL, pending.go),
// like every other pending counter: without it this number disagreed with pending_total
// for as long as it took the sweeper to come round.
//
// The pending count is a COUNT, exactly like comm_channels': reading it delivers
// nothing, stamps nothing and starts no clock. That is what lets the directory be the
// thing a session consults before it decides to speak — a survey that cost a delivery
// would be skipped, and a skipped instruction is worse than none because it still looks
// like a control.
func (s *Store) RoomsFor(ctx context.Context, party string) ([]RoomView, error) {
	rows, err := s.R.QueryContext(ctx, pendingSQL(`
SELECT mine.room_id,
       (SELECT COUNT(*) FROM delivery d
          JOIN message m ON m.id = d.message_row
         WHERE m.scope_id = 'r:' || mine.room_id
           AND d.party_key = ? AND d.state = 'queued'
           AND %NOTEXPIRED%)
  FROM room_member_mirror mine
 WHERE mine.party_key = ?
 ORDER BY mine.room_id`), party, party)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RoomView
	for rows.Next() {
		var v RoomView
		if err := rows.Scan(&v.RoomID, &v.Pending); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		members, err := s.R.QueryContext(ctx,
			`SELECT party_key FROM room_member_mirror WHERE room_id=? ORDER BY party_key`, out[i].RoomID)
		if err != nil {
			return nil, err
		}
		for members.Next() {
			var pk string
			if err := members.Scan(&pk); err != nil {
				members.Close()
				return nil, err
			}
			out[i].Members = append(out[i].Members, pk)
		}
		members.Close()
		if err := members.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// BroadcastAudience counts the stations a broadcast from this party would reach right
// now — the same DISTINCT union Broadcast itself computes, so the directory cannot
// promise a reach the send would not deliver.
func (s *Store) BroadcastAudience(ctx context.Context, party string) (int, error) {
	var n int
	err := s.R.QueryRowContext(ctx, `
SELECT COUNT(DISTINCT other.party_key)
  FROM room_member_mirror mine
  JOIN room_member_mirror other ON other.room_id = mine.room_id
 WHERE mine.party_key = ? AND other.party_key <> ?`, party, party).Scan(&n)
	return n, err
}
