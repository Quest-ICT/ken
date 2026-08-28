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

	// SessionKey is the CONVERSATION that owns this endpoint, empty for endpoints that predate
	// migration 0019 and for any driven by a secret. Unlike the station key of the same name this
	// one AUTHORISES — presenting it drives this endpoint's mail — so it is as sensitive as the
	// secret it replaces and must never be logged or written down.
	SessionKey string
	// BoundByStationKeyID is the station key that authorised the binding. Revoking
	// that key severs every endpoint it bound (S6) — without this column, revocation
	// would stop future bindings and leave the leaked capability running.
	BoundByStationKeyID string
	BoundAt             string
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
		hash    string
		revoked sql.NullString
		label   sql.NullString
		hint    sql.NullString
		skey    sql.NullString
	)
	err := s.R.QueryRowContext(ctx, `
SELECT id, endpoint_id, secret_sha256, token_id, actor_id, label, host_hint,
       created_at, last_seen_at, revoked_at,
       COALESCE(station_id,''), COALESCE(bound_by_station_key_id,''), COALESCE(bound_at,''),
       session_key
FROM endpoint WHERE endpoint_id=?`, endpointID).
		Scan(&ep.ID, &ep.EndpointID, &hash, &ep.Owner.TokenID, &ep.Owner.ActorID, &label, &hint,
			&ep.CreatedAt, &ep.LastSeenAt, &revoked,
			&ep.StationID, &ep.BoundByStationKeyID, &ep.BoundAt, &skey)
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

// ListEndpoints returns the endpoints owned by this instance, newest first. Scoped by
// space from day 1 even though only one exists today: an unscoped listing would be
// the enumeration surface in a multi-human future, and scoping it later would be a
// behavioural break for anything that relied on the full list.
func (s *Store) ListEndpoints(ctx context.Context) ([]Endpoint, error) {
	rows, err := s.R.QueryContext(ctx, `
SELECT id, endpoint_id, token_id, actor_id, COALESCE(label,''), COALESCE(host_hint,''),
       created_at, last_seen_at, COALESCE(secret_rotated_at,''), COALESCE(rotate_count,0),
       COALESCE(station_id,''), COALESCE(bound_by_station_key_id,''), COALESCE(bound_at,''),
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
			&ep.RotatedAt, &ep.RotateCount,
			&ep.StationID, &ep.BoundByStationKeyID, &ep.BoundAt, &ep.SessionKey); err != nil {
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
       COALESCE(station_id,''), COALESCE(bound_by_station_key_id,''), COALESCE(bound_at,'')
FROM endpoint WHERE id=?`, id).
		Scan(&ep.ID, &ep.EndpointID, &ep.Owner.TokenID, &ep.Owner.ActorID, &label, &hint, &ep.CreatedAt, &ep.LastSeenAt,
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

// EndpointReassignResult reports what a console reassignment did, including the displacement the
// operator did not ask for explicitly and must still be told about.
type EndpointReassignResult struct {
	Endpoint *Endpoint
	// TakenFromID is the endpoint that LOST this conversation key, empty when nothing held it.
	TakenFromID    string
	TakenFromLabel string
}

// ReassignEndpointToSession points an EXISTING mailbox at a conversation, from the console.
//
// *** WHY: RECOVERING A STATION WITHOUT ITS MAIL IS HALF A RECOVERY. ***
//
// Vlad, on station takeover: "it might even be used to re-establish comm channels." It has to
// be, because the station half alone leaves the new session holding a post whose mailbox it cannot
// open. A claimed endpoint is driven by the DEAD conversation's key; an unclaimed one by a secret
// that was shown once, to a session that is gone. The only existing answer was `rotate` — which
// mints a fresh secret for the human to relay and the session to write to disk, and a chat session
// has no disk. That ceremony is the exact thing 3.36.0 removed.
//
// SO THE RECOVERY IS ONE STRING, USED TWICE: the session states its conversation key, the human
// pastes it into the station form and this one, and the session's next poll reads the mail
// waiting in the mailbox it just inherited. Channels, links and queued messages are untouched —
// only the pointer that says which conversation drives this endpoint moves.
//
// THE OWNER TOKEN IS NOT TOUCHED, AND THAT IS LOAD-BEARING. `auth` re-checks at every use that the
// bearer's token matches the endpoint's owner, so a key alone can never drive a mailbox from
// another account. If the taking-over session bears a DIFFERENT Ken token — a claude.ai grant
// recovering a station a CLI session left — the operator must also repoint the endpoint to that
// token, which the console already does next to this control. Silently repointing here would move
// an estate boundary as a side effect of a convenience.
//
// AN EMPTY KEY RELEASES, so a mailbox pointed at the wrong conversation is not stuck to it.
func (s *Store) ReassignEndpointToSession(ctx context.Context, endpointID, sessionKey string) (*EndpointReassignResult, error) {
	// endpointByIDNoSecret already refuses a REVOKED endpoint (ErrDenied), so revocation needs no
	// second check here — and a second check that could disagree with the first is how a revoked
	// mailbox would come back to life on one path and not the other.
	ep, err := s.endpointByIDNoSecret(ctx, endpointID)
	if err != nil {
		return nil, err
	}

	key := strings.TrimSpace(sessionKey)
	if key == "" {
		if _, err := s.W.ExecContext(ctx,
			`UPDATE endpoint SET session_key=NULL WHERE endpoint_id=?`, endpointID); err != nil {
			return nil, err
		}
		ep.SessionKey = ""
		return &EndpointReassignResult{Endpoint: ep}, nil
	}

	// TAKEN FROM WHOEVER HOLDS IT, AND REPORTED — the same ruling as the station form, for the
	// same reason: a chat session asked for its key has usually already claimed a fresh empty
	// mailbox under it, so refusing would fail the main path. Nothing is destroyed; the displaced
	// endpoint keeps its channels and can be reassigned back.
	res := &EndpointReassignResult{}
	if other, err := s.endpointBySessionKey(ctx, key); err == nil && other.EndpointID != endpointID {
		if _, err := s.W.ExecContext(ctx,
			`UPDATE endpoint SET session_key=NULL WHERE endpoint_id=?`, other.EndpointID); err != nil {
			return nil, err
		}
		res.TakenFromID, res.TakenFromLabel = other.EndpointID, other.Label
	} else if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	if _, err := s.W.ExecContext(ctx,
		`UPDATE endpoint SET session_key=? WHERE endpoint_id=? AND revoked_at IS NULL`,
		key, endpointID); err != nil {
		return nil, err
	}
	ep.SessionKey = key
	res.Endpoint = ep
	return res, nil
}

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
	secret, err := randBase62(40)
	if err != nil {
		return nil, err
	}
	if _, err := s.W.ExecContext(ctx, `
INSERT INTO endpoint(endpoint_id, secret_sha256, token_id, actor_id, label, station_id, bound_at)
VALUES(?,?,?,?,?,?,strftime('%Y-%m-%dT%H:%M:%fZ','now'))
ON CONFLICT DO NOTHING`,
		endpointID, sha256Hex(secret), owner.TokenID, owner.ActorID, stationID, stationID); err != nil {
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
