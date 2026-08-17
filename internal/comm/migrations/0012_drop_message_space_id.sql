-- ============================================================================
-- Ken COMM — migration 0012: drop `message.space_id`, which never held anything
-- ============================================================================
-- Batch 4 of docs/FINISHING.md: retire the duplicated generation.
--
-- 0009 declared `space_id INTEGER NOT NULL DEFAULT 1` on the rebuilt `message` table
-- and nothing has written or read it since. The claim is stronger than "unused": the
-- PRE-0009 message table had no such column, so 0009's own backfill could not carry a
-- value into it and does not list it. Every row on every deployment holds the literal
-- default 1. There is nothing to preserve and nothing to migrate.
--
-- WHY IT WAS ADDED AND WHY IT IS NOT NEEDED. A message's space is reachable through
-- `sender_endpoint -> endpoint.space_id`, which is the join the console counters
-- already use, and `internal/comm/admin.go` explains why that join is the right one:
-- it works for EVERY scope — channel, room and broadcast — without a schema change,
-- whereas a column on `message` only ever answered for the channel shape. The one
-- assumption is the one that file already names: nothing moves an endpoint between
-- spaces.
--
-- WHY THIS IS A NEW FILE RATHER THAN AN EDIT TO 0009. SQLite stores a table's CREATE
-- statement verbatim in sqlite_master, comments included. Editing an applied migration
-- makes a fresh install's stored schema differ from an upgraded deployment's while
-- changing nothing about either — the exact drift a schema band exists to catch. The
-- same reasoning is why migration 0017's prose was left alone when it was found to be
-- misleading.
--
-- REVERSIBILITY, STATED PLAINLY. Dropping a column cannot be undone by reverting the
-- binary. That is acceptable here only because no binary in this project's history
-- ever referenced the column, so a rollback to any earlier release still runs. If that
-- ever stops being true, this migration cannot be the one to find out.

BEGIN;

ALTER TABLE message DROP COLUMN space_id;

INSERT INTO schema_migration(version) VALUES (12);

COMMIT;
