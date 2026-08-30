package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// The writes the operator console makes (docs/STATIONS.md §10).
//
// Everything here is HUMAN-ONLY by construction. None of it is reachable from
// /station/mcp, and that is the design rather than an oversight: approving a
// request, typing a station's name, transferring assets and archiving are the
// capabilities the curation gate withholds. A session asks; a person decides.
//
// The one shape worth stating up front, because it is the reason this file is
// separate from stations.go: a request and its consequence must land TOGETHER.
// Approving a station request creates a station AND resolves the request; if the
// second half were a separate call, a crash between them would leave a pending
// request whose station already exists, and the operator would approve it twice.

// ErrRequestNotPending is returned when a request has already been decided. It is
// distinct from "not found" on purpose: the console shows it as "someone already
// handled this" rather than as a broken link, which is the likely truth when two
// browser tabs are open on the same queue.
var ErrRequestNotPending = errors.New("this request is no longer pending — it was already decided")

// ErrTransferCollision reports asset names that exist on BOTH stations. It carries
// the names because the human cannot act on a bare refusal: they have to rename or
// drop something, and they need to know what.
type ErrTransferCollision struct {
	Class     string // "notes" | "locker"
	Colliding []string
}

func (e *ErrTransferCollision) Error() string {
	return fmt.Sprintf("%s transfer refused — %d name(s) exist on both stations: %s (rename or remove them on one side first)",
		e.Class, len(e.Colliding), strings.Join(e.Colliding, ", "))
}

// ApproveStationRequest turns a pending `station` request into a real station, with
// the name the HUMAN typed. The name_hint the agent supplied is not consulted here
// at all — the console may show it, but this function takes only what the operator
// entered (S3).
//
// Atomic: the station is created and the request resolved in one transaction, so the
// queue can never show a pending request whose station already exists.
func (s *Store) ApproveStationRequest(ctx context.Context, requestID, name string, actorID int64) (*Station, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("a station needs a name — the human types it, and there is no default")
	}
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var kind, purpose string
	err = tx.QueryRowContext(ctx,
		`SELECT kind, COALESCE(purpose,'') FROM station_request WHERE request_id=? AND state='pending'`,
		requestID).Scan(&kind, &purpose)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrRequestNotPending
	}
	if err != nil {
		return nil, err
	}
	// NAMES NO SIBLING, because there are now two and this message named one. It said "approve it
	// with ApproveLinkRequest" for every non-station kind, so a 'room' request that reached here —
	// which is exactly what happened before the console grew a room branch — sent the operator to a
	// function that would also refuse it. A refusal that misdirects is worse than a bare one.
	if kind != "station" {
		return nil, fmt.Errorf("request %s is a %s request, not a station request — approve it from the %s section of the console", requestID, kind, kind)
	}

	stationID, err := randBase62(16)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO station(station_id, name, purpose, created_by_actor_id) VALUES(?,?,?,?)`,
		stationID, name, purpose, actorID); err != nil {
		if isUniqueViolation(err) {
			return nil, ErrStationNameTaken
		}
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE station_request
   SET state='approved', decided_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'), decided_by_actor_id=?
 WHERE request_id=? AND state='pending'`, actorID, requestID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.StationByID(ctx, stationID)
}

