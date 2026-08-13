-- ============================================================================
-- Ken COMM — migration 0011: notices become a QUERY, not mail
-- ============================================================================
-- Slice 4 of the COMM addressing plan, and the last structural one before the
-- channel itself can be retired.
--
-- WHAT A NOTICE IS TODAY: when a message expires unread, or a requested reply goes
-- past its deadline, Ken WRITES A MESSAGE to the original sender — a real row in
-- `message` with kind='status', occupying the scope, counting against backpressure,
-- carrying its own expiry, and needing its own acknowledgement. A failure signal
-- implemented as a second failure-prone delivery.
--
-- WHY THAT IS WORTH REMOVING RATHER THAN MAINTAINING, in the order the reasons were
-- learned rather than the order they matter:
--
--   * Sweep WRITES. A pass whose job is to delete things also inserts them, which
--     means it can hit backpressure, can fail mid-way, and rolls back the deletions
--     when the insert fails. That coupling is why one unread ROOM message stopped
--     expiry, body retention, the metadata purge, file cleanup and idle-endpoint
--     removal in 3.0.0 and 3.0.1: the notice path scanned a column that is NULL for
--     room mail, and the whole transaction went with it.
--
--   * The information was always DERIVABLE. Every fact a notice carries — this
--     message expired, nobody replied — is already in `message` and `delivery`. The
--     notice is a denormalised copy that can disagree with its source, and this
--     project has now been bitten three times by exactly that shape.
--
--   * A notice can itself expire. It is stamped with a TTL, so the signal reporting
--     a failure to deliver is subject to the same failure it reports.
--
-- WHAT REPLACES IT: `comm_poll` computes notices from the sender's own rows on every
-- call, and this table records how far each party has read. No message is written, so
-- Sweep never inserts, and a notice cannot expire because it is not stored.
--
-- WHY A WATERMARK RATHER THAN AN ACK: a notice has no identity to acknowledge — it is
-- a view over rows that already exist. "I have seen everything up to this moment" is
-- the only statement that makes sense about a derived stream, and it is one row per
-- party rather than one per notice.
--
-- EXISTING kind='status' ROWS ARE LEFT ALONE. They are ordinary messages now: they
-- poll, they ack, they expire on the normal schedule. Deleting them would be
-- destroying mail a session may not have read yet, to tidy a table.
BEGIN;

CREATE TABLE notice_watermark (
  -- 's:<station_id>' or 'e:<endpoint rowid>' — the same party key everything else in
  -- this database is addressed by, so a session that reconnects under a new endpoint
  -- keeps its place in the stream.
  party_key TEXT PRIMARY KEY,
  -- seen_at is CONFIRMED: notices at or before it are never shown again.
  seen_at   TEXT NOT NULL,
  -- shown_at is what the LAST poll put in front of the caller, not yet confirmed.
  --
  -- TWO COLUMNS BECAUSE A NEW TOOL CANNOT REACH A RUNNING SESSION. The obvious design
  -- is an explicit "I have read my notices" call, and it is unusable here: MCP tool
  -- lists pin at conversation start, so a tool added today is invisible to every
  -- session already running — the exact population most likely to have messages dying
  -- unread. A design whose only clearing mechanism is a new call would repeat notices
  -- forever for precisely those sessions.
  --
  -- So the confirmation rides the poll that was going to happen anyway: each poll
  -- promotes the PREVIOUS poll's shown_at into seen_at, then records what it shows.
  -- A notice is cleared by the caller COMING BACK rather than by the call that showed
  -- it, so a fault between the query and the caller holding the result cannot drop it.
  -- A result lost in transit still loses the notice — the server cannot tell a
  -- delivered result from a discarded one — and that is the accepted cost of not
  -- requiring a confirmation call no running session could make.
  shown_at  TEXT NOT NULL DEFAULT ''
) WITHOUT ROWID;

INSERT INTO schema_migration(version) VALUES (11);

COMMIT;
