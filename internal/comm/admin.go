package comm

import "context"

// *** AdminChannel AND ListChannelsForConsole ARE DELETED WITH THE CHANNEL (slice 7, 5.0.0). ***
//
// They powered the Comm page's channel table. A conversation between two stations is now the LINK,
// listed on the Stations page, where the control is Suspend — reversible, where closing a channel
// was not.

// PendingCode AND ListPendingCodes ARE DELETED, with the pairing code they described. The console
// listed unredeemed codes so an operator could see what they had minted; nothing mints one now.

// Stats is the operator's at-a-glance view, and the source for metrics.
type Stats struct {
	Endpoints int
	// Unacked counts MESSAGES with at least one outstanding delivery.
	Unacked int
	// DeliveriesUnacked counts the outstanding DELIVERIES themselves.
	//
	// Before rooms these two were always equal, so nothing distinguished them and nothing
	// had to. One body sent to three members is 1 message and 3 deliveries, and the pair
	// answers two different operational questions: "how many things have not landed" is
	// messages, "how much work is stuck" is deliveries. Neither is wrong, which is exactly
	// why the ambiguity is expensive — the numbers simply differ and nothing ever looks
	// inconsistent enough to prompt a question.
	//
	// ken-prod-ops predicted a metric step in deliveries, observed it in messages, and
	// chased a phantom 3-unit gap through three sampler ticks before finding that both
	// numbers were right.
	DeliveriesUnacked int
	BodyBytes         int64 // retained message bodies; the thing that grows a disk
	Files             int   // live attachments (offered or awaiting delivery)
	FileBytes         int64 // relay bytes currently held on disk
}

// StatsFor reports counters for this instance.
//
// MESSAGE COUNTERS SCOPE BY THE SENDER'S ENDPOINT, NOT BY A CHANNEL. They used to say
// `FROM message m JOIN channel c ON c.id=m.channel_id`, and a room or broadcast message has
// channel_id NULL, so an INNER JOIN dropped every one of them: the operator's at-a-glance
// view and the `ken_comm_messages_unacked` metric both counted channel traffic only, and
// silently reported a busy room-only deployment as idle.
//
// `message.sender_endpoint` is NOT NULL and references `endpoint`, which already carries
// `space_id` and indexes it — so the sender's space is available for EVERY scope without a
// schema change. (There is a `message.space_id` column, added by migration 0009; it is
// written by nothing and read by nothing, so populating and backfilling it would be a data
// rewrite to reach a fact already one join away.)
//
// THE ONE ASSUMPTION, stated so it can be rechecked: nothing moves an endpoint between
// spaces. Verified — every `UPDATE endpoint SET` in this package touches only last_seen_at,
// station binding, or revocation. If a space-move is ever added, a message's attributed
// space would follow its sender, where `channel.space_id` was fixed at creation; that is
// the moment to revisit this.
//
// BODY BYTES COMES FROM body_bytes, NOT FROM LENGTH(body). SQLite's LENGTH() on a TEXT
// value counts CHARACTERS; a gauge named _bytes over a UTF-8 column therefore under-reports
// by however much non-ASCII the traffic carries. Measured on production 3.5.1: 308,940
// reported against 310,655 actual, 65 of 70 body-bearing rows affected — 0.55% low, which
// is exactly the kind of wrong that survives review because it looks right.
//
// body_bytes was already written at every insert site and already covered by a test that
// says it "must survive for accounting"; the accounting metric simply did not use it.
// FileBytes two lines down always did the equivalent thing with stored_bytes.
//
// THIS IS ONLY CORRECT BECAUSE BLANKING WRITES NULL, NOT ”. The predicate below excludes
// blanked rows by `body IS NOT NULL`, and those rows keep their body_bytes forever — on
// production that is 1.27 MB of accounting for text that no longer exists. If retention
// ever blanks to an empty string instead, this line silently reports several times the
// truth. The dependency is on a choice made in message.go, so it is written down here.
//
// ATTACHMENT COUNTERS KEEP THE CHANNEL JOIN, deliberately. A file offer still binds a
// channel rowid, so there are no room-scoped attachment rows to miss. They become the same
// bug the moment file exchange learns about scopes, and not before.
func (s *Store) StatsFor(ctx context.Context) (Stats, error) {
	var st Stats
	// THE TWO FILE COUNTERS ARE SCOPED THROUGH THE SENDER, NOT THROUGH THE CHANNEL.
	// They INNER JOINed the channel table, so an attachment belonging to a room or a pair —
	// which carries no channel_id since 0017 — was INVISIBLE to both. An operator would have
	// read Files=0 while the relay held bytes, which is worse than an obviously broken number.
	// Every attachment has a sender endpoint and every endpoint has a space, so this covers
	// all four scope kinds and matches how the message counters above resolve the same thing.
	err := s.R.QueryRowContext(ctx, `
SELECT
  (SELECT COUNT(*) FROM endpoint WHERE revoked_at IS NULL),
  (SELECT COUNT(*) FROM message m JOIN endpoint e ON e.id=m.sender_endpoint
     WHERE EXISTS (SELECT 1 FROM delivery d
            WHERE d.message_row = m.id AND d.state IN ('queued','delivered'))),
  (SELECT COUNT(*) FROM delivery d
     JOIN message m ON m.id = d.message_row
     JOIN endpoint e ON e.id = m.sender_endpoint
     WHERE d.state IN ('queued','delivered')),
  (SELECT COALESCE(SUM(m.body_bytes),0) FROM message m JOIN endpoint e ON e.id=m.sender_endpoint
     WHERE m.body IS NOT NULL),
  (SELECT COUNT(*) FROM attachment a JOIN endpoint e ON e.id=a.sender_endpoint
     WHERE a.state IN ('offered','ready')),
  (SELECT COALESCE(SUM(a.stored_bytes),0) FROM attachment a JOIN endpoint e ON e.id=a.sender_endpoint
     WHERE a.state IN ('offered','ready'))`).
		Scan(&st.Endpoints, &st.Unacked, &st.DeliveriesUnacked,
			&st.BodyBytes, &st.Files, &st.FileBytes)
	return st, err
}

