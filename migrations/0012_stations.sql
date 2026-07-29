-- 0012_stations.sql — durable, human-named AI working identities (docs/STATIONS.md).
--
-- A STATION is a post that AI sessions staff and outlive: it owns a notebook, a task
-- list and a small file locker, and it is what COMM addresses, so a peer relationship
-- survives the session that created it. An endpoint (comm.db) stays what it is —
-- ephemeral, swept, a credentialed READER — and points AT a station here. Every
-- cross-database pointer therefore runs from the expendable file to this one, never
-- the reverse (STATIONS.md S7): a dangling pointer in comm.db is a row to drop, one
-- here would be corruption in the file we promise to restore.
--
-- These tables SHIP EMPTY unless KEN_STATION_ENABLED is set. ken.db migrations are
-- unconditional, so the flag gates the tool surface, the instructions and the console
-- — not the schema. An empty table costs a snapshot nothing, and a forward-compatible
-- schema is exactly what COMPATIBILITY.md permits.
--
-- Everything here lands in every nightly snapshot, ×15 with retention and Litestream
-- (S12), which is why every content table is bounded by settings rather than by hope.

BEGIN;

-- The station itself. Created, named, published, renamed, archived and reassigned ONLY
-- by a human (S3) — no tool writes `name`. `station_id` is the opaque, server-minted
-- routing identity; `name` is display and is never an address, or the first release
-- ships a namespace an agent can squat.
CREATE TABLE station (
  id                   INTEGER PRIMARY KEY,
  station_id           TEXT    NOT NULL UNIQUE,   -- opaque; the ONLY thing comm.db points at
  space_id             INTEGER NOT NULL DEFAULT 1,
  name                 TEXT    NOT NULL,          -- human-typed at approval; unique per space
  purpose              TEXT    NOT NULL DEFAULT '',
  -- Self-described fields carry their untrustworthiness IN THE NAME (S8), because a
  -- sibling verified:false key does not survive a harness flattening the result.
  self_described_about TEXT    NOT NULL DEFAULT '',
  self_described_tags  TEXT    NOT NULL DEFAULT '[]' CHECK (json_valid(self_described_tags)),
  published            INTEGER NOT NULL DEFAULT 0, -- human-only; an agent cannot advertise itself
  state                TEXT    NOT NULL DEFAULT 'active' CHECK (state IN ('active','archived')),
  created_by_actor_id  INTEGER NOT NULL REFERENCES actor(id),
  created_at           TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  advertised_at        TEXT,
  archived_at          TEXT,
  -- Ordering key for the console's cross-station view (§11.8). ken.db facts only —
  -- tasks touched, pages edited — never messages, which live in the expendable file.
  last_activity_at     TEXT
);
CREATE UNIQUE INDEX idx_station_name ON station(space_id, name);
CREATE INDEX idx_station_state ON station(space_id, state);

-- A station KEY is an api_token row (S5): same table, same hashing, same revocation,
-- same `ken token revoke`. It gains a nullable station_id — NULL means a key that can
-- call exactly one tool, station_request, which is how a session with no station asks
-- for one. The `kens_` prefix is contract and is declared in COMPATIBILITY.md.
ALTER TABLE api_token ADD COLUMN station_id TEXT;
-- retired = stop binding NEW endpoints, leave live ones alone (the "I moved machines"
-- path). revoked_at (already present) = stop binding AND sever, because you revoke on
-- leakage and a revocation that leaves the capability live is theatre (S6).
ALTER TABLE api_token ADD COLUMN retired_at TEXT;
CREATE INDEX idx_api_token_station ON api_token(station_id);

-- An approved relationship. UNDIRECTED: station_a < station_b by id, enforced by the
-- caller, so a pair has exactly one row and a denial cannot be sidestepped by asking
-- from the other side (S9).
CREATE TABLE station_link (
  id                   INTEGER PRIMARY KEY,
  link_id              TEXT    NOT NULL UNIQUE,
  space_id             INTEGER NOT NULL DEFAULT 1,
  station_a            TEXT    NOT NULL REFERENCES station(station_id) ON DELETE CASCADE,
  station_b            TEXT    NOT NULL REFERENCES station(station_id) ON DELETE CASCADE,
  state                TEXT    NOT NULL DEFAULT 'active' CHECK (state IN ('active','dormant','revoked')),
  approved_by_actor_id INTEGER NOT NULL REFERENCES actor(id),
  approved_at          TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  revoked_at           TEXT,
  CHECK (station_a < station_b)
);
CREATE UNIQUE INDEX idx_station_link_pair ON station_link(station_a, station_b);

