-- ============================================================================
-- Ken COMM — migration 0014: drop `delivery.notified_at`, outlived twice
-- ============================================================================
-- Batch 4 of docs/FINISHING.md: retire the duplicated generation. This column is the
-- clearest case in the batch, because it survived TWO successive redesigns of the
-- mechanism it belonged to.
--
--   0003  added `message.notified_at` so that a repeating sweep would notify exactly
--         once — the stamp WAS the exactly-once guarantee.
--   0009  carried it onto `delivery` in the split, correctly: with several recipients,
--         "have I told the sender about THIS delivery" is a per-delivery question.
--   0011  replaced written notices with a DERIVED QUERY, and moved exactly-once into
--         `notice_watermark` — because a query cannot have the bug the stamp existed to
--         prevent, but it CAN repeat what the reader has already seen.
--
-- The stamp was not removed at step three; it was simply stopped being written. It has
-- ZERO references in Go — not in the store, not in the server, not in a single test —
-- and the only occurrences of its name anywhere are the three migrations above.
--
-- WHAT WOULD BE LOST: nothing that is not better recorded elsewhere. The watermark
-- carries the reader's position, and it is per-party, which the stamp never was.
--
-- Keeping a dead exactly-once marker beside a live one is exactly how a future session
-- concludes there are two mechanisms and goes looking for which is authoritative.

BEGIN;

ALTER TABLE delivery DROP COLUMN notified_at;

INSERT INTO schema_migration(version) VALUES (14);

COMMIT;
