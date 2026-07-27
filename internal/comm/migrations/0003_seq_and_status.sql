-- ============================================================================
-- Ken COMM — migration 0003: durable sequences and status messages
-- ============================================================================
-- Two corrections found by auditing the 1.2 line before release.
--
-- 1. SEQUENCE DURABILITY. Sequences were assigned as MAX(seq)+1 over surviving
--    rows, so once the metadata sweep purged a direction's history the counter
--    RESET to 1. That breaks the documented "strictly ascending per channel and
--    direction" promise, and worse: a retried cumulative acknowledgement
--    (ack_up_to_seq) computed against the old numbering would silently settle
--    brand-new messages that had been re-issued the same low sequence numbers.
--    The high-water mark now lives in its own table and never goes backwards,
--    because it is not tied to the lifetime of any message row.
--
-- 2. STATUS MESSAGES. The contract promises the sender is told when a message
--    expires undelivered, or when a required reply misses its deadline —
--    otherwise a requester whose peer died waits forever, which is precisely
--    what reply deadlines exist to prevent. A server-authored message needs to
--    be distinguishable from peer traffic (a peer must not be able to forge
--    one), so messages gain a `kind`.
-- ============================================================================

BEGIN;

-- The per-direction high-water mark. Survives message purges by construction.
CREATE TABLE channel_seq (
  channel_id      INTEGER NOT NULL REFERENCES channel(id) ON DELETE CASCADE,
  sender_endpoint INTEGER NOT NULL REFERENCES endpoint(id) ON DELETE CASCADE,
  next_seq        INTEGER NOT NULL,
  PRIMARY KEY (channel_id, sender_endpoint)
) WITHOUT ROWID;

-- Seed from existing traffic so an in-place upgrade never reissues a number.
INSERT INTO channel_seq(channel_id, sender_endpoint, next_seq)
SELECT channel_id, sender_endpoint, MAX(seq) + 1 FROM message
GROUP BY channel_id, sender_endpoint;

-- 'status' rows are authored by the SERVER about a message's fate. Keeping them
-- a distinct kind means a receiving agent can trust the distinction, and a peer
-- cannot fabricate one: every send path writes 'message'.
ALTER TABLE message ADD COLUMN kind TEXT NOT NULL DEFAULT 'message'
  CHECK (kind IN ('message','status'));

-- notified_at marks a message whose sender has already been told about its
-- failure, so a repeating sweep notifies exactly once.
ALTER TABLE message ADD COLUMN notified_at TEXT;

INSERT INTO schema_migration(version) VALUES (3);

COMMIT;
