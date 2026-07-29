-- ============================================================================
-- Ken COMM — migration 0006: station-bound endpoints and claim-once delivery
-- ============================================================================
-- docs/STATIONS.md S4 and S5. Two changes that only make sense together.
--
-- 1. AN ENDPOINT MAY BELONG TO A STATION.
--
-- Today an endpoint is the whole identity of a session's mailbox, which is why
-- losing its secret is terminal and why a replacement session starts from nothing.
-- Binding an endpoint to a station moves the identity up a level: the STATION owns
-- the logical inbox, and an endpoint becomes a credentialed READER of it. A
-- replacement session binds a fresh endpoint to the same station and inherits the
-- unread mail — no new pairing code, no peer involvement, no human in the loop.
--
-- Both pointers are opaque text into ken.db with NO foreign key, and that is S7's
-- rule rather than an omission: cross-database pointers run from the EXPENDABLE
-- file to the DURABLE one, never the reverse. Under the only restore skew that
-- actually occurs — ken.db restored backwards while comm.db stays current — a
-- station_id that no longer resolves must be treated as UNBOUND, not as an error.
-- A dangling pointer here is a row to drop; one in the other direction would be
-- corruption in the file we promise to restore.
--
-- bound_by_station_key_id is what makes revocation mean something (S6): revoking a
-- station key severs every endpoint that key bound, rather than leaving the leaked
-- capability running until an idle sweep happens to notice.
--
-- 2. DELIVERY BECOMES CLAIM-ONCE WITH A LEASE.
--
-- Once several endpoints can read one station's inbox, "delivered" stops being a
-- property of an endpoint. The first reader to poll CLAIMS a message; the claim is
-- recorded here; one ack settles it for the whole station.
--
-- The claim is a LEASE, not a transfer of ownership. A claim that is not
-- acknowledged before it expires returns the message to the unclaimed tail and
-- increments delivery_count — per STATION, so a redelivered message may reach a
-- DIFFERENT reader than first saw it. Without the lease, a session that claims and
-- then dies strands its messages permanently, and COMM's C6 promise — a message
-- delivered but never acted upon comes back — would simply be false.
--
-- Fan-out was rejected explicitly: delivering one message to every reader would
-- make COMM the broker §1 says it is not, would multiply the per-channel
-- unacknowledged count, and would re-create exactly the shared-inbox accident the
-- per-endpoint secret was invented to prevent.
--
-- Both columns are nullable and additive: an UNBOUND endpoint behaves exactly as it
-- did before this migration, which is what keeps the shipped path — comm_register
-- with no station, pairing codes, comm_join — valid indefinitely.
BEGIN;

ALTER TABLE endpoint ADD COLUMN station_id TEXT;
ALTER TABLE endpoint ADD COLUMN bound_by_station_key_id TEXT;
ALTER TABLE endpoint ADD COLUMN bound_at TEXT;

-- Which reader currently holds a message, and until when. Both NULL means the
-- message is in the unclaimed tail, which is also the state every pre-existing row
-- is in — so unbound traffic is unaffected.
-- INTEGER, matching recipient_endpoint: this is a comm.db -> comm.db pointer, so
-- it uses the rowid convention the message table already uses. S7's opaque-text
-- rule governs pointers that CROSS databases, which this one does not.
ALTER TABLE message ADD COLUMN claimed_by_endpoint INTEGER REFERENCES endpoint(id) ON DELETE SET NULL;
ALTER TABLE message ADD COLUMN claim_expires_at TEXT;

CREATE INDEX idx_endpoint_station ON endpoint(station_id) WHERE station_id IS NOT NULL;
CREATE INDEX idx_endpoint_bound_by ON endpoint(bound_by_station_key_id)
  WHERE bound_by_station_key_id IS NOT NULL;

-- The poll query's shape: unacked messages for a recipient, either unclaimed or
-- with an expired claim. Partial on the un-acked rows, which is the only set a
-- poll ever scans.
CREATE INDEX idx_message_claim ON message(recipient_endpoint, claim_expires_at)
  WHERE acked_at IS NULL;

INSERT INTO schema_migration(version) VALUES (6);

COMMIT;