// DenyStationRequest records a refusal. A reason is REQUIRED for the same purpose a
// task's resolution line is required: the next request from the same station arrives
// to a human who can see what was already said no to, instead of re-deciding blind.
//
// For a link request this also feeds the denial ledger, whose mute window is what
// stops a persistent session from re-asking until a tired human says yes.
func (s *Store) DenyStationRequest(ctx context.Context, requestID, reason string, actorID int64) error {
	if strings.TrimSpace(reason) == "" {
		return errors.New("a denial needs a reason — the next request from this station reaches a human who should not have to re-decide blind")
	}
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var kind string
	var fromStation, toStation *string
	err = tx.QueryRowContext(ctx,
		`SELECT kind, from_station, to_station FROM station_request WHERE request_id=? AND state='pending'`,
		requestID).Scan(&kind, &fromStation, &toStation)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrRequestNotPending
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE station_request
   SET state='denied', decided_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),
       decided_by_actor_id=?, decision_reason=?
 WHERE request_id=? AND state='pending'`, actorID, reason, requestID); err != nil {
		return err
	}

	// TWO KINDS, TWO DENIAL POLICIES — and this comment described three until link requests were
	// retired, which is the shape that goes stale silently: prose that still explains a branch the
	// code no longer has reads as documentation, not as a leftover.
	//
	//   room    escalates a mute, but CreateRoomRequest computes it from the asking station's own
	//           denied rows rather than from a table — station_link_denial was keyed on a PAIR and
	//           a room request has none, so storing (x,x) there would have been a lie in the
	//           schema. Nothing to do here; the mute is read at ask time. That table is gone with
	//           migration 0026 and this is the reason nothing surviving needed it.
	//   station does NOT mute: there is nothing to key it against, and a session with no station
	//           has no other way to ask.
	//
	// `kind`, fromStation and toStation are still read above because the state transition needs the
	// row; nothing branches on them here any more.
	_, _, _ = kind, fromStation, toStation
	return tx.Commit()
}

// orderPair returns the two ids in the canonical order the CHECK (station_a <
// station_b) constraint requires. Storing the pair unordered would let A→B and B→A
// become two different rows, which would silently double every link and halve every
// mute window.
func orderPair(x, y string) (string, string) {
	if x <= y {
		return x, y
	}
	return y, x
}

// StationUsage is what the console shows against the configured caps.
type StationUsage struct {
	StationID   string
	Notes       int
	NoteBytes   int64
	OpenTasks   int
	TotalTasks  int
	LockerFiles int
	LockerBytes int64
	// The live-key count is GONE with station keys: a station has no credentials of its own.
}

// StationAssetUsage counts what a station is holding. Reported per station rather
// than per instance because the caps are per station, and a total across stations would
// hide the one that is actually full.
// OCTET_LENGTH, not LENGTH: SQLite's LENGTH() on TEXT counts CHARACTERS, so a field named
// NoteBytes fed by it under-reports by however much non-ASCII a notebook holds — and this
// number is compared against a byte CAP, so the station that is actually full is the one
// most likely to be under-reported. station_notes.go already uses OCTET_LENGTH throughout;
// this was the one query in the package that did not, which is why it read as correct.
func (s *Store) StationAssetUsage(ctx context.Context, stationID string) (*StationUsage, error) {
	u := &StationUsage{StationID: stationID}
	err := s.R.QueryRowContext(ctx, `
