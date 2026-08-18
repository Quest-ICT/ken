package comm

import (
	"context"
	"database/sql"
)

// The station-link mirror (internal/comm/migrations/0015_station_link_mirror.sql).
//
// ken.db owns links; this is a projection of them, kept here so a pair send can check
// authorisation inside its own writer transaction. Same construction as the room
// mirror, same rule: A MIRROR MAY BE STALE, NEVER AUTHORITATIVE. Nothing in this file
// decides that two stations may talk; it copies a decision a human already made.

// ReplaceLinkMirror swaps the whole projection in one transaction.
//
// Wholesale, for the reason ReplaceRoomMirror states and this case sharpens: an
// incremental sync that misses a REMOVAL leaves a revoked link still authorising sends.
// Revocation is the operation a human reaches for when something has gone wrong, so it
// is the one that must not be the fragile path.
//
// epoch is ken.db's roster generation as of the read that produced these pairs, and it
// is the SAME counter the room mirror uses — the two projections are refreshed together
// by one caller, so one generation describes both. A link change bumps it for the same
// reason an archived station does: it changes who a message can reach.
func (s *Store) ReplaceLinkMirror(ctx context.Context, pairs [][2]string, epoch int64) error {
	return s.tx(ctx, func(t *sql.Tx) error {
		if _, err := t.ExecContext(ctx, `DELETE FROM station_link_mirror`); err != nil {
			return err
		}
		for _, p := range pairs {
			// The CHECK in the schema would reject a mis-ordered pair, which is the
			// point; ordering here as well means the caller cannot accidentally rely on
			// the database to be the only guard.
			a, b := p[0], p[1]
			if a > b {
				a, b = b, a
			}
			if _, err := t.ExecContext(ctx,
				`INSERT INTO station_link_mirror(station_a, station_b) VALUES(?,?)`,
				a, b); err != nil {
				return err
			}
		}
		_, err := t.ExecContext(ctx, `
UPDATE mirror_state SET roster_epoch=?, refreshed_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
 WHERE id=1`, epoch)
		return err
	})
}

// areLinked reports whether the mirror holds an approved link between two stations.
//
// Takes the transaction rather than the reader pool ON PURPOSE. This is the
// authorisation check for a pair send, and room_send.go already states the rule it
// follows: a check performed outside the writing transaction is advisory, because a
// human can revoke the link between the check and the insert and the window is as long
// as the rest of the request.
func areLinked(ctx context.Context, t *sql.Tx, x, y string) (bool, error) {
	a, b := orderStations(x, y)
	var ok bool
	err := t.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM station_link_mirror WHERE station_a=? AND station_b=?)`,
		a, b).Scan(&ok)
	return ok, err
}

// LinkedStations lists the stations a station may address, newest ordering irrelevant —
// sorted for a stable comm_channels result.
//
// Reads from the mirror rather than asking ken.db, so `comm_channels` answers the same
// question the send path will answer. A listing that disagreed with the send would be
// worse than no listing: it would name a peer and then refuse the message.
func (s *Store) LinkedStations(ctx context.Context, stationID string) ([]string, error) {
	rows, err := s.R.QueryContext(ctx, `
SELECT CASE WHEN station_a = ?1 THEN station_b ELSE station_a END AS peer
  FROM station_link_mirror
 WHERE station_a = ?1 OR station_b = ?1
 ORDER BY peer`, stationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var peer string
		if err := rows.Scan(&peer); err != nil {
			return nil, err
		}
		out = append(out, peer)
	}
	return out, rows.Err()
}
