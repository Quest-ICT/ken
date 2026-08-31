package comm

import (
	"context"
	"database/sql"
	"errors"
	"strings"
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
	// StationID is the durable station this endpoint reads for, or "" when unbound
	// (docs/STATIONS.md S4). An opaque id into ken.db with no foreign key: S7's rule
	// is that cross-database pointers run expendable -> durable, and under restore
	// skew an id that no longer resolves is treated as UNBOUND rather than as an
	// error. Bound endpoints share their station's inbox with claim-once delivery.
	StationID string

	// SessionKey is the CONVERSATION that owns this endpoint, empty for endpoints that predate
	// migration 0019 and for any driven by a secret. Unlike the station key of the same name this
	// one AUTHORISES — presenting it drives this endpoint's mail — so it is as sensitive as the
	// secret it replaces and must never be logged or written down.
	SessionKey string
	// BoundByStationKeyID IS DELETED with station keys. It named the key that authorised a
	// binding so revoking that key could sever every endpoint it bound; there are no station keys
	// and nothing binds, so the column recorded which retired credential had done a retired thing.
	BoundAt string
}

// endpointBySessionKey resolves a claimed endpoint. Revoked endpoints are refused here rather than
// returned for the caller to check, so no path can accidentally hand one back.
func (s *Store) endpointBySessionKey(ctx context.Context, sessionKey string) (*Endpoint, error) {
	var id string
	err := s.R.QueryRowContext(ctx,
		`SELECT endpoint_id FROM endpoint WHERE session_key=? AND revoked_at IS NULL`, sessionKey).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return s.endpointByIDNoSecret(ctx, id)
}

// endpointByIDNoSecret loads an endpoint without verifying a secret. Unexported and used only by
// the session-key path, where the key IS the credential — exporting it would offer a way to load
// any endpoint by id, which is precisely what the secret exists to prevent for unclaimed ones.
func (s *Store) endpointByIDNoSecret(ctx context.Context, endpointID string) (*Endpoint, error) {
	var (
		ep      Endpoint
		revoked sql.NullString
		label   sql.NullString
		hint    sql.NullString
		skey    sql.NullString
	)
	err := s.R.QueryRowContext(ctx, `
SELECT id, endpoint_id, token_id, actor_id, label, host_hint,
       created_at, last_seen_at, revoked_at,
       COALESCE(station_id,''), COALESCE(bound_at,''),
       session_key
FROM endpoint WHERE endpoint_id=?`, endpointID).
		Scan(&ep.ID, &ep.EndpointID, &ep.Owner.TokenID, &ep.Owner.ActorID, &label, &hint,
			&ep.CreatedAt, &ep.LastSeenAt, &revoked,
			&ep.StationID, &ep.BoundAt, &skey)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if revoked.Valid && revoked.String != "" {
		return nil, ErrDenied
	}
	ep.Label, ep.HostHint, ep.SessionKey = label.String, hint.String, skey.String
	return &ep, nil
}

// *** FIVE FUNCTIONS WERE DELETED HERE, AND THE REACHABILITY GATE IS WHAT FOUND THEM. ***
//
// AuthenticateEndpoint, AuthenticateEndpointBySessionKey, BindEndpointToStation,
// ClaimEndpointForSession and UnbindEndpointFromStation. Every one existed to answer "which
// mailbox is this, and is it attached to a station" — a question the intrinsic mailbox deletes:
// a station comes with one, so there is nothing to authenticate separately, claim, attach or
// detach.
//
// They were not hunted down by hand. `internal/audit` fails when an exported *Store method has no
// production caller, so removing the tools left exactly these five standing in a list. That is the
// gate doing the job it was written for, on the largest deletion it has seen.

// *** REPOINTING IS DELETED. ***
//
// It moved a mailbox from one owning TOKEN to another, and existed because the operating
// convention was one token per machine: retiring a machine's token would otherwise strand its
// mailbox. There is one credential now — the OAuth grant — and MailboxFor refreshes the owner on
// every call, so a mailbox follows its station across whatever grant happens to be current. There
// is nothing left to move by hand.