-- A request awaiting the human: either "create me a station" or "let me talk to X".
-- The reason is shown ONLY to the human and never delivered to the target before
-- approval — otherwise every request is a one-shot unauthorized message channel (S9).
CREATE TABLE station_request (
  id                     INTEGER PRIMARY KEY,
  request_id             TEXT    NOT NULL UNIQUE,
  space_id               INTEGER NOT NULL DEFAULT 1,
  kind                   TEXT    NOT NULL CHECK (kind IN ('station','link')),
  from_station           TEXT    REFERENCES station(station_id) ON DELETE CASCADE,
  from_token_id          TEXT    NOT NULL,        -- audit string; may dangle by design
  to_station             TEXT    REFERENCES station(station_id) ON DELETE CASCADE,
  name_hint              TEXT,                    -- NON-BINDING; the human types the name
  purpose                TEXT    NOT NULL DEFAULT '',
  reason                 TEXT    NOT NULL DEFAULT '',
  -- The transitive path: A cannot make a channel but can talk B into asking for one to
  -- C, and B's request then reaches the human looking like B's own idea. Computed like
  -- entry_version.via_comm, badged in the console (S9).
  prompted_by_peer_traffic INTEGER,
  state                  TEXT    NOT NULL DEFAULT 'pending'
                                 CHECK (state IN ('pending','approved','denied','expired')),
  created_at             TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  decided_at             TEXT,
  decided_by_actor_id    INTEGER REFERENCES actor(id),
  decision_reason        TEXT
);
CREATE INDEX idx_station_request_pending ON station_request(space_id, state, created_at);

-- A human's "no", kept DURABLY here while the pending request is expendable — so a
-- refusal survives a comm.db loss. Undirected, matching the link's own shape; muting
-- an ordered pair would let the same relationship be re-asked from the other side.
CREATE TABLE station_link_denial (
  id            INTEGER PRIMARY KEY,
  space_id      INTEGER NOT NULL DEFAULT 1,
  station_a     TEXT    NOT NULL,
  station_b     TEXT    NOT NULL,
  denial_count  INTEGER NOT NULL DEFAULT 1,
  muted_until   TEXT,                            -- exponential: 1h, 6h, 24h, 7d
  last_denied_at TEXT   NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  CHECK (station_a < station_b)
);
CREATE UNIQUE INDEX idx_station_link_denial_pair ON station_link_denial(station_a, station_b);

-- NOTEBOOK. Working state, not knowledge (S10): never searched by kb_search, and the
-- only route into the knowledge base is a pending promotion a human converts.
CREATE TABLE station_note (
  id            INTEGER PRIMARY KEY,
  station_id    TEXT    NOT NULL REFERENCES station(station_id) ON DELETE CASCADE,
  key           TEXT    NOT NULL,                -- 'handoff' is a reserved convention
  title         TEXT    NOT NULL DEFAULT '',
  tags          TEXT    NOT NULL DEFAULT '[]' CHECK (json_valid(tags)),
  body          TEXT    NOT NULL DEFAULT '',
  rev           INTEGER NOT NULL DEFAULT 1,
  updated_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  -- Provenance is ken.db facts only. NEVER an endpoint id: that pointer is guaranteed
  -- to dangle once the sweep runs, and does not exist at all with COMM off (S7).
  updated_by_token_id TEXT,
  updated_by_actor_id INTEGER REFERENCES actor(id),
  hearsay_at_write    INTEGER,
  UNIQUE (station_id, key)
);

