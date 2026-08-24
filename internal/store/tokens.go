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

// CommTokenOwner resolves a token to the owner tuple a COMM endpoint carries, and refuses any
// token that could not authenticate one.
//
// THE SAME QUERY THE COMM SURFACE ITSELF USES to build a principal — actor from the token, space
// from the ACTOR, never a hardcoded space — so a re-point cannot produce an owner tuple that
// differs from what authentication would compute for the same token. Two resolutions of one
// question is how they drift, and the drift here would be silent: the endpoint would simply stop
// authenticating, indistinguishably from a leaked secret.
//
// THE WHOLE TUPLE MATTERS, not just the id. A binding voucher compares `issued_to_actor` against
// the endpoint's actor, so an endpoint re-pointed by token alone onto a token under a different
// actor could never be re-bound afterwards — failing forever with a message that blames the
// voucher rather than the re-point.
//
// REFUSING A REVOKED OR NON-COMM TARGET IS PART OF THE OPERATION, not a nicety: re-pointing onto
// one produces an endpoint that authenticates NOWHERE and fails exactly like a compromised one.
// A control that manufactures the defect class it exists to cure is worse than no control.
func (s *Store) CommTokenOwner(ctx context.Context, tokenID string) (actorID, spaceID int64, err error) {
	var scopesJSON string
	var revoked sql.NullString
	err = s.R.QueryRowContext(ctx, `
SELECT t.actor_id, a.space_id, t.scopes, t.revoked_at
FROM api_token t JOIN actor a ON a.id = t.actor_id
WHERE t.token_id = ?`, tokenID).Scan(&actorID, &spaceID, &scopesJSON, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, ErrNotACommToken
	}
	if err != nil {
		return 0, 0, err
	}
	if revoked.Valid {
		return 0, 0, ErrNotACommToken
	}
	var scopes []string
	if err := json.Unmarshal([]byte(scopesJSON), &scopes); err != nil {
		return 0, 0, err
	}
	for _, sc := range scopes {
		if sc == "comm" {
			return actorID, spaceID, nil
		}
	}
	return 0, 0, ErrNotACommToken
}

// ErrNotAStationKey refuses a re-point target that cannot have authorised a binding.
//
// The mirror of ErrNotACommToken, and the two are NOT interchangeable: a comm token owns an
// endpoint, a station key authorised its binding, and the console can move either. Pointing a
// binding at a plain comm token would name an authority that can never be revoked as a station
// key, which is a control that reads as present and is not.
var ErrNotAStationKey = errors.New("that token cannot have bound an endpoint — it must exist, be unrevoked, and be a station key")

// StationKeyStation resolves a station key to the station it belongs to, refusing any token
// that could not have authorised a binding.
//
// WHY THE STATION COMES BACK RATHER THAN A BARE OK. `endpoint.bound_by_station_key_id` is the
// lever that severs a session when its key is revoked (S6), so moving a binding onto a key of
// a DIFFERENT station would hand that station's operator a lever over someone else's session —
// and would take the real station's lever away. The caller passes this station id into the
// re-point statement's WHERE clause, so the endpoint's own station has to match for any row to
// move. The rule is enforced by the UPDATE rather than by a check preceding it (§9.5 of
// docs/IDENTITY.md), which is what makes a stale console page fail loudly instead of laundering
// an authority.
//
// REFUSING A REVOKED KEY IS THE POINT OF THE OPERATION, not a nicety. `IsStationKeyRevoked`
// treats a revoked key — and a MISSING one — as severing every endpoint it bound, so
// re-pointing onto a dead key produces a session that is refused on its next call and fails
// identically to one whose key leaked. That is the defect class this control exists to cure.
func (s *Store) StationKeyStation(ctx context.Context, tokenID string) (string, error) {
	var stationID sql.NullString
	var revoked sql.NullString
	err := s.R.QueryRowContext(ctx,
		`SELECT station_id, revoked_at FROM api_token WHERE token_id = ?`, tokenID).Scan(&stationID, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotAStationKey
	}
	if err != nil {
		return "", err
	}
	if revoked.Valid || !stationID.Valid || stationID.String == "" {
		return "", ErrNotAStationKey
	}
	return stationID.String, nil
}

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
