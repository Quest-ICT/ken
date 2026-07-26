-- ============================================================================
-- Ken — schema migration 0001 (baseline)
-- ============================================================================
-- SQLite (WAL mode). This file is the SOURCE OF TRUTH for the KB data model.
--
-- Idioms — native SQLite, not patterns carried over from a client/server database
-- (see docs/DESIGN.md §2/§3):
--   * INTEGER PRIMARY KEY rowid + last_insert_rowid(); NO sequences, NO LASTVAL.
--     Reliable here BECAUSE all writes serialize through one writer connection.
--   * Timestamps are ISO-8601 UTC TEXT (strftime('%Y-%m-%dT%H:%M:%fZ','now')).
--   * JSON columns are TEXT guarded with CHECK(json_valid(...)); queried via json_each/json_extract.
--   * Enums enforced with CHECK(... IN (...)).
--   * Audit columns (lock_version/created_at/updated_at/updater) are APP-MANAGED
--     through the single writer (deterministic) rather than via BEFORE-UPDATE
--     SET triggers, which SQLite does not support in that form.
--
-- Apply on a connection already configured with:
--   PRAGMA journal_mode = WAL;
--   PRAGMA foreign_keys = ON;
--   PRAGMA busy_timeout = 10000;
--   PRAGMA synchronous  = NORMAL;
-- ============================================================================

BEGIN;

