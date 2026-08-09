package comm

import (
	"context"
	"database/sql"
	"errors"
)

// Channel joins exactly two distinct endpoints, full-duplex.
//
// There is deliberately no turn or "waiting" state here: channel-level
// turn-taking is a distributed state machine that wedges when a session dies
// mid-turn. Request/response is a property of a message instead (see message.go).
type Channel struct {
	ID           int64 // internal rowid
	ChannelID    string
	SpaceID      int64
	OwnerActorID int64 // the human who authorized the pairing
	EndpointA    int64
	EndpointB    int64 // 0 until the second endpoint joins
	State        string
	CreatedAt    string
	OpenedAt     string
}

// Open reports whether the channel may carry traffic.
func (c *Channel) Open() bool { return c.State == "open" }

// MintPairingCode creates a human-authorized pairing code and returns the
// plaintext exactly once; only its SHA-256 is stored.
//
// This is COMM's structural gate: an agent cannot conjure a channel, because
// channel creation requires a value only the human web UI can produce. It is the
// same move that makes the curation gate trustworthy — withhold the capability
// rather than instruct the model not to use it — applied at the one place in COMM
// where it is available.
func (s *Store) MintPairingCode(ctx context.Context, spaceID, humanActorID int64, label string) (string, error) {
	code, err := randBase62(10)
	if err != nil {
		return "", err
	}
	_, err = s.W.ExecContext(ctx, `
INSERT INTO pairing_code(code_sha256, space_id, human_actor_id, label, expires_at)
VALUES(?,?,?,?, strftime('%Y-%m-%dT%H:%M:%fZ','now',?))`,
		sha256Hex(code), spaceID, humanActorID, nullStr(label),
		nowExpr(s.lim().PairingCodeTTLSeconds))
	if err != nil {
		return "", err
	}
	return code, nil
}

