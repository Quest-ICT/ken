package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"testing/fstest"
)

// The migration ken.db has not needed yet, and the reason its runner is now
// hardened. 0002 widens a column by the only route SQLite offers: build a new
// table, copy, DROP the old one, rename. With foreign keys ENFORCED that DROP runs
// an implicit DELETE FROM, which fires every ON DELETE action pointing at the
// table — SET NULL severs, CASCADE deletes — and raises nothing. ken.db is full of
// both: station has eight cascading children, entry three.
var rebuildFS = fstest.MapFS{
	"0001_init.sql": &fstest.MapFile{Data: []byte(`BEGIN;
CREATE TABLE schema_migration(version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE parent(id INTEGER PRIMARY KEY, name TEXT);
CREATE TABLE child(id INTEGER PRIMARY KEY, parent_id INTEGER REFERENCES parent(id) ON DELETE SET NULL);
INSERT INTO parent(id, name) VALUES (1, 'p');
INSERT INTO child(id, parent_id) VALUES (1, 1);
INSERT INTO schema_migration(version) VALUES (1);
COMMIT;`)},
	"0002_rebuild.sql": &fstest.MapFile{Data: []byte(`BEGIN;
CREATE TABLE parent_new(id INTEGER PRIMARY KEY, name TEXT NOT NULL DEFAULT '');
INSERT INTO parent_new(id, name) SELECT id, name FROM parent;
DROP TABLE parent;
ALTER TABLE parent_new RENAME TO parent;
INSERT INTO schema_migration(version) VALUES (2);
COMMIT;`)},
}

// unmigratedStore opens a store WITHOUT applying ken.db's own migrations, so a
// synthetic set can be applied to an empty file. newStore (write_test.go) migrates.
func unmigratedStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func childParent(t *testing.T, st *Store) sql.NullInt64 {
	t.Helper()
	var v sql.NullInt64
	if err := st.R.QueryRow(`SELECT parent_id FROM child WHERE id = 1`).Scan(&v); err != nil {
		t.Fatalf("read child: %v", err)
	}
	return v
}

func TestKenRunnerRebuildsATableWithoutSeveringItsChildren(t *testing.T) {
	ctx := context.Background()

	// POSITIVE CONTROL, RUN FIRST. The same two files applied the way this runner
	// applied them until now — straight onto the pool, enforcement on — must SEVER the
	// link. If it ever stops severing, the fixture has stopped reproducing the hazard
	// and the assertion below proves nothing.
	ctl := unmigratedStore(t)
	for _, name := range []string{"0001_init.sql", "0002_rebuild.sql"} {
		if _, err := ctl.W.ExecContext(ctx, string(rebuildFS[name].Data)); err != nil {
			t.Fatalf("control: apply %s: %v", name, err)
		}
	}
	if link := childParent(t, ctl); link.Valid {
		t.Fatalf("control: child.parent_id = %d after an unhardened rebuild, want NULL — "+
			"the fixture no longer reproduces the silent severing this test exists to catch",
			link.Int64)
	}

	// THE PATH UNDER TEST is Store's own wiring: its writer pool pinned, its reader
	// pool for schema_migration. Only the migration set is injected, because ken.db has
	// no rebuild migration yet and a fresh-install test proves nothing about one.
	st := unmigratedStore(t)
	if err := st.migrateFrom(ctx, rebuildFS, "*.sql"); err != nil {
		t.Fatalf("migrateFrom: %v", err)
	}
	link := childParent(t, st)
	if !link.Valid || link.Int64 != 1 {
		t.Fatalf("child.parent_id = %v after the rebuild, want 1 — the DROP severed it, "+
			"which is what happens when foreign keys are enforced during a table rewrite", link)
	}
	var maxv int
	if err := st.R.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migration`).Scan(&maxv); err != nil {
		t.Fatalf("read schema_migration: %v", err)
	}
	if maxv != 2 {
		t.Errorf("schema_migration MAX(version) = %d, want 2", maxv)
	}
}
