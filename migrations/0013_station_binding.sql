-- ============================================================================
-- Ken — migration 0013: binding vouchers (docs/STATIONS.md S5)
-- ============================================================================
-- A session staffing a station needs its COMM endpoint to BELONG to that station,
-- so the station owns the inbox and a replacement session inherits the mail. The
-- obvious way to do that would be to pass the station key to comm_register — and
-- that is exactly what S5 forbids: a station key is a long-lived credential, and
-- tool arguments are model output. They land in transcripts, harness logs and
-- scrollback, and via the notebook potentially in a backup. The key travels as an
-- Authorization header on /station and nowhere else.
--
-- So the binding is done with a VOUCHER instead: the session asks /station (where
-- its key already is, in the header) for a short-lived single-use token, and hands
-- THAT to comm_register. A leaked voucher is worth one binding within a few
-- minutes; a leaked station key is worth the station.
--
-- WHY THIS TABLE LIVES IN ken.db AND NOT comm.db. S7's pointer rule: every
-- cross-database pointer runs from the expendable file to the durable one, never
-- the reverse. comm.db may be wiped, is not backed up, and does not exist at all
-- when COMM is off. A voucher is issued by the station surface, which works with
-- COMM disabled, so it cannot live in a database that may not be open. Redemption
-- reads it back through the knowledge-base store, which the comm endpoint already
-- holds for token authentication.
--
-- Single-use is enforced by redeemed_at rather than by deleting the row: an
-- operator investigating "which key bound this endpoint" needs the trail, and a
-- deleted row answers nothing. The sweeper drops expired rows on the usual cadence.
BEGIN;

CREATE TABLE station_binding_voucher (
  id            INTEGER PRIMARY KEY,
  voucher_id    TEXT    NOT NULL UNIQUE,   -- the opaque value handed to comm_register
  station_id    TEXT    NOT NULL REFERENCES station(station_id) ON DELETE CASCADE,
  -- The station key that asked for it. Recorded so revoking that key can sever
  -- every endpoint it bound (S6) — the reason revocation is not merely cosmetic.
  token_id      TEXT    NOT NULL,
  created_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  expires_at    TEXT    NOT NULL,
  redeemed_at   TEXT,
  -- The endpoint it ended up binding. Nullable until redeemed; NOT a foreign key
  -- and never dereferenced as one, because it points into the expendable database
  -- (S7) and is expected to dangle once the COMM sweep runs.
  redeemed_by_endpoint TEXT
);

CREATE INDEX idx_station_voucher_station ON station_binding_voucher(station_id);
CREATE INDEX idx_station_voucher_token   ON station_binding_voucher(token_id);
CREATE INDEX idx_station_voucher_expiry  ON station_binding_voucher(expires_at)
  WHERE redeemed_at IS NULL;

INSERT INTO schema_migration(version) VALUES (13);

COMMIT;
