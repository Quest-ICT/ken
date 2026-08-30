package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
)

const base62Alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// randBase62 returns n cryptographically-random base62 characters.
func randBase62(n int) (string, error) {
	out := make([]byte, n)
	m := big.NewInt(int64(len(base62Alphabet)))
	for i := range out {
		x, err := rand.Int(rand.Reader, m)
		if err != nil {
			return "", err
		}
		out[i] = base62Alphabet[x.Int64()]
	}
	return string(out), nil
}

// FindOrCreateActor returns the id of an actor with the given kind + display
// name, creating it if absent.
func (s *Store) FindOrCreateActor(ctx context.Context, kind, name string) (int64, error) {
	// Atomic get-or-create against the unique (kind, display_name) index — no
	// SELECT-then-INSERT race.
	if _, err := s.W.ExecContext(ctx,
		`INSERT INTO actor(kind, display_name) VALUES(?,?) ON CONFLICT(kind, display_name) DO NOTHING`,
		kind, name); err != nil {
		return 0, err
	}
	var id int64
	err := s.R.QueryRowContext(ctx, `SELECT id FROM actor WHERE kind=? AND display_name=?`, kind, name).Scan(&id)
	return id, err
}

// IssueToken creates an API token for actorID and returns the full token string
// (ken_<id>_<secret>) exactly once; only SHA-256(secret) is persisted.
func (s *Store) IssueToken(ctx context.Context, actorID int64, scopes []string, label string) (string, error) {
	tokenID, err := randBase62(12)
	if err != nil {
		return "", err
	}
	secret, err := randBase62(40)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(secret))
	sj, _ := json.Marshal(scopes)
	if _, err := s.W.ExecContext(ctx,
		`INSERT INTO api_token(token_id, secret_sha256, actor_id, scopes, label) VALUES(?,?,?,?,?)`,
		tokenID, hex.EncodeToString(sum[:]), actorID, string(sj), nullStr(label)); err != nil {
		return "", err
	}
	return "ken_" + tokenID + "_" + secret, nil
}

// TokenRow is a row for `ken token list`.
type TokenRow struct {
	TokenID, ActorName, Kind, Scopes, Label, CreatedAt, LastUsedAt, RevokedAt string
	// Station is the station this key staffs, by NAME, empty for an ordinary token.
	//
	// Without it the listing cannot tell station keys apart: actor, kind, scopes and label
	// are all identical across every key one human minted from one machine, so eight keys
	// rendered as three distinct-looking rows on a live deployment. That is not cosmetic —
	// revoking a station key SEVERS the endpoints bound to it, so an operator picking one
	// of four identical rows had a one-in-four chance of cutting off a different station's
	// COMM. The only thing discriminating them was last_used_at, which stops discriminating
	// the moment two are used in the same window.
	//
	// The value was already stored on api_token.station_id and populated on every station
	// key; only the rendering omitted it. Falls back to the raw station_id if the station
	// row is gone, so a dangling key still says which one it was rather than looking
	// ordinary.
	Station string
}

// ErrNotACommToken refuses a re-point target that cannot own a COMM endpoint.
//
// CallerSafe by nature: the console caller is already authenticated, so naming the reason
// reveals nothing, and a bare refusal would leave an operator guessing between three states.
var ErrNotACommToken = errors.New("that token cannot own a comm endpoint — it must exist, be unrevoked, and carry the `comm` scope")

// *** CommTokenOwner IS DELETED WITH THE RE-POINTING IT VALIDATED. ***
//
// It resolved a token to its actor and refused a revoked or non-comm target, for the console
// control that moved an endpoint from one token to another. Endpoints are not owned by a token any
// more — a mailbox belongs to a station — so there is nothing to re-point and nothing to validate.
//
// Its argument is worth keeping for any future control of this shape: refusing a revoked or
// otherwise unusable target is PART of such an operation, not a nicety, because re-pointing onto
// one produces something that authenticates nowhere and fails exactly like a compromised
// credential. A control that manufactures the defect class it exists to cure is worse than none.

// ErrNotAStationKey AND StationKeyStation ARE BOTH DELETED, along with the station keys they
// described. The error refused a re-point target that could not have authorised a binding; the
// function answered "which station did this key bind for". There are no station keys, no bindings,
// and no rebind picker — a mailbox belongs to a station and nothing re-points it.

