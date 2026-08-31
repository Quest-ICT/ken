-- comm.sql — the WHOLE comm database, created in one step.
--
-- KEN DOES NOT MIGRATE DATABASES. It applies this file to an EMPTY one and otherwise reads the
-- recorded schema version and refuses to start if it is not the number below. Upgrading an
-- existing database is a separate, deliberate act an operator performs with stock sqlite3 —
-- see docs/UPGRADING-THE-DATABASE.md and the scripts in upgrade/.
--
-- WHY THAT WAY ROUND. A migration runner is code that rewrites data nobody is watching, on a
-- schedule set by whoever restarts the service. Ken is installed FRESH, so the runner existed
-- almost entirely for a case that does not arise — and the one time it did arise, three audit
-- rounds found the same migration broken three separate times. Moving the rewrite OUT of the
-- server makes it a thing an operator runs on purpose, reads the output of, and can verify with
-- the same sqlite3 they already use to check the result.
--
-- THIS FILE IS GENERATED, from a database built by the previous schema plus the upgrade script,
-- so it cannot disagree with what that script produces. Do not hand-edit it: change the upgrade
-- script, regenerate, and let the equivalence test compare the two.
--
-- SCHEMA VERSION 22.

-- NO `PRAGMA foreign_keys=OFF` HERE, AND THAT ABSENCE IS LOAD-BEARING.
--
-- A pragma is PER CONNECTION, not per statement, and this file is executed on the writer the
-- server keeps for its whole life. A file that turned enforcement off and did not turn it back on
-- left every ON DELETE CASCADE in the database inert for that process — measured: purging a
-- message stopped taking its deliveries with it, SQLite reused the freed rowid, and the next
-- insert collided with a delivery row that should not have existed.
--
-- Creating tables needs no such pragma. SQLite does not resolve a foreign key's target at CREATE
-- time, so the forward references below are fine in any order. The UPGRADE scripts do disable it,
-- deliberately and briefly, and turn it back on — but they run in their own sqlite3 process where
-- the blast radius ends with the command.

BEGIN;

CREATE TABLE attachment (
  id                 INTEGER PRIMARY KEY,
  attachment_id      TEXT    NOT NULL UNIQUE,      -- opaque, server-minted; also the on-disk filename
  sender_endpoint    INTEGER NOT NULL REFERENCES endpoint(id) ON DELETE CASCADE,
  recipient_endpoint INTEGER REFERENCES endpoint(id) ON DELETE CASCADE,

  -- The file's name as the RECEIVER should know it. Validated server-side to a
  -- bare basename (no separators, no dot-dot, no control bytes) so an offer can
  -- never steer a session toward an arbitrary local path — the exploit the C9
  -- redesign exists to prevent.
  name               TEXT    NOT NULL,
  size_bytes         INTEGER NOT NULL,             -- declared by the sender at offer time
  sha256             TEXT    NOT NULL,             -- declared; uploads are verified against it
  transfer           TEXT    NOT NULL CHECK (transfer IN ('path','upload')),
  -- Optional note accompanying the offer. Persisted only so an 'upload' offer's
  -- note can ride the message that is enqueued later, at upload completion.
  note               TEXT,
  -- For 'path' transfers: SHA-256 of the rendezvous nonce (C9). The receiver
  -- proves shared-filesystem access by echoing the nonce whose hash this is.
  -- Carried opaquely; the server never sees the nonce itself.
  nonce_sha256       TEXT,

  state              TEXT    NOT NULL DEFAULT 'offered'
                       CHECK (state IN ('offered','ready','done','failed','expired')),
  stored_bytes       INTEGER NOT NULL DEFAULT 0,   -- bytes actually held on disk (uploads only)
  message_id         INTEGER REFERENCES message(id) ON DELETE SET NULL,
  idempotency_key    TEXT,

  expires_at         TEXT    NOT NULL,
  created_at         TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ready_at           TEXT,
  done_at            TEXT
, scope_id TEXT NOT NULL);
CREATE TABLE delivery (
  id                  INTEGER PRIMARY KEY,
  message_row         INTEGER NOT NULL REFERENCES message(id) ON DELETE CASCADE,
  -- The reader, as an identity that survives reconnection.
  party_key           TEXT    NOT NULL,   -- 's:<station_id>' | 'e:<endpoint rowid>'
  -- Audit only, and SET NULL rather than CASCADE: when the idle sweep removes an
  -- endpoint, the record that a delivery happened must outlive the endpoint that
  -- received it. Deleting the row instead would erase a message's history because a
  -- reader went quiet.
  recipient_endpoint  INTEGER REFERENCES endpoint(id) ON DELETE SET NULL,
  state               TEXT    NOT NULL DEFAULT 'queued'
                        CHECK (state IN ('queued','delivered','acked','expired')),
  delivery_count      INTEGER NOT NULL DEFAULT 0,
  claimed_by_endpoint INTEGER REFERENCES endpoint(id) ON DELETE SET NULL,
  claim_expires_at    TEXT,
  first_delivered_at  TEXT,
  acked_at            TEXT,
  acked_by_endpoint   INTEGER REFERENCES endpoint(id) ON DELETE SET NULL,
  -- NULL at send, ARMED at first delivery. A deadline that starts before anyone has
  -- been handed the message is a deadline against a weekend.
  reply_deadline_at   TEXT,
  replied_by          INTEGER REFERENCES message(id) ON DELETE SET NULL);
