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
		_, err := t.ExecContext(ctx, `
UPDATE mirror_state SET roster_epoch=?, refreshed_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
 WHERE id=1`, epoch)
		return err
	})
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

// RoomsForParty lists the rooms a party belongs to. This is the read behind "which
// rooms am I in", and behind the authorization check on a room-addressed send.
func (s *Store) RoomsForParty(ctx context.Context, party string) ([]string, error) {
	rows, err := s.R.QueryContext(ctx,
		`SELECT room_id FROM room_member_mirror WHERE party_key=? ORDER BY room_id`, party)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
