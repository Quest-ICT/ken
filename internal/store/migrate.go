package store

import (
	"context"
	"io/fs"

	"github.com/Quest-ICT/ken/internal/dbmigrate"
	"github.com/Quest-ICT/ken/migrations"
)

// Migrate applies ken.db's embedded migrations in lexical order, skipping versions
// already recorded in schema_migration. It is idempotent. All migrations are plain
// SQL (no loadable extensions), so all apply unconditionally; the embeddings table
// is created empty and only populated when a provider is configured.
//
// The runner is internal/dbmigrate — the SAME one comm.db uses, including
// disabling foreign keys for the duration of the run and proving with
// PRAGMA foreign_key_check that the result still holds together. ken.db needs that
// MORE than comm.db does, not less: station has eight ON DELETE CASCADE children,
// entry has three, and a DROP TABLE with enforcement on fires every one of them
// without raising. Until this call was routed there, this runner had none of it,
// through nineteen migrations.
func (s *Store) Migrate() error {
	return s.migrateFrom(context.Background(), migrations.FS, "*.sql")
}

// migrateFrom is Migrate with the migration set injected. It exists so a REBUILD
// migration can be exercised against this store's own wiring before ken.db has
// one: comm.db learned at migration 0009 that a fresh-install test proves nothing
// about a migration that rewrites a populated table.
func (s *Store) migrateFrom(ctx context.Context, fsys fs.FS, glob string) error {
	return dbmigrate.Run(ctx, s.W, s.R, fsys, glob)
}

// appliedVersions reads the schema_migration table; a missing table (fresh db)
// yields an empty set rather than an error.
func (s *Store) appliedVersions() (map[int]bool, error) {
	return dbmigrate.Applied(context.Background(), s.R)
}

// versionOf parses the leading integer of a migration filename ("0001_init.sql" -> 1).
func versionOf(filename string) int { return dbmigrate.Version(filename) }