SELECT
  (SELECT COUNT(*)                     FROM station_note   WHERE station_id=?),
  (SELECT COALESCE(SUM(OCTET_LENGTH(body)),0) FROM station_note WHERE station_id=?),
  (SELECT COUNT(*)                     FROM station_task   WHERE station_id=? AND state='open'),
  (SELECT COUNT(*)                     FROM station_task   WHERE station_id=?),
  (SELECT COUNT(*)                     FROM station_locker WHERE station_id=?),
  (SELECT COALESCE(SUM(size_bytes),0)  FROM station_locker WHERE station_id=?)`,
		stationID, stationID, stationID, stationID, stationID, stationID).
		Scan(&u.Notes, &u.NoteBytes, &u.OpenTasks, &u.TotalTasks, &u.LockerFiles, &u.LockerBytes)
	if err != nil {
		return nil, err
	}
	return u, nil
}

// TransferResult reports what an asset transfer actually moved.
type TransferResult struct {
	Notes  int
	Tasks  int
	Locker int
	// Vault is the number of SECRETS moved. It was absent until 2026-08-26 because the vault
	// was not moved at all — see the note on TransferStationAssets.
	Vault int
}

// TransferStationAssets moves assets from one station to another — the answer to "a
// session is gone and its work should not be", and to "this machine is being
// replaced".
//
// Three properties, each load-bearing:
//
//   - ATOMIC across every class in one transaction. A half-moved station is worse
//     than an unmoved one: the human would have to reconstruct which classes went.
//   - REFUSED ENTIRELY on any name collision, with the colliding names returned.
//     Merging silently would let a `handoff` page overwrite a `handoff` page — and
//     since every station is expected to keep one, that collision is the COMMON case,
//     not the edge. The human renames or drops, then retries.
//   - The MESSAGE QUEUE NEVER MOVES. It lives in comm.db, it is expendable by
//     design, and pointing it at a new station would drag an expendable pointer into
//     a durable move (S7).
//
// *** THE VAULT DID NOT MOVE EITHER, AND NOTHING SAID SO — CORRECTED 2026-08-26. ***
//
// This function is documented as the answer to "a session is gone and its work should not
// be", and it left that session's CREDENTIALS behind. The comment above carefully explains
// why the message queue stays put and was silent about secrets, so an operator reading it
// would have concluded the transfer was complete. Found while designing station takeover
// for chat sessions, which is exactly the path that would have hit it.
//
// It moves now, on Vlad's ruling and for the reason he gave: the whole point is that work
// survives a session, and secrets are the part hardest to recreate — an API key nobody has
// a second copy of is worse to lose than a note.
//
// TWO CONSEQUENCES, both deliberate:
//
//   - VAULT NAMES CAN COLLIDE, so they are pre-checked like notes and locker blobs. A
//     silent merge here would overwrite one credential with another and the loser would be
//     unrecoverable from the destination.
//   - THE MOVE IS AUDITED AND THE TRAIL STAYS PUT. Every secret transferred writes one
//     station_vault_read row with via='transfer' against the SOURCE — the same meaning that
//     value already carries for station_vault_send — and the source's EXISTING read rows are
//     left where they are, because they record reads that happened there. The secret and its
//     revision history move; the record of who saw it does not. The value is never decrypted
//     to do any of this: the ciphertext is re-pointed, and ownership changing is the event.
//
// Tasks cannot collide — they are keyed by an opaque id, not a name — so they always
// move cleanly. Notes and locker blobs are keyed by human-chosen names and can.
func (s *Store) TransferStationAssets(ctx context.Context, fromID, toID string, notes, tasks, locker, vault bool, byTokenID string, byActorID int64) (*TransferResult, error) {
	if fromID == toID {
		return nil, errors.New("source and destination are the same station")
	}
	if !notes && !tasks && !locker && !vault {
		return nil, errors.New("nothing selected to transfer")
	}
	for _, id := range []string{fromID, toID} {
		if _, err := s.StationByID(ctx, id); err != nil {
			return nil, fmt.Errorf("station %s: %w", id, err)
		}
	}

	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	// Collisions are checked BEFORE anything moves, and across every selected class,
	// so a refusal reports the whole problem at once. Reporting notes now and locker
	// on the retry would make the human fix it twice.
	if notes {
		names, err := collidingNames(ctx, tx, `
SELECT a.key FROM station_note a JOIN station_note b ON a.key=b.key
 WHERE a.station_id=? AND b.station_id=? ORDER BY a.key`, fromID, toID)
		if err != nil {
			return nil, err
		}
		if len(names) > 0 {
			return nil, &ErrTransferCollision{Class: "notebook", Colliding: names}
		}
	}
	if locker {
		names, err := collidingNames(ctx, tx, `
SELECT a.name FROM station_locker a JOIN station_locker b ON a.name=b.name
 WHERE a.station_id=? AND b.station_id=? ORDER BY a.name`, fromID, toID)
		if err != nil {
			return nil, err
		}
		if len(names) > 0 {
			return nil, &ErrTransferCollision{Class: "locker", Colliding: names}
		}
	}

	if vault {
		names, err := collidingNames(ctx, tx, `
