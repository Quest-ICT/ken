package comm

import (
	"context"
	"database/sql"
	"embed"
	"io/fs"
	"path/filepath"
	"sort"
	"testing"

	"github.com/Quest-ICT/ken/internal/dbmigrate"
)

// The delivery split is a REBUILD of the busiest table in comm.db, on a live
// deployment holding real conversations. A fresh-install test proves nothing about
// that: it exercises the new schema, never the migration.
//
// So this one applies 0001–0008, writes the kind of rows a running deployment has —
// a queued message, a delivered-unacked one, an acked one with its body swept, an
// attachment, a reply chain — and only then applies 0009 and checks what survived.
//
// It exists because the migration this is adapted from would have silently emptied
// the delivery table on every deployment that ran it. That defect was invisible to
// inspection and to a fresh-install test, and visible immediately here.
func migrateThrough(t *testing.T, db *sql.DB, upTo int, mfs embed.FS) {
	t.Helper()
	files, err := fs.Glob(mfs, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	for _, f := range files {
		v := dbmigrate.Version(f)
		if v == 0 || v > upTo {
			continue
		}
		body, err := mfs.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatalf("apply %s: %v", f, err)
		}
	}
}

func TestDeliverySplitMigratesALiveDatabaseWithoutLosingAnything(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "comm.db")

	// Open the store WITHOUT migrating, so the schema can be built to version 8.
	st, err := Open(path, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	migrateThrough(t, st.W, 8, migrationFS)

	ctx := context.Background()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := st.W.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	// Two endpoints, one of them staffed — so the migration has both party forms to
	// derive, 's:' and 'e:'.
	exec(`INSERT INTO endpoint(id, endpoint_id, secret_sha256, token_id, actor_id, label, station_id)
	      VALUES(1,'epA','h','tok',1,'a','st-alpha'), (2,'epB','h','tok',1,'b',NULL)`)
	exec(`INSERT INTO channel(id, channel_id, space_id, owner_actor_id, endpoint_a, endpoint_b, state)
	      VALUES(1,'chan1',1,1,1,2,'open')`)

	// Four messages covering every state a real database holds, including an acked
	// one whose body the retention sweep already blanked.
	exec(`INSERT INTO message(id, message_id, channel_id, seq, sender_endpoint, recipient_endpoint,
	        body, body_sha256, body_bytes, state, delivery_count, expires_at, created_at,
	        first_delivered_at, acked_at, requires_response, reply_deadline_at)
	      VALUES
	        (1,'m1',1,1,1,2,'queued body','h',11,'queued',0,'2099-01-01T00:00:00.000Z','2026-08-01T10:00:00.000Z',NULL,NULL,0,NULL),
	        (2,'m2',1,2,1,2,'delivered body','h',14,'delivered',3,'2099-01-01T00:00:00.000Z','2026-08-01T11:00:00.000Z','2026-08-02T09:00:00.000Z',NULL,1,'2026-08-03T09:00:00.000Z'),
	        (3,'m3',1,1,2,1,NULL,'h',9,'acked',1,'2099-01-01T00:00:00.000Z','2026-08-01T12:00:00.000Z','2026-08-01T12:30:00.000Z','2026-08-01T13:00:00.000Z',0,NULL),
	        (4,'m4',1,3,1,2,'a reply','h',7,'queued',0,'2099-01-01T00:00:00.000Z','2026-08-01T13:00:00.000Z',NULL,NULL,0,NULL)`)
	exec(`UPDATE message SET reply_to=3 WHERE id=4`)
	exec(`UPDATE message SET replied_by=4 WHERE id=3`)
	// An attachment, which is the other table with a foreign key into message — the
	// one a cascading DROP would silently orphan by setting its message_id to NULL.
	exec(`INSERT INTO attachment(id, attachment_id, channel_id, sender_endpoint, recipient_endpoint,
	        message_id, name, size_bytes, sha256, transfer, state, expires_at)
	      VALUES(1,'att1',1,2,1,3,'report.txt',10,'h','upload','done','2099-01-01T00:00:00.000Z')`)

	// The migration under test.
	if err := st.Migrate(); err != nil {
		t.Fatalf("0009 failed: %v", err)
	}

	// 1. EVERY MESSAGE SURVIVED, with its body state intact.
	var msgs int
	if err := st.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM message`).Scan(&msgs); err != nil {
		t.Fatal(err)
	}
	if msgs != 4 {
		t.Fatalf("%d messages after the rebuild, want 4", msgs)
	}
	var body sql.NullString
	if err := st.R.QueryRowContext(ctx, `SELECT body FROM message WHERE message_id='m3'`).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if body.Valid {
		t.Error("a swept body came back — the rebuild invented content")
	}

	// 2. EVERY MESSAGE HAS ITS DELIVERY ROW, with the state carried over. This is the
	// assertion the source migration would have failed: the table exists and is empty.
	var deliveries int
	if err := st.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM delivery`).Scan(&deliveries); err != nil {
		t.Fatal(err)
	}
	if deliveries != 4 {
		t.Fatalf("%d delivery rows for 4 messages.\n"+
			"Every recipient, every ack, every unread message lives in this table — a migration that reports success with it empty is the whole reason this test exists.", deliveries)
	}
	for _, tc := range []struct{ msg, state, party string }{
		{"m1", "queued", "e:2"},       // recipient endpoint 2 is unstaffed
		{"m2", "delivered", "e:2"},    //
		{"m3", "acked", "s:st-alpha"}, // recipient endpoint 1 IS staffed
		{"m4", "queued", "e:2"},
	} {
		var state, party string
		var count int
		if err := st.R.QueryRowContext(ctx, `
SELECT d.state, d.party_key, d.delivery_count FROM delivery d
JOIN message m ON m.id = d.message_row WHERE m.message_id=?`, tc.msg).Scan(&state, &party, &count); err != nil {
			t.Fatalf("%s: %v", tc.msg, err)
		}
		if state != tc.state {
			t.Errorf("%s delivery state = %q, want %q", tc.msg, state, tc.state)
		}
		if party != tc.party {
			t.Errorf("%s party = %q, want %q — a staffed recipient must migrate to its STATION, or a reconnecting session loses its mail", tc.msg, party, tc.party)
		}
	}
	// The redelivery counter is per-recipient state and must not reset to zero.
	var dc int
	if err := st.R.QueryRowContext(ctx, `
SELECT d.delivery_count FROM delivery d JOIN message m ON m.id=d.message_row WHERE m.message_id='m2'`).Scan(&dc); err != nil {
		t.Fatal(err)
	}
	if dc != 3 {
		t.Errorf("delivery_count = %d, want 3 — a recipient would stop being able to tell a redelivery from a first sight", dc)
	}

	// 3. THE ATTACHMENT STILL POINTS AT ITS MESSAGE. attachment.message_id is
	// ON DELETE SET NULL, so a cascading drop severs every file from its message
	// without deleting a row or raising an error.
	var attMsg sql.NullInt64
	if err := st.R.QueryRowContext(ctx, `SELECT message_id FROM attachment WHERE attachment_id='att1'`).Scan(&attMsg); err != nil {
		t.Fatal(err)
	}
	if !attMsg.Valid || attMsg.Int64 != 3 {
		t.Errorf("the attachment's message link is %v, want 3 — the drop severed it", attMsg)
	}

	// 4. THE REPLY CHAIN SURVIVED, across the table it points into.
	var replyTo sql.NullInt64
	if err := st.R.QueryRowContext(ctx, `SELECT reply_to FROM message WHERE message_id='m4'`).Scan(&replyTo); err != nil {
		t.Fatal(err)
	}
	if !replyTo.Valid || replyTo.Int64 != 3 {
		t.Errorf("reply_to = %v, want 3", replyTo)
	}
	var repliedBy sql.NullInt64
	if err := st.R.QueryRowContext(ctx, `SELECT replied_by FROM delivery WHERE message_row=3`).Scan(&repliedBy); err != nil {
		t.Fatal(err)
	}
	if !repliedBy.Valid || repliedBy.Int64 != 4 {
		t.Errorf("replied_by = %v on the delivery row, want 4 — the answer link is per-recipient now and must carry across", repliedBy)
	}

	// 5. SCOPE NUMBERING IS PER SCOPE AND CONTIGUOUS. The old seq was per (channel,
	// sender), so two senders each had their own 1, 2, 3 — the numbers collide when
	// merged, and ack_up_to_seq is a RANGE over them.
	rows, err := st.R.QueryContext(ctx, `SELECT message_id, scope_id, scope_seq FROM message ORDER BY scope_seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var seqs []int64
	for rows.Next() {
		var mid, scope string
		var seq int64
		if err := rows.Scan(&mid, &scope, &seq); err != nil {
			t.Fatal(err)
		}
		if scope != "ch:chan1" {
			t.Errorf("%s has scope %q, want ch:chan1", mid, scope)
		}
		seqs = append(seqs, seq)
	}
	for i, s := range seqs {
		if s != int64(i+1) {
			t.Fatalf("scope sequence is %v, want 1..%d with no duplicates — colliding numbers make a range ack settle mail nobody read", seqs, len(seqs))
		}
	}

	// 6. THE COUNTER CONTINUES FROM THE MIGRATED DATA rather than restarting, which
	// would collide the next send with an existing row.
	var next int64
	if err := st.R.QueryRowContext(ctx, `SELECT next_seq FROM scope_counter WHERE scope_id='ch:chan1'`).Scan(&next); err != nil {
		t.Fatal(err)
	}
	if next != int64(len(seqs))+1 {
		t.Errorf("next_seq = %d, want %d", next, len(seqs)+1)
	}

	// 7. AND NOTHING DANGLES. Migrate() runs this check itself, so reaching here with
	// a clean result is partly a check on the check.
	var fkRows int
	if err := st.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&fkRows); err != nil {
		t.Fatal(err)
	}
	if fkRows != 0 {
		t.Errorf("%d dangling foreign key references after the rebuild", fkRows)
	}
}
