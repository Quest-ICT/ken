package comm

import "context"

// AdminChannel is one row of the operator's channel view: the channel plus the
// human-readable labels of both ends and its current queue depth.
//
// This exists separately from ListChannels (which answers "what can THIS endpoint
// talk on") because the operator's question is different: "what is talking to what,
// and is anything piling up". A security model whose enforcement point is the human
// needs the human to be able to see, and stop, what is happening.
type AdminChannel struct {
	ChannelID string
	// Label is the human name the operator gave when minting the pairing code
	// (e.g. "Ken dev <-> prod"); empty when the code had no label. This is the
	// identifier a human recognizes — the opaque ChannelID is for machines.
	Label        string
	State        string
	SpaceID      int64
	OwnerActorID int64
	EndpointA    string // endpoint_id of the first party
	LabelA       string
	EndpointB    string // empty until the second party joins
	LabelB       string
	// StationA/StationB name the STATION seated at each end, empty when that seat is
	// unbound. The console groups by these rather than by endpoint id: a channel belongs
	// to the station, and every reader of that station is affected by anything done to it.
	// Preferring the snapshot over the live binding matches ChannelFor — the operator is
	// looking at what was authorised, not at where a binding has since moved.
	StationA  string
	StationB  string
	Unacked   int // messages queued or delivered but not yet acked
	CreatedAt string
	OpenedAt  string
}