SELECT a.name FROM station_vault a JOIN station_vault b ON a.name=b.name
 WHERE a.station_id=? AND b.station_id=? ORDER BY a.name`, fromID, toID)
		if err != nil {
			return nil, err
		}
		if len(names) > 0 {
			return nil, &ErrTransferCollision{Class: "vault", Colliding: names}
		}
	}

	res := &TransferResult{}
	move := func(sql string) (int, error) {
		r, err := tx.ExecContext(ctx, sql, toID, fromID)
		if err != nil {
			return 0, err
		}
		n, _ := r.RowsAffected()
		return int(n), nil
	}
	if notes {
		// Revisions follow their page: the history is part of the asset, and a page
		// that arrives with no past is a worse handoff than no page at all.
		if res.Notes, err = move(`UPDATE station_note SET station_id=? WHERE station_id=?`); err != nil {
			return nil, err
		}
		if _, err = move(`UPDATE station_note_revision SET station_id=? WHERE station_id=?`); err != nil {
			return nil, err
		}
	}
	if tasks {
		if res.Tasks, err = move(`UPDATE station_task SET station_id=? WHERE station_id=?`); err != nil {
			return nil, err
		}
	}
	if locker {
		if res.Locker, err = move(`UPDATE station_locker SET station_id=? WHERE station_id=?`); err != nil {
			return nil, err
		}
	}
	if vault {
		// AUDITED FIRST, AGAINST THE SOURCE, and it stays there. via='transfer' already means
		// exactly this on the other path — station_vault_send records "this station's secret
		// left it" — so a station transfer writes the same event for the same reason.
		//
		// THE READ TRAIL DOES NOT MOVE, and that is the deliberate half. Those rows record
		// reads that HAPPENED AT THE SOURCE; relocating them would make the destination's log
		// assert reads from before it held the secret, and would erase the source's record of
		// ever having held it. The log answers "who could see this value" — moving it would
		// make it answer that question wrongly at both ends at once.
		if _, err = tx.ExecContext(ctx, `
INSERT INTO station_vault_read(station_id, name, via, by_token_id, by_actor_id)
SELECT station_id, name, 'transfer', ?, ? FROM station_vault WHERE station_id=?`,
			nullStr(byTokenID), byActorID, fromID); err != nil {
			return nil, err
		}
		// HISTORY DOES MOVE, for the same reason note revisions follow their page: a credential
		// that arrives with no previous values cannot be rolled back any more, and
		// reversibility is the vault's founding promise.
		if res.Vault, err = move(`UPDATE station_vault SET station_id=? WHERE station_id=?`); err != nil {
			return nil, err
		}
		if _, err = move(`UPDATE station_vault_history SET station_id=? WHERE station_id=?`); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	_ = s.TouchStationActivity(ctx, toID)
	return res, nil
}

// collidingNames runs a pre-flight collision query inside the transfer transaction,
// so the answer cannot go stale between the check and the move.
func collidingNames(ctx context.Context, tx *sql.Tx, query, fromID, toID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, query, fromID, toID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// StationLink is an approved peer relationship. Materializing channels from one is
// slice 4; the console lists them now so approving a link is not a write into a void.
type StationLink struct {
	LinkID     string
	StationA   string
	StationB   string
	NameA      string
	NameB      string
	State      string // active | dormant | revoked
	ApprovedAt string
}

// ListStationLinks returns the instance's links with both station names resolved, so the
// console never has to show an opaque id to a human.
func (s *Store) ListStationLinks(ctx context.Context) ([]StationLink, error) {
	rows, err := s.R.QueryContext(ctx, `
