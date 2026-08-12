-- ============================================================================
-- Ken — migration 0018: the hearsay marker learns DIRECTED from BROADCAST
-- ============================================================================
-- `via_comm` is a boolean: this entry was written while its actor had recently
-- received inter-session traffic. That was a reasonable signal when every message had
-- exactly one recipient, because "you received something" meant somebody addressed YOU.
--
-- Rooms and broadcast break it. One send to a nine-station room marks NINE actors, so
-- the marker fires far more often while meaning far less. ken-prod-ops had already
-- measured the badge as nearly always on before rooms existed — three sessions
-- exchanging eleven messages in a day kept the window continuously open — and observed
-- the consequence precisely: "a badge that is almost always present carries less
-- information than one that is sometimes absent — the failure mode is a curator
-- learning to ignore it."
--
-- So the marker keeps its boolean and gains a KIND. A curator reading a proposal can
-- now tell two different claims apart:
--
--   directed   somebody sent this actor a message, to it specifically
--   broadcast  this actor was one of several recipients of a room message
--
-- Ranked, not merged: the store returns directed sources first, because the strongest
-- reason to look twice belongs at the top.
--
-- WHY A NEW COLUMN RATHER THAN WIDENING via_comm: its CHECK is
-- `via_comm IS NULL OR via_comm = 1`, and SQLite cannot alter a CHECK in place. A
-- rebuild of entry_version — the table holding every revision of every entry — to widen
-- one boolean would be the most expensive migration in the project for the least
-- reason. The boolean also still means exactly what it meant, so every existing reader
-- keeps working and nothing has to be backfilled.
--
-- NULL for every row written before this ships, and NULL is honest: those entries were
-- marked without the distinction being recorded, and inventing one now would be
-- fabricating provenance, which is the one thing this whole mechanism exists to avoid.
BEGIN;

ALTER TABLE entry_version ADD COLUMN via_comm_kind TEXT
  CHECK (via_comm_kind IS NULL OR via_comm_kind IN ('directed','broadcast'));

INSERT INTO schema_migration(version) VALUES (18);

COMMIT;
