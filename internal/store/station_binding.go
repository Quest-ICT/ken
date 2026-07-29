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

// IssueBindingVoucher mints a single-use voucher for a station. Called from the
// station endpoint, where the caller has already proven possession of a station key.
//
// tokenID is recorded so revoking that key can later sever every endpoint it bound
// (S6). Without it, revocation would stop future bindings but leave the leaked
// capability running — which S6 calls theatre, correctly.
func (s *Store) IssueBindingVoucher(ctx context.Context, stationID, tokenID string) (string, error) {
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
INSERT INTO station_binding_voucher(voucher_sha256, station_id, token_id, expires_at)
VALUES(?,?,?, strftime('%Y-%m-%dT%H:%M:%fZ','now', ?))`,
		voucherHash(voucher), stationID, tokenID, fmt.Sprintf("+%d seconds", int(VoucherTTL.Seconds())))
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
func (s *Store) RedeemBindingVoucher(ctx context.Context, voucher, endpointID string) (stationID, tokenID string, err error) {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = tx.Rollback() }()

	h := voucherHash(voucher)
	res, err := tx.ExecContext(ctx, `
UPDATE station_binding_voucher
   SET redeemed_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'), redeemed_by_endpoint=?
 WHERE voucher_sha256=?
   AND redeemed_at IS NULL
   AND expires_at > strftime('%Y-%m-%dT%H:%M:%fZ','now')`, endpointID, h)
	if err != nil {
		return "", "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
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
