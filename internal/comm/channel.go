package comm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Channel joins exactly two distinct endpoints, full-duplex.
//
// There is deliberately no turn or "waiting" state here: channel-level
// turn-taking is a distributed state machine that wedges when a session dies
// mid-turn. Request/response is a property of a message instead (see message.go).
type Channel struct {
	ID           int64 // internal rowid
	ChannelID    string
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
func (s *Store) MintPairingCode(ctx context.Context, humanActorID int64, label string) (string, error) {
	code, err := randBase62(10)
	if err != nil {
		return "", err
	}
	_, err = s.W.ExecContext(ctx, `
INSERT INTO pairing_code(code_sha256, human_actor_id, label, expires_at)
VALUES(?,?,?, strftime('%Y-%m-%dT%H:%M:%fZ','now',?))`,
		sha256Hex(code), humanActorID, nullStr(label),
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
			humanID  int64
			chanID   sql.NullInt64
			consumed sql.NullString
			pcLabel  sql.NullString
		)
		err := t.QueryRowContext(ctx, `
SELECT id, human_actor_id, channel_id, consumed_at, label
FROM pairing_code
WHERE code_sha256=? AND expires_at > strftime('%Y-%m-%dT%H:%M:%fZ','now')`,
			sha256Hex(code)).Scan(&pcID, &humanID, &chanID, &consumed, &pcLabel)
		if errors.Is(err, sql.ErrNoRows) {
			// Expired and unknown are indistinguishable on purpose: a caller must
			// not be able to probe which codes exist or existed.
			return ErrNotFound
		}
		if err != nil {
			return err
		}

		// First redeem: create the pending channel and bind the code to it.
		if !chanID.Valid {
			channelID, err := randBase62(22)
			if err != nil {
				return err
			}
			// THE AUTHORISING PAIR IS SNAPSHOTTED HERE TOO, not only on the linked
			// path. Migration 0008 moved link revocation onto these columns precisely so
			// authorisation could not be re-derived from a binding an agent can change —
			// but only OpenLinkedChannel was taught to write them. A channel opened by
			// PAIRING CODE between two station-bound endpoints therefore carried NULLs,
			// and the predicate that finds "open channels between these two stations"
			// could not see it: revoking the link left the channel open, while the console
			// counted zero live channels and reported the revocation as complete.
			//
			// NULL when the joiner is unbound, which is correct rather than a gap: there is
			// no station whose link could authorise it, so there is nothing for a link
			// revocation to reach.
			res, err := t.ExecContext(ctx, `
INSERT INTO channel(channel_id, owner_actor_id, endpoint_a, state, label, station_a)
VALUES(?,?,?, 'pending', ?, ?)`, channelID, humanID, ep.ID, nullStr(pcLabel.String), nullStr(ep.StationID))
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
		// A STATION MUST NOT BECOME ITS OWN PEER. The rowid comparison above answers
		// "is this exact connection already seated", and the schema's
		// CHECK (endpoint_b <> endpoint_a) enforces only the same literal rowid — so a
		// SECOND endpoint of a station that already holds a seat matched neither, fell
		// through, and took the free one. The channel then had station_a = station_b, and
		// ChannelFor's station arms resolved that station's peer to itself.
		//
		// It is not an exotic path: a replacement session re-redeeming the code its
		// predecessor was given is the ordinary way to reach it, and the code is still
		// valid for its TTL. What that session actually wants is this branch — its station
		// is already a party, so the join is a no-op and the channel is returned as it
		// stands.
		var stnA, stnB string
		if err := t.QueryRowContext(ctx,
			`SELECT COALESCE(station_a,''), COALESCE(station_b,'') FROM channel WHERE id=?`,
			cur.ID).Scan(&stnA, &stnB); err != nil {
			return err
		}
		if ep.StationID != "" && (ep.StationID == stnA || ep.StationID == stnB) {
			ch = cur // the STATION is already on it; a second reader changes nothing
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
UPDATE channel SET endpoint_b=?, station_b=?, state='open', opened_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
WHERE id=? AND endpoint_b IS NULL AND state='pending'`, ep.ID, nullStr(ep.StationID), cur.ID)
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
	var snapA, snapB string
	err := s.R.QueryRowContext(ctx, `
SELECT c.id, c.channel_id, c.owner_actor_id, c.endpoint_a, c.endpoint_b,
       c.state, c.created_at, c.opened_at,
       COALESCE(ea.station_id,''), COALESCE(eb.station_id,''),
       COALESCE(c.station_a,''), COALESCE(c.station_b,'')
FROM channel c
LEFT JOIN endpoint ea ON ea.id = c.endpoint_a
LEFT JOIN endpoint eb ON eb.id = c.endpoint_b
WHERE c.channel_id=?`, channelID).
		Scan(&ch.ID, &ch.ChannelID, &ch.OwnerActorID, &ch.EndpointA, &bID, &ch.State, &ch.CreatedAt, &opn,
			&stnA, &stnB, &snapA, &snapB)
	if errors.Is(err, sql.ErrNoRows) {
		// D2. A ROOM ID PASSED AS channel_id LANDS HERE, and a bare "not found" is what
		// made a working station conclude the feature did not exist.
		//
		// ken-promo, cold and with no peer evidence: passed a room id as channel_id
		// because channel_id is the only addressing parameter their captured schema has,
		// got "not found", searched their tools for a room-send call, found none, and
		// reported to their human that rooms were receive-only. They are the PROMOTION
		// station — copy written that afternoon would have said so.
		//
		// The same call answers precisely once you already know the answer: passing both
		// parameters returns "pass exactly one of channel_id or to_room". THE GOOD ERROR
		// IS UNREACHABLE FROM THE STATE A NEW CALLER IS IN, which is the whole defect.
		// Mentioning rooms here costs a string and ends the detour.
		//
		// MARKED CallerSafe, because wrapping alone was not enough and that is the part
		// this shipped without. The 3.3.0 mapper answers every ErrNotFound with the
		// literal "not found", so both strings below were in the binary and neither
		// reached a caller — production probed the running image with this exact call and
		// got the same bare error promo got before any of it was written.
		if s.callerIsInRoom(ctx, ep, channelID) {
			return nil, 0, CallerSafe(fmt.Errorf("%w: %q is a ROOM, not a channel — address it with to_room instead of channel_id. "+
				"A room needs no pairing code: your human putting your station in it is the whole authorisation", ErrNotFound, channelID))
		}
		return nil, 0, CallerSafe(fmt.Errorf("%w: no channel %q. If you meant a ROOM, the parameter is to_room rather than channel_id — "+
			"comm_directory lists the rooms you are in", ErrNotFound, channelID))
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
	//
	// THE SNAPSHOT ON THE CHANNEL ROW IS CONSULTED AS WELL AS THE LIVE BINDING, because
	// the live one is mutable by an agent tool. `comm_unbind` clears endpoint.station_id
	// — and it is prescribed BY NAME in the guidance a session gets when a sequence
	// collides — after which the join above yields '' while channel.station_a still names
	// the station. The successor then matched no arm and got ErrNotFound, while Poll,
	// which is party-keyed, kept handing it the mail: it could read its station's messages
	// and neither reply, offer a file, nor ack cumulatively. That is precisely the
	// poll-but-cannot-answer half-feature the paragraph above says this branch exists to
	// prevent, reintroduced through a column the same tool can clear.
	//
	// `openChannelsBetweenStations` already states the principle in full — authorisation
	// is a fact about the past and must not be re-derived from state that has moved — and
	// reads the snapshot for exactly this reason. The authorisation check itself did not.
	//
	// THE SNAPSHOT IS NOT MADE AUTHORITATIVE HERE, deliberately. It is NULL on every
	// channel opened by a pairing code before migration 0008, which is most of them on a
	// real deployment — dropping the live join would strand those channels' successors
	// rather than fix anything. Backfilling the column is a migration, and a migration
	// ships alone. Until then the two are consulted together: the snapshot can only add
	// membership the human authorised, never remove any that works today.
	var peer int64
	switch {
	case ep.ID == ch.EndpointA:
		peer = ch.EndpointB
	case ep.ID == ch.EndpointB:
		peer = ch.EndpointA
	case ep.StationID != "" && (ep.StationID == stnA || ep.StationID == snapA):
		peer = ch.EndpointB
	case ep.StationID != "" && (ep.StationID == stnB || ep.StationID == snapB):
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
	// STATION-SCOPED, matching Poll and ChannelFor. An endpoint bound to a station
	// can RECEIVE on a channel its predecessor joined — that is the whole point of the
	// station owning the inbox — so listing only channels this endpoint's own rowid
	// sits on left a replacement session able to poll mail and reply to it while
	// comm_channels reported ZERO channels. It could act on a conversation it could
	// not enumerate, which is worst for the exact case stations exist for: a takeover.
	//
	// The three predicates must agree. They did not, and the drift was invisible
	// because each is correct in isolation.
	seatQ := `endpoint_a=?1 OR endpoint_b=?1`
	args := []any{ep.ID}
	if ep.StationID != "" {
		seatQ = `endpoint_a IN (SELECT id FROM endpoint WHERE station_id=?2)
              OR endpoint_b IN (SELECT id FROM endpoint WHERE station_id=?2)`
		args = append(args, ep.StationID)
	}
	rows, err := s.R.QueryContext(ctx, `
SELECT id, channel_id, owner_actor_id, endpoint_a, COALESCE(endpoint_b,0), state,
       created_at, COALESCE(opened_at,'')
FROM channel WHERE `+seatQ+` ORDER BY created_at DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Channel
	for rows.Next() {
		var c Channel
		if err := rows.Scan(&c.ID, &c.ChannelID, &c.OwnerActorID, &c.EndpointA, &c.EndpointB,
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
SELECT id, channel_id, owner_actor_id, endpoint_a, endpoint_b, state, created_at, opened_at
FROM channel WHERE id=?`, id).
		Scan(&c.ID, &c.ChannelID, &c.OwnerActorID, &c.EndpointA, &bID, &c.State, &c.CreatedAt, &opn)
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
INSERT INTO channel(channel_id, owner_actor_id, endpoint_a, endpoint_b, state, opened_at, label,
                    station_a, station_b)
VALUES(?,?,?,?, 'open', strftime('%Y-%m-%dT%H:%M:%fZ','now'), ?, ?, ?)`,
			channelID, ownerActorID, a.ID, b.ID, nullStr(label),
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
SELECT id, channel_id, owner_actor_id, endpoint_a, endpoint_b, state, created_at, opened_at
FROM channel WHERE channel_id=?`, channelID).
		Scan(&ch.ID, &ch.ChannelID, &ch.OwnerActorID, &ch.EndpointA, &bID, &ch.State, &ch.CreatedAt, &opn)
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

// PendingForEndpoint counts the messages waiting for an endpoint on each open channel,
// WITHOUT delivering any of them.
//
// This exists so "check what is waiting before you send" can be a LOOK rather than a
// delivery. comm_poll is the only other way to find out, and polling takes delivery: it
// stamps first_delivered_at, arms the expiry and reply clocks, and hands the session
// messages it is then on the hook to handle. An instruction that costs a delivery every
// time you send is an instruction that gets skipped — and a skipped instruction is worse
// than none, because it still looks like a control.
//
// Counts 'queued' only, not 'delivered'. A delivered-but-unacked message has already
// been shown to this session; telling it to "go and read what is waiting" would be
// telling it to re-read something it has.
//
// 'queued' ALONE IS NOT ENOUGH, and since this query was written it was all this
// query asked. A message can be expired with its delivery still 'queued' — the sweeper
// flips it on an interval — so the count also carries the shared expiry clause
// (pendingNotExpiredSQL, pending.go), in the delivery JOIN and not the WHERE, so a
// channel with nothing waiting still reports 0 instead of vanishing from the map. That is the same distinction that made
// waiting_for_you fire on mail its recipient had already replied to.
//
// CHANNEL SCOPES ONLY, and the limit is stated here because it cannot be seen from the
// signature: the query is `FROM channel`, so a scope with no channel row contributes
// nothing and can contribute nothing. Room mail is counted by RoomsFor, broadcast by
// BroadcastPendingFor, and everything at once by PendingTotalFor (pending.go). Widening
// THIS function is not the fix — its result is keyed by channel_id, and there is no
// channel_id to key a room under.
func (s *Store) PendingForEndpoint(ctx context.Context, ep *Endpoint) (map[string]int, error) {
	// TWO COLUMNS ARE CALLED channel_id AND THEY ARE NOT THE SAME THING.
	// channel.channel_id is the opaque server-minted TEXT id a session sees;
	// message.channel_id is an INTEGER foreign key to channel.id (0001_init.sql:84
	// vs :153). Joining them by name compares an integer to a string, matches nothing,
	// and returns zero for every channel — which is indistinguishable from an empty
	// inbox, so a caller believes it. The join is on c.id; only the SELECT emits the
	// public id.
	//
	// Plain `?` with the id repeated, never `?N`: message.go:207 records that mixing
	// numbered and auto-assigned placeholders renumbers the auto-assigned ones and binds
	// the wrong values silently.
	// The count is over DELIVERY rows for this endpoint's PARTY. Both party forms are
	// matched for a bound endpoint, exactly as Poll and Ack do — a count that used the
	// station form alone would read zero for mail filed under the endpoint's own rowid
	// after a backwards ken.db restore, and zero here is indistinguishable from an
	// empty inbox, so a caller believes it.
	party := endpointPartyKey(ep.ID)
	altParty := party
	if ep.StationID != "" {
		party = stationParty(ep.StationID)
	}
	// THE SEAT PREDICATE IS STATION-SCOPED, matching ListChannels above.
	//
	// It was endpoint-scoped while the LIST was station-scoped, and the two disagreeing is
	// worse than either being wrong alone: a successor endpoint bound to the same station
	// got the inherited channel LISTED — because ListChannels was widened for exactly that
	// takeover case — with `pending: 0` beside it, while mail sat queued for the station.
	//
	// A missing row is a silence a caller can notice. A row that says zero is an
	// ASSERTION, and this one was false in the situation stations exist for. The
	// endpoint's own rowid is still matched, so an unbound endpoint is unaffected.
	seat := `(c.endpoint_a = ? OR c.endpoint_b = ?)`
	args := []any{party, altParty, ep.ID, ep.ID}
	if ep.StationID != "" {
		seat = `(c.endpoint_a IN (SELECT id FROM endpoint WHERE station_id=?)
              OR c.endpoint_b IN (SELECT id FROM endpoint WHERE station_id=?))`
		args = []any{party, altParty, ep.StationID, ep.StationID}
	}
	// Built as a plain-`?` fragment with its arguments appended in order, NOT copied from
	// ListChannels — that one uses `?1`/`?2`, and the note above forbids mixing numbered
	// with auto-assigned placeholders in one statement. `channel.station_a`/`station_b`
	// are deliberately NOT used here either: those are the authorisation snapshot taken
	// when the channel was opened, not the current binding.
	rows, err := s.R.QueryContext(ctx, pendingSQL(`
SELECT c.channel_id, COUNT(d.id)
  FROM channel c
  LEFT JOIN message m ON m.channel_id = c.id
  LEFT JOIN delivery d
    ON d.message_row = m.id
   AND (d.party_key = ? OR d.party_key = ?)
   AND d.state = 'queued'
   AND %NOTEXPIRED%
 WHERE c.state='open' AND `+seat+`
 GROUP BY c.channel_id`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// callerIsInRoom reports whether this id names a room THE CALLER IS A MEMBER OF.
//
// Membership, not existence, and the difference is a security property rather than a
// nicety. My first version asked only whether the room existed, and the test written
// alongside it caught the consequence immediately: a station that is NOT in a room got
// told "that is a ROOM", which confirms its existence. That is precisely the oracle
// comm_open_channel's uniform refusal exists to close — reopened by a helpful error
// message, which is how these usually come back.
//
// A member already knows the room exists, so telling them costs nothing. Everyone else
// gets the generic text, which mentions rooms only as a CONCEPT — true for every caller
// and informative to none of them about who is in what.
func (s *Store) callerIsInRoom(ctx context.Context, ep *Endpoint, id string) bool {
	party := endpointPartyKey(ep.ID)
	if ep.StationID != "" {
		party = stationParty(ep.StationID)
	}
	var n int
	if err := s.R.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM room_member_mirror WHERE room_id=? AND party_key=?`, id, party).Scan(&n); err != nil {
		return false
	}
	return n > 0
}