CREATE TABLE endpoint (
  id            INTEGER PRIMARY KEY,
  endpoint_id   TEXT    NOT NULL UNIQUE,       -- opaque, server-minted; the only address
  token_id      TEXT    NOT NULL,              -- ken.db api_token.token_id (no FK: other db)
  actor_id      INTEGER NOT NULL,              -- ken.db actor.id           (no FK: other db)
  label         TEXT,                          -- human-readable decoration; NEVER an address
  host_hint     TEXT,                          -- opaque same-host hint; never authorizes
  created_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  last_seen_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  revoked_at    TEXT
, station_id TEXT, bound_at TEXT, session_key TEXT);
CREATE TABLE "message" (
  id                 INTEGER PRIMARY KEY,
  message_id         TEXT    NOT NULL UNIQUE,
  scope_id           TEXT    NOT NULL,
  scope_seq          INTEGER NOT NULL,   -- per SCOPE, total, across all senders
  -- Nullable from here on: a room message belongs to no channel. Still written for
  -- every 'ch:' message so slice 7 can retire the column with evidence rather than
  -- hope.
  sender_endpoint    INTEGER NOT NULL REFERENCES endpoint(id) ON DELETE CASCADE,
  sender_party       TEXT    NOT NULL,   -- 's:<station_id>' | 'e:<endpoint rowid>'

  idempotency_key    TEXT,
  body               TEXT,
  body_sha256        TEXT    NOT NULL,
  body_bytes         INTEGER NOT NULL,
  kind               TEXT    NOT NULL DEFAULT 'message' CHECK (kind IN ('message','status')),

  requires_response  INTEGER NOT NULL DEFAULT 0 CHECK (requires_response IN (0,1)),
  -- Inert until rooms: with one recipient, 'any' and 'all' are the same question.
  response_mode      TEXT    NOT NULL DEFAULT 'any' CHECK (response_mode IN ('any','all')),
  answered_at        TEXT,               -- first reply from ANY recipient
  reply_to           INTEGER REFERENCES message(id) ON DELETE SET NULL,

  expires_at         TEXT    NOT NULL,
  created_at         TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),

  -- How many parties this was addressed to, and the roster epoch at send time. Both
  -- are 1 and 0 for every row that exists today; slice 5 gives them meaning, and slice
  -- 6 uses the epoch to lapse a standing auto-process grant when membership moves.
  audience_size      INTEGER NOT NULL DEFAULT 1,
  audience_epoch     INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE mirror_state (
  id           INTEGER PRIMARY KEY CHECK (id = 1),
  roster_epoch INTEGER NOT NULL DEFAULT 0,
  refreshed_at TEXT    NOT NULL
);
CREATE TABLE notice_watermark (
  -- 's:<station_id>' or 'e:<endpoint rowid>' — the same party key everything else in
  -- this database is addressed by, so a session that reconnects under a new endpoint
  -- keeps its place in the stream.
  party_key TEXT PRIMARY KEY,
  -- seen_at is CONFIRMED: notices at or before it are never shown again.
  seen_at   TEXT NOT NULL,
  -- shown_at is what the LAST poll put in front of the caller, not yet confirmed.
  --
  -- TWO COLUMNS BECAUSE A NEW TOOL CANNOT REACH A RUNNING SESSION. The obvious design
  -- is an explicit "I have read my notices" call, and it is unusable here: MCP tool
  -- lists pin at conversation start, so a tool added today is invisible to every
  -- session already running — the exact population most likely to have messages dying
  -- unread. A design whose only clearing mechanism is a new call would repeat notices
  -- forever for precisely those sessions.
  --
  -- So the confirmation rides the poll that was going to happen anyway: each poll
  -- promotes the PREVIOUS poll's shown_at into seen_at, then records what it shows.
  -- A notice is cleared by the caller COMING BACK rather than by the call that showed
  -- it, so a fault between the query and the caller holding the result cannot drop it.
  -- A result lost in transit still loses the notice — the server cannot tell a
  -- delivered result from a discarded one — and that is the accepted cost of not
  -- requiring a confirmation call no running session could make.
  shown_at  TEXT NOT NULL DEFAULT ''
) WITHOUT ROWID;
CREATE TABLE room_member_mirror (
  room_id   TEXT NOT NULL,
  -- ALWAYS 's:<station_id>'. Rooms hold stations, never endpoints — an endpoint is a
  -- connection and a room is a standing arrangement between posts. Stored in party form
  -- anyway so Send can use it directly against `delivery.party_key` with no translation
  -- step to get wrong.
  party_key TEXT NOT NULL,
  PRIMARY KEY (room_id, party_key)
) WITHOUT ROWID;
CREATE TABLE schema_migration (
  version    INTEGER PRIMARY KEY,
  applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE TABLE scope_counter (scope_id TEXT PRIMARY KEY, next_seq INTEGER NOT NULL) WITHOUT ROWID;
CREATE TABLE station_link_mirror (
  station_a TEXT NOT NULL,
  station_b TEXT NOT NULL,
  PRIMARY KEY (station_a, station_b),
  CHECK (station_a < station_b)
) WITHOUT ROWID;
CREATE TABLE transfer_grant (
  id            INTEGER PRIMARY KEY,
  grant_sha256  TEXT    NOT NULL UNIQUE,
  attachment_id INTEGER NOT NULL REFERENCES attachment(id) ON DELETE CASCADE,
  endpoint_id   INTEGER NOT NULL REFERENCES endpoint(id) ON DELETE CASCADE,
  kind          TEXT    NOT NULL CHECK (kind IN ('upload','download')),
  expires_at    TEXT    NOT NULL,
  consumed_at   TEXT,
  created_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE UNIQUE INDEX idx_attachment_idem
  ON attachment(scope_id, sender_endpoint, idempotency_key)
  WHERE idempotency_key IS NOT NULL;
CREATE INDEX idx_attachment_msg   ON attachment(message_id) WHERE message_id IS NOT NULL;
CREATE INDEX idx_attachment_scope ON attachment(scope_id, state);
CREATE INDEX idx_attachment_state ON attachment(state, expires_at);
CREATE INDEX idx_delivery_expiry ON delivery(state, message_row);
CREATE INDEX idx_delivery_inbox  ON delivery(party_key, state, id);
CREATE UNIQUE INDEX idx_delivery_unique ON delivery(message_row, party_key);
CREATE INDEX idx_endpoint_owner ON endpoint(actor_id);
CREATE UNIQUE INDEX idx_endpoint_session_key
    ON endpoint(session_key) WHERE session_key IS NOT NULL;
CREATE UNIQUE INDEX idx_endpoint_station ON endpoint(station_id)
  WHERE station_id IS NOT NULL AND revoked_at IS NULL;
CREATE INDEX idx_endpoint_token ON endpoint(token_id);
CREATE INDEX idx_grant_attachment ON transfer_grant(attachment_id);
CREATE INDEX idx_grant_expires    ON transfer_grant(expires_at);
CREATE INDEX idx_message_expires ON message(expires_at);
CREATE UNIQUE INDEX idx_message_idem
  ON message(scope_id, sender_party, idempotency_key) WHERE idempotency_key IS NOT NULL;
CREATE INDEX idx_message_scope   ON message(scope_id, created_at);
CREATE UNIQUE INDEX idx_message_scope_seq ON message(scope_id, scope_seq);
CREATE INDEX idx_message_sender ON message(sender_party, kind);
CREATE INDEX idx_message_settled ON message(created_at);
CREATE INDEX idx_room_mirror_party ON room_member_mirror(party_key);

-- The rows a freshly created database carries.
INSERT INTO mirror_state(id, roster_epoch, refreshed_at)
  VALUES (1, 0, strftime('%Y-%m-%dT%H:%M:%fZ','now'));

INSERT INTO schema_migration(version) VALUES (22);

COMMIT;
