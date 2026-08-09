package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// voucherHash is what is stored. Same treatment as every other secret in this
// codebase — see IssueStationKey and the session-id hashing added in 1.4.1.
func voucherHash(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}

// Binding vouchers (docs/STATIONS.md S5).
//
// The problem this solves: an endpoint should belong to a station, but the only
// credential that proves station membership is the station key — and that key must
// never appear as a tool argument. Tool arguments are model output; they land in
// transcripts, harness logs and scrollback, and via the notebook potentially in a
// backup. The key travels as an Authorization header on /station and nowhere else.
//
// So the session asks /station — where its key already is, in the header — for a
// short-lived single-use voucher, and passes THAT to comm_register on the other
// surface. The blast radius of a leaked voucher is one binding inside a few
// minutes; the blast radius of a leaked station key is the station.

// VoucherTTL is deliberately short. A voucher is redeemed by the same session that
// asked for it, in its very next tool call, so minutes is generous — and every
// additional minute is time a value sitting in a transcript stays live.
const VoucherTTL = 5 * time.Minute

// ErrVoucherInvalid covers unknown, expired and already-redeemed vouchers with ONE
// error. The three cases are never distinguished to a caller: the redeemer is the
// comm endpoint, which is authenticated but is NOT the station's credential holder,
// so telling it which vouchers exist would leak across the very boundary the
// voucher indirection was introduced to protect.
var ErrVoucherInvalid = errors.New("binding voucher is not valid — it may be unknown, already used, or expired (they last a few minutes; ask /station for a fresh one)")

// ErrVoucherNotYours is returned when a voucher is real and live but was issued to a
// different actor than the one now presenting it.
//
// This is DISTINGUISHED from ErrVoucherInvalid, which deliberately collapses
// unknown, used and expired into one string — so the difference needs defending, or
// a later reader will "fix" it back by merging them.
//
// Collapsing those three protects a secret an attacker might GUESS. This one cannot
// be reached by guessing: the caller must already hold a live 32-character voucher,
// which means it was handed to them. Against that caller the distinction reveals one
// bit — "the voucher is real, and you are not who it was for" — and they cannot act
// on it, because the actor is what the check requires and it is not something they
// can present.
//
// What the distinction buys is the entire diagnosis. An actor mismatch is a SETUP
// error, not an attack: the station key was minted under a different actor than the
// one holding the comm token, which is a configuration a deployment can sit in for
// months without symptom. Reported as "voucher is not valid" it looks like an expiry
// race, and the operator issues fresh vouchers forever, each failing identically.
// ErrVoucherNotForThisEndpoint is returned when a voucher is real and live but names
// a different endpoint than the one redeeming it.
//
// Distinguished from ErrVoucherNotYours for a practical reason: the two demand
// OPPOSITE responses. This one is fixed by asking for a voucher that names your
// endpoint — a retry that works. An actor mismatch is fixed only by re-minting a
// station key from the console, and retrying it forever is exactly what a collapsed
// error causes.
//
// Safe to distinguish on the same reasoning as ErrVoucherNotYours: reaching this
// requires already holding a live 32-character voucher, so it cannot be reached by
// guessing, and the one bit it reveals — "real, but not for you" — is not actionable
// by someone who cannot produce the named endpoint's secret.
var ErrVoucherNotForThisEndpoint = errors.New("this binding voucher names a different endpoint than the one redeeming it. " +
	"A voucher is minted FOR one endpoint and only that endpoint can use it, so a voucher that leaked or was meant " +
	"for another session is useless here. Ask /station for a voucher naming THIS endpoint_id and redeem it with comm_bind")

var ErrVoucherNotYours = errors.New("this binding voucher was issued to a different identity than the one presenting it — " +
	"the station key that minted it belongs to a different actor than the comm token this endpoint registered under. " +
	"Nothing is wrong with the voucher. Mint a station key under the actor that holds this endpoint's comm token " +
	"(the /stations console lists which actor each key belongs to and whether it has a comm token) and try again")

