-- ============================================================================
-- Ken COMM — migration 0009: a message has RECIPIENTS, not a recipient
-- ============================================================================
-- Slice 3 of the COMM addressing plan, and the structural prerequisite for rooms.
-- Nothing about two-party messaging changes here: every existing conversation keeps
-- working, every message keeps its state, and no tool gains or loses a field. What
-- changes is that the shape stops forbidding a third participant.
--
-- WHAT MADE FAN-OUT IMPOSSIBLE, precisely:
--
--   message.recipient_endpoint INTEGER NOT NULL      one recipient, by column
--   message.state / delivery_count / acked_at / …    one recipient's progress,
--                                                    stored on the message itself
--   UNIQUE (channel_id, sender_endpoint, seq)        numbering per direction of a PAIR
--
-- A message addressed to five stations has five delivery states and one body. Those
-- are different lifetimes in the same row, so they split: `message` keeps what is
-- true of the message (body, sender, sequence, expiry), and the new `delivery` table
-- keeps one row per recipient with everything that varies between them.
--
-- SCOPE REPLACES CHANNEL as the addressing unit. A tagged string:
--   'ch:<channel_id>'   a legacy two-party channel — every row after this migration
--   'r:<room_id>'       a room, including DM rooms — slice 5
-- The tag is what keeps the two namespaces from colliding, the same reasoning 0007
-- applied to sender_key ('e:' vs 's:'), and for the same reason: a room named "42"
-- and a channel named "42" must not be one scope. ':' is safe as a separator because
-- both ids are randBase62.
--
-- PARTY REPLACES ENDPOINT as the recipient. 's:<station_id>' when the reader is
-- staffed, 'e:<endpoint_id>' when it is not — again 0007's convention, now applied
-- to the receiving side. This is what lets a session reconnect under a new endpoint
-- and still find its station's mail, which is already true in the poll predicate and
-- was not true of the storage.
--
-- WHY THIS IS A REBUILD AND NOT A SET OF ALTERs: `recipient_endpoint` is NOT NULL and
-- `channel_id` must become nullable for rooms, and SQLite can relax neither in place.
-- One rebuild now beats a second one in slice 5.
--
-- WHY THE OLD RECIPIENT COLUMNS ARE STAGED IN AN FK-FREE TABLE FIRST, which looks
-- like fussiness and is not. With foreign keys enforced, DROP TABLE runs an implicit
-- DELETE FROM, so dropping `message` fires every ON DELETE action aimed at it: a
-- `delivery` table populated beforehand would be CASCADE-emptied and the migration
-- would report success with zero deliveries. The plan this comes from wrote
-- `PRAGMA foreign_keys=OFF` inside the transaction to prevent exactly that; the pragma
-- is a documented no-op inside a transaction and does nothing at all. Measured against
-- this driver before writing this line: 2 child rows in, 0 out, no error.
--
-- Migrate() now disables enforcement outside the transaction and runs
-- foreign_key_check afterwards, which fixes it for every future rebuild too. This file
-- does not RELY on that: `legacy_recipient` is a CREATE TABLE AS SELECT, so it carries
-- no foreign keys and nothing can cascade into it, and `delivery` is built after the
-- rename. The migration is correct whether or not enforcement happens to be off.
--
-- NOT INCLUDED, deliberately, though the source plan lists them:
--   retain_body_until   1.6.0 made body retention a live setting evaluated at sweep
--                       time. Materialising a per-message deadline at send would
--                       freeze the value an operator is most likely to change DURING
--                       an incident, which is the one case live settings exist for.
--   party_load          slice 3 of the plan; a budget table nothing maintains is
--                       worse than none, because the next reader trusts it.
--   notice_watermark    slice 4's, and it needs no rebuild to add later.
BEGIN;

CREATE TABLE message_new (
  id                 INTEGER PRIMARY KEY,
  message_id         TEXT    NOT NULL UNIQUE,
  space_id           INTEGER NOT NULL DEFAULT 1,

  scope_id           TEXT    NOT NULL,
  scope_seq          INTEGER NOT NULL,   -- per SCOPE, total, across all senders
  -- Nullable from here on: a room message belongs to no channel. Still written for
  -- every 'ch:' message so slice 7 can retire the column with evidence rather than
  -- hope.
  channel_id         INTEGER REFERENCES channel(id) ON DELETE CASCADE,
  sender_endpoint    INTEGER NOT NULL REFERENCES endpoint(id) ON DELETE CASCADE,
  sender_party       TEXT    NOT NULL,   -- 's:<station_id>' | 'e:<endpoint rowid>'

  idempotency_key    TEXT,
  body               TEXT,
  body_sha256        TEXT    NOT NULL,
  body_bytes         INTEGER NOT NULL,
  kind               TEXT    NOT NULL DEFAULT 'message' CHECK (kind IN ('message','status')),

  requires_response  INTEGER NOT NULL DEFAULT 0 CHECK (requires_response IN (0,1)),
  -- Inert until rooms: with one recipient, 'any' and 'all' are the same question.
  response_mode      TEXT    NOT NULL DEFAULT 'any' CHECK (response_mode IN ('any','all')),
  answered_at        TEXT,               -- first reply from ANY recipient
  reply_to           INTEGER REFERENCES message(id) ON DELETE SET NULL,

  expires_at         TEXT    NOT NULL,
  created_at         TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),

  -- How many parties this was addressed to, and the roster epoch at send time. Both
  -- are 1 and 0 for every row that exists today; slice 5 gives them meaning, and slice
  -- 6 uses the epoch to lapse a standing auto-process grant when membership moves.
  audience_size      INTEGER NOT NULL DEFAULT 1,
  audience_epoch     INTEGER NOT NULL DEFAULT 0
);

