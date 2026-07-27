-- ============================================================================
-- Ken COMM — migration 0004: human label on the channel
-- ============================================================================
-- The console identifies a channel by its opaque channel_id and by the labels of
-- its two endpoints ("public-dev ↔ ken-prod-ops"). Neither is how a human thinks
-- of a channel: the opaque id is for machines, and endpoint labels drift and blur
-- once channels accumulate ("which of these was the dev↔prod one?").
--
-- The human already NAMES the channel at the one moment they think about it — when
-- they mint the pairing code, whose optional label ("Ken dev <-> prod") is exactly
-- that name. But that label lived only on the pairing_code row, which is transient
-- (unredeemed codes are swept; even a consumed code is not retained on purpose).
-- Copy it onto the durable channel at creation so the console can lead with the
-- name the operator chose.
--
-- Nullable and additive: pre-existing channels (and any created without a code
-- label) simply have none, and the console falls back to the endpoint labels.
BEGIN;

ALTER TABLE channel ADD COLUMN label TEXT;

INSERT INTO schema_migration(version) VALUES (4);

COMMIT;
