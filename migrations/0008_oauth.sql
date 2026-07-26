-- ============================================================================
-- Ken — migration 0008: optional OAuth 2.1 authorization server
-- ============================================================================
-- Powers the claude.ai remote-MCP "custom connector" flow (OAuth-only on
-- personal accounts). The AS issues short-lived opaque access tokens + rotating
-- refresh tokens bound to a human-approved GRANT; an agent connecting this way
-- gets the same capability set as a CLI agent token (read | write-draft |
-- propose — NEVER curate). Entirely inert unless KEN_OAUTH_ENABLED is set: these
-- tables just stay empty.
--
-- Conventions match 0001 (SQLite/WAL): INTEGER PK rowid, ISO-8601 UTC TEXT
-- timestamps, CHECK enums, json_valid JSON columns. Only SHA-256 of any secret
-- (codes, tokens) is ever stored — never the plaintext.
-- ============================================================================

BEGIN;

-- A registered OAuth client. claude.ai self-registers via RFC 7591 Dynamic
-- Client Registration on every fresh connection; it is a PUBLIC client (PKCE, no
-- secret), so none is stored. redirect_uris is the exact-match allowlist the
-- authorize + token endpoints validate against (the open-redirect guard).
CREATE TABLE oauth_client (
  client_id     TEXT PRIMARY KEY,                 -- high-entropy public id we mint
  client_name   TEXT,
  redirect_uris TEXT NOT NULL CHECK (json_valid(redirect_uris)),  -- JSON array
  created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

-- A durable authorization a human granted to a client. The connector's own 'ai'
-- actor (actor_id) authors MCP writes; human_actor_id records who consented.
-- Revoking a grant (revoked_at) instantly kills all its tokens — every MCP call
-- re-checks the grant is live, so revocation latency is independent of token TTL.
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
CREATE INDEX idx_oauth_grant_client ON oauth_grant(client_id);
CREATE INDEX idx_oauth_grant_live   ON oauth_grant(revoked_at);

-- Single-use PKCE authorization code. Deleted the instant it is exchanged (the
-- atomic DELETE row-count is the double-spend guard); short expiry as a backstop.
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

-- Access + refresh tokens (opaque; only SHA-256 stored). Access: short TTL.
-- Refresh: rotated on every use — a re-presented (already-rotated/revoked)
-- refresh token is a theft signal that revokes the whole grant.
CREATE TABLE oauth_token (
  token_sha256 TEXT PRIMARY KEY,
  grant_id     INTEGER NOT NULL REFERENCES oauth_grant(id) ON DELETE CASCADE,
  kind         TEXT    NOT NULL CHECK (kind IN ('access','refresh')),
  expires_at   TEXT    NOT NULL,
  created_at   TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  revoked_at   TEXT
);
CREATE INDEX idx_oauth_token_grant ON oauth_token(grant_id);

INSERT INTO schema_migration(version) VALUES (8);

COMMIT;
