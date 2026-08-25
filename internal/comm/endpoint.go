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
// RepointEndpointOwner moves ONE endpoint to a different owning token, keeping everything
// else about it: its id, its secret, its channels and seats, its station binding, its queued
// mail and its claims.
//
// WHY THIS EXISTS. `endpoint.token_id` was write-once. Every endpoint a token registered was
// welded to it for life, so retiring that credential meant re-registering every session it
// carried — and on the live estate ELEVEN endpoints hang off one token, including the channel
// the two stations would use to report that it had gone wrong. Production filed the same
// column twice: as a rotation gap on 2026-08-18 and as a transition landmine on 2026-08-24.
// The endpoint had a rotation story and the token did not, which is backwards — the token is
// the credential more likely to leak, because it is shared and long-lived.
//
// THE WHOLE OWNER TUPLE MOVES, not just the token id. `actor_id` and `space_id` are part of
// the owner and other machinery reads them: a voucher redeemed later compares `issued_to_actor`
// against the endpoint's actor, so an endpoint re-pointed by token alone onto a token under a
// different actor can never be re-bound — ErrVoucherNotYours, permanently, with a message that
// blames the voucher.
//
// CONDITIONAL, NEVER CHECK-THEN-ACT. `fromTokenID` is in the WHERE, so the operation is
// idempotent and a stale console page fails loudly instead of moving a row someone else already
// moved. §9.5 of docs/IDENTITY.md states that rule and says it outlives the mechanism it came
// from.
//
// AND IT DOES NOT TOUCH THE CLAIMS. Unbind, revoke and sever all release
// `delivery.claimed_by_endpoint`, each for the same stated reason — "a severed reader is never
// coming back to ack". A RE-POINTED reader is coming back. Copying that pattern by reflex would
// hand a live session's in-flight mail to a sibling reader mid-conversation.
//
// A REVOKED ENDPOINT IS REFUSED, exactly as rotation refuses one: re-pointing it would quietly
// resurrect a capability an operator deliberately destroyed.
//
// THE TARGET MUST BE AN api_token ROW, and this package cannot check that — `internal/comm`
// does not import `internal/store` and must not learn to (S7's pointer rule). The caller
// validates: present, not revoked, carrying the `comm` scope. Re-pointing onto a dead or
// non-comm token produces an endpoint that authenticates nowhere and fails IDENTICALLY to one
// with a leaked secret, which is the defect class this control exists to cure.
func (s *Store) RepointEndpointOwner(ctx context.Context, endpointID, fromTokenID string, to Owner) error {
	res, err := s.W.ExecContext(ctx, `
UPDATE endpoint SET token_id=?, actor_id=?, space_id=?
 WHERE endpoint_id=? AND token_id=? AND revoked_at IS NULL`,
		to.TokenID, to.ActorID, to.SpaceID, endpointID, fromTokenID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Unknown, revoked, and already-moved are one answer. The console caller is
		// already authenticated so it loses nothing, and the three never diverge into a
		// probe — the same stance RotateEndpointSecret takes below.
		return ErrNotFound
	}
	return nil
}

