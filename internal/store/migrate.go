package store

import (
	"context"

	"github.com/Quest-ICT/ken/internal/dbschema"
	"github.com/Quest-ICT/ken/schema"
)

// Migrate is a misnomer kept for its callers: it does not migrate anything.
//
// It creates ken.db from schema/ken.sql when the file is empty, checks the recorded version
// otherwise, and refuses to continue if the database is not at the version this binary requires.
// See package schema for why the rewrite lives outside the server, and internal/dbschema for why
// the refusal is the load-bearing half.
//
// ken.db is the DURABLE database — the knowledge base and every station's notebook, tasks, locker
// and vault — so there is no "delete it and start again" here, which is exactly why an unexpected
// version stops the boot rather than being repaired in place by code nobody is watching.
func (s *Store) Migrate() error {
	return dbschema.Apply(context.Background(), s.W, s.R,
		schema.Ken, schema.KenVersion, "ken.db", "docs/UPGRADING-THE-DATABASE.md")
}

// schemaVersion reports the version recorded in this database, or 0 when it is empty.
func (s *Store) schemaVersion() (int, error) {
	return dbschema.Version(context.Background(), s.R)
}