SELECT l.link_id, l.station_a, l.station_b,
       COALESCE(a.name,'(deleted)'), COALESCE(b.name,'(deleted)'),
       l.state, l.approved_at
  FROM station_link l
  LEFT JOIN station a ON a.station_id = l.station_a
  LEFT JOIN station b ON b.station_id = l.station_b

 ORDER BY l.approved_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StationLink
	for rows.Next() {
		var l StationLink
		if err := rows.Scan(&l.LinkID, &l.StationA, &l.StationB, &l.NameA, &l.NameB, &l.State, &l.ApprovedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// boolOrNilStore writes 1 or NULL rather than 1 or 0, so "not marked" and "marked
// clean" stay distinguishable. With COMM off there is no hearsay signal at all, and a
// column reading 0 would claim knowledge the server does not have.
func boolOrNilStore(b bool) any {
	if b {
		return 1
	}
	return nil
}

// AreStationsLinked reports whether an ACTIVE link joins two stations. This is the
// authorization check for materializing a channel without a pairing code: a dormant
// link (either station archived) does not authorize anything until it is restored.
func (s *Store) AreStationsLinked(ctx context.Context, x, y string) (bool, error) {
	a, b := orderPair(x, y)
	var ok bool
	err := s.R.QueryRowContext(ctx, `
SELECT EXISTS(SELECT 1 FROM station_link
               WHERE station_a=? AND station_b=? AND state='active')`, a, b).Scan(&ok)
	return ok, err
}

// SetStationLinkSuspended turns a relationship off, or back on.
//
// *** IT REPLACES RevokeStationLink, AND THE DIFFERENCE IS THE WHOLE POINT. *** Vlad: "'suspend'
// button instead of revoke button (I want to be able to 'resume' it). 'revoke' concept is out of
// the table." A relationship between two of his own stations should never be terminal — there is
// no threat model in which it must be, because there is no other tenant, and a one-way control
// makes a mistake permanent for no gain.
//
// GUARDED ON THE SOURCE STATE, deliberately. Suspending only touches an ACTIVE link and resuming
// only touches a SUSPENDED one, so neither can collide with `dormant` — which ArchiveStation sets
// and clears under exactly the same discipline. That guard is what lets one column carry two
// independent reasons to be not-active, and it is asserted rather than assumed.
//
// Killing the live channel is the CALLER's job: the channel lives in the expendable database this
// package must not reach into.
func (s *Store) SetStationLinkSuspended(ctx context.Context, linkID string, suspend bool) error {
	from, to, stamp := "active", "suspended", "strftime('%Y-%m-%dT%H:%M:%fZ','now')"
	if !suspend {
		from, to, stamp = "suspended", "active", "NULL"
	}
	res, err := s.W.ExecContext(ctx, `
UPDATE station_link SET state=?, suspended_at=`+stamp+`
 WHERE link_id=? AND state=?`, to, linkID, from)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	// The epoch moves here too, and this is the direction that matters most: the pair
	// scope a revoked link authorised must stop accepting sends, and it stops when the
	// caller refreshes the mirror. A revocation the mirror never learns about is a
	// permission the human believes they withdrew.
	return s.bumpRosterEpoch(ctx)
}

// LinkMirrorRows lists the pairs comm.db should treat as authorised: ACTIVE links whose
// BOTH stations are active.
//
// The state filter is the whole point and it matches AreStationsLinked exactly — a
// dormant link (either station archived) authorises nothing until it is restored. Two
// readers of one rule eventually drift; if a third appears, this predicate is the one to
// share.
func (s *Store) LinkMirrorRows(ctx context.Context) ([][2]string, error) {
	rows, err := s.R.QueryContext(ctx, `
SELECT l.station_a, l.station_b
  FROM station_link l
  JOIN station a ON a.station_id = l.station_a
  JOIN station b ON b.station_id = l.station_b
 WHERE l.state='active' AND a.state='active' AND b.state='active'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][2]string
	for rows.Next() {
		var a, b string
		if err := rows.Scan(&a, &b); err != nil {
			return nil, err
		}
		out = append(out, [2]string{a, b})
	}
	return out, rows.Err()
}

// StationLinkByID resolves one link with both station names, so the caller can name
// the pair in a confirmation and hand the two station ids to COMM.
//
// Separate from RevokeStationLink on purpose: the revoke is a write and this is the
// read that must happen BEFORE it — once the row says 'revoked' the console still
// needs to say whose relationship just ended.
// LinkStateBetween reports the state of the link joining two stations, and whether the target
// station exists at all. It is the refusal path's source of truth.
//
// *** WHY THIS EXISTS: THE MIRROR CANNOT TELL "SUSPENDED" FROM "NEVER HEARD OF IT". ***
//
// comm.db's station_link_mirror carries ACTIVE links only, so a suspended pair vanishes from it
// completely. The send path asked the mirror whether the target appeared in ANY row and, on a miss,
// answered "no station with that id is known here — check the id". In a two-station estate that is
// ALWAYS the answer after a suspend: the human turned the relationship off, and the session was
// told it had mistyped an id comm_directory had just handed it. It re-checks and retries, which is
// the one behaviour the SUSPENDED refusal exists to prevent.
//
// The mirror is right to hold only active links — it is the hot-path authorisation projection, and
// a suspended row in it would BE a permission. The mistake was inferring a REASON from a projection
// built to answer a different question. This asks ken.db, which is the authority, on the cold path
// where one extra read costs nothing.
//
// Returns ("", false, nil) when no such station exists; (state, true, nil) otherwise, with state
// empty when both stations exist and no link joins them.
func (s *Store) LinkStateBetween(ctx context.Context, x, y string) (state string, targetExists bool, err error) {
	if err := s.R.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM station WHERE station_id=?)`, y).Scan(&targetExists); err != nil {
		return "", false, err
	}
	if !targetExists {
		return "", false, nil
	}
	a, b := orderPair(x, y)
	err = s.R.QueryRowContext(ctx,
		`SELECT state FROM station_link WHERE station_a=? AND station_b=?`, a, b).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return "", true, nil
	}
	if err != nil {
		return "", true, err
	}
	return state, true, nil
}

func (s *Store) StationLinkByID(ctx context.Context, linkID string) (StationLink, error) {
	var l StationLink
	err := s.R.QueryRowContext(ctx, `
SELECT l.link_id, l.station_a, l.station_b,
       COALESCE(a.name,'(deleted)'), COALESCE(b.name,'(deleted)'),
       l.state, l.approved_at
  FROM station_link l
  LEFT JOIN station a ON a.station_id = l.station_a
  LEFT JOIN station b ON b.station_id = l.station_b
 WHERE l.link_id=?`, linkID).
		Scan(&l.LinkID, &l.StationA, &l.StationB, &l.NameA, &l.NameB, &l.State, &l.ApprovedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return StationLink{}, ErrNotFound
	}
	return l, err
}

// CreateRoomRequest files a session's ask for a ROOM, naming no other station.
//
// *** VLAD DECIDED THIS ON 2026-08-06 AND IT WAS DECLINED IN CODE SIX DAYS LATER. *** His words,
// overriding the session's own recommendation: "ROOM CREATION: sessions may REQUEST, human
// approves — NOT the humans-only option I recommended … Same shape as the curation gate, which is
// the right instinct — the agent proposes, the human promotes." Migration 0024 carries the whole
// account of why the schema objection that blocked it is no longer true.
//
// *** IT NAMES NO STATION, AND THAT IS THE SAFETY PROPERTY RATHER THAN A SIMPLIFICATION. ***
//
// 0017's surviving argument is that "a room is a set of stations a human decided should talk to
// each other; there is no version of that decision an agent should be making for itself." A
// request that names no members does not touch it: the human still decides membership, entirely,
// at the console. It also means there is no station name to resolve, so the enumeration oracle
// that CreateStationLinkRequest needs its identical-wording refusal to close cannot arise here.
//
// THREE PROPERTIES MIRRORED FROM THE LINK PATH, deliberately, because they were each paid for:
//
//   - THE REASON IS NEVER DELIVERED TO ANYONE. It is stored for the human and shown only in the
//     console (0012's S9 note). Without that rule a request is a one-shot unauthorized message
//     channel — and a room request would be a broadcast one.
//   - A MUTED STATION IS SILENTLY DROPPED and receives the ordinary "submitted, pending review"
//     answer. Telling the caller it was muted lets a persistent session probe the human's past
//     decisions one request at a time.
//   - hearsay MARKS THE TRANSITIVE PATH. A peer can talk this station into asking, and the request
//     then reaches the human looking like its own idea.
//
// WHERE IT DIVERGES, AND WHY. The link mute lives in station_link_denial, keyed on an unordered
// PAIR. A room request has no pair, and overloading a table whose every column says "link" to
// store (x,x) would be a lie in the schema. So the same escalating ladder is computed from the
// request rows themselves — which needs no new table and cannot drift from the denials it counts.
//
// Returns the request id, or ("", nil) when silently dropped; the caller reports success either way.
func (s *Store) CreateRoomRequest(ctx context.Context, tokenID, fromStation, nameHint, reason string, hearsay bool) (string, error) {
	if strings.TrimSpace(fromStation) == "" {
		return "", errors.New("a room request must come from a station")
	}
	if strings.TrimSpace(reason) == "" {
		return "", errors.New("a room request needs a reason — it is the only thing the human has to decide on, " +
			"and a request with none asks them to guess")
	}
	if _, err := s.StationByID(ctx, fromStation); err != nil {
		return "", err
	}

	// THE MUTE, computed from the station's own denied room requests. Same ladder as links:
	// 1 denial an hour, then 6, then 24, then a week.
	var denials int
	var lastDenied sql.NullString
	if err := s.R.QueryRowContext(ctx, `
SELECT COUNT(*), MAX(decided_at) FROM station_request
 WHERE kind='room' AND from_station=? AND state='denied'`, fromStation).Scan(&denials, &lastDenied); err != nil {
		return "", err
	}
	if denials > 0 && lastDenied.Valid {
		window := "+7 days"
		switch denials {
		case 1:
			window = "+1 hour"
		case 2:
			window = "+6 hours"
		case 3:
			window = "+24 hours"
		}
		var muted bool
		if err := s.R.QueryRowContext(ctx,
			`SELECT strftime('%Y-%m-%dT%H:%M:%fZ','now') < strftime('%Y-%m-%dT%H:%M:%fZ',?,?)`,
			lastDenied.String, window).Scan(&muted); err != nil {
			return "", err
		}
		if muted {
			// Silently dropped. The caller is told what every caller is told.
			return "", nil
		}
	}

	// AT MOST ONE PENDING ROOM ASK PER STATION. A session that asks twice must not put two rows in
	// front of the human to decide identically — the same rule the link path applies to a pair.
	var existing string
	err := s.R.QueryRowContext(ctx,
		`SELECT request_id FROM station_request WHERE kind='room' AND state='pending' AND from_station=?`,
		fromStation).Scan(&existing)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}

	id, err := randBase62(12)
	if err != nil {
		return "", err
	}
	// to_station stays NULL: this request is about nobody else, which is the whole point.
	if _, err := s.W.ExecContext(ctx, `
INSERT INTO station_request(request_id, kind, from_station, from_token_id, name_hint, reason,
                            prompted_by_peer_traffic)
VALUES(?,'room',?,?,?,?,?)`,
		id, fromStation, tokenID, nullStr(strings.TrimSpace(nameHint)), reason,
		boolOrNilStore(hearsay)); err != nil {
		return "", err
	}
	return id, nil
}

