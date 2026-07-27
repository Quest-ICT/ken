-- ============================================================================
-- Ken COMM — migration 0001: inter-session communication
-- ============================================================================
-- Schema for the OPT-IN, OFF-BY-DEFAULT session-to-session messaging subsystem
-- (design decision D9; full contract in docs/COMM.md). This lives in its OWN
-- database file (data/comm/comm.db), never in ken.db, because message traffic is
-- high-churn and EXPENDABLE while knowledge is low-churn and DURABLE. Keeping the
-- files apart keeps ephemeral WAL churn out of the replicated database and out of
-- the KB's single writer, and it is why losing this file costs an in-flight
-- conversation and never costs knowledge.
--
-- Conventions match ken.db's 0001 (SQLite/WAL): INTEGER PK rowid, ISO-8601 UTC
-- TEXT timestamps stamped by the SERVER, CHECK enums, SHA-256 of every secret and
-- never the plaintext.
--
-- CROSS-DATABASE OWNERSHIP — read before adding a column. actor_id, space_id and
-- token_id identify rows in ken.db, so they CANNOT be REFERENCES here: SQLite
-- foreign keys do not span database files. They are plain columns, validated by
-- the caller (which holds both handles) and deliberately not enforced by the
-- engine. Do not "fix" this by moving these tables into ken.db — that would undo
-- the separation above.
-- ============================================================================

BEGIN;

