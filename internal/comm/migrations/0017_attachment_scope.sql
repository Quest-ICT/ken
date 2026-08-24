-- ============================================================================
-- Ken COMM — migration 0017: the attachment table becomes SCOPE-shaped
-- ============================================================================
-- SHIPS ALONE with its code under Rule 4. It rewrites no row's DATA, but it relaxes two
-- NOT NULL constraints and tightens a third, and the code that follows depends on all
-- three — an older binary rolled back over this migration would still work, but a NEWER
-- binary against an OLDER schema would fail every offer at the SQL layer.
--
-- WHAT WAS BROKEN. `attachment` was channel-shaped: `channel_id NOT NULL` and
-- `recipient_endpoint NOT NULL`, one endpoint where a room needs a party set. COMM
-- addresses four ways — ch:, r:, p:, b: — and files worked on exactly one of them.
--
-- *** THE HOLE IS BIGGER THAN ROOMS, WHICH IS WHAT THE ORIGINAL FINDING UNDERSTATED. ***
-- `comm_send{to_station}` is the path Ken's own instructions call "the SIMPLEST way to
-- reach a peer", and a station pair deliberately has NO CHANNEL ROW — that is the point of
-- the derived `p:<a>|<b>` scope, "nothing to open, join or expire". So two linked stations
-- could not exchange a file AT ALL, and the workaround (mint a pairing code) re-creates the
-- very channel row the pair model exists to eliminate and splits the conversation in two:
-- the file lands in ch:… while the talking happens in p:….
--
-- THE SEAM WAS CUT IN 0010 AND NEVER USED. That migration added `scope_id` and backfilled
-- it from each attachment's channel, with a comment explaining exactly this fix. Then
-- nothing wrote it: `internal/comm/file.go` contained the string "scope" ZERO times, and
-- every attachment written since 0010 carries scope_id NULL. So this migration must
-- RE-BACKFILL before it can tighten the column — tightening first aborts the upgrade with a
-- constraint failure, which is a blocked deployment rather than a bad one, but still.
--
-- *** THIS IS NOT A TABLE REBUILD, AND THAT IS A PROPERTY OF THE PINNED DRIVER. ***
-- Measured, not assumed, at ncruces/go-sqlite3 embedding SQLite 3.53.3: ALTER TABLE ...
-- ALTER COLUMN ... DROP NOT NULL / SET NOT NULL both work in place. Verified with a
-- control — a NULL was REFUSED before the ALTER and accepted after — and the table's five
-- indexes, its column comments and its foreign-key clauses all survived, with
-- integrity_check ok and foreign_key_check empty.
--
-- IT DOES NOT WORK AT SQLite 3.50.4. So this migration hard-pins comm.db to a driver
-- carrying >= 3.53, and NOTHING ELSE IN THE REPO ASSERTS THAT FLOOR. Downgrading the
-- driver below it would make a fresh install fail here rather than silently differ, which
-- is the safe direction, but it is a real constraint and this comment is where it is
-- written down. The older create-copy-drop-rename rebuild remains the fallback if that
-- floor ever has to be given up.

BEGIN;

-- 1. RE-BACKFILL. Rows written before 0010 already have a scope; rows written since carry
--    NULL because OfferFile never set the column. Both are channel attachments, so both
--    derive the same way. Done BEFORE the tightening, or the tightening aborts.
UPDATE attachment
   SET scope_id = 'ch:' || (SELECT c.channel_id FROM channel c WHERE c.id = attachment.channel_id)
 WHERE scope_id IS NULL AND channel_id IS NOT NULL;

-- 2. THE SCOPE IS NOW THE ADDRESS, so it may not be absent.
ALTER TABLE attachment ALTER COLUMN scope_id SET NOT NULL;

-- 3. AND THE CHANNEL-SHAPED COLUMNS BECOME OPTIONAL rather than being dropped.
--
--    Kept, not removed, and deliberately: `channel_id` still carries the truth for a
--    channel attachment and its ON DELETE CASCADE is how revoking a channel takes its
--    files with it. `recipient_endpoint` stays because a 'path' transfer's rendezvous is
--    between two specific endpoints on one filesystem, which is genuinely endpoint-shaped
--    rather than party-shaped. What changes is that neither is REQUIRED: a room or pair
--    offer sets scope_id and leaves both NULL.
ALTER TABLE attachment ALTER COLUMN channel_id DROP NOT NULL;
ALTER TABLE attachment ALTER COLUMN recipient_endpoint DROP NOT NULL;

-- 4. THE INDEX WAS PARTIAL ON A COLUMN THAT WAS ALMOST ALWAYS NULL, so it indexed only
--    pre-0010 rows — a covering index over a population that stopped growing in July.
--    Now that scope_id is NOT NULL the predicate can never exclude anything, and a partial
--    index whose WHERE is always true is a full index wearing a confusing hat.
DROP INDEX IF EXISTS idx_attachment_scope;
CREATE INDEX idx_attachment_scope ON attachment(scope_id, state);

INSERT INTO schema_migration(version) VALUES (17);

COMMIT;
