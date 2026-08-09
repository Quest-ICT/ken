-- ============================================================================
-- Ken COMM — migration 0008: a channel remembers WHICH PAIR authorised it
-- ============================================================================
-- Link revocation asked "which channels belong to stations A and B?" and answered
-- it by JOINing endpoint and reading station_id AT QUERY TIME. That reads the
-- endpoint's CURRENT binding, not the binding that existed when the channel was
-- authorised — and binding is mutable by an agent tool, with no human in the loop.
--
-- Two failures, both measured:
--
--   EVASION. A session calls comm_unbind — one tool call, no voucher, no human,
--   and the tool's own description RECOMMENDS it ("before your human revokes the
--   station key that bound you"). Its endpoint's station_id becomes NULL, so the
--   channel matches nothing. The operator clicks Revoke on the link and the
--   console reports 0 live channels, the sweep closes 0, and the flash reads "No
--   channels were open." Both sides keep talking under a permission that has been
--   withdrawn, and nothing anywhere shows it. This is the instructed path, not an
--   attack.
--
--   COLLATERAL. The mirror case is worse, because it severs traffic the operator
--   did not aim at. Rebind an endpoint from infra to dev and the channel infra
--   opened with prod becomes invisible to infra/prod's own revoke AND is killed by
--   revoking the unrelated dev/prod link.
--
-- THE FIX: snapshot the authorising pair on the channel row at open time, and match
-- on the snapshot. Authorisation is a fact about the past — who was permitted to
-- open this, when it was opened — and it must not be re-derived from state that has
-- moved since.
--
-- BACKFILL AND ITS HONEST LIMIT. Existing rows carry no record of who authorised
-- them, so the best available source is the current binding — which is exactly the
-- value that may already have drifted. Rows whose endpoints are still bound get a
-- correct-or-better answer than today; rows with an ALREADY-UNBOUND endpoint get
-- NULL and stay invisible to link revocation, precisely as they are today. That is
-- not a regression, and it cannot be fixed from inside the database because the
-- authorising binding was never written down.
--
-- DO NOT TREAT A NULL PAIR AS A DEFECT TO CLEAN UP. Two completely different
-- histories produce it and NOTHING DISTINGUISHES THEM:
--
--   (1) a station link authorised the channel, and revocation can no longer see it
--       — the real defect, and rare;
--   (2) no station link was ever involved. A channel opened with a PAIRING CODE
--       between two unbound endpoints is the ordinary case and has no link to
--       revoke, so nothing is wrong with it at all.
--
-- On the deployment this was written against, seven of nine open channels had a NULL
-- pair and SIX of the seven were kind (2) — including the operator's own working
-- channels. An earlier version of this comment told operators to close them, which
-- would have severed the live estate. The distinction is unrecoverable: the
-- authorising binding was never recorded, which is the whole reason this migration
-- exists.
--
-- So the query below REPORTS; it does not prescribe. Only a row whose two stations
-- genuinely hold a station_link is kind (1), and answering that needs ken.db, which
-- this database cannot reach.
--
--     SELECT c.channel_id, ea.station_id, eb.station_id
--       FROM channel c
--       JOIN endpoint ea ON ea.id = c.endpoint_a
--       JOIN endpoint eb ON eb.id = c.endpoint_b
--      WHERE c.state='open' AND (c.station_a IS NULL OR c.station_b IS NULL);
--
-- THE FIX FOR THE CLASS IS BINDING, NOT CLOSING. A NULL pair means an endpoint is
-- not bound to a station; binding the inboxes removes most of these rows at the
-- source and is what stations exist for.
--
-- Added rather than rebuilt: two nullable columns need no table rebuild, and
-- entry into the NULL state is a data fact rather than a constraint to enforce.
-- ============================================================================
BEGIN;

ALTER TABLE channel ADD COLUMN station_a TEXT;
ALTER TABLE channel ADD COLUMN station_b TEXT;

-- Best available reconstruction for existing rows. NULL where an endpoint has
-- already been unbound — see the limit above.
UPDATE channel
   SET station_a = (SELECT station_id FROM endpoint WHERE id = channel.endpoint_a),
       station_b = (SELECT station_id FROM endpoint WHERE id = channel.endpoint_b);

-- Both column orders are matched by the revocation predicate, so index both.
CREATE INDEX idx_channel_station_a ON channel(station_a) WHERE station_a IS NOT NULL;
CREATE INDEX idx_channel_station_b ON channel(station_b) WHERE station_b IS NOT NULL;

INSERT INTO schema_migration(version) VALUES (8);

COMMIT;