// IssueBindingVoucher mints a single-use voucher for a station. Called from the
// station endpoint, where the caller has already proven possession of a station key.
//
// tokenID is recorded so revoking that key can later sever every endpoint it bound
// (S6). Without it, revocation would stop future bindings but leave the leaked
// capability running — which S6 calls theatre, correctly.
//
// forEndpoint is the ONE comm endpoint that may redeem this voucher, and it is what
// stops the voucher being a bearer capability (migration 0015). actorID and spaceID
// record the issuing identity and are checked too, but they are the SETUP guard, not
// the security property — see RedeemBindingVoucher, which explains why both remain.
//
// forEndpoint is not validated here and cannot be: this is the durable store, the
// endpoint lives in the expendable one (S7), and a foreign key across that boundary
// is exactly what S7 forbids. A voucher naming an endpoint that does not exist is
// simply a voucher nobody can redeem, which fails closed.
func (s *Store) IssueBindingVoucher(ctx context.Context, stationID, tokenID, forEndpoint string, actorID, spaceID int64) (string, error) {
	if forEndpoint == "" {
		// Refused rather than defaulted. An empty nomination would mint a voucher
		// whose redemption predicate can never match, so the session would receive a
		// perfectly-formed credential that fails at the next call for no visible
		// reason — the failure mode this whole design exists to reduce.
		return "", fmt.Errorf("%w: a binding voucher must name the endpoint that will redeem it", ErrInvalid)
	}
	voucher, err := randBase62(32)
	if err != nil {
		return "", err
	}
	// Stored HASHED, like every other secret in Ken (passwords, token secrets,
	// endpoint secrets, session ids). It is short-lived and single-use, which is an
	// argument for a small blast radius — not an argument for being the one
	// credential kept in cleartext, and least of all in ken.db, which is the file the
	// backup story copies off-box. BACKUP.md's guarantee is "no credential Ken STORES
	// is replayable"; a plaintext voucher would have made that false the day it
	// shipped.
	_, err = s.W.ExecContext(ctx, `
INSERT INTO station_binding_voucher(voucher_sha256, station_id, token_id, issued_to_actor, issued_in_space, issued_for_endpoint, expires_at)
VALUES(?,?,?,?,?,?, strftime('%Y-%m-%dT%H:%M:%fZ','now', ?))`,
		voucherHash(voucher), stationID, tokenID, actorID, spaceID, forEndpoint, fmt.Sprintf("+%d seconds", int(VoucherTTL.Seconds())))
	if err != nil {
		return "", err
	}
	return voucher, nil
}