// ListTokens lists all API tokens, newest first.
func (s *Store) ListTokens(ctx context.Context) ([]TokenRow, error) {
	rows, err := s.R.QueryContext(ctx, `
SELECT t.token_id, a.display_name, a.kind, t.scopes, COALESCE(t.label,''),
       t.created_at, COALESCE(t.last_used_at,''), COALESCE(t.revoked_at,''),
       COALESCE(st.name, t.station_id, '')
FROM api_token t JOIN actor a ON a.id=t.actor_id
LEFT JOIN station st ON st.station_id = t.station_id
ORDER BY t.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TokenRow
	for rows.Next() {
		var r TokenRow
		if err := rows.Scan(&r.TokenID, &r.ActorName, &r.Kind, &r.Scopes, &r.Label, &r.CreatedAt, &r.LastUsedAt, &r.RevokedAt, &r.Station); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// TokenIsRevoked reports whether a token id is revoked or simply absent.
//
// ABSENT COUNTS AS REVOKED, deliberately. The caller asks this to decide whether a credential can
// ever be presented again, and a token id with no row can no more be presented than a revoked one.
// Answering "not revoked" for a missing row would be technically true and operationally backwards.
//
// Added 2026-08-27 for the SECOND DOOR into the dead-seat defect: revoking a token marks api_token
// and never touches comm.db, so an endpoint owned by a revoked token is unreachable while its own
// revoked_at stays NULL. See comm.PeerSeatOwner for the whole account.
func (s *Store) TokenIsRevoked(ctx context.Context, tokenID string) (bool, error) {
	if strings.TrimSpace(tokenID) == "" {
		return true, nil
	}
	var live int
	err := s.R.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM api_token WHERE token_id=? AND revoked_at IS NULL`, tokenID).Scan(&live)
	return live == 0, err
}

// RevokeToken soft-revokes a token by id.
func (s *Store) RevokeToken(ctx context.Context, tokenID string) error {
	res, err := s.W.ExecContext(ctx,
		`UPDATE api_token SET revoked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE token_id=? AND revoked_at IS NULL`, tokenID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("token not found or already revoked")
	}
	return nil
}

// CreateHumanUser creates a human actor with an Argon2id password hash.
func (s *Store) CreateHumanUser(ctx context.Context, name, pwHash string) (int64, error) {
	res, err := s.W.ExecContext(ctx,
		`INSERT INTO actor(kind, display_name, pw_hash) VALUES('human', ?, ?) ON CONFLICT(kind, display_name) DO NOTHING`,
		name, pwHash)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, errors.New("a user with that name already exists")
	}
	return res.LastInsertId()
}

// CreateFirstAdmin atomically creates the initial human admin ONLY if no human
// user exists yet (the first-run wizard). Returns created=false (no error) if one
// already exists — a single INSERT...WHERE NOT EXISTS that closes the check-then-
// insert TOCTOU a separate SELECT+INSERT would leave open (two concurrent /setup
// posts can't both create an admin).
func (s *Store) CreateFirstAdmin(ctx context.Context, name, pwHash string) (bool, error) {
	res, err := s.W.ExecContext(ctx,
		`INSERT INTO actor(kind, display_name, pw_hash)
		 SELECT 'human', ?, ? WHERE NOT EXISTS (SELECT 1 FROM actor WHERE kind='human')`,
		name, pwHash)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// HumanUser is a row for `ken user list`.
type HumanUser struct {
	ActorID   int64
	Name      string
	CreatedAt string
}

// ListHumanUsers lists human (login) actors.
func (s *Store) ListHumanUsers(ctx context.Context) ([]HumanUser, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT id, display_name, created_at FROM actor WHERE kind='human' ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HumanUser
	for rows.Next() {
		var u HumanUser
		if err := rows.Scan(&u.ActorID, &u.Name, &u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// CountHumanUsers returns the number of human (login) actors; 0 drives the
// first-run setup wizard.
func (s *Store) CountHumanUsers(ctx context.Context) (int, error) {
	var n int
	err := s.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM actor WHERE kind='human'`).Scan(&n)
	return n, err
}

// HumanCred carries a human actor's login credential.
type HumanCred struct {
	ActorID int64
	Name    string
	PwHash  string
}

// HumanByName returns the login credential for a human actor (web login).
func (s *Store) HumanByName(ctx context.Context, name string) (*HumanCred, error) {
	var c HumanCred
	err := s.R.QueryRowContext(ctx,
		`SELECT id, display_name, COALESCE(pw_hash,'') FROM actor WHERE kind='human' AND display_name=?`, name).
		Scan(&c.ActorID, &c.Name, &c.PwHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// TouchToken updates last_used_at, throttled to at most ~once per minute per
// token, so the read path doesn't amplify into a write on every request.
func (s *Store) TouchToken(ctx context.Context, tokenID string) {
	_, _ = s.W.ExecContext(ctx, `
UPDATE api_token SET last_used_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
WHERE token_id=? AND (last_used_at IS NULL OR last_used_at < strftime('%Y-%m-%dT%H:%M:%fZ','now','-60 seconds'))`, tokenID)
}
