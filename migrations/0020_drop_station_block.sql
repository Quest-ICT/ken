-- ============================================================================
-- Ken — migration 0020: drop `station_block`, a deny that never denied anything
-- ============================================================================
-- Vlad's decision, 2026-08-24. The three store functions were removed in aa09ce3; this
-- drops the table they wrote to. It ships ALONE under Rule 4 because it is a schema
-- change, and this one really is a data-destroying statement rather than an additive index.
--
-- WHAT IS BEING DROPPED, STATED HONESTLY. Not dead code — a SECURITY CONTROL THAT DID
-- NOTHING. It shipped in the schema of every deployment since 3.0.0 (2458b02), could be
-- written through an exported store method, and BUMPED THE ROSTER EPOCH so the write looked
-- consequential to every mirror consumer. And no send path ever consulted it. Measured
-- before the decision: zero references to `station_block` anywhere in `internal/comm/`,
-- against 6 for `station_link_mirror` and 11 for `room_member_mirror` on the identical
-- search. All four send entry points were read end to end.
--
-- IT WAS ALSO UNENFORCEABLE FROM WHERE THE SENDS HAPPEN, which is what turned a deferred
-- question into a decidable one. `comm.Store` holds handles to comm.db alone — no ken.db
-- handle — and comm.db has no block mirror, while links and rooms each have one. So "wire
-- it" was never the console-surface-plus-check it had been costed as; it is a
-- cross-database projection change.
--
-- AND THE OWNER'S OWN RULE HAD ALREADY ANSWERED IT, written in DECISIONS-BATCH5.md:176 and
-- unconnected to this item for a week: "If P2 is chosen with the link requirement intact,
-- the default is not permissive and station_block stays optional." P2 shipped with the link
-- requirement intact — pair_send.go calls areLinked and refuses with ErrNotLinked.
--
-- *** WHAT THIS COSTS, AND IT IS NOT NOTHING. *** The capability is NOT superseded by
-- anything that exists. Revoking a link kills only `to_station`; `roomMembers` and the
-- broadcast union carry no link predicate, and `AddRoomMember` requires no link to exist —
-- so two stations with no link at all still reach each other through any shared room and
-- through `to_room:"all"`. Archiving stops everything but retires the post entirely. To
-- stop ONE PAIR an operator must still remove a station from a room and cost it that room's
-- other relationships, which is precisely the "narrow something wide" the comment above
-- this table named. That gap is recorded in PARKING-LOT.md #25 with the shape a future fix
-- would take, so it outlives the code rather than disappearing with it.
--
-- SAFE TO DROP, established rather than assumed. Verified on the live ken.db when the
-- decision was framed: ZERO ROWS, and the only objects in `sqlite_master` naming it are the
-- table and its own index — no foreign key, trigger or view in either direction. `station`
-- and `actor` are referenced BY it and not the reverse, so dropping it removes two inbound
-- references and breaks nothing. The unique index goes with the table automatically; it is
-- dropped explicitly first anyway, because relying on an implicit cascade is how a rebuild
-- somewhere else inherits an orphan.
--
-- IRREVERSIBLE, AND THAT IS THE POINT OF SHIPPING IT ALONE. A rollback to an older binary
-- over this migration finds no table — but no code in any released version reads or writes
-- it either, so nothing observes the absence. The one thing a rollback must never do is
-- discard behaviour fixes along with a data rewrite, which is why 3.15.0 was cut first.

BEGIN;

DROP INDEX IF EXISTS idx_station_block_pair;
DROP TABLE IF EXISTS station_block;

INSERT INTO schema_migration(version) VALUES (20);

COMMIT;