// RedeemBindingVoucher consumes a voucher and reports which station it binds to.
// Called from comm_register on the OTHER endpoint, which is why it takes no station
// argument: the voucher is the only thing that decides, so a caller cannot ask to be
// bound to a station it was not given a voucher for.
//
// endpointID is the comm.db endpoint being bound. It is stored for the operator
// trail only and is never dereferenced — it points into the expendable database and
// is expected to dangle once the COMM sweep runs (S7).
//
// Redemption is a conditional UPDATE rather than a read-then-write, so two
// concurrent registrations racing on one voucher cannot both succeed: exactly one
// UPDATE reports a row.
//
// TWO checks, and they are not redundant. Do not remove either believing the other
// covers it — they answer different questions and only one of them is security.
//
//  1. endpointID must be the endpoint the voucher NAMED. This is the security
//     property. Redeeming therefore requires that endpoint's own secret, which the
//     voucher does not carry, so a leaked voucher is inert in anyone else's hands.
//
//  2. byActor must be the actor the voucher was issued to. This is the SETUP guard.
//     It catches a station key minted under a different actor than the machine's comm
//     token — a misconfiguration that otherwise has no symptom at all until it
//     silently defeats the hearsay marker (see IssueStationKey). It is defence in
//     depth for (1), never a substitute.
//
// The history matters, because check (2) shipped alone and was described as closing
// the hole. It did not. As first written, redemption checked the hash, the single-use
// flag, the expiry and the station's state, and nothing about the holder — anything
// possessing the string could bind its own endpoint to the station's inbox, and the
// only control was a human remembering "never send a voucher over COMM". Adding the
// actor check narrowed that to "same actor", and the accompanying claim — that a
// leaked voucher then grants nothing the comm token does not already grant — was
// FALSE. A comm token alone registers an UNBOUND endpoint; it confers no station's
// mail. Binding is precisely the capability it does not give.
//
// ken-prod-ops found the consequence by measuring rather than reading: six of their
// eight stations share one actor, because the actor is per MACHINE. So the check
// narrowed the credential to six sessions on one workstation, and the voucher held a
// WEAKER binding than the per-station key that minted it. Check (1) is the fix, and
// it is the reason binding no longer happens inside comm_register: registration has
// no endpoint id yet, so a voucher passed there could never name one.
func (s *Store) RedeemBindingVoucher(ctx context.Context, voucher, endpointID string, byActor int64) (stationID, tokenID string, err error) {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = tx.Rollback() }()

	h := voucherHash(voucher)
	// Both predicates compare with `=`, so a NULL column can never match. That is how
	// vouchers minted before 0014/0015 are refused rather than grandfathered: they
	// carry NULL in the very columns that authorise redemption. Stated here because
	// it is invisible at the call site and reads like an omission.
	res, err := tx.ExecContext(ctx, `
UPDATE station_binding_voucher
   SET redeemed_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'), redeemed_by_endpoint=?
 WHERE voucher_sha256=?
   AND redeemed_at IS NULL
   AND expires_at > strftime('%Y-%m-%dT%H:%M:%fZ','now')
   AND issued_for_endpoint=?
   AND issued_to_actor=?`, endpointID, h, endpointID, byActor)
	if err != nil {
		return "", "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Separate the setup error from the expiry race, INSIDE the transaction so
		// the diagnosis reads the same snapshot the UPDATE just failed against.
		//
		// The read is for the error message only and never authorises anything: it
		// runs exclusively on the path where the UPDATE already declined, so no
		// outcome of it can bind an endpoint. A voucher redeemed legitimately between
		// the two statements would be reported as a mismatch instead of as used —
		// wrong words, right refusal.
		// One read, two causes, and they need OPPOSITE advice: a wrong endpoint means
		// ask for a voucher naming yours, while a wrong actor means no voucher will
		// ever work until a key is re-minted from the console. Collapsing them would
		// send a session retrying forever against a setup it cannot fix by retrying.
		var otherEndpoint, otherActor bool
		if err := tx.QueryRowContext(ctx, `
SELECT COALESCE(MAX(issued_for_endpoint IS NOT NULL AND issued_for_endpoint<>?), 0),
       COALESCE(MAX(issued_to_actor     IS NOT NULL AND issued_to_actor    <>?), 0)
  FROM station_binding_voucher
 WHERE voucher_sha256=?
   AND redeemed_at IS NULL
   AND expires_at > strftime('%Y-%m-%dT%H:%M:%fZ','now')`,
			endpointID, byActor, h).Scan(&otherEndpoint, &otherActor); err != nil {
			return "", "", err
		}
		// Actor first when both are wrong: it is the cause that a retry cannot fix.
		if otherActor {
			return "", "", ErrVoucherNotYours
		}
		if otherEndpoint {
			return "", "", ErrVoucherNotForThisEndpoint
		}
		return "", "", ErrVoucherInvalid
	}

	// Read back inside the same transaction: the row is now claimed, so this cannot
	// observe another redemption.
	err = tx.QueryRowContext(ctx,
		`SELECT v.station_id, v.token_id
		   FROM station_binding_voucher v
		   JOIN station s ON s.station_id = v.station_id
		  WHERE v.voucher_sha256=? AND s.state='active'`, h).Scan(&stationID, &tokenID)
	if errors.Is(err, sql.ErrNoRows) {
		// The voucher was valid but its station has been archived since it was
		// issued. Refuse rather than bind: an archived station's keys stop binding
		// (S3), and honouring a voucher minted before the archive would be a hole
		// straight through that.
		return "", "", ErrVoucherInvalid
	}
	if err != nil {
		return "", "", err
	}
	if err := tx.Commit(); err != nil {
		return "", "", err
	}
	return stationID, tokenID, nil
}

