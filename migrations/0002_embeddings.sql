-- ============================================================================
-- Ken — migration 0002: embeddings (semantic search)
-- ============================================================================
-- A plain BLOB table — NO SQLite extension. Vectors are little-endian float32
-- arrays; cosine KNN is computed in Go (brute-force, which is fine at
-- single-user scale — the design's "flat" approach). The table is always
-- created but stays EMPTY until an embedding provider is configured
-- (KEN_EMBED_*) and `ken embed backfill` runs. Search only consults it when a
-- query vector is supplied, so it is a no-op when embeddings are off.
-- ============================================================================
BEGIN;

CREATE TABLE entry_embedding (
  version_id INTEGER PRIMARY KEY REFERENCES entry_version(id) ON DELETE CASCADE,
  model_id   TEXT    NOT NULL,
  dim        INTEGER NOT NULL,
  vec        BLOB    NOT NULL
);

INSERT INTO schema_migration(version) VALUES (2);

COMMIT;
