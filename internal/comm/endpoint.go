package comm

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
)

// Endpoint is one AI session's communication point.
//
// Sender identity derived from an Endpoint is honestly described as
// "token-authenticated, endpoint-scoped": trustworthy across machines and users,
// advisory between two sessions that share one token (the operating convention is
// one token per MACHINE). The endpoint secret is what stops two sessions on one
// box from polling and acking each other's messages by accident.
type Endpoint struct {
	ID         int64 // internal rowid
	EndpointID string
	Owner      Owner
	Label      string
	HostHint   string
	CreatedAt  string
	LastSeenAt string
	// RotatedAt and RotateCount are DISPLAY state for the console ("did I already
	// rotate this one?"). comm.db is expendable and not backed up, so the
	// authoritative audit record is the server log, not these columns.
	RotatedAt   string
	RotateCount int

	// StationID is the durable station this endpoint reads for, or "" when unbound
	// (docs/STATIONS.md S4). An opaque id into ken.db with no foreign key: S7's rule
	// is that cross-database pointers run expendable -> durable, and under restore
	// skew an id that no longer resolves is treated as UNBOUND rather than as an
	// error. Bound endpoints share their station's inbox with claim-once delivery.
	StationID string
	// BoundByStationKeyID is the station key that authorised the binding. Revoking
	// that key severs every endpoint it bound (S6) — without this column, revocation
	// would stop future bindings and leave the leaked capability running.
	BoundByStationKeyID string
	BoundAt             string
}

