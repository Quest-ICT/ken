-- ============================================================================
-- Ken COMM — migration 0002: file exchange (docs/COMM.md §11)
-- ============================================================================
-- Two tables: an ATTACHMENT (the fact that a file moved, or was offered, between
-- two endpoints) and a TRANSFER GRANT (a one-time credential for moving the bytes
-- over HTTP). The bytes themselves never enter the database — uploads live as
-- files under <comm dir>/files/, named by the attachment's opaque id, and are
-- deleted once delivered or expired. The attachment ROW outlives the bytes for
-- the same reason message metadata outlives message bodies: it is the only thing
-- an operator can investigate after the fact (name, size, sha256, who, when),
-- on a service that relays files between machines.
--
-- Two transfer modes, decided by the SENDER per offer:
--   'path'   — same-host handoff. No bytes touch the server: the offer carries a
--              server-VALIDATED basename plus hashes, and the sessions move the
--              file through a shared exchange directory after a nonce rendezvous
--              (C9). The attachment row exists purely as audit + envelope.
--   'upload' — server relay. The sender PUTs the bytes against a one-time grant;
--              the message referencing the attachment is enqueued only when the
--              upload completes and its sha256 matches the offer, so the
--              receiver never observes partial state.
-- ============================================================================

BEGIN;

CREATE TABLE attachment (
  id                 INTEGER PRIMARY KEY,
  attachment_id      TEXT    NOT NULL UNIQUE,      -- opaque, server-minted; also the on-disk filename
  channel_id         INTEGER NOT NULL REFERENCES channel(id) ON DELETE CASCADE,
  sender_endpoint    INTEGER NOT NULL REFERENCES endpoint(id) ON DELETE CASCADE,
  recipient_endpoint INTEGER NOT NULL REFERENCES endpoint(id) ON DELETE CASCADE,

  -- The file's name as the RECEIVER should know it. Validated server-side to a
  -- bare basename (no separators, no dot-dot, no control bytes) so an offer can
  -- never steer a session toward an arbitrary local path — the exploit the C9
  -- redesign exists to prevent.
  name               TEXT    NOT NULL,
  size_bytes         INTEGER NOT NULL,             -- declared by the sender at offer time
  sha256             TEXT    NOT NULL,             -- declared; uploads are verified against it
  transfer           TEXT    NOT NULL CHECK (transfer IN ('path','upload')),
  -- Optional note accompanying the offer. Persisted only so an 'upload' offer's
  -- note can ride the message that is enqueued later, at upload completion.
  note               TEXT,
  -- For 'path' transfers: SHA-256 of the rendezvous nonce (C9). The receiver
  -- proves shared-filesystem access by echoing the nonce whose hash this is.
  -- Carried opaquely; the server never sees the nonce itself.
  nonce_sha256       TEXT,

  state              TEXT    NOT NULL DEFAULT 'offered'
                       CHECK (state IN ('offered','ready','done','failed','expired')),
  stored_bytes       INTEGER NOT NULL DEFAULT 0,   -- bytes actually held on disk (uploads only)
  message_id         INTEGER REFERENCES message(id) ON DELETE SET NULL,
  idempotency_key    TEXT,

  expires_at         TEXT    NOT NULL,
  created_at         TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ready_at           TEXT,
  done_at            TEXT
);

-- A re-offer with the same key returns the original attachment, mirroring
-- message idempotency: a lost tool response must not mint a second grant chain.
CREATE UNIQUE INDEX idx_attachment_idem
  ON attachment(channel_id, sender_endpoint, idempotency_key)
  WHERE idempotency_key IS NOT NULL;

CREATE INDEX idx_attachment_state ON attachment(state, expires_at);
CREATE INDEX idx_attachment_msg   ON attachment(message_id) WHERE message_id IS NOT NULL;

-- One-time credentials for moving bytes over HTTP. Only the SHA-256 of the grant
-- is stored, like every other secret in Ken. A grant is bound to one attachment,
-- one endpoint, and one direction; it is consumed on use and expires quickly —
-- it only has to survive being passed to curl.
CREATE TABLE transfer_grant (
  id            INTEGER PRIMARY KEY,
  grant_sha256  TEXT    NOT NULL UNIQUE,
  attachment_id INTEGER NOT NULL REFERENCES attachment(id) ON DELETE CASCADE,
  endpoint_id   INTEGER NOT NULL REFERENCES endpoint(id) ON DELETE CASCADE,
  kind          TEXT    NOT NULL CHECK (kind IN ('upload','download')),
  expires_at    TEXT    NOT NULL,
  consumed_at   TEXT,
  created_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE INDEX idx_grant_attachment ON transfer_grant(attachment_id);
CREATE INDEX idx_grant_expires    ON transfer_grant(expires_at);

INSERT INTO schema_migration(version) VALUES (2);

COMMIT;
