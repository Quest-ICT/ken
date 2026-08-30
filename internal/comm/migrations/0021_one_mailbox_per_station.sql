-- ============================================================================
-- COMM migration 0021: one LIVE mailbox per station, enforced rather than assumed
-- ============================================================================
-- *** MailboxFor RACES ITSELF, AND TWO OTHER FILES ALREADY CLAIM IT CANNOT. ***
--
-- MailboxFor is get-or-create by station, guarded with `ON CONFLICT DO NOTHING`. There was nothing
-- to conflict WITH — `idx_endpoint_station` (migration 0006) is a plain index — so two comm calls
-- arriving together both looked, both found nothing, and both inserted. Reproduced by the 4.0.0
-- pre-release audit: 98 of 100 attempts at zero stagger, none at 1 ms.
--
-- The consequence today is cosmetic: readers resolve with `ORDER BY id LIMIT 1`, so every caller
-- consistently sees the older row. What is NOT cosmetic is that the invariant is load-bearing and
-- stated as fact by the code depending on it — channel.go's self-peer guard reasons that a station
-- has exactly one mailbox, and file_test.go asserts a successor resolves to the same row, guarding a
-- stranding that returns the moment a station has two.
--
-- *** THE FIRST VERSION OF THIS MIGRATION BROKE REAL UPGRADED DATABASES IN THREE WAYS. ***
-- Found by the post-fix audit, reproduced against comm.db files built by the real v3.42.0 binary.
-- All three came from the same mistake — re-pointing rows by STATION instead of by the specific
-- rows about to be deleted:
--
--   1. NINE COLUMNS REFERENCE endpoint(id); it re-pointed four. channel.endpoint_a/_b,
--      transfer_grant.endpoint_id and delivery.claimed_by_endpoint/acked_by_endpoint were left
--      dangling by the DELETE — foreign keys are OFF for the run, so no CASCADE fires — and the
--      post-migration foreign_key_check failed the boot. Not rare: before 4.0.0 a station
--      accumulated one endpoint PER SESSION.
--   2. A STATION WHOSE ONLY ENDPOINTS ARE REVOKED aborted it permanently. The re-point targeted
--      `MIN(id) WHERE revoked_at IS NULL`, which is NULL when every row is revoked, against a
--      NOT NULL column: "NOT NULL constraint failed: message.sender_endpoint", rolled back, stuck.
--   3. ROWS POINTING AT A REVOKED ENDPOINT WERE SILENTLY RE-ATTRIBUTED to the live mailbox even
--      where no duplicate existed — message provenance rewritten by a migration whose header said
--      it changed nothing any caller could observe.
--
-- The fix is to name the losers explicitly, once, and drive everything off that list: only rows
-- referencing a row that is ABOUT TO BE DELETED are touched, and a revoked endpoint is never a
-- loser because it is never deleted.
--
-- PARTIAL ON TWO CONDITIONS. `station_id IS NOT NULL` because an unbound endpoint has none and a
-- full unique index would collide on every NULL (migration 0019 gives the same reasoning for
-- session_key). `revoked_at IS NULL` because that is the predicate mailboxByStation itself filters
-- on: the invariant readers depend on is "one LIVE mailbox per station", and an index stricter than
-- the query it protects would stop a station ever getting a mailbox again after one was revoked.

BEGIN;

-- The losers, named once. A loser is a LIVE mailbox that is not the lowest-numbered live mailbox of
-- its station — which is precisely the row every reader already resolves to, so collapsing onto it
-- is invisible to callers. Revoked rows are never losers and are never deleted.
CREATE TABLE _mailbox_merge AS
SELECT e.id AS loser,
       (SELECT MIN(e2.id) FROM endpoint e2
         WHERE e2.station_id = e.station_id AND e2.revoked_at IS NULL) AS winner
  FROM endpoint e
 WHERE e.station_id IS NOT NULL
   AND e.revoked_at IS NULL
   AND e.id > (SELECT MIN(e2.id) FROM endpoint e2
                WHERE e2.station_id = e.station_id AND e2.revoked_at IS NULL);

