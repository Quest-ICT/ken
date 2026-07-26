package store

import (
	"io/fs"
	"testing"

	"github.com/Quest-ICT/ken/migrations"
)

// TestMigrateIdempotent locks two properties for 1.0: running Migrate() again is a
// no-op success, and every embedded migration is recorded in schema_migration
// exactly once — catching a non-idempotent re-run or a future migration that forgets
// to self-record its version.
func TestMigrateIdempotent(t *testing.T) {
	st := newStore(t) // newStore already Migrate()s once
	if err := st.Migrate(); err != nil {
		t.Fatalf("second Migrate(): %v", err)
	}

	files, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	want := 0
	applied, err := st.appliedVersions()
	if err != nil {
		t.Fatalf("appliedVersions: %v", err)
	}
	for _, f := range files {
		v := versionOf(f)
		if v == 0 {
			continue
		}
		want++
		if !applied[v] {
			t.Errorf("migration %s (v%d) not recorded as applied", f, v)
		}
	}

	var rows, distinct int
	if err := st.R.QueryRow(`SELECT COUNT(*), COUNT(DISTINCT version) FROM schema_migration`).Scan(&rows, &distinct); err != nil {
		t.Fatalf("count schema_migration: %v", err)
	}
	if rows != distinct {
		t.Errorf("schema_migration has duplicate versions: %d rows but %d distinct", rows, distinct)
	}
	if distinct != want {
		t.Errorf("recorded %d migration versions, want %d embedded", distinct, want)
	}
}