// SweepBindingVouchers drops expired unredeemed vouchers. Redeemed ones are KEPT:
// they are the trail answering "which key bound this endpoint", which is the first
// question asked when a station key turns out to have leaked.
func (s *Store) SweepBindingVouchers(ctx context.Context) (int, error) {
	res, err := s.W.ExecContext(ctx, `
DELETE FROM station_binding_voucher
 WHERE redeemed_at IS NULL
   AND expires_at <= strftime('%Y-%m-%dT%H:%M:%fZ','now')`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// StationKeyOwner reports the station a key is bound to, or "" for a station-less
// key. Used when severing: the console needs to know what a revocation will hit
// before it happens, and S6 requires stating the count before the click.
func (s *Store) StationKeyOwner(ctx context.Context, tokenID string) (string, error) {
	var station sql.NullString
	err := s.R.QueryRowContext(ctx,
		`SELECT station_id FROM api_token WHERE token_id=?`, tokenID).Scan(&station)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return station.String, nil
}

// ErrStationKeyRevoked is returned to a caller whose station key has been revoked.
// It is DISTINGUISHABLE from an ordinary auth failure on purpose (S6): a model that
// is told its key was revoked reports that to its human, while one that merely sees
// "invalid" retries in a loop. This does not weaken §5's unprobeability, because it
// is returned only AFTER the endpoint's own secret has verified — it informs a
// proven holder and tells a prober nothing.
var ErrStationKeyRevoked = errors.New("the station key that bound this endpoint has been revoked — tell your human; you cannot reconnect with it, and a new key must be minted from the console")

// IsStationKeyRevoked reports whether a station key has been revoked.
//
// This exists because severing cannot be made reliable at the REVOKING end. Both
// revoke paths — the /tokens console and `ken token revoke` — go through
// RevokeToken, and the CLI runs in a SEPARATE PROCESS with no comm.db handle at all,
// so a revocation issued there can never reach into the message database to mark
// endpoints. Making the check happen at USE instead means every revocation path
// works, including ones added later that forget about stations: it fails closed by
// construction rather than by remembering.
//
// The eager sweep (comm.SeverEndpointsBoundBy) still runs where a comm handle
// exists, because it also RELEASES CLAIMS — a severed reader is never coming back to
// ack, and leaving its claims to expire would hide those messages from the station's
// remaining readers for the rest of the lease.
func (s *Store) IsStationKeyRevoked(ctx context.Context, tokenID string) (bool, error) {
	if tokenID == "" {
		return false, nil
	}
	var revoked sql.NullString
	err := s.R.QueryRowContext(ctx,
		`SELECT revoked_at FROM api_token WHERE token_id=?`, tokenID).Scan(&revoked)
	if errors.Is(err, sql.ErrNoRows) {
		// The key row is gone entirely. Treat as revoked: a binding whose authority
		// cannot be produced must not keep working.
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return revoked.Valid && revoked.String != "", nil
}

// ActorCandidate is an actor that could own a station key, with whether it already
// holds a comm token — which is the thing that has to match (S5).
type ActorCandidate struct {
	ID       int64
	Kind     string
	Name     string
	HasComm  bool
	CommTags string // labels of its comm tokens, for a human to recognise the machine
}

// ActorsForStationKey lists the actors a station key could sensibly be minted under,
// comm-token holders first.
//
// This exists because the previous default was actively wrong in a way nothing
// surfaced. A station key was minted under a HUMAN actor — the CLI hardcoded the
// kind, the console used the logged-in curator's — while COMM tokens default to an
// `ai` actor, and `(kind, display_name)` is unique, so the two were different rows
// with different ids. The hearsay window joins on the actor, so it could never match:
// `hearsay_at_write` was permanently false on any deployment that followed the
// documented setup, and the only remedy the shipped commands offered was to
// deliberately mislabel an AI session's token as human — repairing one provenance
// signal by corrupting the one the whole curation model rests on.
//
// The marker is biased toward over-reporting precisely because a false negative
// silently launders hearsay into the knowledge base. A mismatched actor produced
// exactly that false negative, on every station write, with no symptom.
func (s *Store) ActorsForStationKey(ctx context.Context) ([]ActorCandidate, error) {
	rows, err := s.R.QueryContext(ctx, `
SELECT a.id, a.kind, a.display_name,
       EXISTS(SELECT 1 FROM api_token t
               WHERE t.actor_id=a.id AND t.revoked_at IS NULL
                 AND t.scopes LIKE '%"comm"%') AS has_comm,
       COALESCE((SELECT GROUP_CONCAT(COALESCE(t.label,''), ', ') FROM api_token t
                  WHERE t.actor_id=a.id AND t.revoked_at IS NULL
                    AND t.scopes LIKE '%"comm"%'), '')
  FROM actor a
 ORDER BY has_comm DESC, a.kind, a.display_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ActorCandidate
	for rows.Next() {
		var c ActorCandidate
		if err := rows.Scan(&c.ID, &c.Kind, &c.Name, &c.HasComm, &c.CommTags); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// FindActor resolves an existing actor by kind and name WITHOUT creating one.
// Distinct from FindOrCreateActor on purpose: minting a station key should never
// invent an actor, because a typo would then produce a key that authenticates
// perfectly and marks nothing.
func (s *Store) FindActor(ctx context.Context, kind, name string) (int64, error) {
	var id int64
	err := s.R.QueryRowContext(ctx,
		`SELECT id FROM actor WHERE kind=? AND display_name=?`, kind, name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return id, err
}
