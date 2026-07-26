-- ============================================================================
-- Ken — migration 0003: web sessions (the human curator UI)
-- ============================================================================
-- Server-side sessions for the human web login. The session id is a
-- high-entropy random token carried in the __Host-ken_sess cookie; each session
-- carries its own CSRF token (rotated on login). Sessions cascade-delete with
-- their actor.
-- ============================================================================

BEGIN;

CREATE TABLE web_session (
  id           TEXT PRIMARY KEY,               -- high-entropy cookie value
  actor_id     INTEGER NOT NULL REFERENCES actor(id) ON DELETE CASCADE,
  csrf_token   TEXT NOT NULL,
  created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  expires_at   TEXT NOT NULL,
  last_seen_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX idx_web_session_actor   ON web_session(actor_id);
CREATE INDEX idx_web_session_expires ON web_session(expires_at);

INSERT INTO schema_migration(version) VALUES (3);

COMMIT;
