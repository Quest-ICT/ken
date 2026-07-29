package store

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
)

// Stations — durable, human-named AI working identities (docs/STATIONS.md).
//
// The split this file exists to hold: a STATION is durable and lives here, in the
// backed-up database; an ENDPOINT stays ephemeral in comm.db and merely points at one.
// Nothing in this file ever stores an endpoint id (S7) — that pointer is guaranteed to
// dangle once the COMM sweep runs, and does not exist at all when COMM is off.
//
// The capability the whole design rests on is WITHHELD rather than requested: no
// function here lets an agent create, name, publish, rename or archive a station. An
// agent may only file a request; a human decides and types the name (S3).

// ErrStationNameTaken is returned when a name collides within a space. Names are
// display-only and unique per space; routing is always by the opaque station_id, so a
// collision is a human-facing inconvenience rather than an addressing failure.
var ErrStationNameTaken = errors.New("station name already in use in this space")

// Station is a durable working identity.
type Station struct {
	StationID          string
	SpaceID            int64
	Name               string // human-typed; never agent-supplied
	Purpose            string
	SelfDescribedAbout string   // a CLAIM (S8) — the field name carries that
	SelfDescribedTags  []string // ditto
	Published          bool
	State              string // active | archived
	CreatedAt          string
	AdvertisedAt       string
	LastActivityAt     string
}

// CreateStation creates a station with a HUMAN-supplied name. There is deliberately no
// agent-reachable path to this function: an agent files a station request and a human
// approves it, typing the name at that moment (S3).
func (s *Store) CreateStation(ctx context.Context, spaceID int64, name, purpose string, actorID int64) (*Station, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("a station needs a name")
	}
	stationID, err := randBase62(16)
	if err != nil {
		return nil, err
	}
	_, err = s.W.ExecContext(ctx,
		`INSERT INTO station(station_id, space_id, name, purpose, created_by_actor_id) VALUES(?,?,?,?,?)`,
		stationID, spaceID, name, purpose, actorID)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrStationNameTaken
		}
		return nil, err
	}
	return s.StationByID(ctx, stationID)
}

// StationByID resolves the opaque routing id — the only identifier anything outside
// this package should hold.
func (s *Store) StationByID(ctx context.Context, stationID string) (*Station, error) {
	return s.stationWhere(ctx, `station_id=?`, stationID)
}

// StationByName resolves a display name within a space. For CONSOLE and CLI use only:
// a name is not an address, and no agent-facing path may route by it (S3).
func (s *Store) StationByName(ctx context.Context, spaceID int64, name string) (*Station, error) {
	return s.stationWhere(ctx, `space_id=? AND name=?`, spaceID, name)
}

func (s *Store) stationWhere(ctx context.Context, where string, args ...any) (*Station, error) {
	var st Station
	var tags, advertised, lastAct sql.NullString
	err := s.R.QueryRowContext(ctx, `
SELECT station_id, space_id, name, purpose, self_described_about, self_described_tags,
       published, state, created_at, advertised_at, last_activity_at
FROM station WHERE `+where, args...).
		Scan(&st.StationID, &st.SpaceID, &st.Name, &st.Purpose, &st.SelfDescribedAbout, &tags,
			&st.Published, &st.State, &st.CreatedAt, &advertised, &lastAct)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if tags.Valid {
		_ = json.Unmarshal([]byte(tags.String), &st.SelfDescribedTags)
	}
	st.AdvertisedAt, st.LastActivityAt = advertised.String, lastAct.String
	return &st, nil
}

