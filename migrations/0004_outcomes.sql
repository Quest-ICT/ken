-- ============================================================================
-- Ken — migration 0004: outcome feedback (self-curating signal)
-- ============================================================================
-- Agents report whether a fetched entry actually helped (kb_record_outcome).
-- 'was-wrong' also flags the entry stale for human review. Over time these feed
-- maturity and surface entries that need attention.
-- ============================================================================
BEGIN;

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
CREATE INDEX idx_outcome_entry ON entry_outcome(entry_id);

INSERT INTO schema_migration(version) VALUES (4);

COMMIT;
