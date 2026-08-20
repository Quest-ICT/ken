-- ============================================================================
-- Ken COMM — migration 0016: index `message.sender_party`, which every poll scans
-- ============================================================================
-- `NoticesFor` runs on EVERY comm_poll and filters `WHERE m.sender_party = ?1`
-- (internal/comm/notice.go). There has never been an index on that column, so SQLite
-- answers it with a full table scan. Measured with EXPLAIN QUERY PLAN before this
-- migration: `SCAN m`.
--
-- WHAT THAT COSTS, AND WHY IT IS THE WRONG SHAPE RATHER THAN MERELY SLOW. The scan is
-- over the whole `message` table, so a caller's poll gets slower as OTHER sessions
-- accumulate history. A quiet session pays for a noisy deployment. Timed at 0.511 ->
-- 7.668 -> 37.710 ms per call at 1k -> 20k -> 100k total messages with the caller's own
-- inbox held constant at 5 AND NO NOTICES RETURNED — i.e. the cost is paid in full to
-- discover there is nothing to report.
--
-- This is the same coupling a 2026-08-03 task recorded against `Poll`, which was fixed
-- by giving Poll a recipient-scoped index. The fix moved the cost rather than removing
-- it: notices were derived at poll time in 3.4.0, and the new query inherited the old
-- shape. Worth naming, because "we fixed that" was true and the symptom came back.
--
-- WHY (sender_party, kind) AND NOT sender_party ALONE. Both call sites pair the sender
-- with `m.kind = 'message'` (notice.go), and `kind` is low-cardinality — 'message' is
-- effectively everything, with 'status' surviving only as pre-3.4.0 rows. Leading with
-- the selective column and carrying `kind` lets both predicates be satisfied from the
-- index without visiting the row. The remaining clauses are EXISTS subqueries over
-- `delivery`, which have their own indexes.
--
-- ADDITIVE, AND ROLLBACK-SAFE. An index is not data: nothing is rewritten, no row moves,
-- and an older binary rolled back over this migration simply never uses it. That is the
-- property Rule 4 protects — "a rollback must never discard behaviour fixes along with a
-- data rewrite" — and there is no data rewrite here. It still SHIPS ALONE, because the
-- rule is about what a release contains, not about how dangerous this particular
-- statement is.
--
-- COST OF THE INDEX ITSELF, stated rather than assumed: one B-tree over a TEXT column on
-- a table that already carries five indexes. Writes take one more update per insert;
-- `message` is written once per send and never updated in place on this column, so the
-- write cost is paid once per message and the read benefit is paid on every poll by
-- every session.

BEGIN;

CREATE INDEX idx_message_sender ON message(sender_party, kind);

INSERT INTO schema_migration(version) VALUES (16);

COMMIT;
