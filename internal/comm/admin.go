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
	Unacked      int // messages queued or delivered but not yet acked
	CreatedAt    string
	OpenedAt     string
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
       (SELECT COUNT(*) FROM message m
         WHERE m.channel_id = c.id AND m.state IN ('queued','delivered')),
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
	Unacked      int
	BodyBytes    int64 // retained message bodies; the thing that grows a disk
	Files        int   // live attachments (offered or awaiting delivery)
	FileBytes    int64 // relay bytes currently held on disk
}

// StatsFor reports counters for one space.
func (s *Store) StatsFor(ctx context.Context, spaceID int64) (Stats, error) {
	var st Stats
	err := s.R.QueryRowContext(ctx, `
SELECT
  (SELECT COUNT(*) FROM endpoint WHERE space_id=? AND revoked_at IS NULL),
  (SELECT COUNT(*) FROM channel  WHERE space_id=? AND state='open'),
  (SELECT COUNT(*) FROM message m JOIN channel c ON c.id=m.channel_id
     WHERE c.space_id=? AND m.state IN ('queued','delivered')),
  (SELECT COALESCE(SUM(LENGTH(m.body)),0) FROM message m JOIN channel c ON c.id=m.channel_id
     WHERE c.space_id=? AND m.body IS NOT NULL),
  (SELECT COUNT(*) FROM attachment a JOIN channel c ON c.id=a.channel_id
     WHERE c.space_id=? AND a.state IN ('offered','ready')),
  (SELECT COALESCE(SUM(a.stored_bytes),0) FROM attachment a JOIN channel c ON c.id=a.channel_id
     WHERE c.space_id=? AND a.state IN ('offered','ready'))`,
		spaceID, spaceID, spaceID, spaceID, spaceID, spaceID).
		Scan(&st.Endpoints, &st.OpenChannels, &st.Unacked, &st.BodyBytes, &st.Files, &st.FileBytes)
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
func (s *Store) ConsoleFingerprint(ctx context.Context, spaceID int64) (int64, error) {
	var n int64
	err := s.R.QueryRowContext(ctx, `
SELECT
  (SELECT COUNT(*) FROM endpoint WHERE space_id=? AND revoked_at IS NULL) * 2
+ (SELECT COUNT(*) FROM channel  WHERE space_id=?) * 3
+ (SELECT COUNT(*) FROM channel  WHERE space_id=? AND state='open') * 5
+ (SELECT COUNT(*) FROM pairing_code WHERE space_id=? AND consumed_at IS NULL
     AND expires_at > strftime('%Y-%m-%dT%H:%M:%fZ','now')) * 7
+ (SELECT COUNT(*) FROM message m JOIN channel c ON c.id=m.channel_id
     WHERE c.space_id=? AND m.state IN ('queued','delivered')) * 11`,
		spaceID, spaceID, spaceID, spaceID, spaceID).Scan(&n)
	return n, err
}
