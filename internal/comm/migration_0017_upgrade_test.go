package comm

import (
	"context"
	"testing"
)

// *** THE UPGRADE PATH, WHICH IS THE ONLY PATH THAT CAN GO WRONG. ***
//
// A fresh install runs 0017 against an empty table and cannot fail. An UPGRADE runs it against
// rows written by every prior release — and every attachment written since migration 0010
// carries `scope_id` NULL, because 0010 cut the seam and nothing ever wrote it. So the
// tightening in step 2 has real rows to reject, and without the re-backfill in step 1 it
// aborts and the deployment cannot upgrade at all.
//
// This reconstructs the pre-0017 state on a migrated database rather than driving the old
// binary: relax the column, blank the scopes, and re-run the migration's own statements. It
// tests the STATEMENTS, which is what ships, rather than a build of a previous release.
func TestMigration0017UpgradesRowsWrittenBeforeIt(t *testing.T) {
	s := newStore(t, DefaultLimits())
	ctx := context.Background()
	ex := func(q string, a ...any) {
		t.Helper()
		if _, err := s.W.ExecContext(ctx, q, a...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	ep := stationEndpoint(t, s, "tok-a", "st-alpha")

	// Rebuild the world as 0016 left it: a channel attachment whose scope was never written.
	ex(`ALTER TABLE attachment ALTER COLUMN scope_id DROP NOT NULL`)
	ex(`INSERT INTO channel(channel_id, space_id, owner_actor_id, state, endpoint_a)
	    VALUES('chOLD', 1, 1, 'open', ?)`, ep.ID)
	ex(`INSERT INTO attachment(attachment_id, channel_id, sender_endpoint, recipient_endpoint,
	                          name, size_bytes, sha256, transfer, expires_at, scope_id)
	    VALUES('attOLD',(SELECT id FROM channel WHERE channel_id='chOLD'),?,?,'old.txt',5,'x','upload','2030-01-01',NULL)`,
		ep.ID, ep.ID)

	var nulls int
	if err := s.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM attachment WHERE scope_id IS NULL`).Scan(&nulls); err != nil {
		t.Fatal(err)
	}
	if nulls != 1 {
		t.Fatalf("fixture: %d rows with a NULL scope, want 1 — this test cannot exercise the backfill", nulls)
	}

	// Step 1 of the migration, verbatim.
	ex(`UPDATE attachment
	       SET scope_id = 'ch:' || (SELECT c.channel_id FROM channel c WHERE c.id = attachment.channel_id)
	     WHERE scope_id IS NULL AND channel_id IS NOT NULL`)
	// Step 2 must now succeed. Before the backfill it would abort with a constraint failure,
	// which is a blocked upgrade — the failure mode this ordering exists to prevent.
	ex(`ALTER TABLE attachment ALTER COLUMN scope_id SET NOT NULL`)

	var got string
	if err := s.R.QueryRowContext(ctx,
		`SELECT scope_id FROM attachment WHERE attachment_id='attOLD'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "ch:chOLD" {
		t.Fatalf("the pre-0017 row backfilled to %q, want ch:chOLD — an upgraded attachment lost its address", got)
	}
	// AND IT IS STILL LOADABLE, through the reader that now LEFT JOINs. An INNER join here
	// was fine for this row and fatal for a room one; the reader must serve both.
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	att, err := attachmentByID(ctx, tx, "attOLD")
	if err != nil {
		t.Fatalf("a pre-0017 attachment is no longer loadable after the migration: %v", err)
	}
	if att.ChannelID != "chOLD" || att.ScopeID != "ch:chOLD" {
		t.Fatalf("loaded channel=%q scope=%q", att.ChannelID, att.ScopeID)
	}
}
