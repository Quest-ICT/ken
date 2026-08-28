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

// ErrStationNameTaken is returned when a name collides within this instance. Names are
// display-only and unique per instance; routing is always by the opaque station_id, so a
// collision is a human-facing inconvenience rather than an addressing failure.
var ErrStationNameTaken = errors.New("station name already in use in this space")

// Station is a durable working identity.
type Station struct {
	StationID          string
	Name               string // human-typed; never agent-supplied
	Purpose            string
	SelfDescribedAbout string   // a CLAIM (S8) — the field name carries that
	SelfDescribedTags  []string // ditto
	Published          bool
	State              string // active | archived
	CreatedAt          string
	AdvertisedAt       string
	LastActivityAt     string
	// SessionKey is the CONVERSATION that owns this workspace — empty for stations that
	// predate migration 0023, and for any staffed by whichever session picks them up. It
	// SELECTS and never authorises; see the migration for why that distinction is the whole
	// safety argument.
	SessionKey string
}

// CreateStation creates a station with a HUMAN-supplied name. There is deliberately no
// agent-reachable path to this function: an agent files a station request and a human
// approves it, typing the name at that moment (S3).
func (s *Store) CreateStation(ctx context.Context, name, purpose string, actorID int64) (*Station, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("a station needs a name")
	}
	stationID, err := randBase62(16)
	if err != nil {
		return nil, err
	}
	_, err = s.W.ExecContext(ctx,
		`INSERT INTO station(station_id, name, purpose, created_by_actor_id) VALUES(?,?,?,?)`,
		stationID, name, purpose, actorID)
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

// StationByName resolves a display name within this instance. For CONSOLE and CLI use only:
// a name is not an address, and no agent-facing path may route by it (S3).
//
// THAT CONTRACT WAS VIOLATED BY A CALLER IN THIS REPO. station_link_request resolved its
// to_station argument through here (stationserver.go), and this query filters on nothing but
// name — no `published`, no `state`. So an agent could hand it any string and read
// the answer: a name that exists produced a filed request, a name that did not produced a
// refusal. Two distinguishable outcomes is an enumeration oracle over every station name in
// this instance, including the ones deliberately withheld from station_directory — and worse than
// reading, a correct guess FILED A REQUEST, putting an agent-authored ask for an unpublished
// post in front of its human. Found by sweep and confirmed by execution on 2026-08-19.
//
// The remedy is StationByNameVisibleTo below, not a filter here: the console legitimately
// resolves any name in this instance, including archived and unpublished ones, and narrowing this
// would break the surface the rule reserves it for.
func (s *Store) StationByName(ctx context.Context, name string) (*Station, error) {
	return s.stationWhere(ctx, `name=?`, name)
}

// StationByNameVisibleTo resolves a name ONLY among the stations the asker may already see —
// the same predicate station_directory lists by. It is the agent-safe counterpart to
// StationByName, and the only name-resolution an agent-facing path may use.
//
// WHAT IT GUARANTEES, stated as the property rather than the query: a station the caller
// cannot see is indistinguishable from one that does not exist. Both return sql.ErrNoRows, so
// the caller's single refusal covers both and a guessed name yields nothing — not a filed
// request, not a different error, not a timing difference worth having.
//
// The predicate is a deliberate copy of ListStationsVisibleTo's (published OR linked, never
// archived, never yourself). Two readers of one visibility rule can drift, and if a third
// appears this pair is what should be factored — but a shared helper today would have to
// serve one query returning a list and one returning a row, and the duplication is currently
// the honest cost of keeping both readable.
func (s *Store) StationByNameVisibleTo(ctx context.Context, fromStation, name string) (*Station, error) {
	return s.stationWhere(ctx, `name=?1
   AND state <> 'archived'
   AND station_id <> ?2
   AND (published=1 OR EXISTS(
         SELECT 1 FROM station_link l
          WHERE l.state='active'
            AND ((l.station_a=?2 AND l.station_b=station.station_id)
              OR (l.station_b=?2 AND l.station_a=station.station_id))))`,
		name, fromStation)
}