// ListStations returns every station in a space, newest activity first. Console-facing:
// what an AGENT may see is narrower (published stations plus its own links, §5).
func (s *Store) ListStations(ctx context.Context, spaceID int64) ([]Station, error) {
	rows, err := s.R.QueryContext(ctx, `
SELECT station_id, space_id, name, purpose, self_described_about, self_described_tags,
       published, state, created_at, advertised_at, last_activity_at
FROM station WHERE space_id=?
ORDER BY COALESCE(last_activity_at, created_at) DESC`, spaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Station
	for rows.Next() {
		var st Station
		var tags, advertised, lastAct sql.NullString
		if err := rows.Scan(&st.StationID, &st.SpaceID, &st.Name, &st.Purpose, &st.SelfDescribedAbout,
			&tags, &st.Published, &st.State, &st.CreatedAt, &advertised, &lastAct); err != nil {
			return nil, err
		}
		if tags.Valid {
			_ = json.Unmarshal([]byte(tags.String), &st.SelfDescribedTags)
		}
		st.AdvertisedAt, st.LastActivityAt = advertised.String, lastAct.String
		out = append(out, st)
	}
	return out, rows.Err()
}

// SetStationSelfDescription is the ONE station field an agent may write. It is stored in
// columns whose names say the value is a claim, so a reader that flattens the result
// still sees it marked (S8).
func (s *Store) SetStationSelfDescription(ctx context.Context, stationID, about string, tags []string) error {
	tj, err := json.Marshal(tags)
	if err != nil {
		return err
	}
	if tags == nil {
		tj = []byte("[]")
	}
	_, err = s.W.ExecContext(ctx, `
UPDATE station SET self_described_about=?, self_described_tags=?,
       advertised_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
WHERE station_id=?`, about, string(tj), stationID)
	return err
}

// RenameStation / SetStationPublished / ArchiveStation are HUMAN-only operations,
// reachable from the console and the CLI and from no tool.
func (s *Store) RenameStation(ctx context.Context, stationID, name string) error {
	_, err := s.W.ExecContext(ctx, `UPDATE station SET name=? WHERE station_id=?`, strings.TrimSpace(name), stationID)
	if isUniqueViolation(err) {
		return ErrStationNameTaken
	}
	return err
}

func (s *Store) SetStationPublished(ctx context.Context, stationID string, published bool) error {
	_, err := s.W.ExecContext(ctx, `UPDATE station SET published=? WHERE station_id=?`, published, stationID)
	return err
}

// ArchiveStation is reversible (S3/§10): assets are kept, links go dormant rather than
// revoked so unarchiving restores them, and the NAME is held — releasing it is a
// separate act that makes the archive irreversible, because a released name can be
// taken by a new station.
func (s *Store) ArchiveStation(ctx context.Context, stationID string, archived bool) error {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	state, linkFrom, linkTo := "active", "dormant", "active"
	archivedAt := "NULL"
	if archived {
		state, linkFrom, linkTo = "archived", "active", "dormant"
		archivedAt = "strftime('%Y-%m-%dT%H:%M:%fZ','now')"
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE station SET state=?, archived_at=`+archivedAt+` WHERE station_id=?`, state, stationID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE station_link SET state=? WHERE state=? AND (station_a=? OR station_b=?)`,
		linkTo, linkFrom, stationID, stationID); err != nil {
		return err
	}
	return tx.Commit()
}

// TouchStationActivity stamps last_activity_at from ken.db facts only — a task touched
// or a page edited. Deliberately NOT messages: those live in the expendable file, and
// the console's cross-station ordering must not depend on a database that may be absent
// (S7, §11.8).
func (s *Store) TouchStationActivity(ctx context.Context, stationID string) error {
	_, err := s.W.ExecContext(ctx,
		`UPDATE station SET last_activity_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE station_id=?`, stationID)
	return err
}

// --- Station keys -----------------------------------------------------------
//
// A station key is an api_token row (S5), so it inherits hashing, revocation,
// `ken token list` and `ken token revoke` rather than re-implementing them worse. Two
// things are new: a nullable station_id, and `retired_at` as a second, non-severing
// stop.

// StationKey is a row for the console's key list.
type StationKey struct {
	TokenID, StationID, Label, CreatedAt, LastUsedAt, RetiredAt, RevokedAt string
}

// IssueStationKey mints a `kens_`-prefixed key. stationID may be empty: such a key can
// call exactly one tool, station_request, which is how a session with no station asks
// for one (S3).
//
// actorID must be the SAME actor as that machine's comm token, because the hearsay
// window is keyed on the actor — a different actor silently defeats
// prompted_by_peer_traffic, and a marker that fails open without saying so is worse
// than no marker (S5). The caller enforces that; this function records what it is told.
func (s *Store) IssueStationKey(ctx context.Context, actorID int64, stationID, label string, scopes []string) (string, error) {
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
	if _, err := s.W.ExecContext(ctx, `
INSERT INTO api_token(token_id, secret_sha256, actor_id, scopes, label, station_id)
VALUES(?,?,?,?,?,?)`,
		tokenID, hex.EncodeToString(sum[:]), actorID, string(sj), nullStr(label), nullStr(stationID)); err != nil {
		return "", err
	}
	return "kens_" + tokenID + "_" + secret, nil
}

// RetireStationKey stops the key binding NEW endpoints and leaves live ones alone — the
// graceful "I moved machines" path. Revocation is the other verb and it SEVERS; see
// RevokeToken plus the endpoint-severing pass in the comm layer (S6).
func (s *Store) RetireStationKey(ctx context.Context, tokenID string) error {
	res, err := s.W.ExecContext(ctx,
		`UPDATE api_token SET retired_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		 WHERE token_id=? AND station_id IS NOT NULL AND retired_at IS NULL AND revoked_at IS NULL`, tokenID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListStationKeys lists a station's keys for the console, including retired and revoked
// ones — a key nobody uses should be visible before it is a problem (§8).
func (s *Store) ListStationKeys(ctx context.Context, stationID string) ([]StationKey, error) {
	rows, err := s.R.QueryContext(ctx, `
SELECT token_id, COALESCE(station_id,''), COALESCE(label,''), created_at,
       COALESCE(last_used_at,''), COALESCE(retired_at,''), COALESCE(revoked_at,'')
FROM api_token WHERE station_id=? ORDER BY created_at DESC`, stationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StationKey
	for rows.Next() {
		var k StationKey
		if err := rows.Scan(&k.TokenID, &k.StationID, &k.Label, &k.CreatedAt,
			&k.LastUsedAt, &k.RetiredAt, &k.RevokedAt); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// StationPrincipal is what a verified station key resolves to.
type StationPrincipal struct {
	TokenID   string
	ActorID   int64
	StationID string // empty = a station-less key: station_request and nothing else
	Scopes    []string
}

// AuthenticateStationKey verifies a `kens_<id>_<secret>` credential.
//
// Retired and revoked keys are both refused here, and indistinguishably from an unknown
// one — extending COMM's unprobeability house rule (§5). The one place a caller learns
// WHY it was cut off is after its endpoint secret has already verified (S6), which
// informs a proven holder and tells a prober nothing.
func (s *Store) AuthenticateStationKey(ctx context.Context, presented string) (*StationPrincipal, error) {
	rest, ok := strings.CutPrefix(presented, "kens_")
	if !ok {
		return nil, ErrNotFound
	}
	tokenID, secret, ok := strings.Cut(rest, "_")
	if !ok || tokenID == "" || secret == "" {
		return nil, ErrNotFound
	}
	var storedHash, scopesJSON string
	var actorID int64
	var stationID sql.NullString
	err := s.R.QueryRowContext(ctx, `
SELECT secret_sha256, actor_id, scopes, station_id FROM api_token
WHERE token_id=? AND revoked_at IS NULL AND retired_at IS NULL`, tokenID).
		Scan(&storedHash, &actorID, &scopesJSON, &stationID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(secret))
	if subtle.ConstantTimeCompare([]byte(storedHash), []byte(hex.EncodeToString(sum[:]))) != 1 {
		return nil, ErrNotFound
	}
	var scopes []string
	_ = json.Unmarshal([]byte(scopesJSON), &scopes)
	// A key with no station scope is not a station key, whatever its prefix.
	if !hasScope(scopes, "station") {
		return nil, ErrNotFound
	}
	return &StationPrincipal{TokenID: tokenID, ActorID: actorID, StationID: stationID.String, Scopes: scopes}, nil
}

func hasScope(scopes []string, want string) bool {
	for _, s := range scopes {
		if s == want {
			return true
		}
	}
	return false
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique")
}
