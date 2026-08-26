-- ============================================================================
-- Ken — migration 0022: `station_vault_read.via` learns a third value, 'transfer'
-- ============================================================================
-- SHIPS ALONE WITH ITS CODE under Rule 4, in the same release as
-- `station_vault_send` (internal/store/station_vault.go). The two cannot be separated: the
-- code writes a `via` value this CHECK constraint refuses, so an older schema under a newer
-- binary fails every transfer at the SQL layer, and a newer schema under an older binary is
-- simply unused. Same shape as 0017_attachment_scope.
--
-- WHAT CHANGES: the constraint `CHECK (via IN ('station','console'))` becomes
-- `CHECK (via IN ('station','console','transfer'))`. Nothing else — same columns, same types,
-- same index, every existing row copied verbatim.
--
-- *** WHY A THIRD VALUE RATHER THAN REUSING 'station'. *** The read log answers exactly one
-- question, and it is the only question worth asking after something leaks: WHO SAW THIS SECRET.
-- A session reading its own credential and a session handing that credential to another station
-- are materially different events, and filing the second as the first would make the log say a
-- secret was read when it was actually COPIED SOMEWHERE ELSE. The whole point of auditing reads
-- is undone by a category that quietly absorbs the more serious case.
--
-- WHY A TABLE REBUILD: SQLite cannot alter a CHECK constraint in place. The table is an
-- append-only audit log with no dependents — no FK points AT it, no trigger reads it, no view
-- names it — so the copy is a plain INSERT ... SELECT and the row count is the whole
-- verification. Confirmed against a migrated database before this was written: the only objects
-- in sqlite_master naming `station_vault_read` are the table and its own index.
--
-- FOREIGN KEYS ARE HANDLED BY THE RUNNER, NOT BY THIS FILE. `internal/dbmigrate` disables
-- enforcement on a connection pinned for the whole run and proves the result with
-- `PRAGMA foreign_key_check` at the end. This migration deliberately contains NO pragma of its
-- own, for two reasons the runner spells out: inside BEGIN/COMMIT the pragma is a documented
-- NO-OP (measured against this driver: parent rebuilt, 2 child rows inserted, 0 afterwards), and
-- re-enabling it after COMMIT would switch enforcement back on for every LATER migration in the
-- same run — turning this file into a trap for a rebuild that has not been written yet.
--
-- ROW COUNT IS THE ASSERTION. If this migration ever silently dropped rows it would be
-- destroying the only record of who read a credential, which is the one thing in this schema
-- that cannot be reconstructed from anywhere else. On the deployment this was written for the
-- table holds ZERO rows, so today it is a no-op — and that is precisely why it is being done
-- now rather than after the log matters.

BEGIN;

CREATE TABLE station_vault_read_new (
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

INSERT INTO station_vault_read_new (id, station_id, name, via, read_at, by_token_id, by_actor_id)
SELECT id, station_id, name, via, read_at, by_token_id, by_actor_id FROM station_vault_read;

DROP TABLE station_vault_read;

ALTER TABLE station_vault_read_new RENAME TO station_vault_read;

-- Recreated by hand: the index does not follow a RENAME onto the new table under the name the
-- old one used, and a read log without its (station, time) index turns the console's audit panel
-- into a full scan the day it finally has rows.
CREATE INDEX idx_station_vault_read ON station_vault_read(station_id, read_at DESC);

INSERT INTO schema_migration(version) VALUES (22);

COMMIT;