// RepointEndpointsOfToken moves EVERY live endpoint of one token in a single statement, and
// returns how many moved.
//
// The bulk verb is not convenience: eleven endpoints on one token is the shape that makes a
// per-endpoint control feel like the ceremony it was built to remove. One statement means the
// estate cannot end up half-moved, which is the state nobody has a recovery story for.
func (s *Store) RepointEndpointsOfToken(ctx context.Context, fromTokenID string, to Owner) (int, error) {
	res, err := s.W.ExecContext(ctx, `
UPDATE endpoint SET token_id=?, actor_id=?, space_id=?
 WHERE token_id=? AND revoked_at IS NULL`,
		to.TokenID, to.ActorID, to.SpaceID, fromTokenID)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

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
// derived from the space-scoped console listing would be SHORTER than what the button moves,
// which is the failure this pair exists to prevent rather than to introduce.
// TestTheBlastRadiusListAndCountCannotDisagree pins the two together.
func (s *Store) EndpointsOwnedBy(ctx context.Context, tokenID string) ([]EndpointRef, error) {
	return s.endpointRefs(ctx, `token_id=? AND revoked_at IS NULL`, tokenID)
}

func (s *Store) EndpointsBoundBy(ctx context.Context, keyID string) ([]EndpointRef, error) {
	return s.endpointRefs(ctx, `bound_by_station_key_id=? AND revoked_at IS NULL`, keyID)
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

// RepointEndpointBinder moves ONE bound endpoint onto a different station key of the SAME
// station, keeping everything else: its id, its secret, its channels and seats, its station,
// its queued mail and its claims.
//
// WHY THIS EXISTS — THE SECOND WELD, and it is the one the first one's write-up got wrong.
// A bound endpoint is welded to TWO credentials, not one. `token_id` says which token may
// drive it; `bound_by_station_key_id` says which station key authorised the binding, and that
// column is checked at USE on every single call (commserver.go, via store.IsStationKeyRevoked)
// with a MISSING row treated as revoked. So retiring a station key kills its bound endpoints
// just as surely as retiring their comm token does, through a column nobody was looking at.
// docs/IDENTITY.md §9.3 listed the token weld and called bound endpoints the safer case. They
// are the case with two welds.
//
// AND IT IS SMALLER THAN THE FIRST ONE, measured rather than assumed. ken-prod-ops counted 8
// live bound endpoints against 8 distinct binding keys on 2026-08-24 — 1:1, so retiring one
// key today costs exactly one session, recoverable one at a time, while the token weld
// concentrates eleven on one credential including the channel the report would travel on.
// That makes this a correctness fix rather than a blast-radius fix, and it is why the token
// half shipped first. THE RATIO IS AN ACCIDENT OF PROVISIONING, NOT A PROPERTY: nothing stops
// one key from binding several endpoints, which is why the bulk verb below exists and why
// /tokens states the count before the click rather than trusting the shape of today's estate.
//
// THE SAME-STATION RULE IS IN THE `WHERE`, NEVER IN A CHECK BEFORE IT. `ofStation` is the
// station the caller resolved the TARGET key to; the statement requires the endpoint's own
// station to equal it. Moving a binding onto another station's key would hand that station's
// operator a sever lever over this session and take the real station's lever away — an
// authority laundered, with every count still reconciling. Enforced by the UPDATE, it cannot
// happen; enforced by an `if` above the UPDATE, it happens the first time two operators click
// at once.
//
// `fromKeyID` is in the WHERE for the same reason RepointEndpointOwner puts the old token
// there: idempotent, and a stale console page fails loudly rather than moving a row somebody
// else already moved.
//
// IT DOES NOT TOUCH THE CLAIMS. Sever, unbind and revoke all release
// `delivery.claimed_by_endpoint` because "a severed reader is never coming back to ack". A
// re-pointed reader is coming back — copying that pattern by reflex would hand a live
// session's in-flight mail to a sibling reader mid-conversation.
//
// PREVENTIVE ON ONE REVOCATION PATH AND CURATIVE ON THE OTHER, which is worth stating exactly
// because the two look identical from the console. Revoking a key from /tokens also SEVERS the
// endpoints it bound — SeverEndpointsBoundBy marks them revoked — and a revoked endpoint is
// refused here exactly as rotation and owner re-pointing refuse one. There is no un-revoke path
// anywhere in the tree, so on that path this is a move to make BEFORE retiring the key, the
// same ordering §10 of docs/IDENTITY.md derives for the token weld. RETIRING it needs no
// preparation at all, because retire severs nothing here — that is the difference the two verbs
// exist to carry, and it is easy to flatten into one word.
//
// `ken token revoke` cannot sever anything: it runs in a separate process with no comm.db
// handle. Its endpoints stay unrevoked in this table and are refused one call at a time by
// store.IsStationKeyRevoked, which is why that check exists at use. Those rows are still live
// here, so re-pointing them onto a working key REPAIRS a session that has already stopped
// answering — the only repair for it that does not cost a re-registration.
//
// THE TARGET MUST BE AN api_token ROW and this package cannot check that — `internal/comm`
// does not import `internal/store` and must not learn to (S7's pointer rule). The caller
// validates with store.StationKeyStation: present, unrevoked, a station key, and the station
// it names is what lands in `ofStation`.
func (s *Store) RepointEndpointBinder(ctx context.Context, endpointID, fromKeyID, toKeyID, ofStation string) error {
	res, err := s.W.ExecContext(ctx, `
UPDATE endpoint SET bound_by_station_key_id=?
 WHERE endpoint_id=? AND bound_by_station_key_id=? AND station_id=? AND revoked_at IS NULL`,
		toKeyID, endpointID, fromKeyID, ofStation)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Unknown, revoked, already-moved and wrong-station are one answer. The console
		// caller is already authenticated so it loses nothing, and the four never diverge
		// into a probe — the same stance RepointEndpointOwner takes above.
		return ErrNotFound
	}
	return nil
}

// RepointEndpointsBoundBy moves EVERY live endpoint one station key bound onto another key of
// the same station, in a single statement, and returns how many moved.
//
// The mirror of RepointEndpointsOfToken, and it exists for the case today's estate does not
// have rather than the one it does. At 1:1 this is the per-endpoint verb with extra steps; the
// moment one key binds several sessions it is the difference between retiring a key and
// half-retiring it, which is the state nobody has a recovery story for.
func (s *Store) RepointEndpointsBoundBy(ctx context.Context, fromKeyID, toKeyID, ofStation string) (int, error) {
	res, err := s.W.ExecContext(ctx, `
UPDATE endpoint SET bound_by_station_key_id=?
 WHERE bound_by_station_key_id=? AND station_id=? AND revoked_at IS NULL`,
		toKeyID, fromKeyID, ofStation)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

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
		if n == 0 {
			return nil
		}

		// ADOPT THE SEATS THIS ENDPOINT ALREADY OCCUPIES.
		//
		// channel.station_a/b snapshots the authorising pair, written when a SEAT IS FILLED
		// (channel.go:115 at creation, :179 at open) from ep.StationID at that instant. A
		// session that joined a pairing-code channel while UNBOUND and bound afterwards left
		// NULL there forever, because binding touched only this table and nothing revisited it.
		//
		// THAT NULL IS NOT COSMETIC. The pair predicate is snapshot-only
		// (openChannelsBetweenStations), so such a channel is invisible to the blast-radius
		// count a human is shown before revoking a link, invisible to the revocation sweep
		// itself, and invisible to OpenLinkedChannel's reuse lookup — which then opens a SECOND
		// channel between two stations already talking, fragmenting the conversation its own
		// doc comment promises not to fragment.
		//
		// WHY THIS IS SAFE WHERE A LATER BACKFILL IS NOT, and migration 0008 is explicit that a
		// later one is not: 0008 warns the current binding "is exactly the value that may
		// already have drifted". Here it cannot have drifted — the binding is being established
		// in this transaction, for this endpoint, now. And it only ever fills NULL, so a pair
		// recorded at seat-fill time always wins over anything derived later.
		//
		// WHAT IT MEANS, stated because it widens revocation: a channel whose two seats are now
		// both station-owned becomes visible to link revocation between those stations. That is
		// the intent — a channel between two stations that revocation cannot see is the EVASION
		// defect 0008 exists to close, and adopt-after-join reopened it by another route.
		if _, err := t.ExecContext(ctx, `
UPDATE channel SET station_a=?1
 WHERE endpoint_a=(SELECT id FROM endpoint WHERE endpoint_id=?2)
   AND station_a IS NULL`, stationID, endpointID); err != nil {
			return err
		}
		if _, err := t.ExecContext(ctx, `
UPDATE channel SET station_b=?1
 WHERE endpoint_b=(SELECT id FROM endpoint WHERE endpoint_id=?2)
   AND station_b IS NULL`, stationID, endpointID); err != nil {
			return err
		}
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