-- A CHANNEL WHOSE TWO SEATS COLLAPSE ONTO ONE ROW cannot survive: the schema forbids it
-- (CHECK endpoint_b <> endpoint_a), and it is degenerate anyway — a station paired with itself,
-- where every message sent came back to the sender. Revoked rather than deleted, so its messages
-- and attachments (both ON DELETE CASCADE) are kept and the operator can still see it happened.
UPDATE channel SET state = 'revoked',
       revoked_at = COALESCE(revoked_at, strftime('%Y-%m-%dT%H:%M:%fZ','now'))
 WHERE endpoint_b IS NOT NULL
   AND COALESCE((SELECT winner FROM _mailbox_merge WHERE loser = channel.endpoint_a), channel.endpoint_a)
     = COALESCE((SELECT winner FROM _mailbox_merge WHERE loser = channel.endpoint_b), channel.endpoint_b);

DELETE FROM channel
 WHERE endpoint_b IS NOT NULL
   AND COALESCE((SELECT winner FROM _mailbox_merge WHERE loser = channel.endpoint_a), channel.endpoint_a)
     = COALESCE((SELECT winner FROM _mailbox_merge WHERE loser = channel.endpoint_b), channel.endpoint_b);

-- All NINE columns that reference endpoint(id), each re-pointed only where it names a loser.
UPDATE message SET sender_endpoint = (SELECT winner FROM _mailbox_merge WHERE loser = message.sender_endpoint)
 WHERE sender_endpoint IN (SELECT loser FROM _mailbox_merge);

UPDATE delivery SET recipient_endpoint = (SELECT winner FROM _mailbox_merge WHERE loser = delivery.recipient_endpoint)
 WHERE recipient_endpoint IN (SELECT loser FROM _mailbox_merge);
UPDATE delivery SET claimed_by_endpoint = (SELECT winner FROM _mailbox_merge WHERE loser = delivery.claimed_by_endpoint)
 WHERE claimed_by_endpoint IN (SELECT loser FROM _mailbox_merge);
UPDATE delivery SET acked_by_endpoint = (SELECT winner FROM _mailbox_merge WHERE loser = delivery.acked_by_endpoint)
 WHERE acked_by_endpoint IN (SELECT loser FROM _mailbox_merge);

UPDATE attachment SET sender_endpoint = (SELECT winner FROM _mailbox_merge WHERE loser = attachment.sender_endpoint)
 WHERE sender_endpoint IN (SELECT loser FROM _mailbox_merge);
UPDATE attachment SET recipient_endpoint = (SELECT winner FROM _mailbox_merge WHERE loser = attachment.recipient_endpoint)
 WHERE recipient_endpoint IN (SELECT loser FROM _mailbox_merge);

UPDATE transfer_grant SET endpoint_id = (SELECT winner FROM _mailbox_merge WHERE loser = transfer_grant.endpoint_id)
 WHERE endpoint_id IN (SELECT loser FROM _mailbox_merge);

UPDATE channel SET endpoint_a = (SELECT winner FROM _mailbox_merge WHERE loser = channel.endpoint_a)
 WHERE endpoint_a IN (SELECT loser FROM _mailbox_merge);
UPDATE channel SET endpoint_b = (SELECT winner FROM _mailbox_merge WHERE loser = channel.endpoint_b)
 WHERE endpoint_b IN (SELECT loser FROM _mailbox_merge);

DELETE FROM endpoint WHERE id IN (SELECT loser FROM _mailbox_merge);
DROP TABLE _mailbox_merge;

DROP INDEX IF EXISTS idx_endpoint_station;
CREATE UNIQUE INDEX idx_endpoint_station ON endpoint(station_id)
  WHERE station_id IS NOT NULL AND revoked_at IS NULL;

INSERT INTO schema_migration(version) VALUES (21);

COMMIT;
