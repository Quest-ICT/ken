-- ============================================================================
-- Ken — migration 0021: `space_id` goes, and so does the `space` table
-- ============================================================================
-- IDENTITY.md §10 step 5, the last step of the identity work, deferred by Vlad on
-- 2026-08-25 and taken on 2026-08-26 once ken-prod-ops had verified 3.29.0 and 3.29.1.
-- SHIPS ALONE under Rule 4, with its comm.db counterpart (0018) and its code.
--
-- WHAT GOES: `space_id` from seven tables in this database — actor, entry, comm_room,
-- station, station_link, station_link_denial, station_request — and then the `space`
-- table itself, which after that has no referents.
--
-- *** WHY IT IS SAFE, MEASURED RATHER THAN ASSUMED. *** There has only ever been one
-- space. `0001_init.sql:44` inserts `space(1, 'personal')` and NOTHING anywhere else
-- inserts a space: no CreateSpace, no INSERT INTO space in any later migration, no
-- console route, no CLI subcommand, no MCP tool. Every `space_id` column is
-- `NOT NULL DEFAULT 1`, so every row in every deployment that has ever run Ken carries
-- the same value. The column has been a constant since the first release, written by
-- every insert and read by predicates that could not fail.
--
-- IDENTITY.md §9.1 is the decision this implements: "Nothing can create a second space,
-- and multi-user is now federation between instances." A column that can hold only one
-- value is not a tenancy model — it is the SHAPE of one, and keeping it makes the
-- remaining plumbing look intentional to whoever reads it next.
--
-- *** WHAT THIS COSTS, STATED PLAINLY. *** Ken can no longer become multi-tenant by
-- filling in a column. That door is deliberately closed: §9.1 says a second user is
-- another INSTANCE, federated over COMM, not another row-set behind a predicate. If that
-- decision is ever reversed, this is a real migration to write again — and it should be,
-- because the half-built version was never load-bearing and pretending otherwise is what
-- this whole document exists to stop.
--
-- *** AND THE TESTS GO IN THIS SAME COMMIT, DELIBERATELY. *** §9.1: "Whoever removes it
-- deletes that test in the same commit, deliberately and reviewably, and says why."
--
-- §9.1 NAMED ONE TEST. THERE WERE FOUR, PLUS A FIFTH ASSERTION INSIDE AN UNRELATED ONE.
-- A plan that names one instance is a sample, not an inventory — the same lesson this
-- project keeps relearning, here in the document that exists to record it. Found by
-- grepping for the concept rather than for the name §9.1 supplied:
--
--   TestPairingCodeIsSpaceScoped     (comm)   DELETED — its predicate is gone
--   TestListEndpointsIsScopedBySpace (comm)   DELETED — scoping it asserted is gone
--   TestStatsStillScopeToOneSpace    (comm)   DELETED — premise was a multi-space deployment
--   a space-isolation clause inside TestConsoleFingerprint     DELETED — the rest of the
--                                            test, that console changes move the number, stands
--   TestStationNameUniquePerSpace    (store)  KEPT and RENAMED to
--                                            TestStationNameIsUniqueAndCollisionIsNamed
--
-- The kept one matters: station-name uniqueness is REAL and survives, and
-- CreateStationAutoNamed's collision retry depends on ErrStationNameTaken — which is the
-- auto-provisioning path shipped in 3.26.0. Only its "another space may reuse it" clause
-- was a claim about a second space. Deleting it by keyword would have removed a live
-- control on a live feature.
--
-- They were REAL checks, not dead ones — that is why the rule exists. A refuter deleted
-- the three-line predicate in JoinChannel and CI went red in seconds. But the state they
-- asserted was unreachable, exercised only by fixtures that wrote a second space row by
-- hand. A control exercised solely by a fixture manufacturing its own precondition is a
-- control over a hypothetical. Removing the predicates first and the tests later would
-- leave live controls nothing exercises, which is exactly the condition this project keeps
-- finding and paying for. Both halves leave together.
--
-- INDEX REBUILDS, AND ONE OF THEM IS NOT WHAT IT LOOKS LIKE. Four indexes lead with
-- `space_id` and are recreated without it; SQLite cannot drop a column any index
-- references, so they come down first and go back up after. Note `idx_comm_room_name` is
-- a PARTIAL unique index (`WHERE kind='topic'`) — recreating it without the predicate
-- would silently widen a uniqueness constraint across every room kind, so the WHERE
-- clause is carried across verbatim. `idx_entry_space` is NOT recreated: it indexed
-- `space_id` alone, so with the column gone there is nothing left of it.
--
-- VERIFIED BEFORE IT WAS WRITTEN. Every statement below was run against a freshly
-- migrated ken.db copy with `PRAGMA foreign_keys=ON`: all statements OK,
-- `PRAGMA foreign_key_check` empty, `PRAGMA integrity_check` = ok. The two FK-bearing
-- columns (actor, entry -> space(id)) drop cleanly and take their constraints with them,
-- which is what lets `DROP TABLE space` succeed at the end rather than fail on a
-- surviving reference.

BEGIN;

DROP INDEX IF EXISTS idx_entry_space;
DROP INDEX IF EXISTS idx_station_name;
DROP INDEX IF EXISTS idx_station_state;
DROP INDEX IF EXISTS idx_station_request_pending;
DROP INDEX IF EXISTS idx_comm_room_name;

ALTER TABLE actor               DROP COLUMN space_id;
ALTER TABLE entry               DROP COLUMN space_id;
ALTER TABLE comm_room           DROP COLUMN space_id;
ALTER TABLE station             DROP COLUMN space_id;
ALTER TABLE station_link        DROP COLUMN space_id;
ALTER TABLE station_link_denial DROP COLUMN space_id;
ALTER TABLE station_request     DROP COLUMN space_id;

-- Recreated without the leading constant. Same columns, same uniqueness, same partial
-- predicate on comm_room — only the dead first column is gone.
CREATE UNIQUE INDEX idx_station_name           ON station(name);
CREATE        INDEX idx_station_state          ON station(state);
CREATE        INDEX idx_station_request_pending ON station_request(state, created_at);
CREATE UNIQUE INDEX idx_comm_room_name         ON comm_room(name) WHERE kind='topic';

-- Last, because until the columns above are gone this table still has referents.
DROP TABLE space;

INSERT INTO schema_migration(version) VALUES (21);

COMMIT;
