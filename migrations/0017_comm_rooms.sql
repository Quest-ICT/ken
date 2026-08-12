-- ============================================================================
-- Ken — migration 0017: ROOMS, the many-party addressing rooms and broadcast need
-- ============================================================================
-- Slice 5 of the COMM addressing plan. Slice 3 made a message able to have several
-- recipients; this decides WHO they are.
--
-- WHY ROOMS LIVE IN ken.db AND NOT IN comm.db, which is the decision everything else
-- follows from: a membership list is a HUMAN decision, and human decisions belong in
-- the durable database. comm.db is expendable by design (S7 — every cross-database
-- pointer runs expendable -> durable), so a room that lived there would evaporate with
-- the message queue it exists to address. comm.db gets a derived MIRROR instead, which
-- can be rebuilt from here at any time; losing it loses a cache, never a decision.
--
-- NO ALTER of any existing table, deliberately. A snapshot restore stays cheap, and
-- nothing already in ken.db changes shape because rooms were added.
--
-- WHAT A ROOM IS NOT: it is not a channel with more seats. A channel is authorised by a
-- pairing code passed between two sessions; a room is a set a HUMAN names and fills.
-- That is why there is no agent-facing "create room" path — see the note at the end.
BEGIN;

-- A room is a human-named set of stations.
CREATE TABLE comm_room (
  id                  INTEGER PRIMARY KEY,
  -- Opaque and server-minted, like every other address in Ken. The NAME is for humans
  -- and may be edited; the id is what messages are filed against, so renaming a room
  -- cannot orphan its traffic.
  room_id             TEXT    NOT NULL UNIQUE,
  space_id            INTEGER NOT NULL DEFAULT 1,
  -- Human-typed, NEVER agent-set. A station's self-description is a claim it makes
  -- about itself (S8); a room's name is a fact its owner asserts, and the difference is
  -- the whole reason name-addressing can be trusted at all.
  name                TEXT    NOT NULL,
  -- 'dm' rooms are created implicitly for a pair and are keyed by their members rather
  -- than by a name, which is why the unique index below excludes them.
  kind                TEXT    NOT NULL DEFAULT 'topic' CHECK (kind IN ('topic','dm')),
  purpose             TEXT    NOT NULL DEFAULT '',
  state               TEXT    NOT NULL DEFAULT 'active' CHECK (state IN ('active','archived')),
  created_by_actor_id INTEGER NOT NULL REFERENCES actor(id),
  created_at          TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  archived_at         TEXT
);
CREATE UNIQUE INDEX idx_comm_room_name ON comm_room(space_id, name) WHERE kind='topic';

CREATE TABLE comm_room_member (
  room_id           TEXT    NOT NULL REFERENCES comm_room(room_id)  ON DELETE CASCADE,
  station_id        TEXT    NOT NULL REFERENCES station(station_id) ON DELETE CASCADE,
  added_by_actor_id INTEGER NOT NULL REFERENCES actor(id),
  added_at          TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  PRIMARY KEY (room_id, station_id)
) WITHOUT ROWID;
-- "a station may be in several rooms" is that primary key, for free — and this index is
-- what makes "which rooms am I in" one lookup rather than a scan.
CREATE INDEX idx_comm_room_member_station ON comm_room_member(station_id);

-- A TARGETED DENY that beats the roster and beats a link.
--
-- This is what makes a broad addressing default safe to offer. Without it the only way
-- to stop one station reaching another is to narrow something wide — unpublish a
-- station, revoke a link — which costs every other relationship it had. An operator who
-- has to break four things to fix one will not do it.
CREATE TABLE station_block (
  id                  INTEGER PRIMARY KEY,
  station_a           TEXT    NOT NULL REFERENCES station(station_id) ON DELETE CASCADE,
  station_b           TEXT    NOT NULL REFERENCES station(station_id) ON DELETE CASCADE,
  reason              TEXT    NOT NULL DEFAULT '',
  blocked_by_actor_id INTEGER NOT NULL REFERENCES actor(id),
  created_at          TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  -- An ordered pair, exactly like station_link: a block is symmetric, and storing it
  -- both ways round would let one direction be removed without the other.
  CHECK (station_a < station_b)
);
CREATE UNIQUE INDEX idx_station_block_pair ON station_block(station_a, station_b);

-- Bumped by every membership, roster or block write.
--
-- Carried on every delivered message so a receiver can tell that the set it was
-- addressed WITH has changed since. That matters for one specific thing: a session
-- given a standing instruction to auto-process a room's traffic is trusting a
-- membership it was told about once. When the roster moves, the epoch moves, and the
-- grant it was given no longer describes the room it is in.
CREATE TABLE comm_roster_epoch (
  id         INTEGER PRIMARY KEY CHECK (id = 1),
  epoch      INTEGER NOT NULL,
  updated_at TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
INSERT INTO comm_roster_epoch(id, epoch) VALUES (1, 1);

-- NOTE ON WHY THERE IS NO AGENT REQUEST PATH FOR ROOMS.
--
-- station_request's kind CHECK is IN ('station','link') (0012_stations.sql), SQLite
-- cannot alter a CHECK in place, and rebuilding a live request table to add 'room' is
-- not worth it. So rooms are created by the human, in the console or the CLI, and a
-- session that wants one asks in words.
--
-- That falls out of a schema constraint, and it is also the right answer: a room is a
-- set of stations a human decided should talk to each other. There is no version of
-- that decision an agent should be making for itself.
INSERT INTO schema_migration(version) VALUES (17);

COMMIT;