// JoinChannel redeems a pairing code for an endpoint.
//
// Establishment is two-sided from day 1: the first redeem creates a pending
// channel, the second opens it. Both sides call this even though both currently
// share one owner — turning a unilateral "A opens a channel to B" into an accept
// flow later would tighten an already-shipped tool, which is a breaking change.
//
// Re-redeeming the same code from an endpoint already on the channel is
// idempotent and returns the channel unchanged, so a retried call after a lost
// response cannot consume the code twice or wedge the pairing.
func (s *Store) JoinChannel(ctx context.Context, ep *Endpoint, code string) (*Channel, error) {
	var ch *Channel
	err := s.tx(ctx, func(t *sql.Tx) error {
		var (
			pcID     int64
			spaceID  int64
			humanID  int64
			chanID   sql.NullInt64
			consumed sql.NullString
			pcLabel  sql.NullString
		)
		err := t.QueryRowContext(ctx, `
SELECT id, space_id, human_actor_id, channel_id, consumed_at, label
FROM pairing_code
WHERE code_sha256=? AND expires_at > strftime('%Y-%m-%dT%H:%M:%fZ','now')`,
			sha256Hex(code)).Scan(&pcID, &spaceID, &humanID, &chanID, &consumed, &pcLabel)
		if errors.Is(err, sql.ErrNoRows) {
			// Expired and unknown are indistinguishable on purpose: a caller must
			// not be able to probe which codes exist or existed.
			return ErrNotFound
		}
		if err != nil {
			return err
		}

		// A code may only pair endpoints within its own space. Today there is one
		// space, so this cannot fire; it is here because it must be true before a
		// second human exists, not after.
		if spaceID != ep.Owner.SpaceID {
			return ErrDenied
		}

		// First redeem: create the pending channel and bind the code to it.
		if !chanID.Valid {
			channelID, err := randBase62(22)
			if err != nil {
				return err
			}
			res, err := t.ExecContext(ctx, `
INSERT INTO channel(channel_id, space_id, owner_actor_id, endpoint_a, state, label)
VALUES(?,?,?,?, 'pending', ?)`, channelID, spaceID, humanID, ep.ID, nullStr(pcLabel.String))
			if err != nil {
				return err
			}
			newID, err := res.LastInsertId()
			if err != nil {
				return err
			}
			if _, err := t.ExecContext(ctx, `UPDATE pairing_code SET channel_id=? WHERE id=?`, newID, pcID); err != nil {
				return err
			}
			ch, err = channelByRowID(ctx, t, newID)
			return err
		}

		// Second redeem: open the channel, unless this endpoint is already on it.
		cur, err := channelByRowID(ctx, t, chanID.Int64)
		if err != nil {
			return err
		}
		if cur.EndpointA == ep.ID || cur.EndpointB == ep.ID {
			ch = cur // idempotent re-join
			return nil
		}
		if cur.EndpointB != 0 {
			// Both seats are taken by other endpoints: a third session must not be
			// able to join, and a consumed code must not create a second channel.
			return ErrDenied
		}
		// Only a PENDING channel may be opened. Without this, a human who revokes a
		// half-formed pairing has their brake silently undone: the code stays valid
		// for its TTL, and the second session's join would flip the revoked row back
		// to 'open'. ErrNotFound rather than ErrChannelClosed, so a dead code stays
		// indistinguishable from an unknown one.
		if cur.State != "pending" {
			return ErrNotFound
		}
		// The state guard is repeated in the WHERE clause because the SELECT above and
		// this UPDATE are separate statements: a concurrent RevokeChannel runs on
		// another connection and could land between them.
		res, err := t.ExecContext(ctx, `
UPDATE channel SET endpoint_b=?, state='open', opened_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
WHERE id=? AND endpoint_b IS NULL AND state='pending'`, ep.ID, cur.ID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrNotFound
		}
		if _, err := t.ExecContext(ctx, `
UPDATE pairing_code SET consumed_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, pcID); err != nil {
			return err
		}
		ch, err = channelByRowID(ctx, t, cur.ID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return ch, nil
}

// ChannelFor resolves an open channel by its public id and verifies the endpoint
// belongs to it, returning the peer's rowid.
//
// Membership is re-checked on every operation rather than trusted from an earlier
// call: a channel can be revoked, and an endpoint that was a member a moment ago
// must not keep acting on one.
func (s *Store) ChannelFor(ctx context.Context, ep *Endpoint, channelID string) (*Channel, int64, error) {
	var (
		ch  Channel
		bID sql.NullInt64
		opn sql.NullString
	)
	var stnA, stnB string
	err := s.R.QueryRowContext(ctx, `
SELECT c.id, c.channel_id, c.space_id, c.owner_actor_id, c.endpoint_a, c.endpoint_b,
       c.state, c.created_at, c.opened_at,
       COALESCE(ea.station_id,''), COALESCE(eb.station_id,'')
FROM channel c
LEFT JOIN endpoint ea ON ea.id = c.endpoint_a
LEFT JOIN endpoint eb ON eb.id = c.endpoint_b
WHERE c.channel_id=?`, channelID).
		Scan(&ch.ID, &ch.ChannelID, &ch.SpaceID, &ch.OwnerActorID, &ch.EndpointA, &bID, &ch.State, &ch.CreatedAt, &opn,
			&stnA, &stnB)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, ErrNotFound
	}
	if err != nil {
		return nil, 0, err
	}
	ch.EndpointB, ch.OpenedAt = bID.Int64, opn.String

	// Membership is by endpoint OR, for a bound endpoint, by STATION.
	//
	// The station half is not a convenience: without it, inheriting a predecessor's
	// mail is a half-feature. A replacement reader is by construction NOT a member of
	// the channel its predecessor joined — that is the whole point, no re-pairing —
	// so an endpoint-only check lets it POLL the inherited messages and then refuse
	// every follow-up: it could not reply to them (Send resolves the peer through
	// here) and could not acknowledge cumulatively. It would loop on mail it had
	// already acted upon while the sender waited for an answer that could not be
	// sent.
	//
	// Widening this means any reader of a station can act on any channel one of its
	// siblings joined. That IS the model: S4 makes the STATION the party to the
	// relationship, and the endpoint merely a credentialed reader of it.
	var peer int64
	switch {
	case ep.ID == ch.EndpointA:
		peer = ch.EndpointB
	case ep.ID == ch.EndpointB:
		peer = ch.EndpointA
	case ep.StationID != "" && ep.StationID == stnA:
		peer = ch.EndpointB
	case ep.StationID != "" && ep.StationID == stnB:
		peer = ch.EndpointA
	default:
		// Not a member. ErrNotFound, not ErrDenied: a non-member must not learn
		// that this channel id exists.
		return nil, 0, ErrNotFound
	}
	if !ch.Open() || peer == 0 {
		return &ch, 0, ErrChannelClosed
	}
	return &ch, peer, nil
}

// ListChannels returns the channels an endpoint belongs to.
func (s *Store) ListChannels(ctx context.Context, ep *Endpoint) ([]Channel, error) {
	rows, err := s.R.QueryContext(ctx, `
SELECT id, channel_id, space_id, owner_actor_id, endpoint_a, COALESCE(endpoint_b,0), state,
       created_at, COALESCE(opened_at,'')
FROM channel WHERE endpoint_a=? OR endpoint_b=? ORDER BY created_at DESC`, ep.ID, ep.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Channel
	for rows.Next() {
		var c Channel
		if err := rows.Scan(&c.ID, &c.ChannelID, &c.SpaceID, &c.OwnerActorID, &c.EndpointA, &c.EndpointB,
			&c.State, &c.CreatedAt, &c.OpenedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// RevokeChannel closes a channel permanently. This is the operator's brake: a
// security model whose enforcement point is the human needs the human to have one.
func (s *Store) RevokeChannel(ctx context.Context, channelID string) error {
	res, err := s.W.ExecContext(ctx, `
UPDATE channel SET state='revoked', revoked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
WHERE channel_id=? AND state<>'revoked'`, channelID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// openChannelsBetweenStations is the pair predicate shared by the count and the
// revoke below. Both column orders are matched because which seat a station took is
// an accident of who opened the channel.
//
// It reads the SNAPSHOT on the channel row, never a JOIN to the endpoint's current
// station_id. Binding is mutable by an agent tool: an earlier version derived the
// pair at query time, so a single comm_unbind — the path comm_unbind's own
// description recommends — made a channel invisible to the revocation meant to end
// it, while the console reported "0 live channels" and the sweep closed none. The
// mirror case severed an UNRELATED link's traffic. Authorisation is a fact about the
// past and must not be re-derived from state that has moved. See migration 0008.
const openChannelsBetweenStations = `
  FROM channel c
 WHERE c.state='open'
   AND ((c.station_a=? AND c.station_b=?) OR (c.station_a=? AND c.station_b=?))`

// CountOpenChannelsBetweenStations reports how much live traffic revoking a link
// would end. It exists to be shown BEFORE the click: S6 asks for the blast radius
// in front of the human, and "revoke" with no number attached is a button people
// either avoid or press twice.
//
// Returns 0 rather than an error when the pair has never spoken, which is the
// common case and is not a failure.
func (s *Store) CountOpenChannelsBetweenStations(ctx context.Context, stationA, stationB string) (int, error) {
	if stationA == "" || stationB == "" {
		return 0, nil
	}
	var n int
	err := s.R.QueryRowContext(ctx,
		`SELECT COUNT(*)`+openChannelsBetweenStations,
		stationA, stationB, stationB, stationA).Scan(&n)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// RevokeChannelsBetweenStations closes every open channel between two stations and
// returns how many it closed.
//
// This is the caller RevokeStationLink's doc comment asks for: revoking the LINK
// withdraws the permission, but a channel opened while the permission held keeps
// working, because the channel row carries its own state. Ending the relationship
// without ending its live traffic is a revocation that revokes nothing observable —
// the same shape as a flag with no reader.
//
// Idempotent: revoking twice closes nothing the second time and returns 0. A pair
// that never spoke is not an error.
func (s *Store) RevokeChannelsBetweenStations(ctx context.Context, stationA, stationB string) (int, error) {
	if stationA == "" || stationB == "" {
		return 0, nil
	}
	var n int64
	err := s.tx(ctx, func(t *sql.Tx) error {
		res, err := t.ExecContext(ctx, `
UPDATE channel
   SET state='revoked', revoked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
 WHERE id IN (SELECT c.id`+openChannelsBetweenStations+`)`,
			stationA, stationB, stationB, stationA)
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		return nil
	})
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

// channelByRowID loads a channel inside an open transaction.
func channelByRowID(ctx context.Context, t *sql.Tx, id int64) (*Channel, error) {
	var (
		c   Channel
		bID sql.NullInt64
		opn sql.NullString
	)
	err := t.QueryRowContext(ctx, `
SELECT id, channel_id, space_id, owner_actor_id, endpoint_a, endpoint_b, state, created_at, opened_at
FROM channel WHERE id=?`, id).
		Scan(&c.ID, &c.ChannelID, &c.SpaceID, &c.OwnerActorID, &c.EndpointA, &bID, &c.State, &c.CreatedAt, &opn)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	c.EndpointB, c.OpenedAt = bID.Int64, opn.String
	return &c, nil
}

// OpenLinkedChannel materializes a channel between two station-bound endpoints whose
// stations a human has already linked (docs/STATIONS.md S9).
//
// This is what a link is FOR. Without it a link records the human's decision without
// ever spending it, and every conversation still costs a pairing code — which is the
// step the link exists to remove. The decision itself is not removed: the channel can
// only come into existence because a human approved the relationship, which is the
// same gate one level up.
//
// The caller MUST have verified the link in the knowledge-base store first; this
// package cannot see it. That is S7's boundary, not laziness — comm.db holds no
// durable authorization and must not start.
//
// Opened directly rather than left pending: the pairing flow is pending-until-both-join
// because a CODE is a rendezvous between two sessions that have not met. A link has
// already established that both stations may talk, so there is nothing to wait for.
// Idempotent — asking twice returns the existing channel rather than a second one,
// because a session that retries after a lost response must not fragment the
// conversation into two.
func (s *Store) OpenLinkedChannel(ctx context.Context, a, b *Endpoint, ownerActorID int64, label string) (*Channel, error) {
	if a.ID == b.ID {
		return nil, ErrDenied
	}
	if a.StationID == "" || b.StationID == "" {
		return nil, ErrDenied
	}
	var out *Channel
	err := s.tx(ctx, func(t *sql.Tx) error {
		// An existing OPEN channel between these two STATIONS is reused, not
		// duplicated — matched on stations rather than endpoints so a replacement
		// session on either side finds the conversation its predecessor was having
		// instead of starting a parallel one.
		var existing string
		err := t.QueryRowContext(ctx, `
SELECT c.channel_id`+openChannelsBetweenStations+`
 LIMIT 1`, a.StationID, b.StationID, b.StationID, a.StationID).Scan(&existing)
		if err == nil {
			ch, _, cerr := s.channelByPublicID(ctx, t, existing)
			out = ch
			return cerr
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}

		channelID, err := randBase62(22)
		if err != nil {
			return err
		}
		res, err := t.ExecContext(ctx, `
INSERT INTO channel(channel_id, space_id, owner_actor_id, endpoint_a, endpoint_b, state, opened_at, label,
                    station_a, station_b)
VALUES(?,?,?,?,?, 'open', strftime('%Y-%m-%dT%H:%M:%fZ','now'), ?, ?, ?)`,
			channelID, a.Owner.SpaceID, ownerActorID, a.ID, b.ID, nullStr(label),
			a.StationID, b.StationID)
		if err != nil {
			return err
		}
		if _, err := res.LastInsertId(); err != nil {
			return err
		}
		ch, _, cerr := s.channelByPublicID(ctx, t, channelID)
		out = ch
		return cerr
	})
	return out, err
}

// channelByPublicID loads a channel inside an open transaction.
func (s *Store) channelByPublicID(ctx context.Context, t *sql.Tx, channelID string) (*Channel, int64, error) {
	var (
		ch  Channel
		bID sql.NullInt64
		opn sql.NullString
	)
	err := t.QueryRowContext(ctx, `
SELECT id, channel_id, space_id, owner_actor_id, endpoint_a, endpoint_b, state, created_at, opened_at
FROM channel WHERE channel_id=?`, channelID).
		Scan(&ch.ID, &ch.ChannelID, &ch.SpaceID, &ch.OwnerActorID, &ch.EndpointA, &bID, &ch.State, &ch.CreatedAt, &opn)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, ErrNotFound
	}
	if err != nil {
		return nil, 0, err
	}
	ch.EndpointB, ch.OpenedAt = bID.Int64, opn.String
	return &ch, ch.EndpointB, nil
}

// Staffing is what COMM knows about whether anyone is actually at a station: how
// many live endpoints are reading for it, and when the freshest of them was last
// seen.
//
// Deliberately two facts rather than one boolean. "Staffed" is a judgement about
// freshness and the right threshold depends on how the reader intends to use it — a
// directory shown to a human and a routing decision made by an agent do not want the
// same cutoff. Reporting the inputs and letting the caller judge is the same choice
// the console makes everywhere else: never fake a number, never hide one.
type Staffing struct {
	Endpoints  int    // live (non-revoked) endpoints bound to the station
	LastSeenAt string // freshest last_seen_at across them; empty when Endpoints is 0
}

// StaffingByStation reports staffing for every station COMM has ever seen an endpoint
// for, in ONE query.
//
// Batch rather than per-station on purpose: a directory listing N stations must not
// cost N round trips, and the per-station form already exists for the single-target
// case (LiveEndpointForStation). Stations with no endpoint are simply absent from the
// map — a missing key means "nobody has ever staffed this", which is what the caller
// wants to render, and materialising a zero row for every station in the space would
// make the map lie about which ones COMM knows.
func (s *Store) StaffingByStation(ctx context.Context) (map[string]Staffing, error) {
	rows, err := s.R.QueryContext(ctx, `
SELECT station_id, COUNT(*), COALESCE(MAX(last_seen_at),'')
  FROM endpoint
 WHERE station_id IS NOT NULL AND station_id <> '' AND revoked_at IS NULL
 GROUP BY station_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]Staffing)
	for rows.Next() {
		var id string
		var st Staffing
		if err := rows.Scan(&id, &st.Endpoints, &st.LastSeenAt); err != nil {
			return nil, err
		}
		out[id] = st
	}
	return out, rows.Err()
}

// LiveEndpointForStation returns the most recently seen live endpoint reading for a
// station, or nil when nobody is staffing it.
//
// "Most recent" rather than "the only one" because a station may legitimately have
// several readers (S4). Picking the freshest is a heuristic for "who is actually
// here", and it does not need to be exact: whichever endpoint is chosen, the message
// lands in the STATION's inbox and any reader can claim it.
func (s *Store) LiveEndpointForStation(ctx context.Context, stationID string) (*Endpoint, error) {
	var id int64
	err := s.R.QueryRowContext(ctx, `
SELECT id FROM endpoint
 WHERE station_id=? AND revoked_at IS NULL
 ORDER BY last_seen_at DESC LIMIT 1`, stationID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return s.endpointByRowID(ctx, id)
}
