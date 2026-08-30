package comm

import (
	"context"
	"database/sql"
	"sync"
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
// epoch is ken.db's roster generation as of the read that produced these pairs, and it is the
// SAME counter the room mirror uses. A link change bumps it for the same reason an archived
// station does: it changes who a message can reach.
//
// THIS COMMENT USED TO SAY "the two projections are refreshed together by one caller, so one
// generation describes both". THEY ARE NOT REFRESHED TOGETHER — both rebuild paths run the
// halves independently and log-and-continue, deliberately, so that one failing cannot take the
// other down. On that shared assumption each half stamped the counter itself, and a partial
// rebuild left the epoch reading FRESH over stale data. The parameter is accepted and ignored
// here now; StampMirrorEpoch writes it once, after both halves succeed.
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
		// NO EPOCH STAMP HERE — StampMirrorEpoch writes it once both halves are rebuilt.
		_ = epoch
		return nil
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

// linkMirrorPush serialises the read-then-replace that rebuilds the projection.
//
// *** THE RACE IS IN THE SHAPE, AND IT HAS TWO CALLERS THAT MUST SHARE ONE LOCK. ***
//
// A push reads the authoritative rows from ken.db and hands the snapshot to ReplaceLinkMirror,
// which DELETEs the whole table and reinserts it. Two pushes overlapping means the older snapshot
// can land last and rewrite the table to a state that was already stale when it was read.
//
// Both directions were measured on the built binary. DE-AUTHORISE: 4–10% of concurrent first
// contacts had their brand-new link deleted by another session's older snapshot, and every
// subsequent send was refused as an unknown station. RE-AUTHORISE: a pre-suspend snapshot landing
// after a console suspend puts the pair back, so sends succeed indefinitely while /stations renders
// the link suspended — rarer, and worse, because suspend is the only operator control over who may
// talk to whom.
//
// commserver took a mutex of its own and the WEB CONSOLE did not, so a console suspend still raced
// a session's first contact — while rooms.go's comment claimed it was "the ONE place either
// projection is pushed". The lock lives here, in the package both callers already import, and it
// covers the READ as well as the write because the read is where the snapshot ages.
//
// A MUTEX IS ENOUGH ONLY BECAUSE KEN IS ONE PROCESS against one data dir. Nothing supports running
// it otherwise; if that ever changes this needs the roster epoch ReplaceLinkMirror currently
// discards.
var linkMirrorPush sync.Mutex

// SyncLinkMirror rebuilds the link projection from the durable database, atomically.
//
// `read` returns the authoritative pairs and the roster epoch — a callback rather than a store
// handle because internal/comm must not import internal/store (S7: the expendable side points at
// the durable one, never the reverse). The callback runs under the lock, so no other push can
// interleave between reading the rows and writing them.
func (s *Store) SyncLinkMirror(ctx context.Context, read func(context.Context) ([][2]string, int64, error)) error {
	linkMirrorPush.Lock()
	defer linkMirrorPush.Unlock()
	pairs, epoch, err := read(ctx)
	if err != nil {
		return err
	}
	return s.ReplaceLinkMirror(ctx, pairs, epoch)
}
