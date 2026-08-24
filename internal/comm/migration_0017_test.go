package comm

import (
	"context"
	"strings"
	"testing"
)

// MIGRATION 0017 MUST RELAX TWO COLUMNS, TIGHTEN A THIRD, AND LOSE NOTHING.
//
// It is not a table rebuild — measured at the pinned driver, ALTER COLUMN DROP/SET NOT NULL
// work in place. This asserts the result rather than the mechanism, because the mechanism is
// a property of a SQLite version that could change under us: if a future driver forces a
// rebuild, this test still describes what the rebuild must produce.
func TestMigration0017MakesAttachmentScopeShaped(t *testing.T) {
	s := newStore(t, DefaultLimits())
	ctx := context.Background()

	var sql string
	if err := s.R.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE name='attachment'`).Scan(&sql); err != nil {
		t.Fatal(err)
	}

	// The two that had to be relaxed.
	for _, col := range []string{"channel_id", "recipient_endpoint"} {
		for _, line := range strings.Split(sql, "\n") {
			if strings.Contains(line, col) && strings.Contains(line, "NOT NULL") {
				t.Errorf("%s is still NOT NULL — a room or pair attachment cannot be written: %s", col, strings.TrimSpace(line))
			}
		}
	}
	// The one that had to be tightened. The scope IS the address now.
	if !strings.Contains(sql, "scope_id TEXT NOT NULL") {
		t.Error("scope_id is not NOT NULL — an attachment could be written with no address at all")
	}
	// WHAT MUST NOT HAVE BEEN LOST. A rebuild that silently dropped a foreign key would
	// leave the table looking right and cascading wrong.
	for _, want := range []string{
		"REFERENCES channel(id) ON DELETE CASCADE",
		"REFERENCES endpoint(id) ON DELETE CASCADE",
		"CHECK (transfer IN ('path','upload'))",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("the table lost %q", want)
		}
	}

	var idxs int
	if err := s.R.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE tbl_name='attachment' AND type='index' AND name NOT LIKE 'sqlite_%'`).
		Scan(&idxs); err != nil {
		t.Fatal(err)
	}
	if idxs < 2 {
		t.Errorf("attachment has %d named indexes; the scope index and the id unique index must both survive", idxs)
	}

	// AND THE INDEX IS NO LONGER PARTIAL. It was `WHERE scope_id IS NOT NULL`, which after
	// 0010 indexed only pre-0010 rows — a covering index over a population that stopped
	// growing. With scope_id NOT NULL the predicate can never exclude anything.
	var idxSQL string
	if err := s.R.QueryRowContext(ctx,
		`SELECT COALESCE(sql,'') FROM sqlite_master WHERE name='idx_attachment_scope'`).Scan(&idxSQL); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(idxSQL, "WHERE") {
		t.Errorf("idx_attachment_scope is still partial: %s", idxSQL)
	}

	// A ROOM-SHAPED ROW MUST NOW BE WRITABLE — the whole point of the migration. A real
	// sender endpoint, because `sender_endpoint` is still NOT NULL and correctly so: an
	// offer always comes FROM exactly one connection, however many parties receive it.
	ep := stationEndpoint(t, s, "tok-a", "st-alpha")
	if _, err := s.W.ExecContext(ctx, `
INSERT INTO attachment(attachment_id, scope_id, sender_endpoint, name, size_bytes, sha256, transfer, expires_at)
VALUES('probe','r:room1',?,'f.txt',5,'x','upload','2030-01-01')`, ep.ID); err != nil {
		t.Fatalf("a scope-shaped attachment with NULL channel_id and NULL recipient could not be written: %v", err)
	}
	// CONTROL: the sender is still required, so this test is not simply observing that
	// every constraint was dropped.
	if _, err := s.W.ExecContext(ctx, `
INSERT INTO attachment(attachment_id, scope_id, name, size_bytes, sha256, transfer, expires_at)
VALUES('probe2','r:room1','f.txt',5,'x','upload','2030-01-01')`); err == nil {
		t.Fatal("an attachment with no sender was accepted — the migration relaxed more than it should")
	}
}

// THE RE-BACKFILL IS NOT COSMETIC: without it the tightening aborts and the upgrade blocks.
// Every attachment written since 0010 carries scope_id NULL, because OfferFile never set the
// column the seam added.
func TestMigration0017BackfillsScopeFromChannel(t *testing.T) {
	s := newStore(t, DefaultLimits())
	ctx := context.Background()
	var nulls int
	if err := s.R.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM attachment WHERE scope_id IS NULL`).Scan(&nulls); err != nil {
		t.Fatal(err)
	}
	if nulls != 0 {
		t.Fatalf("%d attachment(s) have no scope after the migration", nulls)
	}
}