// ApproveRoomRequest turns a pending room request into a room, with the name the HUMAN typed.
//
// THE NAME IS THE HUMAN'S, ALWAYS. name_hint is documented NON-BINDING on the column itself and is
// treated that way here: it is shown in the console as a suggestion and this function takes the
// name as an argument. An approval that silently used the agent's hint would let a session choose
// what its human sees in the room list.
//
// MEMBERSHIP IS NOT TOUCHED. The room is created EMPTY and the human adds members at the console,
// exactly as before this existed. That is the whole reason 0017's principle survives the feature:
// the agent asked, the human decided who.
func (s *Store) ApproveRoomRequest(ctx context.Context, requestID, name string, actorID int64) (*Room, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("a room needs a name — the suggestion in the request is not binding")
	}
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var kind string
	err = tx.QueryRowContext(ctx,
		`SELECT kind FROM station_request WHERE request_id=? AND state='pending'`, requestID).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrRequestNotPending
	}
	if err != nil {
		return nil, err
	}
	// GUARDED LIKE ITS SIBLINGS, and it earns the guard: the console approve handler dispatches on
	// a form field, and a mis-filled form must fail loudly rather than take the wrong branch.
	if kind != "room" {
		return nil, fmt.Errorf("request %s is a %s request — approve it with the matching function", requestID, kind)
	}

	roomID, err := randBase62(16)
	if err != nil {
		return nil, err
	}
	// THE SAME INSERT CreateRoom PERFORMS, kind and all, deliberately rather than a second shape.
	// Two creation paths that differ by a column is how a room made one way stops behaving like a
	// room made the other, and this project has paid for divergence like that more than once.
	if _, err := tx.ExecContext(ctx, `
INSERT INTO comm_room(room_id, name, kind, purpose, created_by_actor_id)
VALUES(?,?,'topic','',?)`, roomID, name, actorID); err != nil {
		// A name collision is the ordinary case — a human approving a second ask for "ops" — so it
		// comes back as the sentinel the console already renders rather than a raw constraint.
		if isUniqueViolation(err) || strings.Contains(err.Error(), "UNIQUE") {
			return nil, ErrRoomNameTaken
		}
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE station_request
   SET state='approved', decided_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'), decided_by_actor_id=?
 WHERE request_id=? AND state='pending'`, actorID, requestID); err != nil {
		return nil, err
	}
	// THE EPOCH MOVES, because CreateRoom moves it. An empty room reaches nobody, so a bump here
	// is arguably redundant — but the argument for matching is stronger than the argument for
	// saving one increment: a consumer must not be able to tell which path made a room. Done in
	// THIS transaction, like ApproveLinkRequest, so an approval is atomic across both facts.
	if _, err := tx.ExecContext(ctx, `
UPDATE comm_roster_epoch SET epoch = epoch + 1,
       updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=1`); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &Room{RoomID: roomID, Name: name, Kind: "topic", State: "active"}, nil
}

// EnsureStationLink creates the relationship between two stations if it does not exist, ACTIVE.
//
// *** THIS IS THE AUTO-APPROVAL, AND IT IS THE HALF THAT REMOVES THE HUMAN FROM THE LOOP. ***
//
// Vlad's decision: comm is available immediately to any session holding the connector, exactly
// like the station surface. His reasoning is Ken's own design doc — IDENTITY.md §4, "single-user
// makes that sufficient… there is no other tenant to protect against" — so the approval was
// guarding a threat model the design explicitly denies.
//
// WHAT IT DELETES BEYOND THE CLICK, and this is the part worth seeing before reading the code: the
// entire apparatus around link REQUESTS existed to stop a session probing its human's refusals. If
// nothing can be refused, the escalating mute ladder, the never-deliver-the-reason rule, and the
// one-pending-ask-per-pair collapse are all guarding an outcome that cannot occur.
//
// *** THE LINK IS NOT ABOLISHED, AND THAT IS ALSO HIS CHOICE. *** ken-prod-ops put both options to
// him and he took auto-approval specifically to keep the audit trail and a surgical off-switch: a
// link still records who spoke to whom and since when, and it can still be suspended.
//
// A SUSPENDED LINK IS NOT RESURRECTED. `ON CONFLICT DO NOTHING` means a human's decision to turn a
// relationship off survives the next message that would have created it — otherwise Suspend would
// be undone by the first thing it was meant to stop, which is the whole failure mode of an
// auto-approving gate. A dormant link is left alone for the same reason: its station is archived,
// and unarchiving is what restores it.
func (s *Store) EnsureStationLink(ctx context.Context, x, y string, actorID int64) (bool, error) {
	if x == "" || y == "" || x == y {
		return false, nil
	}
	a, b := orderPair(x, y)
	linkID, err := randBase62(16)
	if err != nil {
		return false, err
	}
	res, err := s.W.ExecContext(ctx, `
INSERT INTO station_link(link_id, station_a, station_b, approved_by_actor_id)
VALUES(?,?,?,?) ON CONFLICT DO NOTHING`, linkID, a, b, actorID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return false, nil
	}
	// THE ROSTER EPOCH MOVES, for the same reason ApproveLinkRequest moved it: a link changes who
	// a message can reach, and a consumer comparing epochs would otherwise conclude it is looking
	// at the roster it already had.
	if err := s.bumpRosterEpoch(ctx); err != nil {
		return true, err
	}
	return true, nil
}
