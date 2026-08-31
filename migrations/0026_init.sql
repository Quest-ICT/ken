-- 0026_init.sql — THE WHOLE SCHEMA, IN ONE STEP.
--
-- Ken is installed FRESH. Before this file a new database was built by REPLAYING every
-- migration in order: 26 files that created tables only to drop them again, added columns
-- later migrations removed, and rebuilt the same table several times over. The end state was
-- correct and the journey was pure cost — nobody could read the schema without replaying it
-- in their head, and every install paid for history no deployment has.
--
-- This file is that end state, GENERATED from the replay rather than transcribed from it, so
-- it cannot drift from what the chain produced. It is not hand-maintained. There is no
-- pre-4.0.0 database anywhere to upgrade, which is what makes collapsing the chain safe
-- rather than merely tidy.
--
-- IT KEEPS VERSION 26 DELIBERATELY. dbmigrate tracks applied migrations by the NUMBER in
-- the filename, so a database that already recorded 26 finds nothing pending and is left
-- untouched. Numbering it 0001 would make every existing database try to create tables it
-- already has.
--
-- FTS5 SHADOW TABLES ARE ABSENT ON PURPOSE. CREATE VIRTUAL TABLE builds and seeds
-- ['entry_code_fts_config', 'entry_code_fts_content', 'entry_code_fts_data', 'entry_code_fts_docsize', 'entry_code_fts_idx', 'entry_fts_config', 'entry_fts_content', 'entry_fts_data', 'entry_fts_docsize', 'entry_fts_idx']
-- itself; emitting them here makes the file fail on "table already exists".

BEGIN;

