-- ============================================================================
-- Ken COMM — migration 0018: `space_id` goes from comm.db too
-- ============================================================================
-- The comm.db half of IDENTITY.md §10 step 5. SHIPS ALONE under Rule 4, together with
-- ken.db's 0021 and the code for both — the two databases are migrated by one runner in
-- one release, and a binary that has dropped the column in one and not the other would
-- write predicates against a column that exists on only one side.
--
-- WHAT GOES: `space_id` from `channel`, `endpoint` and `pairing_code`.
--
-- *** THERE ARE THREE TABLES HERE, NOT FOUR, AND THAT IS WORTH RECORDING. *** A survey
-- of the MIGRATION TEXT for this change reported four — it counted `message_new`, which
-- is a transitional table an earlier migration created, copied into and renamed. It has
-- never existed in a live schema. Reading the migrations told us about a table that was
-- real for the duration of one transaction years ago; reading `sqlite_master` on a
-- migrated database told us what is actually there. The second is the only one that
-- counts, and the gap between the two is why this file's numbers came from a database.
--
-- NO FOREIGN KEYS TO LOSE, BY DESIGN. These columns were always documented in the schema
-- as "ken.db space.id (no FK: other db)" — a cross-database pointer SQLite cannot enforce
-- and never did. So there is no constraint to drop here and no `space` table on this side;
-- comm.db has been carrying a copy of a constant that pointed at another file's row.
--
-- ONE INDEX. `idx_endpoint_owner(space_id, actor_id)` led with the constant, so every
-- lookup by owner has been paying for a first column that never discriminated. It comes
-- down before the column drops and goes back up as `(actor_id)` — the same lookup,
-- one column narrower.
--
-- VERIFIED BEFORE IT WAS WRITTEN, on a freshly migrated comm.db copy with
-- `PRAGMA foreign_keys=ON`: all statements OK, `PRAGMA integrity_check` = ok.

BEGIN;

DROP INDEX IF EXISTS idx_endpoint_owner;

ALTER TABLE channel      DROP COLUMN space_id;
ALTER TABLE endpoint     DROP COLUMN space_id;
ALTER TABLE pairing_code DROP COLUMN space_id;

CREATE INDEX idx_endpoint_owner ON endpoint(actor_id);

INSERT INTO schema_migration(version) VALUES (18);

COMMIT;
