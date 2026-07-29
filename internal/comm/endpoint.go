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
       created_at, last_seen_at, revoked_at
FROM endpoint WHERE endpoint_id=?`, endpointID).
		Scan(&ep.ID, &ep.EndpointID, &hash, &ep.Owner.TokenID, &ep.Owner.ActorID, &ep.Owner.SpaceID,
			&label, &hint, &ep.CreatedAt, &ep.LastSeenAt, &revoked)
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

// RevokeEndpoint soft-revokes an endpoint, immediately denying further use.
// Its channels stay queryable for the operator; its messages age out normally.
func (s *Store) RevokeEndpoint(ctx context.Context, endpointID string) error {
	res, err := s.W.ExecContext(ctx, `
UPDATE endpoint SET revoked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
WHERE endpoint_id=? AND revoked_at IS NULL`, endpointID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
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
       created_at, last_seen_at, COALESCE(secret_rotated_at,''), COALESCE(rotate_count,0)
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
			&ep.RotatedAt, &ep.RotateCount); err != nil {
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
SELECT id, endpoint_id, token_id, actor_id, space_id, label, host_hint, created_at, last_seen_at
FROM endpoint WHERE id=?`, id).
		Scan(&ep.ID, &ep.EndpointID, &ep.Owner.TokenID, &ep.Owner.ActorID, &ep.Owner.SpaceID,
			&label, &hint, &ep.CreatedAt, &ep.LastSeenAt)
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