CREATE TABLE schema_migration (
  version    INTEGER PRIMARY KEY,
  applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

-- ---------------------------------------------------------------------------
-- Endpoint — one AI session's communication point.
-- ---------------------------------------------------------------------------
-- endpoint_id is opaque and server-minted: routing is ALWAYS by this id, never
-- by `label`, or the first release would ship a global namespace where one
-- session can squat the name another expects and receive its messages.
--
-- secret_sha256 exists because the operating convention is one Ken token per
-- MACHINE, so every session on a box shares a token. Without a per-endpoint
-- secret, two sessions could poll and ack each other's messages — most likely by
-- ACCIDENT, when both register with the same label. This is what makes sender
-- identity honest: token-authenticated and endpoint-scoped, i.e. trustworthy
-- across machines and users, advisory between sessions sharing one token.
--
-- host_hint is an OPTIONAL, opaque, client-supplied string used only to decide
-- whether attempting a same-host filesystem handoff is worth a round-trip. It is
-- NEVER authorization: it is self-reported and therefore spoofable, it compares
-- EQUAL across cloned VM images and UNEQUAL across a bind mount, and two sessions
-- truly on one host may still run as different OS users. Proof of a shared
-- filesystem is a rendezvous (write a nonce, echo it back) — see docs/COMM.md C9.
-- An absent hint must never match another absent hint.
CREATE TABLE endpoint (
  id            INTEGER PRIMARY KEY,
  endpoint_id   TEXT    NOT NULL UNIQUE,       -- opaque, server-minted; the only address
  secret_sha256 TEXT    NOT NULL,              -- SHA-256 of the one-time endpoint secret
  token_id      TEXT    NOT NULL,              -- ken.db api_token.token_id (no FK: other db)
  actor_id      INTEGER NOT NULL,              -- ken.db actor.id           (no FK: other db)
  space_id      INTEGER NOT NULL DEFAULT 1,    -- ken.db space.id           (no FK: other db)
  label         TEXT,                          -- human-readable decoration; NEVER an address
  host_hint     TEXT,                          -- opaque same-host hint; never authorizes
  created_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  last_seen_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  revoked_at    TEXT
);

CREATE INDEX idx_endpoint_owner ON endpoint(space_id, actor_id);
CREATE INDEX idx_endpoint_token ON endpoint(token_id);

-- ---------------------------------------------------------------------------
-- Channel — exactly two distinct endpoints, full-duplex.
-- ---------------------------------------------------------------------------
-- Full-duplex with per-MESSAGE correlation, deliberately NOT half-duplex
-- turn-taking: channel-level turn state is a distributed state machine that
-- wedges when a session dies mid-turn, with no clean way for the peer or the
-- operator to reason about whose turn it was. There is therefore no "turn" or
-- "waiting" column here by design — request/response is a property of a message.
--
-- Ownership is keyed on space_id + the AUTHORIZING HUMAN, never on the actor
-- alone: actors resolve by (kind, display_name), so every token minted with the
-- same actor name collapses to ONE actor row across machines and humans, and an
-- actor-keyed ownership check would reject nothing it was meant to reject.
CREATE TABLE channel (
  id             INTEGER PRIMARY KEY,
  channel_id     TEXT    NOT NULL UNIQUE,      -- opaque, server-minted
  space_id       INTEGER NOT NULL DEFAULT 1,
  owner_actor_id INTEGER NOT NULL,             -- the human who authorized the pairing
  endpoint_a     INTEGER NOT NULL REFERENCES endpoint(id) ON DELETE CASCADE,
  endpoint_b     INTEGER          REFERENCES endpoint(id) ON DELETE CASCADE,  -- NULL until the 2nd join
  state          TEXT    NOT NULL DEFAULT 'pending'
                   CHECK (state IN ('pending','open','revoked')),
  created_at     TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  opened_at      TEXT,
  revoked_at     TEXT,
  -- A channel joins two DISTINCT endpoints: a session must not pair with itself.
  CHECK (endpoint_b IS NULL OR endpoint_b <> endpoint_a)
);

CREATE INDEX idx_channel_a ON channel(endpoint_a, state);
CREATE INDEX idx_channel_b ON channel(endpoint_b, state);

-- ---------------------------------------------------------------------------
-- Pairing code — the human's structural gate on who may talk to whom.
-- ---------------------------------------------------------------------------
-- An agent CANNOT conjure a channel. A human mints a short-lived code in the web
-- UI and hands it to both sessions; each redeems it once. This borrows the
-- property that makes the rest of Ken trustworthy: the curation gate works
-- because a capability is WITHHELD, not because the model is asked nicely.
-- Channel establishment is the one place in COMM where the same trick is
-- available, so it is used here rather than relying on instruction text.
--
-- Only the SHA-256 of the code is stored, like every other secret in Ken.
-- Declared AFTER channel so its foreign key resolves at CREATE time rather than
-- relying on SQLite deferring the lookup to first use.
CREATE TABLE pairing_code (
  id             INTEGER PRIMARY KEY,
  code_sha256    TEXT    NOT NULL UNIQUE,
  space_id       INTEGER NOT NULL DEFAULT 1,   -- ken.db space.id (no FK: other db)
  human_actor_id INTEGER NOT NULL,             -- who authorized (no FK: other db)
  label          TEXT,
  channel_id     INTEGER REFERENCES channel(id) ON DELETE SET NULL,  -- set on first redeem
  expires_at     TEXT    NOT NULL,
  created_at     TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  consumed_at    TEXT                          -- set when the second endpoint joins
);

CREATE INDEX idx_pairing_expires ON pairing_code(expires_at);

-- ---------------------------------------------------------------------------
-- Message — atomic body, ephemeral content, durable metadata.
-- ---------------------------------------------------------------------------
-- THE CONTENT/METADATA SPLIT IS LOAD-BEARING. Acknowledging deletes `body`; the
-- row survives. Deleting the whole record on ack (the intuitive storage win) is
-- mutually exclusive with request/response: with two requests outstanding and
-- both acked, a later reply would reference a row that no longer exists — the
-- server could neither validate it, nor route it, nor tell the sender which
-- request was answered. The body is what has SIZE, so deleting it captures the
-- storage win; the metadata is what has MEANING, so it stays until the exchange
-- completes or ages out. It is also the only thing an operator can investigate
-- after an incident on a service that relays instructions between machines.
--
-- ACK MEANS PROCESSED, NOT RECEIVED, and messages redeliver until acked. Nothing
-- in the transport distinguishes a lost response from a request that never
-- arrived, so exactly-once is not on offer; at-least-once with idempotent
-- operations is, and it matches what the feature wants — a message delivered but
-- never acted upon (a truncated turn) SHOULD come back.
--
-- Every timestamp is stamped by the SERVER clock. Clients supply RELATIVE
-- lifetimes only, so clock skew between agent machines cannot silently shorten
-- or extend anything.
CREATE TABLE message (
  id                   INTEGER PRIMARY KEY,
  message_id           TEXT    NOT NULL UNIQUE,   -- opaque, server-minted
  channel_id           INTEGER NOT NULL REFERENCES channel(id) ON DELETE CASCADE,
  seq                  INTEGER NOT NULL,          -- monotonic per (channel, sender) = per direction
  sender_endpoint      INTEGER NOT NULL REFERENCES endpoint(id) ON DELETE CASCADE,
  recipient_endpoint   INTEGER NOT NULL REFERENCES endpoint(id) ON DELETE CASCADE,
  idempotency_key      TEXT,                      -- client-supplied; a repeat returns the original id

  body                 TEXT,                      -- NULL once acked (or expired) — the split above
  body_sha256          TEXT    NOT NULL,          -- survives body deletion (audit)
  body_bytes           INTEGER NOT NULL,          -- survives body deletion (quota accounting)

  requires_response    INTEGER NOT NULL DEFAULT 0 CHECK (requires_response IN (0,1)),
  reply_to             INTEGER REFERENCES message(id) ON DELETE SET NULL,
  replied_by           INTEGER REFERENCES message(id) ON DELETE SET NULL,

  state                TEXT    NOT NULL DEFAULT 'queued'
                         CHECK (state IN ('queued','delivered','acked','expired')),
  delivery_count       INTEGER NOT NULL DEFAULT 0,   -- lets a receiver detect redelivery

  expires_at           TEXT    NOT NULL,            -- server-stamped; applies while un-acked
  reply_deadline_at    TEXT,                        -- server-stamped when requires_response
  created_at           TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  first_delivered_at   TEXT,
  acked_at             TEXT
);

-- Idempotency: a resend with the same key returns the original message rather
-- than delivering a second copy. Scoped per (channel, sender) so two endpoints
-- cannot collide on a common key like "1".
CREATE UNIQUE INDEX idx_message_idem
  ON message(channel_id, sender_endpoint, idempotency_key)
  WHERE idempotency_key IS NOT NULL;

-- Ordering is promised per channel and DIRECTION, and nowhere else.
CREATE UNIQUE INDEX idx_message_seq ON message(channel_id, sender_endpoint, seq);

-- The poll hot path: this endpoint's un-acked traffic, in order.
CREATE INDEX idx_message_inbox ON message(recipient_endpoint, state, seq);

-- The sweeper's two scans: expire un-acked past TTL, purge settled metadata.
CREATE INDEX idx_message_expires ON message(state, expires_at);
CREATE INDEX idx_message_settled ON message(state, created_at);

INSERT INTO schema_migration(version) VALUES (1);

COMMIT;
