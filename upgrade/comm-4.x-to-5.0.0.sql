-- Ken comm.db: 4.x (schema 21) -> 5.0.0 (schema 22)
--
-- RUN THIS YOURSELF, WITH stock sqlite3, WHILE KEN IS STOPPED. Ken does not migrate databases:
-- it creates one from a single schema file and otherwise checks the version and refuses if it
-- does not match. See docs/UPGRADING-THE-DATABASE.md.
--
--     sudo systemctl stop ken
--     sqlite3 /opt/ken/data/comm/comm.db < comm-4.x-to-5.0.0.sql
--     sudo systemctl restart ken
--
-- RESTART, NEVER START. ken-upgrade starts the service as its last step, so `start` is a no-op on
-- a running unit and the process keeps the pre-script database open: unit active, /healthz ok,
-- messaging entirely absent. Measured on a real deployment upgrading to 5.0.1.
--
-- `sqlite3` MAY NOT BE INSTALLED — Rocky 10 does not ship it. docs/UPGRADING-THE-DATABASE.md has a
-- python3 fallback; it needs isolation_level=None so the BEGIN/COMMIT below is not nested inside
-- Python's implicit transaction.
--
-- comm.db is EXPENDABLE BY DESIGN and is in no backup tier. If anything here fails, the supported
-- recovery is to stop Ken, delete comm.db and its -wal/-shm, and start: messaging rebuilds empty,
-- and the knowledge base and stations are untouched. That is a real option here and it is not one
-- for ken.db — do not carry the habit across.

PRAGMA foreign_keys=OFF;

BEGIN;

-- 1. THE CHANNEL IS RETIRED (slice 7). Its indexes go first: SQLite will not drop a column an
--    index names, and will not drop a table a foreign key still points at.
DROP INDEX IF EXISTS idx_channel_a;
DROP INDEX IF EXISTS idx_channel_b;
DROP INDEX IF EXISTS idx_channel_station_a;
DROP INDEX IF EXISTS idx_channel_station_b;
DROP INDEX IF EXISTS idx_attachment_idem;

ALTER TABLE message    DROP COLUMN channel_id;
ALTER TABLE attachment DROP COLUMN channel_id;
DROP TABLE IF EXISTS channel;

-- 2. FILE-OFFER IDEMPOTENCY WAS UNENFORCED, AND THIS IS THE FIX.
--
--    idx_attachment_idem was keyed on (channel_id, sender_endpoint, idempotency_key) while the
--    lookup has been keyed on scope_id since comm 0017. SQLite treats NULLs as DISTINCT, so every
--    room and pair offer — which carried a NULL channel_id — was outside the unique index
--    entirely: a repeated idempotency_key created a second attachment instead of returning the
--    first.
--
--    THE DE-DUP RUNS FIRST AND IS NOT OPTIONAL. Building the correct index over existing
--    duplicates aborts, and an aborted upgrade here leaves the index absent — which looks exactly
--    like the state you are fixing. Keeping the LOWEST id keeps the offer a caller was told about.
DELETE FROM attachment
 WHERE idempotency_key IS NOT NULL
   AND id NOT IN (SELECT MIN(id) FROM attachment
                   WHERE idempotency_key IS NOT NULL
                   GROUP BY scope_id, sender_endpoint, idempotency_key);

CREATE UNIQUE INDEX idx_attachment_idem
  ON attachment(scope_id, sender_endpoint, idempotency_key)
  WHERE idempotency_key IS NOT NULL;

-- 3. THE MAILBOX HAS NO SECRET AND NOTHING BINDS IT.
--
--    secret_sha256 was NOT NULL, so creating a mailbox minted a random secret and hashed it purely
--    to satisfy the constraint — nothing has verified one since 4.0.0. secret_rotated_at and
--    rotate_count were console display for a rotation that no longer exists, and
--    bound_by_station_key_id named the station key that authorised a binding, when there are no
--    station keys and nothing binds.
DROP INDEX IF EXISTS idx_endpoint_bound_by;

ALTER TABLE endpoint DROP COLUMN secret_sha256;
ALTER TABLE endpoint DROP COLUMN secret_rotated_at;
ALTER TABLE endpoint DROP COLUMN rotate_count;
ALTER TABLE endpoint DROP COLUMN bound_by_station_key_id;

-- 4. THE VERSION KEN WILL CHECK FOR. It refuses to start against any other number.
INSERT INTO schema_migration(version) VALUES (22);

COMMIT;

PRAGMA foreign_keys=ON;

-- 5. PROVE IT, rather than assuming the script ran clean. Both must answer '22' and 'ok', and
--    foreign_key_check must return NOTHING at all.
--        SELECT MAX(version) FROM schema_migration;
--        PRAGMA integrity_check;
--        PRAGMA foreign_key_check;
