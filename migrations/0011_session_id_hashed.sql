-- Web session ids are now stored HASHED (SHA-256 hex of the cookie value), the way
-- api_token has always stored secret_sha256 — see internal/store/sessions.go.
--
-- The raw cookie is a bearer credential: presenting it IS being logged in. The database
-- is copied by design (every snapshot is a byte-complete copy, and KEN_BACKUP_GROUP now
-- lets a deliberately unprivileged account read snapshots), so a raw session id in a
-- backup handed its reader a live login for the remaining life of that session.
--
-- Existing rows CANNOT be migrated: the stored value is the credential itself, and the
-- hash of it is a different string, so a lookup would never match. They are deleted,
-- which logs every curator out exactly once — they sign in again and the new session is
-- stored hashed. Sessions are disposable by design; nothing else references them.
BEGIN;

DELETE FROM web_session;

INSERT INTO schema_migration(version) VALUES (11);

COMMIT;
