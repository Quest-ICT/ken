-- ============================================================================
-- COMM migration 0021: one mailbox per station, enforced rather than assumed
-- ============================================================================
-- *** MailboxFor RACES ITSELF, AND TWO COMMENTS ALREADY CLAIM IT CANNOT. ***
--
-- MailboxFor is get-or-create by station: it looks for the station's mailbox and inserts one when
-- there is none, with `ON CONFLICT DO NOTHING` as the guard. There was nothing to conflict WITH —
-- `idx_endpoint_station` (migration 0006) is a plain index, not a unique one — so two comm calls
-- arriving together both looked, both found nothing, and both inserted. Reproduced by the 4.0.0
-- pre-release audit: 98 of 100 attempts at zero stagger produced a duplicate; none at 1 ms.
--
-- THE CONSEQUENCE TODAY IS COSMETIC, WHICH IS EXACTLY WHY IT NEEDS THE INDEX. Readers resolve with
-- `ORDER BY id LIMIT 1`, so every caller consistently sees the older row and no mail goes missing.
-- What is NOT cosmetic is that the invariant is load-bearing elsewhere and stated as fact:
--
--   * internal/comm/channel.go's self-peer guard reasons that two endpoints of one station cannot
--     both take a seat, "because a station has exactly one mailbox now".
--   * internal/comm/file_test.go asserts a successor session resolves to the SAME row, and the
--     stranding it guards (an attachment freezes its recipient rowid at offer time) comes straight
--     back if a station ever acquires a second mailbox.
--
-- An invariant that the schema does not enforce, asserted in prose by the code that depends on it,
-- is the shape this project keeps paying for.
--
-- PARTIAL ON TWO CONDITIONS, and the second one matters as much as the first.
--
-- station_id is NULL for any endpoint not bound to a station, so a full unique index would collide
-- on every one of those NULLs — the same reasoning migration 0019 gives for session_key.
--
-- AND revoked_at MUST BE NULL, because that is the predicate mailboxByStation itself filters on.
-- The invariant readers depend on is "one LIVE mailbox per station", not "one row ever": a revoked
-- row is invisible to every reader, and an index stricter than the query it protects would stop a
-- station from ever getting a mailbox again after one was revoked. Nothing writes revoked_at today
-- (RevokeEndpoint was deleted in 4.0.0, because it stamped a column nothing reads), so this arm is
-- currently about historical rows — which is exactly when a too-strict index would bite, at
-- migration time, on somebody else's database.
--
-- DUPLICATES ARE COLLAPSED FIRST, keeping the LOWEST rowid: that is the row every reader already
-- resolves to, so collapsing to it changes nothing any caller can observe. Deliveries and
-- attachments reference the surviving row by rowid and are re-pointed before the losers go, so no
-- mail is orphaned. On a fresh install both statements are no-ops.

BEGIN;

-- Re-point anything that named a duplicate at the row readers already use.
UPDATE delivery SET recipient_endpoint = (
  SELECT MIN(e2.id) FROM endpoint e2
   WHERE e2.station_id = (SELECT e1.station_id FROM endpoint e1 WHERE e1.id = delivery.recipient_endpoint)
     AND e2.revoked_at IS NULL)
 WHERE recipient_endpoint IS NOT NULL
   AND (SELECT e1.station_id FROM endpoint e1 WHERE e1.id = delivery.recipient_endpoint) IS NOT NULL;

UPDATE attachment SET recipient_endpoint = (
  SELECT MIN(e2.id) FROM endpoint e2
   WHERE e2.station_id = (SELECT e1.station_id FROM endpoint e1 WHERE e1.id = attachment.recipient_endpoint)
     AND e2.revoked_at IS NULL)
 WHERE recipient_endpoint IS NOT NULL
   AND (SELECT e1.station_id FROM endpoint e1 WHERE e1.id = attachment.recipient_endpoint) IS NOT NULL;

UPDATE attachment SET sender_endpoint = (
  SELECT MIN(e2.id) FROM endpoint e2
   WHERE e2.station_id = (SELECT e1.station_id FROM endpoint e1 WHERE e1.id = attachment.sender_endpoint)
     AND e2.revoked_at IS NULL)
 WHERE (SELECT e1.station_id FROM endpoint e1 WHERE e1.id = attachment.sender_endpoint) IS NOT NULL;

UPDATE message SET sender_endpoint = (
  SELECT MIN(e2.id) FROM endpoint e2
   WHERE e2.station_id = (SELECT e1.station_id FROM endpoint e1 WHERE e1.id = message.sender_endpoint)
     AND e2.revoked_at IS NULL)
 WHERE (SELECT e1.station_id FROM endpoint e1 WHERE e1.id = message.sender_endpoint) IS NOT NULL;

DELETE FROM endpoint
 WHERE station_id IS NOT NULL AND revoked_at IS NULL
   AND id > (SELECT MIN(e2.id) FROM endpoint e2
              WHERE e2.station_id = endpoint.station_id AND e2.revoked_at IS NULL);

DROP INDEX IF EXISTS idx_endpoint_station;
CREATE UNIQUE INDEX idx_endpoint_station ON endpoint(station_id)
  WHERE station_id IS NOT NULL AND revoked_at IS NULL;

INSERT INTO schema_migration(version) VALUES (21);

COMMIT;