// CountEndpointsByToken reports how many LIVE endpoints a token owns, so a revoke confirm can
// state its blast radius before the click.
//
// The mirror of CountEndpointsBoundBy, which counts by the STATION KEY that bound an endpoint
// rather than the token that owns it. Both were needed and only one existed, so /tokens showed
// a count for a station key and nothing at all for a plain comm token — the credential that
// actually carries eleven endpoints on the live estate.
//
// This is also the first reader of `idx_endpoint_token`, which has existed since 0001_init and
// which nothing has ever used. The schema anticipated this operation and nobody wrote it.
func (s *Store) CountEndpointsByToken(ctx context.Context, tokenID string) (int, error) {
	var n int
	err := s.R.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM endpoint WHERE token_id=? AND revoked_at IS NULL`, tokenID).Scan(&n)
	return n, err
}

// EndpointRef names one endpoint a bulk verb would move: enough for a human to recognise it in a
// confirm dialog, and nothing more.
type EndpointRef struct {
	EndpointID string
	Label      string // agent-supplied; "" when the session never named itself
	StationID  string // "" when unbound
}

// EndpointsOwnedBy and EndpointsBoundBy list exactly what the matching bulk verb would move.
//
// WHY A LIST AND NOT JUST A COUNT. The console stated the blast radius as a NUMBER — "this moves
// 11" — beside a button whose effect cannot be undone, and ken-prod-ops put the objection
// precisely: an operator reads eleven, looks at the page, recognises some of them, and clicks. The
// ones they did not recognise move too. On the live estate those included `runway-prod-admin` and
// `rb5009-config`, both in use that week.
//
// A number is a claim about a population; a confirm dialog that names the population is a claim
// the operator can check. That is the same standard S6 already sets for revocation, one step
// further along: not "this will disconnect N live sessions" but which ones.
//
// THE PREDICATE IS THE VERB'S OWN, deliberately character-identical to the UPDATE beside it and
// to the COUNT that /tokens renders — no `space_id`, because the verbs have none either. A list
// derived from the instance-wide console listing would be SHORTER than what the button moves,
// which is the failure this pair exists to prevent rather than to introduce.
// TestTheBlastRadiusListAndCountCannotDisagree pins the two together.
func (s *Store) EndpointsOwnedBy(ctx context.Context, tokenID string) ([]EndpointRef, error) {
	return s.endpointRefs(ctx, `token_id=? AND revoked_at IS NULL`, tokenID)
}

func (s *Store) endpointRefs(ctx context.Context, where string, arg any) ([]EndpointRef, error) {
	rows, err := s.R.QueryContext(ctx, `
SELECT endpoint_id, COALESCE(label,''), COALESCE(station_id,'')
FROM endpoint WHERE `+where+` ORDER BY COALESCE(label,''), endpoint_id`, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EndpointRef
	for rows.Next() {
		var r EndpointRef
		if err := rows.Scan(&r.EndpointID, &r.Label, &r.StationID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RotateEndpointSecret IS DELETED WITH THE SECRET IT ROTATED. It existed as the recovery for a
// session that lost the value it was told to write to a file; there is no secret and no file, so
// there is nothing to rotate and nothing to lose.

// *** RevokeEndpoint IS DELETED. IT COULD NOT DO WHAT ITS ONLY CALLER PROMISED. ***
//
// It stamped revoked_at and nothing else. Nothing in the auth path consults that column any more,
// and MailboxFor recreates a station's mailbox on the next call — so the console button that used
// it reported success and had no effect at all. A security-shaped control that lies is worse than
// no control: an operator who believes a session is cut off stops looking for a way to cut it off.
//
// What stops a session now: archive its station, or withdraw the OAuth authorization.

// ListEndpoints returns the endpoints owned by this instance, newest first. Scoped by
// space from day 1 even though only one exists today: an unscoped listing would be
// the enumeration surface in a multi-human future, and scoping it later would be a
// behavioural break for anything that relied on the full list.
func (s *Store) ListEndpoints(ctx context.Context) ([]Endpoint, error) {
	rows, err := s.R.QueryContext(ctx, `
SELECT id, endpoint_id, token_id, actor_id, COALESCE(label,''), COALESCE(host_hint,''),
       created_at, last_seen_at,
       COALESCE(station_id,''), COALESCE(bound_at,''),
       -- SELECTED SO THE CONSOLE CAN SHOW WHICH CONVERSATION DRIVES EACH MAILBOX. Without it every
       -- endpoint looks unclaimed on the page, and an operator recovering an abandoned one cannot
       -- tell it from a live session's — which is the first thing they need to know.
       COALESCE(session_key,'')
FROM endpoint WHERE revoked_at IS NULL ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Endpoint
	for rows.Next() {
		var ep Endpoint
		if err := rows.Scan(&ep.ID, &ep.EndpointID, &ep.Owner.TokenID, &ep.Owner.ActorID, &ep.Label, &ep.HostHint, &ep.CreatedAt, &ep.LastSeenAt,
			&ep.StationID, &ep.BoundAt, &ep.SessionKey); err != nil {
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
SELECT id, endpoint_id, token_id, actor_id, label, host_hint, created_at, last_seen_at,
       COALESCE(station_id,''), COALESCE(bound_at,'')
FROM endpoint WHERE id=?`, id).
		Scan(&ep.ID, &ep.EndpointID, &ep.Owner.TokenID, &ep.Owner.ActorID, &label, &hint, &ep.CreatedAt, &ep.LastSeenAt,
			&ep.StationID, &ep.BoundAt)
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

// EndpointIDsForStation IS DELETED with the station_me pre-flight it fed. That field told a session
// to compare these ids against the endpoint_id in its credentials file; there is no credentials
// file and no endpoint_id a session holds, because a mailbox belongs to a station and is resolved
// from the conversation's own key.

// *** ReassignEndpointToSession AND EndpointReassignResult ARE DELETED, AND THE DOCS HAD ALREADY
// SAID SO. ***
//
// They pointed a mailbox at a conversation from a console form — the comm half of station recovery,
// back when a mailbox belonged to a SESSION. COMM.md, UPGRADING.md and CHANGELOG all recorded this
// as removed in the 4.0.0 wave while the route, handler, form and this function stayed live. A
// document asserting a deletion that did not happen is worse than no document: it is a claim an
// operator plans around.
//
// A mailbox belongs to a STATION, so recovery is reassigning the STATION — which moves its mail
// together with its notebook, tasks, locker and vault, instead of separately from them.

// MailboxFor returns the station's mailbox, creating it on first use.
//
// *** A STATION COMES WITH A MAILBOX. THERE IS NOTHING TO REGISTER AND NOTHING TO BIND. ***
//
// Vlad, settling it: "I own a home and I don't have to go to the post office to claim a mailbox —
// the mailbox resides in my home." Addressing another station means writing to its mailbox; those
// are the same act, not two.
//
// THE PREMISE THE OLD SPLIT RESTED ON IS DEAD, which is why this is a simplification rather than a
// convenience. 0001_init.sql, unchanged since the first release: "the operating convention is one
// Ken token per MACHINE, so every session on a box shares a token. Without a per-endpoint secret,
// two sessions could poll and ack each other's messages." That risk cannot occur any more —
// station.session_key is UNIQUE (ken.db migration 0023), so one station is held by exactly one
// conversation, and the mailbox is the station's.
//
// The estate had already conceded the point before the design did: of 15 live mailboxes, 8 were
// attached to a station and NONE was attached to more than one, while 7 sat unattached carrying
// FOLDER NAMES — several naming stations that already existed. A distinction nobody could hold.
//
// IDEMPOTENT, and that is the whole interface: call it on every request, get the same row. The row
// itself is now an implementation detail — an id nothing outside this package needs to name.
func (s *Store) MailboxFor(ctx context.Context, stationID string, owner Owner) (*Endpoint, error) {
	if strings.TrimSpace(stationID) == "" {
		return nil, ErrNotFound
	}
	if ep, err := s.mailboxByStation(ctx, stationID); err == nil {
		// The owner is refreshed rather than compared. A station belongs to one human and the
		// grant it arrives under may legitimately change — Vlad revoked eleven tokens in one
		// afternoon — so pinning the mailbox to the credential that happened to create it would
		// strand mail behind a retired token. Ownership of the STATION is checked upstream, by
		// station.Resolve, which is where that question belongs.
		if ep.Owner.TokenID != owner.TokenID || ep.Owner.ActorID != owner.ActorID {
			if _, err := s.W.ExecContext(ctx,
				`UPDATE endpoint SET token_id=?, actor_id=? WHERE endpoint_id=?`,
				owner.TokenID, owner.ActorID, ep.EndpointID); err != nil {
				return nil, err
			}
			ep.Owner = owner
		}
		return ep, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	// FIRST USE. The secret column still exists and still takes a value, because dropping it is a
	// schema change and this is not one — but nothing is ever shown it and no path accepts it, so
	// the row holds a hash of a value that does not exist anywhere.
	endpointID, err := randBase62(22)
	if err != nil {
		return nil, err
	}
	// NO SECRET IS MINTED ANY MORE. secret_sha256 was NOT NULL, so this generated forty random
	// characters and stored their hash on every mailbox creation purely to satisfy the constraint
	// — nothing has verified one since 4.0.0 made the station the identity. The column is gone.
	if _, err := s.W.ExecContext(ctx, `
INSERT INTO endpoint(endpoint_id, token_id, actor_id, label, station_id, bound_at)
VALUES(?,?,?,?,?,strftime('%Y-%m-%dT%H:%M:%fZ','now'))
ON CONFLICT DO NOTHING`,
		endpointID, owner.TokenID, owner.ActorID, stationID, stationID); err != nil {
		return nil, err
	}
	return s.mailboxByStation(ctx, stationID)
}

// mailboxByStation resolves the one live mailbox a station owns.
//
// ORDERED AND LIMITED because history can leave more than one: before mailboxes were intrinsic a
// human could bind several endpoints to one station. The oldest live row wins, deterministically,
// so two concurrent calls cannot disagree about which mailbox a station has.
func (s *Store) mailboxByStation(ctx context.Context, stationID string) (*Endpoint, error) {
	var id string
	err := s.R.QueryRowContext(ctx,
		`SELECT endpoint_id FROM endpoint
          WHERE station_id=? AND revoked_at IS NULL ORDER BY id LIMIT 1`, stationID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return s.endpointByIDNoSecret(ctx, id)
}