CREATE TABLE actor (
  id           INTEGER PRIMARY KEY,
  kind         TEXT NOT NULL CHECK (kind IN ('human','ai')),
  display_name TEXT NOT NULL,
  pw_hash      TEXT,                       -- Argon2id PHC string; only for kind='human'
  created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  updated_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE api_token (
  id            INTEGER PRIMARY KEY,
  token_id      TEXT NOT NULL UNIQUE,       -- public id embedded in the token string
  secret_sha256 TEXT NOT NULL,              -- hex SHA-256 of the secret half
  actor_id      INTEGER NOT NULL REFERENCES actor(id),
  scopes        TEXT NOT NULL DEFAULT '["read"]' CHECK (json_valid(scopes)),
                                            -- subset of: read | write-draft | propose | curate
  label         TEXT,
  created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  last_used_at  TEXT,                       -- THROTTLED writes (~1/min/token) to avoid write amplification on the read path
  revoked_at    TEXT                        -- soft revoke
, station_id TEXT, retired_at TEXT);

CREATE TABLE app_setting (
  key        TEXT NOT NULL PRIMARY KEY,
  value      TEXT NOT NULL,
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  updater    TEXT
);

CREATE TABLE comm_room (
  id                  INTEGER PRIMARY KEY,
  -- Opaque and server-minted, like every other address in Ken. The NAME is for humans
  -- and may be edited; the id is what messages are filed against, so renaming a room
  -- cannot orphan its traffic.
  room_id             TEXT    NOT NULL UNIQUE,
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

CREATE TABLE comm_room_member (
  room_id           TEXT    NOT NULL REFERENCES comm_room(room_id)  ON DELETE CASCADE,
  station_id        TEXT    NOT NULL REFERENCES station(station_id) ON DELETE CASCADE,
  added_by_actor_id INTEGER NOT NULL REFERENCES actor(id),
  added_at          TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  PRIMARY KEY (room_id, station_id)
) WITHOUT ROWID;

CREATE TABLE comm_roster_epoch (
  id         INTEGER PRIMARY KEY CHECK (id = 1),
  epoch      INTEGER NOT NULL,
  updated_at TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE curation_event (
  id         INTEGER PRIMARY KEY,
  entry_id   INTEGER NOT NULL REFERENCES entry(id) ON DELETE CASCADE,
  version_id INTEGER REFERENCES entry_version(id),
  event_type TEXT NOT NULL CHECK (event_type IN
               ('proposed','promoted','superseded','rejected','withdrawn',
                'deprecated','archived','reverified','refuted','flagged_stale')),
  from_state TEXT,
  to_state   TEXT,
  actor_id   INTEGER REFERENCES actor(id),
  actor_kind TEXT CHECK (actor_kind IN ('ai','human','import','system')),
  session_id TEXT,
  note       TEXT,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE entry (
  id                     INTEGER PRIMARY KEY,
  slug                   TEXT NOT NULL UNIQUE,   -- semantic id exposed to the AI, e.g. 'docker-copy-manifests-before-source'
  kind                   TEXT NOT NULL CHECK (kind IN ('user','feedback','project','reference')),
  title                  TEXT NOT NULL,
  summary                TEXT NOT NULL,          -- <=160 chars, the token-light ranking line
  category               TEXT,
  tags                   TEXT NOT NULL DEFAULT '[]'  CHECK (json_valid(tags)),
  triggers               TEXT NOT NULL DEFAULT '[]'  CHECK (json_valid(triggers)),

  lifecycle              TEXT NOT NULL DEFAULT 'draft'
                           CHECK (lifecycle IN ('draft','active','deprecated','archived')),
  staleness              TEXT NOT NULL DEFAULT 'fresh'   -- ORTHOGONAL to lifecycle (DESIGN §2 D4)
                           CHECK (staleness IN ('fresh','aging','stale','refuted')),

  curated_version_id     INTEGER REFERENCES entry_version(id),  -- authoritative head; NULL while draft
  provisional_version_id INTEGER REFERENCES entry_version(id),  -- best usable proposal; NULL if none
  curated_rev            INTEGER NOT NULL DEFAULT 0,            -- promotion count (curated head rev)
  use_count              INTEGER NOT NULL DEFAULT 0,            -- fetched-by-AI counter → maturity signal

  trust_policy           TEXT CHECK (trust_policy IN ('curated_only','high_confidence','all_proposals')),
                                                              -- NULL = inherit global default ('curated_only')

  lock_version           INTEGER NOT NULL DEFAULT 1,  -- optimistic lock / entity audit version; CAS at promotion
  created_at             TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  updated_at             TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  updater                TEXT NOT NULL DEFAULT 'system'
);

CREATE VIRTUAL TABLE entry_code_fts USING fts5(
  code,
  tokenize = 'trigram'
);

CREATE TABLE "entry_embedding" (
  version_id INTEGER NOT NULL REFERENCES entry_version(id) ON DELETE CASCADE,
  model_id   TEXT    NOT NULL,
  dim        INTEGER NOT NULL,
  vec        BLOB    NOT NULL,
  PRIMARY KEY (version_id, model_id)
);

CREATE VIRTUAL TABLE entry_fts USING fts5(
  title, summary, triggers, tags, problem, solution, rationale, caveats,
  tokenize = 'porter unicode61 remove_diacritics 2'
);

CREATE TABLE entry_link (
  id            INTEGER PRIMARY KEY,
  from_entry_id INTEGER NOT NULL REFERENCES entry(id) ON DELETE CASCADE,
  to_slug       TEXT NOT NULL,
  to_entry_id   INTEGER REFERENCES entry(id) ON DELETE SET NULL,
  link_type     TEXT NOT NULL CHECK (link_type IN ('relates','supersedes','refutes','depends_on')),
  created_by    INTEGER REFERENCES actor(id),
  created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  UNIQUE (from_entry_id, to_slug, link_type)
);

CREATE TABLE entry_outcome (
  id         INTEGER PRIMARY KEY,
  entry_id   INTEGER NOT NULL REFERENCES entry(id) ON DELETE CASCADE,
  outcome    TEXT NOT NULL CHECK (outcome IN ('helped','didnt-apply','was-wrong')),
  actor_id   INTEGER REFERENCES actor(id),
  actor_kind TEXT,
  session_id TEXT,
  note       TEXT,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE entry_version (
  id                INTEGER PRIMARY KEY,
  entry_id          INTEGER NOT NULL REFERENCES entry(id) ON DELETE CASCADE,
  rev_no            INTEGER NOT NULL,                 -- 1..N per entry
  state             TEXT NOT NULL DEFAULT 'proposed'
                      CHECK (state IN ('proposed','curated','superseded','rejected','withdrawn')),
  parent_version_id INTEGER REFERENCES entry_version(id),  -- head this was based on: lineage, diff base, rebase warning

  -- ---- frozen content (immutable after insert; see trigger entry_version_immutable) ----
  title             TEXT NOT NULL,
  summary           TEXT NOT NULL,
  problem           TEXT,                             -- "when does this apply?"
  solution          TEXT,                             -- **How to apply**
  rationale         TEXT,                             -- **Why** + trade-offs
  caveats           TEXT,
  code              TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(code)),             -- [{lang,caption,snippet}]
  tags              TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(tags)),
  triggers          TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(triggers)),         -- retrieval symptoms
  applies_to        TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(applies_to)),       -- ["spring-boot 4.0.x", ...] for staleness
  verified_against  TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(verified_against)), -- [{tool,version,date}]

  -- ---- provenance (frozen) ----
  author_actor_id   INTEGER REFERENCES actor(id),
  author_kind       TEXT CHECK (author_kind IN ('ai','human','import')),
  session_id        TEXT,
  confidence        REAL CHECK (confidence >= 0 AND confidence <= 1),  -- AI self-rating
  change_note       TEXT,                             -- the "commit message"

  -- ---- mutable status ----
  superseded_by_version_id INTEGER REFERENCES entry_version(id),
  reviewed_by_actor_id     INTEGER REFERENCES actor(id),
  reviewed_at              TEXT,
  verified_at              TEXT,
  verify_ttl_days          INTEGER,

  created_at        TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')), content_lang TEXT, via_comm INTEGER CHECK (via_comm IS NULL OR via_comm = 1), via_comm_kind TEXT
  CHECK (via_comm_kind IS NULL OR via_comm_kind IN ('directed','broadcast')),
  UNIQUE (entry_id, rev_no)
);