// ListChannelsForSpace returns every channel owned by one space, newest first.
//
// Scoped by space even though only one exists today, for the same reason
// ListEndpoints is: an unscoped listing becomes the enumeration surface the moment
// a second human exists, and narrowing it later would be a behavioural break.
func (s *Store) ListChannelsForSpace(ctx context.Context, spaceID int64) ([]AdminChannel, error) {
	rows, err := s.R.QueryContext(ctx, `
SELECT c.channel_id, COALESCE(c.label,''), c.state, c.space_id, c.owner_actor_id,
       ea.endpoint_id, COALESCE(ea.label,''),
       COALESCE(eb.endpoint_id,''), COALESCE(eb.label,''),
       COALESCE(c.station_a, ea.station_id, ''), COALESCE(c.station_b, eb.station_id, ''),
       (SELECT COUNT(*) FROM message m
         WHERE m.channel_id = c.id AND EXISTS (SELECT 1 FROM delivery d
                WHERE d.message_row = m.id AND d.state IN ('queued','delivered'))),
       c.created_at, COALESCE(c.opened_at,'')
FROM channel c
JOIN endpoint ea ON ea.id = c.endpoint_a
LEFT JOIN endpoint eb ON eb.id = c.endpoint_b
WHERE c.space_id = ?
ORDER BY c.created_at DESC`, spaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AdminChannel
	for rows.Next() {
		var c AdminChannel
		if err := rows.Scan(&c.ChannelID, &c.Label, &c.State, &c.SpaceID, &c.OwnerActorID,
			&c.EndpointA, &c.LabelA, &c.EndpointB, &c.LabelB,
			&c.StationA, &c.StationB,
			&c.Unacked, &c.CreatedAt, &c.OpenedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// PendingCode is an unredeemed pairing code shown to the operator so they can see
// what they minted. The code itself is NOT recoverable — only its hash is stored —
// so this shows metadata only; a lost code is re-minted, never recovered.
type PendingCode struct {
	Label     string
	Joined    int // 0 = nobody has redeemed it yet, 1 = waiting for the second session
	ExpiresAt string
	CreatedAt string
}

// ListPendingCodes returns codes that are minted, unexpired, and not yet fully
// consumed.
func (s *Store) ListPendingCodes(ctx context.Context, spaceID int64) ([]PendingCode, error) {
	rows, err := s.R.QueryContext(ctx, `
SELECT COALESCE(p.label,''),
       CASE WHEN p.channel_id IS NULL THEN 0 ELSE 1 END,
       p.expires_at, p.created_at
FROM pairing_code p
WHERE p.space_id = ?
  AND p.consumed_at IS NULL
  AND p.expires_at > strftime('%Y-%m-%dT%H:%M:%fZ','now')
ORDER BY p.created_at DESC`, spaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PendingCode
	for rows.Next() {
		var c PendingCode
		if err := rows.Scan(&c.Label, &c.Joined, &c.ExpiresAt, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Stats is the operator's at-a-glance view, and the source for metrics.
type Stats struct {
	Endpoints    int
	OpenChannels int
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

// StatsFor reports counters for one space.
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
func (s *Store) StatsFor(ctx context.Context, spaceID int64) (Stats, error) {
	var st Stats
	// THE TWO FILE COUNTERS ARE SCOPED THROUGH THE SENDER, NOT THROUGH THE CHANNEL.
	// They INNER JOINed the channel table, so an attachment belonging to a room or a pair —
	// which carries no channel_id since 0017 — was INVISIBLE to both. An operator would have
	// read Files=0 while the relay held bytes, which is worse than an obviously broken number.
	// Every attachment has a sender endpoint and every endpoint has a space, so this covers
	// all four scope kinds and matches how the message counters above resolve the same thing.
	err := s.R.QueryRowContext(ctx, `
SELECT
  (SELECT COUNT(*) FROM endpoint WHERE space_id=? AND revoked_at IS NULL),
  (SELECT COUNT(*) FROM channel  WHERE space_id=? AND state='open'),
  (SELECT COUNT(*) FROM message m JOIN endpoint e ON e.id=m.sender_endpoint
     WHERE e.space_id=? AND EXISTS (SELECT 1 FROM delivery d
            WHERE d.message_row = m.id AND d.state IN ('queued','delivered'))),
  (SELECT COUNT(*) FROM delivery d
     JOIN message m ON m.id = d.message_row
     JOIN endpoint e ON e.id = m.sender_endpoint
     WHERE e.space_id=? AND d.state IN ('queued','delivered')),
  (SELECT COALESCE(SUM(m.body_bytes),0) FROM message m JOIN endpoint e ON e.id=m.sender_endpoint
     WHERE e.space_id=? AND m.body IS NOT NULL),
  (SELECT COUNT(*) FROM attachment a JOIN endpoint e ON e.id=a.sender_endpoint
     WHERE e.space_id=? AND a.state IN ('offered','ready')),
  (SELECT COALESCE(SUM(a.stored_bytes),0) FROM attachment a JOIN endpoint e ON e.id=a.sender_endpoint
     WHERE e.space_id=? AND a.state IN ('offered','ready'))`,
		spaceID, spaceID, spaceID, spaceID, spaceID, spaceID, spaceID).
		Scan(&st.Endpoints, &st.OpenChannels, &st.Unacked, &st.DeliveriesUnacked,
			&st.BodyBytes, &st.Files, &st.FileBytes)
	return st, err
}

// ConsoleFingerprint returns a single number that changes whenever the console's
// view of a space would look different: an endpoint registered or revoked, a
// channel created/opened/revoked, a pairing code minted or consumed, or messages
// flowing. It backs the /comm page's live auto-refresh — the page reloads when
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
func (s *Store) ConsoleFingerprint(ctx context.Context, spaceID int64) (int64, error) {
	var n int64
	err := s.R.QueryRowContext(ctx, `
SELECT
  (SELECT COUNT(*) FROM endpoint WHERE space_id=? AND revoked_at IS NULL) * 2
+ (SELECT COUNT(*) FROM channel  WHERE space_id=?) * 3
+ (SELECT COUNT(*) FROM channel  WHERE space_id=? AND state='open') * 5
+ (SELECT COUNT(*) FROM pairing_code WHERE space_id=? AND consumed_at IS NULL
     AND expires_at > strftime('%Y-%m-%dT%H:%M:%fZ','now')) * 7
+ (SELECT COUNT(*) FROM message m JOIN endpoint e ON e.id=m.sender_endpoint
     WHERE e.space_id=? AND EXISTS (SELECT 1 FROM delivery d
            WHERE d.message_row = m.id AND d.state IN ('queued','delivered'))) * 11`,
		spaceID, spaceID, spaceID, spaceID, spaceID).Scan(&n)
	return n, err
}