func (s *Store) stationWhere(ctx context.Context, where string, args ...any) (*Station, error) {
	var st Station
	var tags, advertised, lastAct sql.NullString
	err := s.R.QueryRowContext(ctx, `
SELECT station_id, name, purpose, self_described_about, self_described_tags,
       published, state, created_at, advertised_at, last_activity_at,
       COALESCE(session_key,'')
FROM station WHERE `+where, args...).
		Scan(&st.StationID, &st.Name, &st.Purpose, &st.SelfDescribedAbout, &tags,
			&st.Published, &st.State, &st.CreatedAt, &advertised, &lastAct, &st.SessionKey)
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

// ListStations returns every station in this instance, newest activity first. Console-facing:
// what an AGENT may see is narrower (published stations plus its own links, §5).
func (s *Store) ListStations(ctx context.Context) ([]Station, error) {
	rows, err := s.R.QueryContext(ctx, `
SELECT station_id, name, purpose, self_described_about, self_described_tags,
       published, state, created_at, advertised_at, last_activity_at,
       -- SELECTED SO THE CONSOLE CAN SHOW WHO HOLDS EACH POST. Without it every workspace looks
       -- unclaimed on the page, and an operator reassigning one cannot tell a live conversation's
       -- workspace from an abandoned one — which is the first thing they need to know.
       COALESCE(session_key,'')
FROM station
ORDER BY COALESCE(last_activity_at, created_at) DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Station
	for rows.Next() {
		var st Station
		var tags, advertised, lastAct sql.NullString
		if err := rows.Scan(&st.StationID, &st.Name, &st.Purpose, &st.SelfDescribedAbout,
			&tags, &st.Published, &st.State, &st.CreatedAt, &advertised, &lastAct,
			&st.SessionKey); err != nil {
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
//
// RENAMING IS SAFE AT ANY TIME, and that is a property of the design rather than luck.
// Nothing addresses a station by name: `station_link_mirror` in comm.db holds ids only,
// `from_station_name` on a polled message is resolved at read time from this table, and a
// task's `StationName` is a join rather than a stored copy. So a rename lands everywhere at
// once and leaves no stale second copy to reconcile. That is COMM.md §3 — "a human-chosen
// name is never an address" — paying out.
//
// TWO WAYS THIS USED TO REPORT SUCCESS WITHOUT RENAMING ANYTHING, both fixed here:
// a blank name was accepted (CreateStation rejects one, so the two disagreed and the
// console has no way to click a station with no name), and an unknown station_id updated
// zero rows and returned nil — the caller was told the rename happened. That is this
// project's recurring defect: a no-op indistinguishable from the operation.
func (s *Store) RenameStation(ctx context.Context, stationID, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("%w: a station needs a name", ErrInvalid)
	}
	res, err := s.W.ExecContext(ctx, `UPDATE station SET name=? WHERE station_id=?`, name, stationID)
	if isUniqueViolation(err) {
		return ErrStationNameTaken
	}
	if err != nil {
		return err
	}
	// ZERO ROWS MEANS NO SUCH STATION, and nothing else. Measured rather than assumed:
	// SQLite's changes() counts a MATCHED row even when the new value equals the old, so
	// renaming a station to the name it already has reports 1, not 0. (Checked against this
	// driver on 2026-08-21: same value -> 1, new value -> 1, missing id -> 0.) The first
	// draft of this function guarded a "renamed to its own name" case that cannot occur and
	// documented the opposite of the truth.
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("%w: no station %q", ErrNotFound, stationID)
	}
	return nil
}

// SetStationPublished flips the directory listing, and REFUSES AN UNKNOWN STATION rather
// than reporting success over zero rows — see the note on RenameStation above, which is
// eight lines up and got this right on 2026-08-21 while its two neighbours did not.
func (s *Store) SetStationPublished(ctx context.Context, stationID string, published bool) error {
	res, err := s.W.ExecContext(ctx, `UPDATE station SET published=? WHERE station_id=?`, published, stationID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("%w: no station %q", ErrNotFound, stationID)
	}
	return nil
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
	// STOP HERE IF THERE IS NO SUCH STATION, and stop BEFORE the epoch bump below.
	//
	// This used to return nil over zero rows, so the console flashed "archived" for an id
	// that names nothing — and, worse, still advanced the roster generation, telling every
	// consumer in the deployment that membership had changed when nothing had. A no-op that
	// is indistinguishable from success is bad; one that broadcasts a change it did not make
	// is worse, because the lie propagates.
	res, err := tx.ExecContext(ctx,
		`UPDATE station SET state=?, archived_at=`+archivedAt+` WHERE station_id=?`, state, stationID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("%w: no station %q", ErrNotFound, stationID)
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

// *** STATION KEYS ARE RETIRED. ***
//
// A `kens_<id>_<secret>` credential was a per-machine bearer a human minted, delivered and
// protected, so that a session could reach the station surface. Three things removed its reason to
// exist, in order: OAuth grants carry every capability, /mcp became the one surface requiring all
// of them (so a station key reached nothing), and a station is now claimed in-band with
// session_key rather than by presenting a credential.
//
// Vlad's end state, in his words when he corrected my own governance text: "The only token I see
// still exist after all the things we agreed on is the one associated to the OAuth authorization,
// and is exactly one... having two OAuth associations active would be an error, not a feature."
//
// Gone with them: the binding they authorised, and the sever-on-revoke that made revoking one
// meaningful. Archiving a station is the per-session control now.

// StationPrincipal is what a verified station key resolves to.
type StationPrincipal struct {
	TokenID   string
	ActorID   int64
	StationID string // empty = a station-less key: station_request and nothing else
	Scopes    []string
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
func (s *Store) CreateStationRequest(ctx context.Context, tokenID, fromStation, nameHint, purpose string) (string, error) {
	id, err := randBase62(12)
	if err != nil {
		return "", err
	}
	if _, err := s.W.ExecContext(ctx, `
INSERT INTO station_request(request_id, kind, from_station, from_token_id, name_hint, purpose)
VALUES(?,'station',?,?,?,?)`,
		id, nullStr(fromStation), tokenID, nullStr(nameHint), purpose); err != nil {
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
func (s *Store) PendingStationRequests(ctx context.Context) ([]StationRequestRow, error) {
	rows, err := s.R.QueryContext(ctx, `
SELECT r.request_id, r.kind, COALESCE(r.name_hint,''), r.purpose, r.reason, r.created_at,
       COALESCE(r.prompted_by_peer_traffic,0),
       COALESCE(sf.name, r.from_station, ''), COALESCE(st2.name, r.to_station, '')
FROM station_request r
LEFT JOIN station sf  ON sf.station_id  = r.from_station
LEFT JOIN station st2 ON st2.station_id = r.to_station
WHERE r.state='pending' ORDER BY r.created_at`)
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
func (s *Store) ListStationsVisibleTo(ctx context.Context, fromStation string) ([]DirectoryEntry, error) {
	rows, err := s.R.QueryContext(ctx, `
SELECT st.station_id, st.name, st.purpose,
       st.self_described_about, st.self_described_tags,
       st.published, st.state, st.created_at, st.advertised_at, st.last_activity_at,
       EXISTS(SELECT 1 FROM station_link l
               WHERE l.state='active'
                 AND ((l.station_a=st.station_id AND l.station_b=?1)
                   OR (l.station_b=st.station_id AND l.station_a=?1))) AS linked
  FROM station st
 WHERE st.state <> 'archived'
   AND st.station_id <> ?1
   AND (st.published=1 OR linked)
 ORDER BY COALESCE(st.last_activity_at, st.created_at) DESC`, fromStation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DirectoryEntry
	for rows.Next() {
		var e DirectoryEntry
		var tags, advertised, lastAct sql.NullString
		if err := rows.Scan(&e.StationID, &e.Name, &e.Purpose, &e.SelfDescribedAbout,
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

// StationExists reports whether a station id names a live (non-archived) station.
//
// The validation behind the workspace header (docs/IDENTITY.md §4). It answers only yes/no: the
// caller turns a "no" into the same opaque refusal an unknown credential gets, so the header does
// not become a way to enumerate which workspaces a deployment has.
//
// ARCHIVED IS NOT LIVE. Archiving already stops COMM and is documented as severing live endpoints;
// letting a header re-enter an archived workspace would make archive a suggestion. An operator who
// archived a workspace and then saw a session working in it would have no way to explain it.
func (s *Store) StationExists(ctx context.Context, stationID string) (bool, error) {
	var n int
	err := s.R.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM station WHERE station_id=? AND archived_at IS NULL`, stationID).Scan(&n)
	return n > 0, err
}

// StationBySessionKey returns the workspace a CONVERSATION already owns, or ErrNotFound.
//
// This is the lookup that makes "one existing session is always connected to the same workspace"
// true across a client restart. The key is declared by the session itself (see migration 0023);
// it SELECTS and never authorises, so this deliberately does not filter on actor — two callers
// presenting the same conversation key are the same conversation, and forking them into separate
// workspaces would be the silent-divergence failure rather than a safety measure.
func (s *Store) StationBySessionKey(ctx context.Context, sessionKey string) (*Station, error) {
	return s.stationWhere(ctx, `session_key=?1 AND state <> 'archived'`, sessionKey)
}

// ClaimStationForSession finds or creates the workspace belonging to one conversation.
//
// THE WHOLE POINT IS THAT IT IS IDEMPOTENT PER CONVERSATION. Called again by the same session
// after a Claude Desktop restart it returns the SAME workspace, created=false, with its notebook,
// tasks, locker and vault intact. Called by a new conversation it mints a fresh one. Neither case
// costs a human anything, which is what §6 promised and could not deliver while identity lived in
// the connector.
//
// `nameHint` only decorates the LABEL — Vlad: "what I need to be auto is the label that identifies
// the new station so me (the human) can identify it (I won't identify a raw number or a UUID)".
// The key is the identity; the label is for reading, and it stays renameable.
func (s *Store) ClaimStationForSession(ctx context.Context, sessionKey, nameHint string, actorID int64, adoptIfUnclaimed string) (*Station, bool, error) {
	if strings.TrimSpace(sessionKey) == "" {
		return nil, false, errors.New("no session key given")
	}
	// A switch rather than if/else-if, so there is no arrangement of these branches that can
	// return (nil, nil). Mutation testing produced exactly that — a nil station with a nil error,
	// which the caller then dereferenced — and while the shipped path could not reach it, a
	// function whose contract allows "no result and no reason" is one edit away from a panic in a
	// server. The switch makes the third case impossible rather than merely unreached.
	existing, err := s.StationBySessionKey(ctx, sessionKey)
	switch {
	case err == nil:
		return existing, false, nil
	case !errors.Is(err, ErrNotFound):
		return nil, false, err
	}

	// ADOPT the workspace this connection already minted, if it is still unclaimed. Without this
	// a session that called with no arguments and then with a key leaves the first one orphaned —
	// which is precisely what the pre-3.35.0 tool text told sessions to do. The UPDATE's own
	// `session_key IS NULL` clause is the guard: adopting a station that another conversation has
	// already claimed is impossible rather than merely unlikely.
	if adopt := strings.TrimSpace(adoptIfUnclaimed); adopt != "" {
		res, err := s.W.ExecContext(ctx,
			`UPDATE station SET session_key=? WHERE station_id=? AND session_key IS NULL AND state='active'`,
			sessionKey, adopt)
		if err != nil {
			return nil, false, err
		}
		if n, _ := res.RowsAffected(); n == 1 {
			st, err := s.StationByID(ctx, adopt)
			if err != nil {
				return nil, false, err
			}
			// created=false: the workspace already existed, this call only claimed it. Reporting
			// true would tell the session it had just been given a fresh one.
			return st, false, nil
		}
	}

	name := strings.TrimSpace(nameHint)
	if name == "" {
		name = "workspace"
	}
	if len(name) > 60 {
		name = name[:60]
	}
	st, err := s.CreateStationAutoNamed(ctx, name, actorID)
	if err != nil {
		return nil, false, err
	}
	// Claimed in a second statement rather than in the INSERT, because CreateStationAutoNamed
	// owns the name-collision retry and threading a second column through it would couple two
	// unrelated concerns. A failure here leaves an UNCLAIMED station rather than a wrongly-claimed
	// one, which is the right direction to fail: the next call mints another instead of silently
	// handing this conversation someone else's post.
	if _, err := s.W.ExecContext(ctx,
		`UPDATE station SET session_key=? WHERE station_id=? AND session_key IS NULL`,
		sessionKey, st.StationID); err != nil {
		return nil, false, err
	}
	st.SessionKey = sessionKey
	return st, true, nil
}

// CreateStationAutoNamed mints a workspace whose NAME is derived from a folder, disambiguating on
// collision instead of refusing.
//
// docs/IDENTITY.md §5: "Ken mints a workspace id and an auto-name from the folder's basename,
// disambiguated on collision — names are unique per instance (idx_station_name)."
//
// REFUSING ON COLLISION WOULD REBUILD THE DEADLOCK IN MINIATURE. Two folders called `ken-public`
// on one machine is ordinary, and a session that cannot start because another folder took the name
// first is a session waiting on a human again — which is the entire thing this replaces. The name
// is decoration; the id is the identity (COMM.md §3), so decorating a duplicate costs nothing.
//
// The suffix is a plain counter rather than a random tag, because a human reads these on the
// link-approval screen: `ken-public (2)` tells them there are two, which is the useful fact.
func (s *Store) CreateStationAutoNamed(ctx context.Context, name string, actorID int64) (*Station, error) {
	base := strings.TrimSpace(name)
	if base == "" {
		base = "workspace"
	}
	const purpose = "Auto-named from the folder it was first used in. Rename it in the console at any time — " +
		"the name is a label and the id is the identity, so renaming invalidates nothing."
	for n := 1; n <= 50; n++ {
		try := base
		if n > 1 {
			try = fmt.Sprintf("%s (%d)", base, n)
		}
		st, err := s.CreateStation(ctx, try, purpose, actorID)
		if err == nil {
			return st, nil
		}
		// Only a name collision is worth retrying; anything else is a real failure and must
		// surface rather than be retried fifty times into a confusing final error.
		//
		// MATCHED ON THE SENTINEL, NOT ON THE MESSAGE TEXT. The first version of this grepped the
		// error string for "unique" and never matched, because CreateStation returns
		// ErrStationNameTaken whose text is "station name already in use in this space" — so a
		// collision surfaced as a hard refusal and the second folder got no workspace at all.
		// A substring match on a human-readable message is a check that silently stops working
		// the day someone improves the wording.
		if !errors.Is(err, ErrStationNameTaken) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("could not find a free name based on %q after 50 tries", base)
}

// AuthenticateAPITokenForStation verifies a `ken_<id>_<secret>` API token and returns it as a
// station principal — with NO station, which is the state station_me turns into a workspace.
//
// *** WHY A PLAIN API TOKEN REACHES /station/mcp AT ALL. ***
//
// §10 step 2 made an OAuth grant span the three surfaces, and step 4 let a session mint its own
// workspace. Both are true and both were unreachable for the session that reported the deadlock:
// ken-prod-ops measured that Vlad runs Claude Code inside the desktop app, where **sessions are
// non-interactive and cannot perform an OAuth sign-in at all.** The client's own words to him:
// "This session is non-interactive, so Claude cannot run the OAuth flow here."
//
// So the fix that unlocked everything for an OAuth client unlocked nothing for his, and the wall
// was upstream of every check I had written. A `ken_` token is the credential such a session
// already holds and can be given by hand; it proves the same thing an OAuth grant proves — this is
// the human's own session — through a door that does not require a browser.
//
// THE SCOPE IS STILL REQUIRED. This returns whatever the token carries; the middleware refuses it
// unless `station` is among them, exactly as it refuses a station key without it. A comm-only
// token gains nothing here.
func (s *Store) AuthenticateAPITokenForStation(ctx context.Context, tok string) (*StationPrincipal, error) {
	parts := strings.SplitN(tok, "_", 3)
	if len(parts) != 3 || parts[0] != "ken" {
		return nil, ErrNotFound
	}
	tokenID, secret := parts[1], parts[2]
	var (
		actorID    int64
		secretHash string
		scopesJSON string
		revoked    sql.NullString
	)
	err := s.R.QueryRowContext(ctx,
		`SELECT actor_id, secret_sha256, scopes, revoked_at FROM api_token WHERE token_id=?`, tokenID).
		Scan(&actorID, &secretHash, &scopesJSON, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if revoked.Valid {
		return nil, ErrNotFound
	}
	sum := sha256.Sum256([]byte(secret))
	if subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(secretHash)) != 1 {
		return nil, ErrNotFound
	}
	var scopes []string
	_ = json.Unmarshal([]byte(scopesJSON), &scopes)
	return &StationPrincipal{TokenID: tokenID, ActorID: actorID, Scopes: scopes}, nil
}

// ReassignResult reports what a reassignment did, including the part the operator did not ask for
// explicitly and must still be told about.
type ReassignResult struct {
	Station *Station
	// TakenFromName is the workspace that LOST this conversation key, empty when nothing held it.
	// It is reported rather than refused — see ReassignStationToSession for why — and reporting it
	// is what keeps that from being a silent steal.
	TakenFromName string
	TakenFromID   string
}

// ReassignStationToSession points an EXISTING workspace at a conversation, from the console.
//
// *** WHY A HUMAN NEEDS THIS: AN ABANDONED WORKSPACE HAS NO WAY BACK. ***
//
// ClaimStationForSession only ever ADOPTS a station whose `session_key IS NULL`, deliberately —
// stealing a claimed workspace on a declared key would make the key a credential, and it is
// documented as selecting rather than authorising (migration 0023). The consequence is that the
// moment a conversation dies, the workspace it claimed is sealed: its notes, tasks, locker and
// vault are intact and NOTHING can reach them, because the only conversation that could is gone.
//
// Vlad saw the way out: "we can use the fact that a workspace can be re-assigned to tell a chat
// session to recover (take over) an (abandoned) workspace, and it might even be used to
// re-establish comm channels."
//
// THE HUMAN IS THE AUTHORITY, WHICH IS WHY THIS IS CONSOLE-ONLY. Reassignment is exactly the act
// the claim path refuses to perform on a session's say-so. Putting it behind an authenticated
// console form keeps the rule intact — a key still authorises nothing; a PERSON decides who takes
// over a post — and it costs the human no credential handling at all, which is the standing
// requirement: the session invents a key, states it in its reply, and the human pastes that
// string. Nothing secret is ever on screen.
//
// AN EMPTY KEY RELEASES the workspace instead of refusing. Without it there is no way to undo a
// reassignment or to hand a post back to the pool, and a station wrongly pointed at a live
// conversation would be stuck to it forever — the same dead end this exists to open.
//
// ARCHIVED WORKSPACES ARE NOT REASSIGNABLE. Archiving already severs live endpoints and
// StationExists refuses archived ids; letting a reassign re-staff one would make archive a
// suggestion, and an operator would have no way to explain a session working inside it.
func (s *Store) ReassignStationToSession(ctx context.Context, stationID, sessionKey string) (*ReassignResult, error) {
	st, err := s.StationByID(ctx, stationID)
	if err != nil {
		return nil, err
	}
	if st.State == "archived" {
		return nil, errors.New("an archived workspace cannot be reassigned; unarchive it first")
	}

	key := strings.TrimSpace(sessionKey)
	if key == "" {
		if _, err := s.W.ExecContext(ctx,
			`UPDATE station SET session_key=NULL WHERE station_id=?`, stationID); err != nil {
			return nil, err
		}
		st.SessionKey = ""
		return &ReassignResult{Station: st}, nil
	}

	// *** THE KEY IS TAKEN FROM WHOEVER HOLDS IT, AND THE OPERATOR IS TOLD. ***
	//
	// The first cut REFUSED this, which was wrong in the exact case the feature exists for. The
	// human asks a chat session for its key; the session has already called station_me, so it
	// already holds a FRESH EMPTY workspace under that key. Refusing meant the common path failed
	// with "that key is in use" and demanded a second, non-obvious step — release the empty one,
	// then come back. The test written for the recovery flow hit it on the first run.
	//
	// Taking it is safe in a way that is worth stating: NOTHING IS DESTROYED. The displaced
	// workspace keeps every note, task, locker file and secret, stays listed in the console, and
	// can be adopted or reassigned again. Only a pointer moved, and the operator moved it on
	// purpose by typing that key.
	//
	// SO THE SAFETY IS DISCLOSURE, NOT REFUSAL: the result names what was displaced, the console
	// says so in the receipt, and an operator who did not mean it can put it back in one click. A
	// silent steal would be the defect; a refusal in the common case is just a wall.
	res := &ReassignResult{}
	if other, err := s.StationBySessionKey(ctx, key); err == nil && other.StationID != stationID {
		if _, err := s.W.ExecContext(ctx,
			`UPDATE station SET session_key=NULL WHERE station_id=?`, other.StationID); err != nil {
			return nil, err
		}
		res.TakenFromName, res.TakenFromID = other.Name, other.StationID
	} else if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	if _, err := s.W.ExecContext(ctx,
		`UPDATE station SET session_key=? WHERE station_id=? AND state <> 'archived'`,
		key, stationID); err != nil {
		return nil, err
	}
	st.SessionKey = key
	res.Station = st
	return res, nil
}

// StationIsLive reports whether a station exists and is not archived.
//
// ONE STATEMENT REPLACING THREE DRIFTED ONES. The same question was asked three ways in three
// places: an owner-token comparison in comm's auth, IsStationArchived reading `state`, and
// StationExists reading `archived_at`. Two columns, three answers, for one fact.
//
// `state` IS THE ONE TO READ: it carries a CHECK constraint (migration 0012), so an invalid value
// is impossible rather than merely unwritten.
//
// *** IT DOES NOT COMPARE ACTORS, AND THAT IS DELIBERATE — I TRIED IT AND IT WAS WRONG. ***
//
// The first version required `created_by_actor_id = <the caller's actor>`. It refuses the ordinary
// case: a station created by a HUMAN in the console carries the human's actor id, while the agent
// staffing it authenticates as its own. Every console-created station would have been unreachable
// by the session that works in it.
//
// And there is nothing for an actor comparison to protect. IDENTITY.md §4: the security boundary
// is the OAuth grant — whose estate — and single-user is what makes selection sufficient, because
// "there is no other tenant to protect against". Within one instance there is one estate, so
// "belongs to this human" is true of every station by construction. The check it replaced was
// vacuous in a worse way: it compared a per-grant constant to itself.
//
// IF KEN EVER BECOMES MULTI-TENANT, THIS IS THE FUNCTION THAT MUST GROW THE OWNER CHECK. It is the
// single place every surface asks the question, which is why it is worth saying here.
func (s *Store) StationIsLive(ctx context.Context, stationID string) (bool, error) {
	var ok bool
	err := s.R.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM station WHERE station_id=?1 AND state='active')`,
		stationID).Scan(&ok)
	return ok, err
}

// StationIsArchived distinguishes "archived" from "does not exist", for the ONE caller allowed to
// tell them apart — see station.Resolve, which asks it only after an OAuth grant has verified and
// only because an archived station has a remedy a session cannot guess.
func (s *Store) StationIsArchived(ctx context.Context, stationID string) (bool, error) {
	var ok bool
	err := s.R.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM station WHERE station_id=?1 AND state='archived')`,
		stationID).Scan(&ok)
	return ok, err
}

// StationIDBySessionKeyAnyState resolves a conversation key WITHOUT filtering on state.
//
// StationBySessionKey excludes archived stations, which is right for its callers — an archived
// post must not be re-entered. But it makes archive INVISIBLE to a resolver: the key simply fails
// to resolve, and the session is told it never said which station it is, when in fact it said so
// correctly and the station is archived. The remedy differs completely between those two, so the
// resolver has to be able to tell them apart.
//
// It returns an id only. Nothing may act on the row; the sole caller uses it to choose which
// refusal to give.
func (s *Store) StationIDBySessionKeyAnyState(ctx context.Context, sessionKey string) (string, error) {
	var id string
	err := s.R.QueryRowContext(ctx,
		`SELECT station_id FROM station WHERE session_key=?1`, sessionKey).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return id, err
}
