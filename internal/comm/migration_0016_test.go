package comm

import (
	"context"
	"strings"
	"testing"
)

// THE POLL'S NOTICE QUERY MUST NOT SCAN THE WHOLE MESSAGE TABLE.
//
// NoticesFor runs on every comm_poll and filters `WHERE m.sender_party = ?1`. Without an
// index that is `SCAN m`, so a caller's poll gets slower as OTHER sessions accumulate
// history — a quiet session paying for a noisy deployment. Measured before this migration
// at 0.511 -> 37.710 ms/call between 1k and 100k total messages, with the caller's own
// inbox held constant at five and NO notices returned: the full cost paid to learn there is
// nothing to report.
//
// ASSERTS THE PLAN, NOT A DURATION. A timing assertion on a build machine is a flake
// generator and would not say WHY it got slow. The plan is the fact that matters, and it is
// read from a real connection rather than inferred from the SQL.
func TestNoticeQueryUsesAnIndexRatherThanScanningEveryMessage(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()

	plan := func(q string) string {
		t.Helper()
		rows, err := st.R.QueryContext(ctx, "EXPLAIN QUERY PLAN "+q)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var a, b, c int
			var detail string
			if err := rows.Scan(&a, &b, &c, &detail); err != nil {
				t.Fatal(err)
			}
			out = append(out, detail)
		}
		return strings.Join(out, " | ")
	}

	// The shape NoticesFor uses: sender + kind.
	got := plan(`SELECT m.message_id FROM message m WHERE m.sender_party = 's:x' AND m.kind = 'message'`)
	if strings.Contains(got, "SCAN m") {
		t.Errorf("the notice query still scans every message: %q\n"+
			"That makes one session's poll cost proportional to the WHOLE deployment's history, "+
			"which is the coupling migration 0016 exists to remove.", got)
	}
	if !strings.Contains(got, "idx_message_sender") {
		t.Errorf("plan does not use idx_message_sender: %q", got)
	}

	// POSITIVE CONTROL ON THE INSTRUMENT. If EXPLAIN QUERY PLAN reported "SCAN" for
	// nothing at all, the assertion above would pass on a broken probe. A query with no
	// usable index MUST still say SCAN.
	ctl := plan(`SELECT m.message_id FROM message m WHERE m.body_sha256 = 'nope'`)
	if !strings.Contains(ctl, "SCAN") {
		t.Fatalf("the control query did not report a scan (%q) — EXPLAIN QUERY PLAN is not "+
			"reporting what this test assumes, so the assertion above proves nothing", ctl)
	}
}
