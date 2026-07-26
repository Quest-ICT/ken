-- ============================================================================
-- Ken — migration 0005: enforce actor uniqueness
-- ============================================================================
-- A UNIQUE index on (kind, display_name) makes get-or-create race-free:
-- FindOrCreateActor and CreateHumanUser use INSERT ... ON CONFLICT against it,
-- so a cross-pool SELECT-then-INSERT can no longer create duplicate actors.
-- ============================================================================
BEGIN;

CREATE UNIQUE INDEX idx_actor_kind_name ON actor(kind, display_name);

INSERT INTO schema_migration(version) VALUES (5);

COMMIT;
