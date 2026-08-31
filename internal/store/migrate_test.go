package store

import (
	"testing"

	"github.com/Quest-ICT/ken/schema"
)

// TestMigrateIsIdempotentAndRecordsTheVersion.
//
// Migrate creates the database when it is empty and CHECKS it otherwise, so calling it twice must
// be ordinary rather than an error — main.go calls it on every boot, and the second call is the one
// every restart makes.
//
// The version assertion is the half that would otherwise rot silently: a schema file that creates
// every table and records the wrong number leaves the NEXT boot refusing a database this one just
// made, which reads as corruption rather than as a build fault. dbschema.Apply checks it too; this
// pins it from the store's side so the two cannot both be changed to agree on a wrong answer.
func TestMigrateIsIdempotentAndRecordsTheVersion(t *testing.T) {
	s := newStore(t) // calls Migrate once

	got, err := s.schemaVersion()
	if err != nil {
		t.Fatalf("reading schema version: %v", err)
	}
	if got != schema.KenVersion {
		t.Fatalf("a freshly created ken.db records version %d, want %d — the embedded schema and "+
			"the constant beside it disagree", got, schema.KenVersion)
	}

	if err := s.Migrate(); err != nil {
		t.Fatalf("second Migrate on an already-created database: %v\n"+
			"Every restart makes this call; it must be a no-op check, not a re-create.", err)
	}
	again, err := s.schemaVersion()
	if err != nil {
		t.Fatal(err)
	}
	if again != got {
		t.Errorf("the version moved from %d to %d on a second call — Migrate wrote something", got, again)
	}
}
