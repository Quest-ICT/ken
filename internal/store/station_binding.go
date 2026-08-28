package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
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

// --- THE BINDING-VOUCHER CHAIN WAS DELETED HERE, 2026-08-25 (docs/IDENTITY.md §10 step 3) ---
//
// It was the single largest safe deletion the design identified, and §9.2 stated its one
// condition: "The voucher exists SOLELY so a station key never crosses to the comm surface as a
// tool argument. Nothing to hand across, nothing to hand it with."
//
// Step 2 gave one identity all three surfaces. Step 4 replaced the per-folder station KEY with a
// workspace id in a header that authorises nothing. So there stopped being a key to keep off the
// comm surface, and the voucher had nothing left to carry.
//
// What went: IssueBindingVoucher, RedeemBindingVoucher, SweepBindingVouchers, the 5-minute TTL,
// single-use redemption, endpoint pinning, actor matching, hash-at-rest, the hourly janitor sweep,
// and ErrVoucherInvalid / ErrVoucherNotForThisEndpoint / ErrVoucherNotYours — four sentinels whose
// careful wording existed only to tell a session which way a voucher had failed.
//
// WHAT REPLACES IT IS NOT A SMALLER CREDENTIAL, IT IS NO CREDENTIAL. comm_bind reads
// X-Ken-Workspace off the request, checks the workspace is live, and binds with an EMPTY
// bound_by_station_key_id — no key authorised it, so no key can sever it. Revocation moved to the
// credential that owns the endpoint.
//
// THE TABLE IS STILL THERE. Dropping station_binding_voucher is a schema change, and Rule 4 says a
// release carrying one carries nothing else — so it ships alone, later. Nothing reads it now; the
// rows are inert.
//
// The chain is recoverable from git if the design ever reverses: it lived in this file up to
// commit c447808.
// ErrStationKeyRevoked is returned to a caller whose station key has been revoked.
// It is DISTINGUISHABLE from an ordinary auth failure on purpose (S6): a model that
// is told its key was revoked reports that to its human, while one that merely sees
// "invalid" retries in a loop. This does not weaken §5's unprobeability, because it
// is returned only AFTER the endpoint's own secret has verified — it informs a
// proven holder and tells a prober nothing.
var ErrStationKeyRevoked = errors.New("the station key that bound this endpoint has been revoked — tell your human; you cannot reconnect with it, and a new key must be minted from the console")

// ActorCandidate is an actor that could own a station key, with whether it already
// holds a comm token — which is the thing that has to match (S5).
type ActorCandidate struct {
	ID       int64
	Kind     string
	Name     string
	HasComm  bool
	CommTags string // labels of its comm tokens, for a human to recognise the machine
}

// ActorsWithCommStatus lists actors that could sensibly author an agent's writes,
// comm-token holders first.
//
// Two callers, one question. A STATION KEY must be minted under the actor holding
// that machine's comm token, or the hearsay marker silently never fires. A CONNECTOR
// must be pointed at one for exactly the same reason — its writes are authored by the
// grant's actor, and an actor invented from a client's display name can never match
// COMM traffic. Named for the property it reports rather than for the first caller.
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
func (s *Store) ActorsWithCommStatus(ctx context.Context) ([]ActorCandidate, error) {
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

// ActorExists reports whether an actor id names a live row.
//
// Used where an id arrives from a FORM rather than from a lookup: a connector's
// authoring actor is chosen by an operator at consent time, and authorship is the
// field a human reads when deciding whether to promote a proposal. An unvalidated id
// there would attribute one connector's writes to another identity, which is the one
// kind of wrong the curation gate cannot repair afterwards.
func (s *Store) ActorExists(ctx context.Context, id int64) (bool, error) {
	var n int
	err := s.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM actor WHERE id=?`, id).Scan(&n)
	return n > 0, err
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