-- Revision history is an UNDO BUFFER, not content — the one thing besides the terminal
-- task archive that this design prunes without asking (S12's carve-outs).
CREATE TABLE station_note_revision (
  id            INTEGER PRIMARY KEY,
  station_id    TEXT    NOT NULL REFERENCES station(station_id) ON DELETE CASCADE,
  key           TEXT    NOT NULL,
  rev           INTEGER NOT NULL,
  body          TEXT    NOT NULL,
  updated_at    TEXT    NOT NULL,
  updated_by_token_id TEXT,
  updated_by_actor_id INTEGER REFERENCES actor(id),
  hearsay_at_write    INTEGER,
  UNIQUE (station_id, key, rev)
);

-- TASKS. Rows, not prose. The failure being fixed is DECAY (STATIONS.md §11.1), so the
-- fields that matter are the ones that fight it: blocked_on turns the end-of-session
-- guess into a query, and last_briefed_at is the anti-recency ordering key.
CREATE TABLE station_task (
  id                 INTEGER PRIMARY KEY,
  task_id            TEXT    NOT NULL UNIQUE,    -- short and sayable: "close t-7"
  station_id         TEXT    NOT NULL REFERENCES station(station_id) ON DELETE CASCADE,
  text               TEXT    NOT NULL,
  detail             TEXT    NOT NULL DEFAULT '',
  context            TEXT    NOT NULL DEFAULT '',
  blocked_on         TEXT    NOT NULL CHECK (blocked_on IN ('self','human','peer')),
  blocked_on_station TEXT    REFERENCES station(station_id) ON DELETE SET NULL,
  remind_after       TEXT,
  state              TEXT    NOT NULL DEFAULT 'open'
                             CHECK (state IN ('open','done','dropped')),
  resolution         TEXT,
  resolution_link    TEXT,                       -- kb slug+rev | commit | URL | note key
                                                 -- NEVER a COMM message id (S7)
  created_at         TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  created_by_token_id TEXT,
  created_by_actor_id INTEGER REFERENCES actor(id),
  hearsay_at_write    INTEGER,
  -- Named for what Ken can OBSERVE. Stamped only on rows a briefing DISPLAYS, at most
  -- once per staffing session — never by a list call, which is a pure query. Stamping
  -- the whole open set on every connect would show a perfect surfacing history while
  -- the human was told nothing (§11.4).
  last_briefed_at    TEXT,
  briefed_count      INTEGER NOT NULL DEFAULT 0,
  deferred_until     TEXT,
  defer_count        INTEGER NOT NULL DEFAULT 0,
  last_defer_reason  TEXT,
  closed_at          TEXT,
  closed_by_actor_id INTEGER REFERENCES actor(id)
);
CREATE INDEX idx_station_task_open ON station_task(station_id, state, last_briefed_at);
-- The cross-station view (§11.8) filters on blocked_on across every station in a space,
-- so it cannot use the per-station index above.
CREATE INDEX idx_station_task_human ON station_task(state, blocked_on, last_briefed_at);

-- LOCKER. The non-secret half of a working identity (S11) — memory and instruction
-- files, not payloads, and explicitly NOT a credential store. Ken cannot inspect a blob
-- and know it is a secret; the rule is documented, not enforced.
CREATE TABLE station_locker (
  id            INTEGER PRIMARY KEY,
  station_id    TEXT    NOT NULL REFERENCES station(station_id) ON DELETE CASCADE,
  name          TEXT    NOT NULL,
  bytes         BLOB    NOT NULL,
  size_bytes    INTEGER NOT NULL,
  sha256        TEXT    NOT NULL,
  content_type  TEXT    NOT NULL DEFAULT 'application/octet-stream',
  updated_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  updated_by_token_id TEXT,
  updated_by_actor_id INTEGER REFERENCES actor(id),
  UNIQUE (station_id, name)
);

-- The ONLY route from station state into the knowledge base (S10). It is a pending row
-- a human converts in the console — deliberately not a kb_save call, which would need a
-- scope a station token is forbidden and a dedup token HMAC-bound to the caller. It
-- also carries the write-time hearsay marking server-side, and a marking the model
-- retypes is forgeable.
CREATE TABLE station_promotion (
  id            INTEGER PRIMARY KEY,
  promotion_id  TEXT    NOT NULL UNIQUE,
  station_id    TEXT    NOT NULL REFERENCES station(station_id) ON DELETE CASCADE,
  note_key      TEXT    NOT NULL,
  note_rev      INTEGER NOT NULL,
  state         TEXT    NOT NULL DEFAULT 'pending'
                        CHECK (state IN ('pending','converted','discarded')),
  hearsay_at_write INTEGER,
  created_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  decided_at    TEXT,
  entry_slug    TEXT
);
CREATE INDEX idx_station_promotion_pending ON station_promotion(state, created_at);

INSERT INTO schema_migration(version) VALUES (12);

COMMIT;