-- scope_seq is assigned by created_at order within each channel. The old `seq` was
-- per (channel, sender) — two interleaved sequences per channel — so it cannot be
-- carried across; renumbering is the point. Ordering ties break on id, which is
-- insertion order, so the result is stable.
INSERT INTO message_new(id, message_id, scope_id, scope_seq, channel_id, sender_endpoint,
       sender_party, idempotency_key, body, body_sha256, body_bytes, kind,
       requires_response, reply_to, expires_at, created_at)
SELECT m.id, m.message_id,
       'ch:' || c.channel_id,
       ROW_NUMBER() OVER (PARTITION BY m.channel_id ORDER BY m.created_at, m.id),
       m.channel_id, m.sender_endpoint,
       COALESCE('s:' || e.station_id, 'e:' || m.sender_endpoint),
       m.idempotency_key, m.body, m.body_sha256, m.body_bytes, m.kind,
       m.requires_response, m.reply_to, m.expires_at, m.created_at
FROM message m
JOIN channel c ON c.id = m.channel_id
LEFT JOIN endpoint e ON e.id = m.sender_endpoint;

-- Stage the per-recipient columns OUT of the way before the table goes. CTAS: a plain
-- table with no constraints, so the drop below cannot reach it.
CREATE TABLE legacy_recipient AS
SELECT id, recipient_endpoint, state, delivery_count, claimed_by_endpoint,
       claim_expires_at, first_delivered_at, acked_at, reply_deadline_at,
       replied_by, notified_at
FROM message;

DROP TABLE message;
ALTER TABLE message_new RENAME TO message;

CREATE UNIQUE INDEX idx_message_scope_seq ON message(scope_id, scope_seq);
-- Idempotency is per (scope, SENDER PARTY) rather than per sender endpoint: a session
-- that reconnects under a new endpoint and retries the same key must still get its
-- original message back rather than send a duplicate.
CREATE UNIQUE INDEX idx_message_idem
  ON message(scope_id, sender_party, idempotency_key) WHERE idempotency_key IS NOT NULL;
CREATE INDEX idx_message_scope   ON message(scope_id, created_at);
CREATE INDEX idx_message_expires ON message(expires_at);
CREATE INDEX idx_message_settled ON message(created_at);

-- One row per recipient. EVERY per-recipient column moved off `message`.
CREATE TABLE delivery (
  id                  INTEGER PRIMARY KEY,
  message_row         INTEGER NOT NULL REFERENCES message(id) ON DELETE CASCADE,
  -- The reader, as an identity that survives reconnection.
  party_key           TEXT    NOT NULL,   -- 's:<station_id>' | 'e:<endpoint rowid>'
  -- Audit only, and SET NULL rather than CASCADE: when the idle sweep removes an
  -- endpoint, the record that a delivery happened must outlive the endpoint that
  -- received it. Deleting the row instead would erase a message's history because a
  -- reader went quiet.
  recipient_endpoint  INTEGER REFERENCES endpoint(id) ON DELETE SET NULL,
  state               TEXT    NOT NULL DEFAULT 'queued'
                        CHECK (state IN ('queued','delivered','acked','expired')),
  delivery_count      INTEGER NOT NULL DEFAULT 0,
  claimed_by_endpoint INTEGER REFERENCES endpoint(id) ON DELETE SET NULL,
  claim_expires_at    TEXT,
  first_delivered_at  TEXT,
  acked_at            TEXT,
  acked_by_endpoint   INTEGER REFERENCES endpoint(id) ON DELETE SET NULL,
  -- NULL at send, ARMED at first delivery. A deadline that starts before anyone has
  -- been handed the message is a deadline against a weekend.
  reply_deadline_at   TEXT,
  replied_by          INTEGER REFERENCES message(id) ON DELETE SET NULL,
  notified_at         TEXT
);
CREATE UNIQUE INDEX idx_delivery_unique ON delivery(message_row, party_key);
CREATE INDEX        idx_delivery_inbox  ON delivery(party_key, state, id);
CREATE INDEX        idx_delivery_expiry ON delivery(state, message_row);

-- Lossless by construction: a legacy message had EXACTLY ONE recipient, so this is
-- one delivery row per existing message with its state carried over verbatim.
INSERT INTO delivery(message_row, party_key, recipient_endpoint, state, delivery_count,
       claimed_by_endpoint, claim_expires_at, first_delivered_at, acked_at,
       reply_deadline_at, replied_by, notified_at)
SELECT old.id, COALESCE('s:' || r.station_id, 'e:' || old.recipient_endpoint),
       old.recipient_endpoint, old.state, old.delivery_count,
       old.claimed_by_endpoint, old.claim_expires_at, old.first_delivered_at, old.acked_at,
       old.reply_deadline_at, old.replied_by, old.notified_at
FROM legacy_recipient old
LEFT JOIN endpoint r ON r.id = old.recipient_endpoint;

DROP TABLE legacy_recipient;

-- The per-scope counter. Replaces channel_seq's per-(channel, sender) keying, which
-- is why the numbering had two interleaved sequences per channel to begin with.
CREATE TABLE scope_counter (scope_id TEXT PRIMARY KEY, next_seq INTEGER NOT NULL) WITHOUT ROWID;
INSERT INTO scope_counter(scope_id, next_seq)
  SELECT scope_id, MAX(scope_seq) + 1 FROM message GROUP BY scope_id;

INSERT INTO schema_migration(version) VALUES (9);

COMMIT;