// ConsoleFingerprint returns a single number that changes whenever the console's
// view of a space would look different: an endpoint registered or revoked, a
// channel created/opened/revoked, or messages flowing. It backs the /comm page's live auto-refresh — the page reloads when
// this diverges from the value it was rendered with, and updates its "last
// checked" stamp on every poll.
//
// Distinct prime weights make an accidental collision (two offsetting changes
// summing to the same number) unlikely; this is a change detector, not a checksum,
// so unlikely is enough.
//
// The message term scopes by the SENDER'S ENDPOINT for the reason given on StatsFor: it
// joined `channel` before, so room and broadcast traffic moved this number not at all — the
// page's live auto-refresh never fired for a room, and an operator watching /comm during
// active room traffic saw a static screen.
func (s *Store) ConsoleFingerprint(ctx context.Context) (int64, error) {
	var n int64
	err := s.R.QueryRowContext(ctx, `
SELECT
  (SELECT COUNT(*) FROM endpoint WHERE revoked_at IS NULL) * 2
+ (SELECT COUNT(*) FROM message m JOIN endpoint e ON e.id=m.sender_endpoint
     WHERE EXISTS (SELECT 1 FROM delivery d
            WHERE d.message_row = m.id AND d.state IN ('queued','delivered'))) * 11`).Scan(&n)
	return n, err
}

// Broadcast is one estate-wide announcement, as the console shows it.
//
// METADATA ONLY, AND NEVER THE BODY. Same rule as every other console surface here: bodies are
// deleted on ack and are not the operator's business. What an operator needs is who announced
// something to the whole estate, how wide it went, and — the question 2026-08-31 could not answer
// — WHICH stations have not read it yet.
type Broadcast struct {
	MessageID      string
	SenderParty    string
	IdempotencyKey string
	CreatedAt      string
	ExpiresAt      string
	Addressed      int
	Acked          int
	// UnreadParties are the recipients still holding it unacked, as party keys. The caller
	// resolves them to station names; it holds the ken.db handle and this package must not (S7).
	UnreadParties []string
}

// RecentBroadcasts lists the last n estate-wide announcements, newest first, with the total.
//
// *** WHY THE TOTAL TRAVELS WITH THE LIST. *** A truncated list that cannot say it was truncated
// reads as a complete one — the same shape as a reach of 0 that cannot say the query failed. The
// caller renders "the last 20 of 118", copying the station_vault_read audit trail, which is this
// product's one settled pattern for a bounded log.
//
// IT LIVES IN THE EXPENDABLE DATABASE AND DIES WITH IT. That is correct for an operational view
// and wrong for an audit claim, so the console says which it is, and the DURABLE record of a
// broadcast is the COMM: line in the service log. Putting it in ken.db would run a pointer from
// the durable database into the expendable one, which is S7's forbidden direction.
func (s *Store) RecentBroadcasts(ctx context.Context, n int) ([]Broadcast, int, error) {
	if n <= 0 {
		n = 20
	}
	var total int
	if err := s.R.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM message WHERE scope_id LIKE 'b:%'`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.R.QueryContext(ctx, `
SELECT m.message_id, m.sender_party, COALESCE(m.idempotency_key,''), m.created_at, m.expires_at,
       (SELECT COUNT(*) FROM delivery d WHERE d.message_row = m.id),
       (SELECT COUNT(*) FROM delivery d WHERE d.message_row = m.id AND d.acked_at IS NOT NULL)
  FROM message m
 WHERE m.scope_id LIKE 'b:%'
 ORDER BY m.created_at DESC
 LIMIT ?`, n)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Broadcast
	for rows.Next() {
		var b Broadcast
		if err := rows.Scan(&b.MessageID, &b.SenderParty, &b.IdempotencyKey,
			&b.CreatedAt, &b.ExpiresAt, &b.Addressed, &b.Acked); err != nil {
			return nil, 0, err
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	// The unread parties, per message. A second pass rather than a join, because one row per
	// (message, unread recipient) would make the caller re-group what it just flattened.
	for i := range out {
		urows, err := s.R.QueryContext(ctx, `
SELECT d.party_key FROM delivery d JOIN message m ON m.id = d.message_row
 WHERE m.message_id = ? AND d.acked_at IS NULL
 ORDER BY d.party_key`, out[i].MessageID)
		if err != nil {
			return nil, 0, err
		}
		for urows.Next() {
			var p string
			if err := urows.Scan(&p); err != nil {
				urows.Close()
				return nil, 0, err
			}
			out[i].UnreadParties = append(out[i].UnreadParties, p)
		}
		urows.Close()
		if err := urows.Err(); err != nil {
			return nil, 0, err
		}
	}
	return out, total, nil
}
