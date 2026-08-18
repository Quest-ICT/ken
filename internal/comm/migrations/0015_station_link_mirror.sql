-- ============================================================================
-- Ken COMM — migration 0015: mirror the station links, so a pair send can
--                            authorise itself inside its own transaction
-- ============================================================================
-- Batch 6 item P2: `comm_send{to_station:"X"}`. A message addressed to a station
-- lives in a PAIR scope (`p:<a>|<b>`, ids sorted) and is authorised by the
-- `station_link` a human approved — which lives in ken.db.
--
-- WHY A MIRROR AND NOT A CROSS-DATABASE READ. `comm.Store` has no ken.db handle by
-- construction: the two databases are opened separately, version separately, and one
-- may be absent. This is the same shape and the same reasoning as `room_member_mirror`
-- (0010), and the rule that keeps it honest is the same one stated in
-- internal/comm/room_mirror.go: A MIRROR MAY BE STALE, NEVER AUTHORITATIVE. Nothing in
-- comm.db decides who may talk to whom; it only copies the decision. Lose comm.db and
-- every link is still in ken.db, and the next rebuild restores this table exactly.
--
-- WHY NOT AUTHORISE FROM THE CHANNEL ROW INSTEAD. 0008 already snapshots the
-- authorising pair on `channel`, and P3 materialises a channel when a link is approved,
-- so the pair is often already here. It is the wrong authority anyway, for a reason P3
-- wrote down when it shipped: BOTH STATIONS MUST BE STAFFED for that channel to exist,
-- and one may not be — `proxmox-servers` held a station and no endpoint for five days.
-- Authorising off the channel would mean a human approves a link, neither side is
-- connected at that instant, and the permission they granted silently does not exist.
-- The link is the decision; the channel is one way of spending it.
--
-- WHY THE PAIR IS NOT A MEMBERSHIP TABLE. A pair scope's members are its NAME — both
-- station ids are in the scope string — so `membersOfScope` needs no lookup to know who
-- a message goes to. What it cannot know from the string alone is whether the two are
-- permitted to talk, which is exactly and only what this table answers.
--
-- ORDERING IS ENFORCED, NOT ASSUMED. `station_link` in ken.db carries
-- CHECK (station_a < station_b) and every read there goes through orderPair. Repeating
-- the CHECK here means a mirror written in the wrong order fails loudly at the INSERT
-- rather than producing a pair that authorises in one direction and not the other —
-- the asymmetry would look exactly like a permissions bug on whichever side asked
-- second.
--
-- ADDITIVE AND ROLLBACK-SAFE. No existing table is touched and no row is rewritten, so
-- an older binary rolled back over this migration ignores an empty table and loses
-- nothing. That is the property FINISHING.md's Rule 4 exists to protect ("a rollback
-- must never discard behaviour fixes along with a data rewrite"); there is no data
-- rewrite here.

BEGIN;

CREATE TABLE station_link_mirror (
  station_a TEXT NOT NULL,
  station_b TEXT NOT NULL,
  PRIMARY KEY (station_a, station_b),
  CHECK (station_a < station_b)
) WITHOUT ROWID;

INSERT INTO schema_migration(version) VALUES (15);

COMMIT;