// RegisterEndpoint mints a new endpoint for an authenticated session and returns
// it together with its one-time secret, which is never recoverable afterwards.
//
// A repeat registration under the same token and label deliberately creates a NEW
// endpoint rather than attaching to the existing one: silently handing a second
// session the first session's inbox is the failure this avoids, and it is far more
// likely by accident (two sessions with the same label) than by malice.
//
// hostHint is stored opaquely and is never consulted for authorization — see the
// schema comment and docs/COMM.md C9 for why a self-reported machine identity
// cannot prove a shared filesystem.
func (s *Store) RegisterEndpoint(ctx context.Context, owner Owner, label, hostHint string) (*Endpoint, string, error) {
	endpointID, err := randBase62(22)
	if err != nil {
		return nil, "", err
	}
	secret, err := randBase62(40)
	if err != nil {
		return nil, "", err
	}

	var id int64
	err = s.tx(ctx, func(t *sql.Tx) error {
		res, err := t.ExecContext(ctx, `
INSERT INTO endpoint(endpoint_id, secret_sha256, token_id, actor_id, space_id, label, host_hint)
VALUES(?,?,?,?,?,?,?)`,
			endpointID, sha256Hex(secret), owner.TokenID, owner.ActorID, owner.SpaceID,
			nullStr(label), nullStr(hostHint))
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	if err != nil {
		return nil, "", err
	}

	ep, err := s.endpointByRowID(ctx, id)
	if err != nil {
		return nil, "", err
	}
	return ep, secret, nil
}

// AuthenticateEndpoint resolves an endpoint id + secret to an Endpoint, and
// refreshes last_seen_at.
//
// The secret is compared in constant time. A revoked endpoint authenticates as
// ErrDenied rather than ErrNotFound so a caller cannot use the distinction to
// probe which endpoint ids exist.
func (s *Store) AuthenticateEndpoint(ctx context.Context, endpointID, secret string) (*Endpoint, error) {
	var (
		ep      Endpoint
		hash    string
		revoked sql.NullString
		label   sql.NullString
		hint    sql.NullString
	)
	err := s.R.QueryRowContext(ctx, `
SELECT id, endpoint_id, secret_sha256, token_id, actor_id, space_id, label, host_hint,
       created_at, last_seen_at, revoked_at,
       COALESCE(station_id,''), COALESCE(bound_by_station_key_id,''), COALESCE(bound_at,'')
FROM endpoint WHERE endpoint_id=?`, endpointID).
		Scan(&ep.ID, &ep.EndpointID, &hash, &ep.Owner.TokenID, &ep.Owner.ActorID, &ep.Owner.SpaceID,
			&label, &hint, &ep.CreatedAt, &ep.LastSeenAt, &revoked,
			&ep.StationID, &ep.BoundByStationKeyID, &ep.BoundAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if subtle.ConstantTimeCompare([]byte(hash), []byte(sha256Hex(secret))) != 1 {
		return nil, ErrDenied
	}
	if revoked.Valid && revoked.String != "" {
		return nil, ErrDenied
	}
	ep.Label, ep.HostHint = label.String, hint.String

	// Throttled to at most once a minute so a poll loop does not amplify into a
	// write on every request — the same shape as the knowledge base's TouchToken.
	_, _ = s.W.ExecContext(ctx, `
UPDATE endpoint SET last_seen_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
WHERE id=? AND last_seen_at < strftime('%Y-%m-%dT%H:%M:%fZ','now','-60 seconds')`, ep.ID)

	return &ep, nil
}

// RotateEndpointSecret replaces an endpoint's secret and returns the new one,
// shown once. The endpoint keeps its id, its owner and — the point of the whole
// operation — every channel it belongs to, so its peers are unaffected and nothing
// needs re-pairing.
//
// THIS IS DELIBERATELY NOT REACHABLE FROM ANY TOOL, and that placement is the
// entire security argument. One bearer token covers a machine, so the endpoint
// pair is the only thing separating two sessions sharing it; a reissue any SESSION
// could trigger would let any session on that machine seize any endpoint on it.
// That is why deriving a new secret from token material was rejected. The defect
// there is the AUTOMATION, not the reissuing — so rotation lives behind curator
// authentication, which is a credential no session holds or can obtain from the
// machine, and a neighbouring session with the COMM token gains nothing.
//
// Two callers in mind, and the second is the stronger reason to have it:
//
//   - A session lost its secret (context compaction destroys it, and it is
//     unrecoverable by construction). Today that costs one fresh pairing code PER
//     CHANNEL plus coordinated re-joins with every peer.
//   - A secret LEAKED — into a transcript, a log, a file something else could read.
//     Until now the only remedy was revoking the endpoint and rebuilding every
//     channel from scratch, which is why containing a leak was expensive enough to
//     hesitate over. Rotation is the missing incident-response primitive.
//
// A revoked endpoint is refused: rotating one would quietly resurrect a capability
// an operator deliberately destroyed, and the revoke path is what a leak response
// escalates TO, never back from.
func (s *Store) RotateEndpointSecret(ctx context.Context, endpointID string) (string, error) {
	secret, err := randBase62(40)
	if err != nil {
		return "", err
	}
	res, err := s.W.ExecContext(ctx, `
UPDATE endpoint
   SET secret_sha256=?,
       secret_rotated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),
       rotate_count=COALESCE(rotate_count,0)+1
 WHERE endpoint_id=? AND revoked_at IS NULL`, sha256Hex(secret), endpointID)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Unknown and revoked are one answer, matching AuthenticateEndpoint's stance:
		// the console caller is already authenticated, so it loses nothing, and the
		// two cases never diverge into a probe.
		return "", ErrNotFound
	}
	return secret, nil
}

// BindEndpointToStation attaches an endpoint to a station, making it a reader of
// that station's inbox rather than the sole owner of its own (docs/STATIONS.md S4).
//
// Called from comm_register AFTER the caller's binding voucher has been redeemed on
// the durable side: this function trusts stationID because RedeemBindingVoucher is
// what established it, and there is deliberately no path that lets a caller name a
// station directly. Binding is set once at registration and never changed — an
// endpoint that could move between stations would let a session carry another
// station's unread mail across, which is the shared-inbox failure in a new costume.
func (s *Store) BindEndpointToStation(ctx context.Context, endpointID, stationID, keyID string) error {
	var n int64
	err := s.tx(ctx, func(t *sql.Tx) error {
		var rowID int64
		if err := t.QueryRowContext(ctx,
			`SELECT id FROM endpoint WHERE endpoint_id=? AND station_id IS NULL AND revoked_at IS NULL`,
			endpointID).Scan(&rowID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}

		// CARRY THE SEQUENCE COUNTER ACROSS, and this is not bookkeeping — without it
		// adoption breaks every channel the endpoint has already used.
		//
		// The per-channel counter keys on the sending STATION once bound and on the
		// endpoint rowid otherwise. So binding mid-conversation moves this endpoint to a
		// FRESH counter starting at 1, while `message` carries a UNIQUE index on
		// (channel_id, sender_endpoint, seq) — its own earlier messages are already at
		// 1, 2, 3. The next send is a constraint violation and the endpoint simply
		// cannot talk on that channel any more.
		//
		// Found by binding this very repository's session and watching its own channel
		// stop working. Two correct pieces — station-keyed sequencing so a REPLACEMENT
		// session does not restart at 1, and in-place adoption so a RUNNING session
		// keeps its channels — that together switched counter namespaces mid-stream.
		//
		// THE CARRY-FORWARD IS GONE, and so is the defect it patched. Sequence numbers
		// are per SCOPE now (migration 0009), spanning every sender in a conversation,
		// so binding does not move a sender between counters — there is no second
		// namespace to move to. The merge that used to run here was necessary only
		// because numbering was keyed on WHO was sending; keying it on WHERE the
		// message lives makes the question disappear rather than answering it.
		//
		// This also retires the operational warning that went with it: binding a
		// long-lived endpoint no longer restarts its outbound numbering, so nothing has
		// to be counted before and after.

		res, err := t.ExecContext(ctx, `
UPDATE endpoint
   SET station_id=?, bound_by_station_key_id=?,
       bound_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
 WHERE endpoint_id=? AND station_id IS NULL AND revoked_at IS NULL`,
			stationID, keyID, endpointID)
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		return nil
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// UnbindEndpointFromStation returns a bound endpoint to standing alone. It keeps its
// id, its secret and every channel it is in; only the station association goes.
//
// This exists because binding was a ONE-WAY DOOR and nobody should have to walk
// through one to try a feature. An operator weighing adoption asked the right
// question — "is it reversible?" — and the honest answer was no, which is a bad
// answer for a step whose whole purpose is to make things cheaper.
//
// What unbinding means for mail is the reason it is safe. Messages are addressed to
// an ENDPOINT rowid; the station merely widens which endpoint may read them. So
// unbinding narrows this endpoint back to its own mail and strands nothing: anything
// addressed to it is still addressed to it, and anything addressed to a sibling was
// never its to begin with. Claims it currently holds ARE released, because after
// unbinding it will not be polling for them and leaving them held would hide those
// messages from the station's remaining readers for the rest of the lease.
func (s *Store) UnbindEndpointFromStation(ctx context.Context, endpointID string) error {
	var n int64
	err := s.tx(ctx, func(t *sql.Tx) error {
		// Release any claim this endpoint holds on mail addressed to its STATION.
		// Unbinding makes it a stranger to that inbox, and a claim it can no longer
		// act on would hide those messages from the readers that can until the lease
		// expired. Mail addressed to the endpoint ITSELF keeps its claim — that mail
		// is still its own.
		if _, err := t.ExecContext(ctx, `
UPDATE delivery SET claimed_by_endpoint=NULL, claim_expires_at=NULL
 WHERE acked_at IS NULL
   AND claimed_by_endpoint = (SELECT id FROM endpoint WHERE endpoint_id=?)
   AND party_key <> 'e:' || (SELECT id FROM endpoint WHERE endpoint_id=?)`,
			endpointID, endpointID); err != nil {
			return err
		}
		// No counter to hand back, for the reason BindEndpointToStation gives: per-scope
		// numbering has one sequence per conversation, and it does not care who sends.

		res, err := t.ExecContext(ctx, `
UPDATE endpoint SET station_id=NULL, bound_by_station_key_id=NULL, bound_at=NULL
 WHERE endpoint_id=? AND station_id IS NOT NULL AND revoked_at IS NULL`, endpointID)
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		return nil
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SeverEndpointsBoundBy revokes every endpoint a given station key bound, and
// releases their claims. It reports how many were severed so the console can state
// the count BEFORE the click, as S6 requires.
//
// This is what makes revoking a station key mean something. You revoke because the
// key leaked; a revocation that stops future bindings but leaves the already-bound
// sessions running until an idle sweep notices is theatre — and traffic keeps an
// endpoint alive indefinitely, so the sweep may never come.
//
// Claims are released in the same statement rather than left to expire: a severed
// reader is never coming back to ack, so holding its messages for the rest of the
// lease would hide them from the station's remaining readers for no reason.
func (s *Store) SeverEndpointsBoundBy(ctx context.Context, keyID string) (int, error) {
	var n int64
	err := s.tx(ctx, func(t *sql.Tx) error {
		if _, err := t.ExecContext(ctx, `
UPDATE delivery
   SET claimed_by_endpoint=NULL, claim_expires_at=NULL
 WHERE acked_at IS NULL
   AND claimed_by_endpoint IN (SELECT id FROM endpoint WHERE bound_by_station_key_id=?)`,
			keyID); err != nil {
			return err
		}
		res, err := t.ExecContext(ctx, `
UPDATE endpoint SET revoked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
 WHERE bound_by_station_key_id=? AND revoked_at IS NULL`, keyID)
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		return nil
	})
	return int(n), err
}

// CountEndpointsBoundBy reports how many LIVE endpoints a station key bound, so the
// console can say "this will disconnect N live sessions" before the operator clicks
// (S6). A destructive action whose blast radius is only visible afterwards is one an
// operator learns to fear rather than use.
func (s *Store) CountEndpointsBoundBy(ctx context.Context, keyID string) (int, error) {
	var n int
	err := s.R.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM endpoint WHERE bound_by_station_key_id=? AND revoked_at IS NULL`,
		keyID).Scan(&n)
	return n, err
}

// RevokeEndpoint soft-revokes an endpoint, immediately denying further use.
// Its channels stay queryable for the operator; its messages age out normally.
func (s *Store) RevokeEndpoint(ctx context.Context, endpointID string) error {
	var n int64
	err := s.tx(ctx, func(t *sql.Tx) error {
		// Release whatever this endpoint was holding, in the same breath. S4 requires
		// a claim to be released when its endpoint is revoked: a revoked reader is
		// never coming back to ack, so holding its messages for the remainder of the
		// lease would hide them from the station's other readers for no reason — the
		// operator revoked a wedged session precisely so someone else could take over.
		if _, err := t.ExecContext(ctx, `
UPDATE delivery SET claimed_by_endpoint=NULL, claim_expires_at=NULL
 WHERE acked_at IS NULL
   AND claimed_by_endpoint = (SELECT id FROM endpoint WHERE endpoint_id=?)`, endpointID); err != nil {
			return err
		}
		res, err := t.ExecContext(ctx, `
UPDATE endpoint SET revoked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
WHERE endpoint_id=? AND revoked_at IS NULL`, endpointID)
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		return nil
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListEndpoints returns the endpoints owned by one space, newest first. Scoped by
// space from day 1 even though only one exists today: an unscoped listing would be
// the enumeration surface in a multi-human future, and scoping it later would be a
// behavioural break for anything that relied on the full list.
func (s *Store) ListEndpoints(ctx context.Context, spaceID int64) ([]Endpoint, error) {
	rows, err := s.R.QueryContext(ctx, `
SELECT id, endpoint_id, token_id, actor_id, space_id, COALESCE(label,''), COALESCE(host_hint,''),
       created_at, last_seen_at, COALESCE(secret_rotated_at,''), COALESCE(rotate_count,0),
       COALESCE(station_id,''), COALESCE(bound_by_station_key_id,''), COALESCE(bound_at,'')
FROM endpoint WHERE space_id=? AND revoked_at IS NULL ORDER BY created_at DESC`, spaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Endpoint
	for rows.Next() {
		var ep Endpoint
		if err := rows.Scan(&ep.ID, &ep.EndpointID, &ep.Owner.TokenID, &ep.Owner.ActorID, &ep.Owner.SpaceID,
			&ep.Label, &ep.HostHint, &ep.CreatedAt, &ep.LastSeenAt,
			&ep.RotatedAt, &ep.RotateCount,
			&ep.StationID, &ep.BoundByStationKeyID, &ep.BoundAt); err != nil {
			return nil, err
		}
		out = append(out, ep)
	}
	return out, rows.Err()
}

// endpointByRowID loads an endpoint by internal rowid (post-insert read-back).
func (s *Store) endpointByRowID(ctx context.Context, id int64) (*Endpoint, error) {
	var (
		ep    Endpoint
		label sql.NullString
		hint  sql.NullString
	)
	err := s.R.QueryRowContext(ctx, `
SELECT id, endpoint_id, token_id, actor_id, space_id, label, host_hint, created_at, last_seen_at,
       COALESCE(station_id,''), COALESCE(bound_by_station_key_id,''), COALESCE(bound_at,'')
FROM endpoint WHERE id=?`, id).
		Scan(&ep.ID, &ep.EndpointID, &ep.Owner.TokenID, &ep.Owner.ActorID, &ep.Owner.SpaceID,
			&label, &hint, &ep.CreatedAt, &ep.LastSeenAt,
			&ep.StationID, &ep.BoundByStationKeyID, &ep.BoundAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	ep.Label, ep.HostHint = label.String, hint.String
	return &ep, nil
}

// nullStr maps "" to SQL NULL so optional text columns stay NULL rather than
// storing an empty string that COALESCE and IS NULL then have to disagree about.
func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// EndpointIDsForStation lists the PUBLIC endpoint ids currently bound to a station.
//
// Exists so station_me can tell a session which comm endpoint is its own. There was no
// machine-checkable answer to that from either side: station_me knew the station and not the
// endpoint, and a session comparing its credentials file against its memory was comparing two
// things it had itself chosen. One estate host carries eight endpoint credential files across
// five directories in six naming schemes, all owned by one UNIX user — every session having
// followed the "0600, outside a git repo" instruction correctly, and the result being a
// directory of interchangeable-looking secrets. A session used the wrong one.
//
// Revoked endpoints are excluded: the question is "which endpoint should I be using", and a
// revoked one is not an answer.
func (s *Store) EndpointIDsForStation(ctx context.Context, stationID string) ([]string, error) {
	if stationID == "" {
		return nil, nil
	}
	rows, err := s.R.QueryContext(ctx,
		`SELECT endpoint_id FROM endpoint WHERE station_id=? AND revoked_at IS NULL ORDER BY id`, stationID)
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
