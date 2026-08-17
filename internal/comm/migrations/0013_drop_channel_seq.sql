-- ============================================================================
-- Ken COMM — migration 0013: drop `channel_seq`, the numbering that lost
-- ============================================================================
-- Batch 4 of docs/FINISHING.md: retire the duplicated generation.
--
-- `channel_seq` numbered `message.seq`, one counter per (channel, sender_key). 0009
-- rebuilt `message` WITHOUT a `seq` column and replaced the scheme with
-- `scope_counter` — ONE counter per SCOPE across every sender.
--
-- WHY THE SCHEME CHANGED, since it is the whole justification for this file. Two
-- interleaved sequences in one channel made `ack_up_to_seq` — which is a RANGE —
-- able to settle mail travelling the other way that nobody had read. And with a room
-- the old keying has no meaning at all: there is no "direction" among five
-- participants.
--
-- So this is NOT a rival numbering of the same stream, which is why it can go without
-- a compatibility argument: the column it numbered no longer exists. Nothing has read
-- this table since 0009, and its only writer — `nextSeq` — lost both call sites in the
-- same slice that introduced the replacement, then sat with ZERO CALLERS writing a
-- table nothing read until the Go half was deleted in 3.9.0's predecessor commit.
--
-- ONE WRITE PATH DOES SURVIVE UNTIL THIS RUNS, and an adversarial reviewer caught it
-- after the first analysis claimed there were none: `DELETE FROM channel` in the idle
-- sweep cascades here through `channel_id ... REFERENCES channel(id) ON DELETE
-- CASCADE`. It only ever removed rows. After this migration the cascade simply has one
-- fewer target.
--
-- The rows themselves are worthless: they are high-water marks for a column that was
-- dropped four migrations ago.

BEGIN;

DROP TABLE channel_seq;

INSERT INTO schema_migration(version) VALUES (13);

COMMIT;
