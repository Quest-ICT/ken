package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Rooms — the human-owned half of many-party addressing (migrations/0017_comm_rooms.sql,
// docs/COMM.md).
//
// A room is a set of stations a HUMAN named and filled. Everything in this file is
// reachable only from the console and the CLI; there is deliberately no agent path,
// because a room is a decision about which posts should be able to talk to each other
// and there is no version of that an agent should make for itself.
//
// The membership lives here, in ken.db, and comm.db keeps a derived mirror so that Send
// can check it inside its own writer transaction. That direction is fixed: the
// expendable database points at the durable one, never the reverse.

// Room is one room, with its member count already resolved — every surface that lists
// rooms wants it, and asking per row is the N+1 this avoids.
type Room struct {
	RoomID    string
	Name      string
	Kind      string // 'topic' or 'dm'
	Purpose   string
	State     string
	Members   int
	CreatedAt string
}

// RoomMember is one station's membership.
type RoomMember struct {
	StationID   string
	StationName string
	AddedAt     string
}

// ErrRoomNameTaken keeps two rooms from sharing a name in one space. The name is how a
// human refers to a room and how a session addresses one, so a duplicate is not a
// cosmetic problem — it makes `to_room: "ops"` ambiguous at the moment it matters.
var ErrRoomNameTaken = errors.New("a room with that name already exists")

// CreateRoom makes a room. Named by a human, always.
func (s *Store) CreateRoom(ctx context.Context, spaceID int64, name, purpose string, actorID int64) (*Room, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("a room needs a name — it is how sessions address it")
	}
	roomID, err := randBase62(16)
	if err != nil {
		return nil, err
	}
	if _, err := s.W.ExecContext(ctx, `
INSERT INTO comm_room(room_id, space_id, name, kind, purpose, created_by_actor_id)
VALUES(?,?,?,'topic',?,?)`, roomID, spaceID, name, purpose, actorID); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, ErrRoomNameTaken
		}
		return nil, err
	}
	if err := s.bumpRosterEpoch(ctx); err != nil {
		return nil, err
	}
	return &Room{RoomID: roomID, Name: name, Kind: "topic", Purpose: purpose, State: "active"}, nil
}

// AddRoomMember puts a station in a room. Idempotent: adding twice is not an error,
// because the console's obvious failure mode is a double submit and refusing it would
// teach an operator to distrust a button that worked.
func (s *Store) AddRoomMember(ctx context.Context, roomID, stationID string, actorID int64) error {
	if _, err := s.W.ExecContext(ctx, `
INSERT INTO comm_room_member(room_id, station_id, added_by_actor_id) VALUES(?,?,?)
ON CONFLICT(room_id, station_id) DO NOTHING`, roomID, stationID, actorID); err != nil {
		return err
	}
	return s.bumpRosterEpoch(ctx)
}

// RemoveRoomMember takes a station out of a room.
//
// It does NOT touch mail already sent. A message addressed to the room while this
// station was in it stays addressed to it — the audience was decided at send time and
// rewriting it afterwards would mean a recipient's inbox changed because of something
// that happened later. The epoch moves instead, which is how a session learns that the
// set it was told about is no longer the set that exists.
func (s *Store) RemoveRoomMember(ctx context.Context, roomID, stationID string) error {
	if _, err := s.W.ExecContext(ctx,
		`DELETE FROM comm_room_member WHERE room_id=? AND station_id=?`, roomID, stationID); err != nil {
		return err
	}
	return s.bumpRosterEpoch(ctx)
}

// ArchiveRoom retires a room without deleting its history. Reversible, like archiving a
// station: the messages filed against its scope keep their addresses.
func (s *Store) ArchiveRoom(ctx context.Context, roomID string, archived bool) error {
	state, stamp := "active", any(nil)
	if archived {
		state, stamp = "archived", "now"
	}
	if _, err := s.W.ExecContext(ctx, `
UPDATE comm_room SET state=?,
       archived_at = CASE WHEN ? IS NULL THEN NULL ELSE strftime('%Y-%m-%dT%H:%M:%fZ','now') END
 WHERE room_id=?`, state, stamp, roomID); err != nil {
		return err
	}
	return s.bumpRosterEpoch(ctx)
}

