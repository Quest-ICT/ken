package store

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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

// MaxSelfDescriptionBytes caps the one field an agent may write about itself.
//
// It was uncapped, and slice 2 made that dangerous rather than merely untidy: the
// directory tools now carry this text verbatim into every peer's context, so an
// unbounded field became an unbounded write into other agents' working memory. A
// 700 KiB self-description was accepted and returned byte-identical to a different
// station. Every sibling agent-writable payload is bounded — notebook pages at
// 64 KiB, locker blobs at 256 KiB, task text at 512 B — and this one was reachable
// only by the MCP body limit.
//
// 4 KiB is generous for "what I know and am responsible for" and small enough that
// a directory listing many stations stays readable.
const MaxSelfDescriptionBytes = 4 << 10

// MaxSelfDescriptionTags bounds the tag list separately: the byte cap alone would
// permit thousands of one-character tags.
const MaxSelfDescriptionTags = 24

// SetStationSelfDescription is the ONE station field an agent may write. It is stored in
// columns whose names say the value is a claim, so a reader that flattens the result
// still sees it marked (S8).
func (s *Store) SetStationSelfDescription(ctx context.Context, stationID, about string, tags []string) error {
	// REFUSE rather than truncate. A silently shortened self-description is a claim
	// the station did not make, presented to peers as though it did.
	if len(about) > MaxSelfDescriptionBytes {
		return fmt.Errorf("%w: self-description is %d bytes, over the %d-byte cap — it is a summary for other stations, not a document",
			ErrInvalid, len(about), MaxSelfDescriptionBytes)
	}
	if len(tags) > MaxSelfDescriptionTags {
		return fmt.Errorf("%w: %d tags, over the %d-tag cap", ErrInvalid, len(tags), MaxSelfDescriptionTags)
	}
	for _, tag := range tags {
		if len(tag) > 64 {
			return fmt.Errorf("%w: a tag is %d bytes, over the 64-byte cap", ErrInvalid, len(tag))
		}
	}
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
	// THE ROSTER EPOCH MOVES, in both directions. Archiving changes which parties a room
	// delivers to, which is a membership change in everything but name — and a membership
	// change nothing can detect is one nobody is told about. Without this the mirror is
	// rewritten under an unchanged epoch and every consumer believes it is looking at the
	// same roster it already had.
	if _, err := tx.ExecContext(ctx, `
UPDATE comm_roster_epoch SET epoch = epoch + 1,
       updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=1`); err != nil {
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

	// The actor this key was minted under, and whether that same actor holds a live
	// COMM token. Carried for DISPLAY, because the operator cannot otherwise see the
	// one property that decides whether the key can bind an endpoint at all.
	//
	// A mismatch has no symptom until someone tries to bind: the key authenticates
	// perfectly, every station tool works, and only redemption refuses — which is
	// months later and in a different surface. Showing it next to the key turns an
	// invisible misconfiguration into a visible one.
	ActorKind, ActorName string
	ActorHasComm         bool
}

// IssueStationKey mints a `kens_`-prefixed key. stationID may be empty: such a key can
// call exactly one tool, station_request, which is how a session with no station asks
// for one (S3).
//
// actorID must be the SAME actor as that machine's comm token, because the hearsay
// window is keyed on the actor — a different actor silently defeats
// prompted_by_peer_traffic, and a marker that fails open without saying so is worse
// than no marker (S5).
//
// THIS FUNCTION STILL DOES NOT ENFORCE THAT. It records what it is told. An earlier
// version of this comment said "the caller enforces that", naming an enforcer that
// never existed — which cost a production operator real time, because a contract
// comment asserting a guarantee is worse than silence: it stops the reader looking.
//
// What changed in 0014: a mismatch is no longer silent everywhere. BINDING now
// enforces it — RedeemBindingVoucher requires the redeeming endpoint's actor to be
// the one the voucher was issued to, so a key minted under the wrong actor cannot
// bind an endpoint and says so by name. That is a real check, and it is deliberately
// NOT this function's: refusing at mint time would block the legitimate case of a
// deployment that has no comm token yet, and stations run with COMM off by design.
//
// So the hearsay consequence above remains unenforced and silent — a mismatched key
// authenticates perfectly and marks nothing — while the binding consequence is now
// loud. Do not read the new check as covering both. What the callers still do is
// make the right actor the DEFAULT: `ken station key` resolves the actor holding this
// deployment's comm token and says which one it picked, the console offers a picker
// that marks them, and the /stations key table now shows each key's actor and
// whether it holds a comm token.
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

// RetireStationKey stops the key working — INCLUDING for a session holding it right now.
//
// IT DOES NOT "LEAVE LIVE ONES ALONE", and six shipped strings said it did until 2026-08-14.
// AuthenticateStationKey requires `retired_at IS NULL` (below) and the middleware
// re-authenticates EVERY request, so the holder loses the notebook, task list, locker and
// vault at its next call. What survives is the COMM endpoints the key already bound: those
// authenticate on the endpoint secret, not on this key.
//
// So the difference from revocation is narrower than the words suggest — Retire severs the
// STATION surface and spares COMM; Revoke severs both (RevokeToken plus the endpoint-severing
// pass, S6). Neither is the graceful "I moved machines" path this comment used to promise.
//
// The behaviour was corrected in code in 1.5.2 by a commit that touched no .properties and no
// template, so the operator-facing text kept promising the old behaviour for four releases.
// ken-prod-ops found it by reading the auth query rather than the tooltip.
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
SELECT t.token_id, COALESCE(t.station_id,''), COALESCE(t.label,''), t.created_at,
       COALESCE(t.last_used_at,''), COALESCE(t.retired_at,''), COALESCE(t.revoked_at,''),
       COALESCE(a.kind,''), COALESCE(a.display_name,''),
       EXISTS(SELECT 1 FROM api_token c
               WHERE c.actor_id=t.actor_id AND c.revoked_at IS NULL
                 AND c.scopes LIKE '%"comm"%')
FROM api_token t LEFT JOIN actor a ON a.id = t.actor_id
WHERE t.station_id=? ORDER BY t.created_at DESC`, stationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StationKey
	for rows.Next() {
		var k StationKey
		if err := rows.Scan(&k.TokenID, &k.StationID, &k.Label, &k.CreatedAt,
			&k.LastUsedAt, &k.RetiredAt, &k.RevokedAt,
			&k.ActorKind, &k.ActorName, &k.ActorHasComm); err != nil {
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

// CreateStationRequest files an agent's ask for a human decision (S3/S9). The reason and
// purpose are shown ONLY to the human: nothing here is delivered to a target station
// before approval, because a request that reached its target would be a one-shot
// unauthorized message channel.
func (s *Store) CreateStationRequest(ctx context.Context, spaceID int64, tokenID, fromStation, nameHint, purpose string) (string, error) {
	id, err := randBase62(12)
	if err != nil {
		return "", err
	}
	if _, err := s.W.ExecContext(ctx, `
INSERT INTO station_request(request_id, space_id, kind, from_station, from_token_id, name_hint, purpose)
VALUES(?,?,'station',?,?,?,?)`,
		id, spaceID, nullStr(fromStation), tokenID, nullStr(nameHint), purpose); err != nil {
		return "", err
	}
	return id, nil
}

// StationRequestRow is a pending ask awaiting a human decision.
type StationRequestRow struct {
	RequestID, Kind, NameHint, Purpose, Reason, CreatedAt string
	PromptedByPeerTraffic                                 bool
	// FromName and ToName are the two stations a LINK request is between, resolved to the
	// names a human uses. Empty for a 'station' request, which has no counterparty yet.
	//
	// THE QUERY DID NOT SELECT THESE UNTIL 3.11.0, so the console could not show who was
	// asking or who they wanted to reach — the columns were on the table the whole time. An
	// operator approved two link requests on 2026-08-13 and said afterwards that he had not
	// been told what he was approving. He was right: the screen could not tell him, because
	// nothing had fetched it.
	FromName, ToName string
}

// PendingStationRequests lists what is waiting on the human.
func (s *Store) PendingStationRequests(ctx context.Context, spaceID int64) ([]StationRequestRow, error) {
	rows, err := s.R.QueryContext(ctx, `
SELECT r.request_id, r.kind, COALESCE(r.name_hint,''), r.purpose, r.reason, r.created_at,
       COALESCE(r.prompted_by_peer_traffic,0),
       COALESCE(sf.name, r.from_station, ''), COALESCE(st2.name, r.to_station, '')
FROM station_request r
LEFT JOIN station sf  ON sf.station_id  = r.from_station
LEFT JOIN station st2 ON st2.station_id = r.to_station
WHERE r.space_id=? AND r.state='pending' ORDER BY r.created_at`, spaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StationRequestRow
	for rows.Next() {
		var r StationRequestRow
		if err := rows.Scan(&r.RequestID, &r.Kind, &r.NameHint, &r.Purpose, &r.Reason,
			&r.CreatedAt, &r.PromptedByPeerTraffic, &r.FromName, &r.ToName); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// FirstHumanActor returns the earliest human actor, so CLI commands have a sane default.
func (s *Store) FirstHumanActor(ctx context.Context) (int64, error) {
	var id int64
	err := s.R.QueryRowContext(ctx, `SELECT id FROM actor WHERE kind='human' ORDER BY id LIMIT 1`).Scan(&id)
	return id, err
}

// DirectoryEntry is one row of the station directory: a station another station is
// allowed to know exists, plus whether the two may currently talk.
//
// Linked is separate from visibility on purpose. Discovery and permission are
// different questions, and collapsing them makes the directory useless for the case
// it exists to serve — you cannot ask your human for a link to a station you were
// never allowed to see the name of.
type DirectoryEntry struct {
	Station
	Linked bool // an active link with the asking station exists RIGHT NOW
}

// ListStationsVisibleTo returns the stations `fromStation` may know about, newest
// activity first.
//
// VISIBILITY RULE, and this is what finally gives `published` a reader: a station is
// listed when it is PUBLISHED, or when the asking station already holds an active
// link to it. Until now `published` was a human-settable flag that gated nothing —
// writable from the console, read by no query — which is the same unfinished shape as
// a column nothing selects. It now means exactly one thing: listed in the directory.
//
// The link clause is not redundant with publication. A station may be deliberately
// unpublished and still be someone's established peer; hiding an existing
// relationship from the party that holds it would be a lie by omission, and would
// make the directory disagree with what comm_open_channel will actually do.
//
// Archived stations are excluded: the directory answers "who is available", and a
// station nobody is staffing by design is not. Self is excluded for the same reason —
// a session does not need to discover itself.
//
// The caller supplies liveness separately (comm.StaffingByStation); this package must
// not reach into the expendable database (S7).
func (s *Store) ListStationsVisibleTo(ctx context.Context, spaceID int64, fromStation string) ([]DirectoryEntry, error) {
	rows, err := s.R.QueryContext(ctx, `
SELECT st.station_id, st.space_id, st.name, st.purpose,
       st.self_described_about, st.self_described_tags,
       st.published, st.state, st.created_at, st.advertised_at, st.last_activity_at,
       EXISTS(SELECT 1 FROM station_link l
               WHERE l.state='active'
                 AND ((l.station_a=st.station_id AND l.station_b=?2)
                   OR (l.station_b=st.station_id AND l.station_a=?2))) AS linked
  FROM station st
 WHERE st.space_id=?1
   AND st.state <> 'archived'
   AND st.station_id <> ?2
   AND (st.published=1 OR linked)
 ORDER BY COALESCE(st.last_activity_at, st.created_at) DESC`, spaceID, fromStation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DirectoryEntry
	for rows.Next() {
		var e DirectoryEntry
		var tags, advertised, lastAct sql.NullString
		if err := rows.Scan(&e.StationID, &e.SpaceID, &e.Name, &e.Purpose, &e.SelfDescribedAbout,
			&tags, &e.Published, &e.State, &e.CreatedAt, &advertised, &lastAct, &e.Linked); err != nil {
			return nil, err
		}
		if tags.Valid {
			_ = json.Unmarshal([]byte(tags.String), &e.SelfDescribedTags)
		}
		e.AdvertisedAt, e.LastActivityAt = advertised.String, lastAct.String
		out = append(out, e)
	}
	return out, rows.Err()
}

// ErrStationArchived is returned to a session whose station has been retired.
//
// The text names the state AND the remedy, because under the MCP freeze an error string is
// the only channel that reaches a session already running: it cannot be sent a corrected tool
// description, so whatever it needs to know has to be in the refusal it gets.
var ErrStationArchived = errors.New("this station is archived, so it no longer sends or receives on COMM — " +
	"ask your human to unarchive it from the /stations console. Nothing was lost: your endpoint and its " +
	"credentials still work, its notebook and tasks are still readable, and unarchiving restores messaging immediately")

// IsStationArchived reports whether a station is retired.
//
// Read at USE rather than enforced at bind, for the same reason station-key revocation is:
// binding happens once and the state changes afterwards, so a check at bind time answers a
// question nobody asked. It also keeps the operation REVERSIBLE — refusing at use means
// unarchiving restores a session with its existing credentials, where revoking the endpoint
// would force a re-registration, a new secret onto disk and a fresh voucher.
func (s *Store) IsStationArchived(ctx context.Context, stationID string) (bool, error) {
	if stationID == "" {
		return false, nil
	}
	var state string
	err := s.R.QueryRowContext(ctx,
		`SELECT state FROM station WHERE station_id=?`, stationID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		// No station row at all. NOT treated as archived: an endpoint bound to a station
		// that has been deleted outright is a different fault, and answering "archived"
		// would send its human to a console page with nothing on it.
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return state == "archived", nil
}
