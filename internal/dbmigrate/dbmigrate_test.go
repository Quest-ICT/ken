package dbmigrate_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/ncruces/go-sqlite3/driver"

	"github.com/Quest-ICT/ken/internal/dbmigrate"
)

// openPools mirrors what both stores open with: foreign keys ENFORCED on every
// connection, and a writer pool capped at one.
func openPools(t *testing.T) (*sql.DB, *sql.DB) {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "t.db") +
		"?_pragma=busy_timeout(10000)&_pragma=journal_mode(wal)&_pragma=foreign_keys(on)"
	w, err := driver.Open(dsn + "&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	w.SetMaxOpenConns(1)
	r, err := driver.Open(dsn)
	if err != nil {
		_ = w.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close(); _ = r.Close() })
	return w, r
}

const baseSchema = `BEGIN;
CREATE TABLE schema_migration(version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE parent(id INTEGER PRIMARY KEY);
CREATE TABLE child(id INTEGER PRIMARY KEY, parent_id INTEGER NOT NULL REFERENCES parent(id));
INSERT INTO parent(id) VALUES (1);
INSERT INTO schema_migration(version) VALUES (1);
COMMIT;`

func child(body string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte("BEGIN;\n" + body + "\nINSERT INTO schema_migration(version) VALUES (2);\nCOMMIT;")}
}

// Run turns foreign keys OFF for the whole migration, so foreign_key_check at the
// end is the ONLY thing between a broken rewrite and a silent success. A check that
// cannot tell a broken run from an intact one is not a check, so this asserts both
// directions over the same fixture.
func TestRunRefusesAMigrationThatLeavesADanglingReference(t *testing.T) {
	ctx := context.Background()

	// POSITIVE CONTROL, FIRST: the identical shape with a VALID reference must SUCCEED.
	// Without it, "Run returned an error" could mean the fixture never applied at all.
	w, r := openPools(t)
	intact := fstest.MapFS{
		"0001_init.sql":  &fstest.MapFile{Data: []byte(baseSchema)},
		"0002_child.sql": child("INSERT INTO child(id, parent_id) VALUES (1, 1);"),
	}
	if err := dbmigrate.Run(ctx, w, r, intact, "*.sql"); err != nil {
		t.Fatalf("control: a run whose references are intact failed: %v", err)
	}
	var v int
	if err := r.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migration`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != 2 {
		t.Fatalf("control: schema_migration MAX(version) = %d, want 2 — the fixture did not apply", v)
	}

	w2, r2 := openPools(t)
	broken := fstest.MapFS{
		"0001_init.sql":  &fstest.MapFile{Data: []byte(baseSchema)},
		"0002_child.sql": child("INSERT INTO child(id, parent_id) VALUES (1, 999);"),
	}
	err := dbmigrate.Run(ctx, w2, r2, broken, "*.sql")
	if err == nil {
		t.Fatal("Run accepted a migration that left child row 1 pointing at a parent that does not exist — " +
			"enforcement is off for the whole run, so nothing else would ever have said so")
	}
	if !strings.Contains(err.Error(), "dangling foreign key") {
		t.Errorf("the error does not name what went wrong: %v", err)
	}
}

// A run with nothing pending must not pin a connection or pay for the check — this
// is every upgrade that crosses no migration.
func TestRunIsANoOpWhenEverythingIsApplied(t *testing.T) {
	ctx := context.Background()
	w, r := openPools(t)
	only := fstest.MapFS{"0001_init.sql": &fstest.MapFile{Data: []byte(baseSchema)}}
	if err := dbmigrate.Run(ctx, w, r, only, "*.sql"); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if err := dbmigrate.Run(ctx, w, r, only, "*.sql"); err != nil {
		t.Fatalf("second run: %v", err)
	}
	var rows, distinct int
	if err := r.QueryRowContext(ctx, `SELECT COUNT(*), COUNT(DISTINCT version) FROM schema_migration`).Scan(&rows, &distinct); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || distinct != 1 {
		t.Errorf("schema_migration has %d rows / %d distinct after two runs, want 1/1", rows, distinct)
	}
}

// A DATABASE FROM THE FUTURE MUST BE REFUSED RATHER THAN QUIETLY ACCEPTED.
//
// `pending` is "embedded files not yet applied", so an older binary against a newer database
// computes an empty set and returns nil — reporting success while every schema assumption it holds
// is wrong. Ken documents rollback as supported (INSTALL.md: point `current` at a previous release
// and restart), and measured against 4.0.0's databases the v3.42.0 binary booted with an entirely
// ordinary startup log before failing on the first query touching a dropped table.
//
// The rule itself is old — forward-only, downgrade unsupported. What was missing is that
// "unsupported" looked exactly like "fine".
func TestADatabaseNewerThanTheBinaryIsRefused(t *testing.T) {
	ctx := context.Background()
	w, r := openPools(t)
	files := fstest.MapFS{"0001_init.sql": &fstest.MapFile{Data: []byte(baseSchema)}}

	// CONTROL: an ordinary run succeeds, and re-running an up-to-date database is a no-op. If
	// either failed, the refusal below would prove nothing about version ordering.
	if err := dbmigrate.Run(ctx, w, r, files, "*.sql"); err != nil {
		t.Fatalf("control: an ordinary migration run failed: %v", err)
	}
	if err := dbmigrate.Run(ctx, w, r, files, "*.sql"); err != nil {
		t.Fatalf("control: re-running an up-to-date database failed: %v", err)
	}

	// Now claim a version this binary has never heard of, exactly as a newer Ken would leave it.
	if _, err := w.ExecContext(ctx, `INSERT INTO schema_migration(version) VALUES (9999)`); err != nil {
		t.Fatal(err)
	}
	err := dbmigrate.Run(ctx, w, r, files, "*.sql")
	if err == nil {
		t.Fatal("a database at schema 9999 was accepted by a binary that knows nothing about it — " +
			"an operator rolling back gets a clean startup log and a broken deployment")
	}
	// The message must name both numbers and the remedy: whoever meets this is mid-rollback and
	// needs to know that restoring the pre-upgrade snapshot is the way out.
	for _, want := range []string{"9999", "downgrading is not supported", "snapshot"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
}