CREATE TABLE oauth_auth_code (
  code_sha256           TEXT PRIMARY KEY,
  grant_id              INTEGER NOT NULL REFERENCES oauth_grant(id) ON DELETE CASCADE,
  client_id             TEXT    NOT NULL,
  redirect_uri          TEXT    NOT NULL,
  code_challenge        TEXT    NOT NULL,
  code_challenge_method TEXT    NOT NULL,
  scope                 TEXT    NOT NULL,
  resource              TEXT,
  expires_at            TEXT    NOT NULL,
  created_at            TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE oauth_client (
  client_id     TEXT PRIMARY KEY,                 -- high-entropy public id we mint
  client_name   TEXT,
  redirect_uris TEXT NOT NULL CHECK (json_valid(redirect_uris)),  -- JSON array
  created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE oauth_grant (
  id             INTEGER PRIMARY KEY,
  client_id      TEXT    NOT NULL REFERENCES oauth_client(client_id),
  actor_id       INTEGER NOT NULL REFERENCES actor(id),   -- connector actor (author of writes)
  human_actor_id INTEGER NOT NULL REFERENCES actor(id),   -- who approved the connection
  scope          TEXT    NOT NULL,                        -- granted OAuth scope string (cosmetic; capability is fixed)
  resource       TEXT,                                    -- RFC 8707 audience (the MCP URL)
  created_at     TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  revoked_at     TEXT
);

CREATE TABLE oauth_token (
  token_sha256 TEXT PRIMARY KEY,
  grant_id     INTEGER NOT NULL REFERENCES oauth_grant(id) ON DELETE CASCADE,
  kind         TEXT    NOT NULL CHECK (kind IN ('access','refresh')),
  expires_at   TEXT    NOT NULL,
  created_at   TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  revoked_at   TEXT
);

CREATE TABLE schema_migration (
  version    INTEGER PRIMARY KEY,
  applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE station (
  id                   INTEGER PRIMARY KEY,
  station_id           TEXT    NOT NULL UNIQUE,   -- opaque; the ONLY thing comm.db points at
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
, session_key TEXT);

CREATE TABLE "station_link" (
  id                   INTEGER PRIMARY KEY,
  link_id              TEXT    NOT NULL UNIQUE,
  station_a            TEXT    NOT NULL REFERENCES station(station_id) ON DELETE CASCADE,
  station_b            TEXT    NOT NULL REFERENCES station(station_id) ON DELETE CASCADE,
  -- 'active' (live), 'dormant' (automatic — a station is archived), 'suspended' (a human turned
  -- it off and can turn it back on). There is no terminal state.
  state                TEXT    NOT NULL DEFAULT 'active'
                         CHECK (state IN ('active','dormant','suspended')),
  -- Kept, and still NOT NULL: a link records WHO allowed it. Auto-created links carry the actor
  -- whose grant the first contact arrived under, which is the same human either way — one
  -- instance, one human — and keeps the audit column meaningful rather than nullable.
  approved_by_actor_id INTEGER NOT NULL REFERENCES actor(id),
  approved_at          TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  -- suspended_at replaces revoked_at: same shape, reversible meaning. NULL whenever the link is
  -- not suspended, so "when was this turned off" has one answer and clearing it is the resume.
  suspended_at         TEXT,
  CHECK (station_a < station_b)
);

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

CREATE TABLE "station_request" (
  id                     INTEGER PRIMARY KEY,
  request_id             TEXT    NOT NULL UNIQUE,
  -- 'station' (an unknown session asking for a post) or 'room' (a station asking a human to
  -- create a room — naming no members). 'link' was removed in 0026: links are created on first
  -- contact and there is no longer a decision to file.
  kind                   TEXT    NOT NULL CHECK (kind IN ('station','room')),
  from_station           TEXT    REFERENCES station(station_id) ON DELETE CASCADE,
  from_token_id          TEXT    NOT NULL,        -- audit string; may dangle by design
  to_station             TEXT    REFERENCES station(station_id) ON DELETE CASCADE,
  name_hint              TEXT,                    -- NON-BINDING; the human types the name
  purpose                TEXT    NOT NULL DEFAULT '',
  reason                 TEXT    NOT NULL DEFAULT '',
  -- The transitive path: A cannot make a room but can talk B into asking for one, and B's request
  -- then reaches the human looking like B's own idea. Computed like entry_version.via_comm.
  prompted_by_peer_traffic INTEGER,
  state                  TEXT    NOT NULL DEFAULT 'pending'
                                 CHECK (state IN ('pending','approved','denied','expired')),
  created_at             TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  decided_at             TEXT,
  decided_by_actor_id    INTEGER REFERENCES actor(id),
  decision_reason        TEXT
);

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

CREATE TABLE station_vault (
  id            INTEGER PRIMARY KEY,
  station_id    TEXT    NOT NULL REFERENCES station(station_id) ON DELETE CASCADE,
  name          TEXT    NOT NULL,
  secret        TEXT    NOT NULL,
  -- What this is and where it came from. The console shows it in place of the value, so
  -- a human can identify a secret without revealing one.
  note          TEXT    NOT NULL DEFAULT '',
  size_bytes    INTEGER NOT NULL,
  -- Lets the console and the audit trail compare two values, and lets a session confirm
  -- it stored what it meant to, without either surface handling the secret itself.
  sha256        TEXT    NOT NULL,
  rev           INTEGER NOT NULL DEFAULT 1,
  -- The TRUE number of times this value has been handed out, kept here because the read
  -- log below is bounded. Without it the console could only ever say "here are the last
  -- N reads" and never "of how many" — which is the exact shape of the notebook's silent
  -- pruning: a surface that cannot distinguish "few reads" from "many reads, most
  -- dropped".
  read_count    INTEGER NOT NULL DEFAULT 0,
  created_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  updated_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  -- Soft delete. Set, never DELETEd, so the value is recoverable from history.
  deleted_at    TEXT,
  updated_by_token_id TEXT,
  updated_by_actor_id INTEGER REFERENCES actor(id),
  UNIQUE (station_id, name)
);

CREATE TABLE station_vault_history (
  id            INTEGER PRIMARY KEY,
  station_id    TEXT    NOT NULL REFERENCES station(station_id) ON DELETE CASCADE,
  name          TEXT    NOT NULL,
  secret        TEXT    NOT NULL,
  note          TEXT    NOT NULL DEFAULT '',
  size_bytes    INTEGER NOT NULL,
  sha256        TEXT    NOT NULL,
  rev           INTEGER NOT NULL,
  -- 'updated' or 'deleted' — why this value stopped being current.
  reason        TEXT    NOT NULL CHECK (reason IN ('updated','deleted')),
  replaced_at   TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  replaced_by_token_id TEXT,
  replaced_by_actor_id INTEGER REFERENCES actor(id)
);

CREATE TABLE "station_vault_read" (
  id            INTEGER PRIMARY KEY,
  station_id    TEXT    NOT NULL REFERENCES station(station_id) ON DELETE CASCADE,
  name          TEXT    NOT NULL,
  -- 'station' (a station_vault_get call), 'console' (a human clicked reveal), or 'transfer'
  -- (station_vault_send handed the value to ANOTHER station's vault). All three are reads of the
  -- same value and belong in one trail; what differs is where it went.
  via           TEXT    NOT NULL CHECK (via IN ('station','console','transfer')),
  read_at       TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  by_token_id   TEXT,
  by_actor_id   INTEGER REFERENCES actor(id)
);

CREATE TABLE web_session (
  id           TEXT PRIMARY KEY,               -- high-entropy cookie value
  actor_id     INTEGER NOT NULL REFERENCES actor(id) ON DELETE CASCADE,
  csrf_token   TEXT NOT NULL,
  created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  expires_at   TEXT NOT NULL,
  last_seen_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE UNIQUE INDEX idx_actor_kind_name ON actor(kind, display_name);

CREATE INDEX idx_api_token_actor ON api_token(actor_id);

CREATE INDEX idx_api_token_station ON api_token(station_id);

CREATE INDEX idx_ce_created ON curation_event(created_at);

CREATE INDEX idx_ce_entry   ON curation_event(entry_id);

CREATE INDEX idx_comm_room_member_station ON comm_room_member(station_id);

CREATE UNIQUE INDEX idx_comm_room_name         ON comm_room(name) WHERE kind='topic';

CREATE INDEX idx_entry_category  ON entry(category);

CREATE INDEX idx_entry_kind      ON entry(kind);

CREATE INDEX idx_entry_lifecycle ON entry(lifecycle);

CREATE INDEX idx_entry_staleness ON entry(staleness);

CREATE INDEX idx_ev_author ON entry_version(author_actor_id);

CREATE INDEX idx_ev_content_lang ON entry_version(content_lang);

CREATE INDEX idx_ev_entry  ON entry_version(entry_id);

CREATE INDEX idx_ev_state  ON entry_version(state);

CREATE INDEX idx_ev_via_comm ON entry_version(via_comm) WHERE via_comm IS NOT NULL;

CREATE INDEX idx_link_to_id   ON entry_link(to_entry_id);

CREATE INDEX idx_link_to_slug ON entry_link(to_slug);

CREATE INDEX idx_oauth_grant_client ON oauth_grant(client_id);

CREATE INDEX idx_oauth_grant_live   ON oauth_grant(revoked_at);

CREATE INDEX idx_oauth_token_grant ON oauth_token(grant_id);

CREATE INDEX idx_outcome_entry ON entry_outcome(entry_id);

CREATE UNIQUE INDEX idx_station_link_pair ON station_link(station_a, station_b);

CREATE UNIQUE INDEX idx_station_name           ON station(name);

CREATE INDEX idx_station_promotion_pending ON station_promotion(state, created_at);

CREATE INDEX idx_station_request_pending ON station_request(state, created_at);

CREATE UNIQUE INDEX idx_station_session_key
    ON station(session_key) WHERE session_key IS NOT NULL;

CREATE INDEX idx_station_state          ON station(state);

CREATE INDEX idx_station_task_human ON station_task(state, blocked_on, last_briefed_at);

CREATE INDEX idx_station_task_open ON station_task(station_id, state, last_briefed_at);

CREATE INDEX idx_station_vault_history ON station_vault_history(station_id, name, rev DESC);

CREATE INDEX idx_station_vault_read ON station_vault_read(station_id, read_at DESC);

CREATE INDEX idx_web_session_actor   ON web_session(actor_id);

CREATE INDEX idx_web_session_expires ON web_session(expires_at);

CREATE TRIGGER entry_resolve_links
AFTER INSERT ON entry
FOR EACH ROW
BEGIN
  UPDATE entry_link SET to_entry_id = NEW.id
   WHERE to_slug = NEW.slug AND to_entry_id IS NULL;
END;

CREATE TRIGGER entry_version_ad_fts
AFTER DELETE ON entry_version
FOR EACH ROW
BEGIN
  DELETE FROM entry_fts      WHERE rowid = OLD.id;
  DELETE FROM entry_code_fts WHERE rowid = OLD.id;
END;

CREATE TRIGGER entry_version_ai_fts
AFTER INSERT ON entry_version
FOR EACH ROW
BEGIN
  INSERT INTO entry_fts(rowid, title, summary, triggers, tags, problem, solution, rationale, caveats)
  VALUES (
    NEW.id, NEW.title, NEW.summary,
    COALESCE((SELECT group_concat(value, ' ') FROM json_each(NEW.triggers)), ''),
    COALESCE((SELECT group_concat(value, ' ') FROM json_each(NEW.tags)),     ''),
    COALESCE(NEW.problem,''), COALESCE(NEW.solution,''),
    COALESCE(NEW.rationale,''), COALESCE(NEW.caveats,'')
  );
  INSERT INTO entry_code_fts(rowid, code)
  VALUES (
    NEW.id,
    COALESCE((SELECT group_concat(json_extract(value,'$.snippet'), char(10)) FROM json_each(NEW.code)), '')
  );
END;

CREATE TRIGGER entry_version_immutable
BEFORE UPDATE ON entry_version
FOR EACH ROW
WHEN ( NEW.entry_id          IS NOT OLD.entry_id
    OR NEW.rev_no            IS NOT OLD.rev_no
    OR NEW.parent_version_id IS NOT OLD.parent_version_id
    OR NEW.title             IS NOT OLD.title
    OR NEW.summary           IS NOT OLD.summary
    OR NEW.problem           IS NOT OLD.problem
    OR NEW.solution          IS NOT OLD.solution
    OR NEW.rationale         IS NOT OLD.rationale
    OR NEW.caveats           IS NOT OLD.caveats
    OR NEW.code              IS NOT OLD.code
    OR NEW.tags              IS NOT OLD.tags
    OR NEW.triggers          IS NOT OLD.triggers
    OR NEW.applies_to        IS NOT OLD.applies_to
    OR NEW.verified_against  IS NOT OLD.verified_against
    OR NEW.author_actor_id   IS NOT OLD.author_actor_id
    OR NEW.author_kind       IS NOT OLD.author_kind
    OR NEW.session_id        IS NOT OLD.session_id
    OR NEW.confidence        IS NOT OLD.confidence
    OR NEW.change_note       IS NOT OLD.change_note
    OR NEW.via_comm          IS NOT OLD.via_comm
    OR NEW.via_comm_kind     IS NOT OLD.via_comm_kind
    OR NEW.created_at        IS NOT OLD.created_at )
BEGIN
  SELECT RAISE(ABORT, 'entry_version content is immutable — append a new revision instead of updating');
END;

-- The rows a freshly created database carries.
INSERT INTO comm_roster_epoch(id, epoch) VALUES (1, 1);

INSERT INTO schema_migration(version) VALUES (26);

COMMIT;
