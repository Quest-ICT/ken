package comm

import (
	"context"
	"testing"
)

func TestSchemaReaches15WithTheLinkMirror(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	var v int
	if err := st.R.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migration`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	// AT LEAST 15, not exactly 15. This test's subject is the link mirror and its CHECK,
	// not which migration happens to be last — and an equality assertion here fails on
	// every future migration, which trains the next person to edit the number rather than
	// read the test. Migration 0016 broke it that way on 2026-08-20. A skipped migration
	// is still caught: the table and CHECK assertions below would fail.
	if v < 15 {
		t.Fatalf("comm schema version = %d, want at least 15 — migration 0015 did not apply", v)
	}
	var name string
	if err := st.R.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='table' AND name='station_link_mirror'`).Scan(&name); err != nil {
		t.Fatalf("station_link_mirror missing from a freshly migrated database: %v", err)
	}
	// The CHECK must be real: a mis-ordered pair has to be REFUSED by the database, not
	// only by the Go that happens to sort first.
	if _, err := st.W.ExecContext(ctx,
		`INSERT INTO station_link_mirror(station_a, station_b) VALUES('zzz','aaa')`); err == nil {
		t.Fatal("the database accepted a mis-ordered pair — CHECK (station_a < station_b) is not enforced, " +
			"so a pair could authorise in one direction and refuse in the other")
	}
}
