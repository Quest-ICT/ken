package comm

import (
	"context"
	"strconv"
	"strings"
	"testing"
)

// MIGRATION 0017 REQUIRES SQLite >= 3.53, AND NOTHING ELSE IN THE REPO SAYS SO.
//
// It relaxes two NOT NULL constraints and tightens a third with `ALTER TABLE ... ALTER
// COLUMN`, which is in-place at 3.53.3 and A SYNTAX ERROR at 3.50.4. That is what makes the
// migration four statements instead of a create-copy-drop-rename rebuild — a real saving, and
// a real new constraint on the deployment.
//
// The floor is pinned today only by go.mod (ncruces/go-sqlite3 v0.35.2, whose wasm module
// v3.2.35303 encodes 3.53.03). A routine dependency downgrade would take it away silently:
// `go build` would still succeed, every existing test would still pass, and migration 0017
// would fail at the first ALTER — on a FRESH INSTALL, which is the one case nobody upgrades
// their way out of.
//
// So the floor is asserted here rather than left to memory. If this test goes red after a
// dependency bump, the choice is to restore the driver or rewrite 0017 as a table rebuild;
// the rebuild is still the documented fallback and the migration header says so.
func TestSQLiteMeetsTheFloorMigration0017Needs(t *testing.T) {
	s := newStore(t, DefaultLimits())
	var v string
	if err := s.R.QueryRowContext(context.Background(), `SELECT sqlite_version()`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(v, ".")
	if len(parts) < 2 {
		t.Fatalf("unparseable sqlite_version(): %q", v)
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		t.Fatalf("unparseable sqlite_version(): %q", v)
	}
	if major < 3 || (major == 3 && minor < 53) {
		t.Fatalf("SQLite %s is below the 3.53 floor migration 0017 needs.\n"+
			"ALTER TABLE ... ALTER COLUMN DROP/SET NOT NULL is a syntax error below it, so a FRESH "+
			"install fails at 0017 while an existing deployment carries on.\n"+
			"Restore the driver, or rewrite 0017 as a create-copy-drop-rename rebuild.", v)
	}

	// AND THE CAPABILITY ITSELF, not just the version number — a version check that never
	// exercises the feature is a proxy, and this project has been bitten by proxies.
	if _, err := s.W.ExecContext(context.Background(),
		`ALTER TABLE attachment ALTER COLUMN name DROP NOT NULL`); err != nil {
		t.Fatalf("sqlite reports %s but ALTER COLUMN DROP NOT NULL failed: %v", v, err)
	}
}
