-- ============================================================================
-- Ken COMM — migration 0007: sequence numbering follows the STATION
-- ============================================================================
-- docs/STATIONS.md S4 says it plainly and the code did not do it: "For a bound
-- endpoint the per-channel sequence keys on (channel, sender station) rather than
-- sender endpoint, or outbound numbering restarts every reconnect."
--
-- It restarted. With channel_seq keyed on the sender's ENDPOINT rowid, a
-- replacement session — a different endpoint bound to the same station — got a
-- fresh row and began again at 1, while its predecessor had already reached 2 or
-- 20. Two messages in one channel and direction then carry the SAME sequence
-- number, and the damage is not merely cosmetic ordering:
--
--   * polls order by (seq, id), so the stream interleaves the old and new
--     sessions' messages in a nonsensical order;
--   * `ack_up_to_seq` is a RANGE. Acking up to 2 after a takeover settles the
--     replacement's messages AND the predecessor's — including ones nobody read.
--     That is silent mail loss produced by an ordinary, documented call.
--
-- THE FIX, and why the column is text. S4 asks for both regimes to coexist "in one
-- index by writing the station id when there is one and the endpoint id when there
-- is not". The two ids are different types — an endpoint is a rowid, a station is
-- an opaque string — so the key column becomes TEXT holding a tagged value:
-- 'e:<rowid>' for an unbound sender, 's:<station_id>' for a bound one. The tag
-- matters: without it a station named "42" would collide with endpoint rowid 42,
-- which is exactly the kind of collision that is untestable in practice and
-- catastrophic once.
--
-- Rebuilt rather than altered because the PRIMARY KEY changes, and SQLite cannot
-- alter one in place. Existing rows migrate to the 'e:' form, which is what they
-- have always meant — so every unbound conversation continues from the number it
-- had reached rather than restarting, which would reproduce the bug during the
-- upgrade that fixes it.
BEGIN;

CREATE TABLE channel_seq_new (
  channel_id INTEGER NOT NULL REFERENCES channel(id) ON DELETE CASCADE,
  -- 'e:<endpoint rowid>' or 's:<station_id>'. Tagged so the two namespaces cannot
  -- collide, and text so both fit one column and one index.
  sender_key TEXT    NOT NULL,
  next_seq   INTEGER NOT NULL,
  PRIMARY KEY (channel_id, sender_key)
) WITHOUT ROWID;

INSERT INTO channel_seq_new(channel_id, sender_key, next_seq)
SELECT channel_id, 'e:' || sender_endpoint, next_seq FROM channel_seq;

DROP TABLE channel_seq;
ALTER TABLE channel_seq_new RENAME TO channel_seq;

INSERT INTO schema_migration(version) VALUES (7);

COMMIT;
