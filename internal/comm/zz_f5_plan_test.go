package comm

import (
	"context"
	"testing"
)

// TestF5QueryPlans checks what messageByID's "3 joins + correlated subquery"
// actually costs the planner.
func TestF5QueryPlans(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, DefaultLimits())
	fillHubN(t, st, 15, 64, 256)

	plans := map[string]string{
		"messageByID": `SELECT m.message_id, c.channel_id, m.seq, se.endpoint_id, m.body,
       (SELECT r.message_id FROM message r WHERE r.id = m.reply_to),
       a.attachment_id
FROM message m
JOIN channel  c  ON c.id  = m.channel_id
JOIN endpoint se ON se.id = m.sender_endpoint
LEFT JOIN attachment a ON a.message_id = m.id
WHERE m.message_id=?`,
		"claimUPDATE": `UPDATE message SET claimed_by_endpoint=? WHERE message_id=?
  AND (claimed_by_endpoint IS NULL OR claimed_by_endpoint = ? OR claim_expires_at <= '')`,
		"deliveryUPDATE": `UPDATE message SET state='delivered', delivery_count=delivery_count+1 WHERE message_id=?`,
	}
	for name, q := range plans {
		rows, err := st.R.QueryContext(ctx, "EXPLAIN QUERY PLAN "+q, "x", "x", "x")
		if err != nil {
			rows, err = st.R.QueryContext(ctx, "EXPLAIN QUERY PLAN "+q, "x", "x")
			if err != nil {
				rows, err = st.R.QueryContext(ctx, "EXPLAIN QUERY PLAN "+q, "x")
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
			}
		}
		for rows.Next() {
			var id, parent, notused int
			var detail string
			if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
				t.Fatal(err)
			}
			t.Logf("%-15s %s", name, detail)
		}
		rows.Close()
	}
}