// ListRooms returns every room in a space with its member count.
func (s *Store) ListRooms(ctx context.Context, spaceID int64) ([]Room, error) {
	rows, err := s.R.QueryContext(ctx, `
SELECT r.room_id, r.name, r.kind, r.purpose, r.state, r.created_at,
       (SELECT COUNT(*) FROM comm_room_member m WHERE m.room_id = r.room_id)
  FROM comm_room r WHERE r.space_id=? ORDER BY r.state, r.name`, spaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Room
	for rows.Next() {
		var r Room
		if err := rows.Scan(&r.RoomID, &r.Name, &r.Kind, &r.Purpose, &r.State, &r.CreatedAt, &r.Members); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RoomMembers lists a room's stations, with names, for the console.
func (s *Store) RoomMembers(ctx context.Context, roomID string) ([]RoomMember, error) {
	rows, err := s.R.QueryContext(ctx, `
SELECT m.station_id, COALESCE(st.name,''), m.added_at
  FROM comm_room_member m
  LEFT JOIN station st ON st.station_id = m.station_id
 WHERE m.room_id=? ORDER BY st.name`, roomID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RoomMember
	for rows.Next() {
		var m RoomMember
		if err := rows.Scan(&m.StationID, &m.StationName, &m.AddedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// RoomMirrorRows returns every ACTIVE room's membership in the party form comm.db
// stores, ready to replace the mirror wholesale.
//
// Archived rooms are omitted rather than mirrored-and-filtered: the mirror's only job is
// answering "may this party send here", and an archived room's answer is no. Filtering
// at the source means the send path cannot forget to.
//
// ARCHIVED STATIONS ARE OMITTED FOR THE SAME REASON, and their absence was a defect with
// consequences beyond tidiness. An archived station stayed a full first-class recipient:
// counted in `recipients`, `audience_size` and `broadcast_reaches`, with delivery rows
// nobody could read and nobody could ack. Two things followed, both live:
//
//   - The SENDER got a spurious `expired` notice naming that station about one message-TTL
//     later, on every room message — continuous poll-path noise reporting a failure that
//     is really a retired post.
//   - Backpressure counts open deliveries PER SCOPE, so the dead member's permanent backlog
//     consumed the LIVE room's budget. Enough traffic inside one TTL window and every member
//     of that room is refused.
//
// The inner join is safe: comm_room_member.station_id references station(station_id) ON
// DELETE CASCADE, so it can drop no row that is not already an orphan.
func (s *Store) RoomMirrorRows(ctx context.Context) (map[string][]string, error) {
	rows, err := s.R.QueryContext(ctx, `
SELECT m.room_id, m.station_id
  FROM comm_room_member m
  JOIN comm_room r ON r.room_id = m.room_id
  JOIN station st ON st.station_id = m.station_id
 WHERE r.state='active' AND st.state='active'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var roomID, stationID string
		if err := rows.Scan(&roomID, &stationID); err != nil {
			return nil, err
		}
		out[roomID] = append(out[roomID], "s:"+stationID)
	}
	return out, rows.Err()
}

// RosterEpoch is the current membership generation.
func (s *Store) RosterEpoch(ctx context.Context) (int64, error) {
	var e int64
	err := s.R.QueryRowContext(ctx, `SELECT epoch FROM comm_roster_epoch WHERE id=1`).Scan(&e)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return e, err
}

// bumpRosterEpoch advances the generation counter. Every write in this file calls it,
// which is why they all return its error rather than swallowing it: a membership change
// that did not move the epoch is a change no session can detect.
func (s *Store) bumpRosterEpoch(ctx context.Context) error {
	_, err := s.W.ExecContext(ctx, `
UPDATE comm_roster_epoch SET epoch = epoch + 1,
       updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=1`)
	return err
}

// BlockStationPair denies traffic between two stations, beating rooms and links both.
// Stored as an ordered pair so the block cannot be half-removed.
func (s *Store) BlockStationPair(ctx context.Context, a, b, reason string, actorID int64) error {
	if a == b {
		return errors.New("a station cannot be blocked from itself")
	}
	if a > b {
		a, b = b, a
	}
	if _, err := s.W.ExecContext(ctx, `
INSERT INTO station_block(station_a, station_b, reason, blocked_by_actor_id) VALUES(?,?,?,?)
ON CONFLICT(station_a, station_b) DO UPDATE SET reason=excluded.reason`, a, b, reason, actorID); err != nil {
		return err
	}
	return s.bumpRosterEpoch(ctx)
}

// UnblockStationPair removes a block.
func (s *Store) UnblockStationPair(ctx context.Context, a, b string) error {
	if a > b {
		a, b = b, a
	}
	if _, err := s.W.ExecContext(ctx,
		`DELETE FROM station_block WHERE station_a=? AND station_b=?`, a, b); err != nil {
		return err
	}
	return s.bumpRosterEpoch(ctx)
}

// BlockedPairs returns every blocked pair, both directions, as "a|b" keys for a caller
// that wants to test membership cheaply.
//
// The separator is '|' rather than a control byte, and that is not arbitrary: a NUL or a
// tab reads as working, greps as nothing, and is invisible in every log that would
// otherwise show you the key. Station ids are randBase62 and cannot contain it.
func (s *Store) BlockedPairs(ctx context.Context) (map[string]bool, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT station_a, station_b FROM station_block`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var a, b string
		if err := rows.Scan(&a, &b); err != nil {
			return nil, err
		}
		out[fmt.Sprintf("%s|%s", a, b)] = true
		out[fmt.Sprintf("%s|%s", b, a)] = true
	}
	return out, rows.Err()
}