-- ---------------------------------------------------------------------------
-- Migration bookkeeping
-- ---------------------------------------------------------------------------
CREATE TABLE schema_migration (
  version    INTEGER PRIMARY KEY,
  applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

-- ---------------------------------------------------------------------------
-- Identity & tenancy seams — built now (cheap columns), isolation DEFERRED.
-- Everything is space_id=1 until a second party exists (DESIGN §7). Carrying
-- these columns from day 1 makes the collaborative future additive, not a rewrite.
-- ---------------------------------------------------------------------------
CREATE TABLE space (
  id         INTEGER PRIMARY KEY,
  name       TEXT NOT NULL UNIQUE,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
INSERT INTO space(id, name) VALUES (1, 'personal');

-- An actor is any writer: the human curator (kind='human') or an AI (kind='ai').
-- Seeded by the first-run wizard (the human actor needs the Argon2id hash it collects).
CREATE TABLE actor (
  id           INTEGER PRIMARY KEY,
  kind         TEXT NOT NULL CHECK (kind IN ('human','ai')),
  display_name TEXT NOT NULL,
  pw_hash      TEXT,                       -- Argon2id PHC string; only for kind='human'
  space_id     INTEGER NOT NULL DEFAULT 1 REFERENCES space(id),
  created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  updated_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

-- API tokens for the MCP interface. Opaque high-entropy secret ⇒ store SHA-256
-- and constant-time compare. NEVER Argon2 a token (Argon2 exists to slow
-- brute-force of LOW-entropy passwords; on a 256-bit secret it only taxes every call).
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
);
CREATE INDEX idx_api_token_actor ON api_token(actor_id);

-- ---------------------------------------------------------------------------
-- entry — the stable identity of a piece of knowledge. Holds two MOVING refs
-- (curated head, best provisional) plus a DENORMALIZED copy of the curated head's
-- ranking/browse fields (refreshed on promotion) so list/filter views never join
-- version content. Content itself lives in immutable entry_version rows.
-- ---------------------------------------------------------------------------
CREATE TABLE entry (
  id                     INTEGER PRIMARY KEY,
  slug                   TEXT NOT NULL UNIQUE,   -- semantic id exposed to the AI, e.g. 'docker-copy-manifests-before-source'
  kind                   TEXT NOT NULL CHECK (kind IN ('user','feedback','project','reference')),
  space_id               INTEGER NOT NULL DEFAULT 1 REFERENCES space(id),

  -- denormalized from the CURATED head (kept in sync on promotion):
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
CREATE INDEX idx_entry_lifecycle ON entry(lifecycle);
CREATE INDEX idx_entry_staleness ON entry(staleness);
CREATE INDEX idx_entry_kind      ON entry(kind);
CREATE INDEX idx_entry_space     ON entry(space_id);
CREATE INDEX idx_entry_category  ON entry(category);

-- ---------------------------------------------------------------------------
-- entry_version — an IMMUTABLE content blob (a "git blob"). Enhancements APPEND
-- a new row; they never update content. This is the load-bearing decision that
-- resolves curated-vs-always-enhanceable: knowledge is persisted the instant it
-- is proposed, and only human promotion moves entry.curated_version_id.
-- A small set of STATUS columns (state, superseded_by, review/verify stamps) is
-- mutable after insert; the content columns are frozen (enforced by trigger).
-- ---------------------------------------------------------------------------
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

  created_at        TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  UNIQUE (entry_id, rev_no)
);
CREATE INDEX idx_ev_entry  ON entry_version(entry_id);
CREATE INDEX idx_ev_state  ON entry_version(state);
CREATE INDEX idx_ev_author ON entry_version(author_actor_id);

-- Enforce content immutability: allow updates ONLY to the mutable status columns.
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
    OR NEW.created_at        IS NOT OLD.created_at )
BEGIN
  SELECT RAISE(ABORT, 'entry_version content is immutable — append a new revision instead of updating');
END;

-- ---------------------------------------------------------------------------
-- curation_event — append-only reflog. The in-app changelog (replaces the git
-- mirror's commit log under the SQLite-only decision, DESIGN §2 D5).
-- ---------------------------------------------------------------------------
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
CREATE INDEX idx_ce_entry   ON curation_event(entry_id);
CREATE INDEX idx_ce_created ON curation_event(created_at);

-- ---------------------------------------------------------------------------
-- entry_link — resolves [[wikilinks]]. Links may DANGLE (target not yet created),
-- exactly like the flat-file memory system: to_slug is authoritative, to_entry_id
-- is resolved when/if the target appears (see trigger entry_resolve_links).
-- ---------------------------------------------------------------------------
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
CREATE INDEX idx_link_to_slug ON entry_link(to_slug);
CREATE INDEX idx_link_to_id   ON entry_link(to_entry_id);

CREATE TRIGGER entry_resolve_links
AFTER INSERT ON entry
FOR EACH ROW
BEGIN
  UPDATE entry_link SET to_entry_id = NEW.id
   WHERE to_slug = NEW.slug AND to_entry_id IS NULL;
END;

-- ---------------------------------------------------------------------------
-- Search mirrors — one row PER VERSION (keyed by rowid = entry_version.id) so any
-- revision is independently rankable; the query filters state at read time. Kept
-- in sync by triggers on entry_version (content is frozen, so INSERT/DELETE only).
--
-- Prose tokenizer note (bilingual): 'porter' stems English well; if Spanish
-- content dominates, drop 'porter' and keep 'unicode61 remove_diacritics 2'.
-- BM25 column weights are applied at query time: bm25(entry_fts,10,8,8,5,3,2,1,1).
-- ---------------------------------------------------------------------------
CREATE VIRTUAL TABLE entry_fts USING fts5(
  title, summary, triggers, tags, problem, solution, rationale, caveats,
  tokenize = 'porter unicode61 remove_diacritics 2'
);

-- Code/identifier substring search (LASTVAL, busy_timeout, GeneratedKeyHolder …).
CREATE VIRTUAL TABLE entry_code_fts USING fts5(
  code,
  tokenize = 'trigram'
);

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

CREATE TRIGGER entry_version_ad_fts
AFTER DELETE ON entry_version
FOR EACH ROW
BEGIN
  DELETE FROM entry_fts      WHERE rowid = OLD.id;
  DELETE FROM entry_code_fts WHERE rowid = OLD.id;
END;

INSERT INTO schema_migration(version) VALUES (1);

COMMIT;
