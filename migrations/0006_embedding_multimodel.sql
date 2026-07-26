-- 0006: entry_embedding primary key (version_id, model_id).
--
-- Originally the PK was version_id alone, so a version could hold exactly one
-- model's vector; re-embedding under a different model clobbered the old row via
-- INSERT OR REPLACE. Widening the PK to (version_id, model_id) lets multiple
-- models coexist per version (a model switch or A/B overlap) — search already
-- filters candidates by model_id, so it selects the right vectors per query.
--
-- SQLite cannot extend a primary key in place, so this rebuilds the table. Nothing
-- has an inbound foreign key to entry_embedding, so the drop/rename is safe with
-- foreign keys enabled; the vectors are fully regenerable from entry_version in
-- any case.
BEGIN;

CREATE TABLE entry_embedding_new (
  version_id INTEGER NOT NULL REFERENCES entry_version(id) ON DELETE CASCADE,
  model_id   TEXT    NOT NULL,
  dim        INTEGER NOT NULL,
  vec        BLOB    NOT NULL,
  PRIMARY KEY (version_id, model_id)
);

INSERT INTO entry_embedding_new(version_id, model_id, dim, vec)
  SELECT version_id, model_id, dim, vec FROM entry_embedding;

DROP TABLE entry_embedding;
ALTER TABLE entry_embedding_new RENAME TO entry_embedding;

INSERT INTO schema_migration(version) VALUES (6);

COMMIT;
