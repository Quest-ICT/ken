package comm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// File exchange (docs/COMM.md §11). Two transfer modes, chosen by the sender:
//
//   - "path": same-host handoff. No bytes touch the server. The offer carries a
//     server-validated basename plus content and nonce hashes; the sessions move
//     the file through a shared exchange directory after the C9 rendezvous. The
//     attachment row exists as audit and as the message envelope.
//   - "upload": server relay for cross-host transfers. The sender PUTs the bytes
//     against a one-time grant; the message referencing the attachment is
//     enqueued only when the upload completes and its sha256 matches the offer,
//     so the receiver never observes partial state.
//
// There is deliberately NO chunking at this layer and no inline-bytes mode
// beyond an ordinary message body: tool arguments are generated token by token
// by a model, so payload bytes must never travel as tool-call tokens (C8). HTTP
// already does resumable transfer correctly; the agent drives it with a shell.

// Additional sentinel errors for the file surface.
var (
	// ErrFilesDisabled means the operator has not enabled file exchange (or has
	// live-disabled it — the check doubles as a kill switch).
	ErrFilesDisabled = errors.New("file exchange is disabled")
	// ErrQuota means the global storage budget or the free-space floor would be
	// violated. Fails CLOSED on purpose: a refused upload is a retry; a fail-open
	// quota is a disk-full outage that takes the knowledge base's writes with it.
	ErrQuota = errors.New("file storage quota exceeded")
	// ErrBadName means the offered name failed validation.
	ErrBadName = errors.New("invalid attachment name")
)

// clampTTL resolves a caller-supplied lifetime against the operator's configured
// one. A caller may ask for LESS, never more: an unclamped ttl_seconds would let a
// session mint effectively immortal messages and attachments that no sweep could
// settle, silently defeating the operator's live TTL settings and — for files —
// pinning the storage budget forever.
func clampTTL(requested, configured int) int {
	if requested <= 0 || requested > configured {
		return configured
	}
	return requested
}

// maxConcurrentUploads bounds in-flight uploads per sender endpoint. This covers
// the accounting hole between "grant minted" and "bytes counted": an attacker
// cannot hold the budget hostage with many never-completed uploads.
const maxConcurrentUploads = 4

// ValidateAttachmentName enforces the C9 name contract: a bare basename, nothing
// that could steer a receiving session toward an arbitrary local path. The server
// cannot see the client filesystems, but it CAN reject the string — which turns a
// convention into a validated contract.
func ValidateAttachmentName(name string) error {
	if name == "" || len(name) > 255 {
		return ErrBadName
	}
	if name == "." || name == ".." || name == "~" {
		return ErrBadName
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		// No separators (either OS), no control bytes, no DEL. '~' is rejected only
		// as a PREFIX below: inside a name it expands nowhere.
		if c == '/' || c == '\\' || c < 0x20 || c == 0x7f {
			return ErrBadName
		}
	}
	if name[0] == '~' {
		return ErrBadName
	}
	return nil
}

// validSHA256 reports whether s is a lowercase-normalizable 64-hex-digit string,
// and returns the normalized form.
func validSHA256(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) != 64 {
		return "", false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", false
		}
	}
	return s, true
}

// FileOffer is one sender's declaration of a file to move.
type FileOffer struct {
	Name        string
	SizeBytes   int64
	SHA256      string
	Transfer    string // "path" | "upload"
	NonceSHA256 string // required for "path": the C9 rendezvous hash
	Note        string // optional message body accompanying the offer
	// IdempotencyKey mirrors message idempotency: a re-offer with the same key
	// returns the ORIGINAL attachment (with a fresh upload grant while it is still
	// awaiting bytes) instead of minting a second one.
	IdempotencyKey string
	TTLSeconds     int
}

// Attachment is the durable record of a file offer/transfer.
type Attachment struct {
	rowID        int64
	recipientRow int64
	// senderRow is the endpoint that offered it. Needed because the sender is entitled to
	// its own attachment while not being a member of its own audience — membersOfScope
	// answers "who would receive this", and the sender is not one of them.
	senderRow int64

	AttachmentID string
	// ScopeID is the address: ch:<id>, r:<id> or p:<a>|<b>. It is the authoritative
	// field — ChannelID below is a convenience for the one scope kind that has a channel.
	ScopeID string
	// ChannelID is EMPTY for a room or pair attachment, which belong to no channel. It was
	// loaded through an INNER JOIN until 2026-08-24, so a scope-shaped row returned NO ROWS
	// at all — the lookup would have reported "not found" for an attachment that exists.
	ChannelID   string
	Name        string
	SizeBytes   int64
	SHA256      string
	Transfer    string
	NonceSHA256 string
	State       string
	ExpiresAt   string
	MessageID   string // public id of the enqueued message ("" until one exists)
}

// FileInfo is the attachment descriptor carried on a delivered message.
type FileInfo struct {
	AttachmentID string
	Name         string
	SizeBytes    int64
	SHA256       string
	Transfer     string
	NonceSHA256  string
}

// OfferResult is what an accepted offer produced.
type OfferResult struct {
	Attachment *Attachment
	// Message is non-nil for "path" offers, which enqueue immediately.
	Message *Message
	// UploadGrant is the one-time grant plaintext for "upload" offers, shown once.
	UploadGrant string
	// RecipientRow is the peer endpoint's rowid, for the caller's wakeup notify.
	RecipientRow int64
}

// filesDir is where relayed bytes live: <comm dir>/files/<attachment_id>, mode
// 0700/0600, never executable. One directory so an operator can exclude or
// separately mount exactly one path.
func (s *Store) filesDir() string { return filepath.Join(filepath.Dir(s.path), "files") }

// FilePath returns the on-disk location for an attachment id. The id is
// server-minted base62, so it is safe to splice into a path by construction.
func (s *Store) FilePath(attachmentID string) string {
	return filepath.Join(s.filesDir(), attachmentID)
}

// PartPath is the temporary upload target; renamed onto FilePath only after the
// checksum matches, so a final file is complete by construction.
func (s *Store) PartPath(attachmentID string) string { return s.FilePath(attachmentID) + ".part" }

// EnsureFilesDir creates the relay directory (0700) on first use.
func (s *Store) EnsureFilesDir() error { return os.MkdirAll(s.filesDir(), 0o700) }

// CheckFileQuota enforces the global budget and the free-space floor for an
// incoming size. Exported because the HTTP handler re-checks at PUT time: the
// offer-time check bounds grants, this bounds bytes.
func (s *Store) CheckFileQuota(ctx context.Context, incoming int64) error {
	l := s.lim()
	// Count the DECLARED size of in-flight uploads, not just bytes already on disk:
	// an 'offered' attachment has stored_bytes=0 until its PUT completes, so summing
	// stored_bytes alone made every concurrent upload invisible to the budget and let
	// N simultaneous PUTs each pass the check and collectively overshoot it. Reserving
	// the declared size at offer time is what actually closes that window.
	var held int64
	if err := s.R.QueryRowContext(ctx, `
SELECT COALESCE(SUM(CASE WHEN state='offered' THEN size_bytes ELSE stored_bytes END),0)
FROM attachment WHERE state IN ('offered','ready')`).Scan(&held); err != nil {
		return err
	}
	if held+incoming > l.FileBudgetBytes {
		return ErrQuota
	}
	if free, ok := diskFree(filepath.Dir(s.path)); ok && free-incoming < l.FileMinFreeBytes {
		return ErrQuota
	}
	return nil
}

// OfferFile records a file offer from ep on channelID.
//
// "path" offers enqueue their message immediately (there is nothing to wait for);
// "upload" offers return a one-time grant and enqueue only at CompleteUpload.

// recipientFor returns the channel peer's endpoint rowid for a CHANNEL offer and NULL for
// anything else. A room or pair attachment has no single recipient — that is the whole reason
// 0017 relaxed the column — and writing an arbitrary one would resurrect the frozen seat rowid
// this rewrite exists to remove.
func recipientFor(ctx context.Context, t *sql.Tx, chRow any, ep *Endpoint) any {
	row, ok := chRow.(int64)
	if !ok {
		return nil
	}
	var a, b sql.NullInt64
	if err := t.QueryRowContext(ctx, `SELECT endpoint_a, endpoint_b FROM channel WHERE id=?`, row).Scan(&a, &b); err != nil {
		return nil
	}
	if a.Valid && a.Int64 != ep.ID {
		return a.Int64
	}
	if b.Valid && b.Int64 != ep.ID {
		return b.Int64
	}
	return nil
}

// FileAddr is where an offer goes. EXACTLY ONE field is set — the same three-way choice
// comm_send already offers, arriving late on the file surface because attachments were
// channel-shaped until migration 0017.
type FileAddr struct {
	ChannelID string
	RoomID    string
	StationID string
}

// resolveOfferScope turns an address into a scope and authorises it, INSIDE the caller's
// transaction so the answer cannot go stale between the check and the insert.
//
// Each arm delegates to the check that already governs sends of that kind rather than
// inventing a file-specific one: a room offer is legal exactly when a room send is, a pair
// offer exactly when a pair send is. Two rules for the same relationship is how they drift.
func (s *Store) resolveOfferScope(ctx context.Context, t *sql.Tx, ep *Endpoint, addr FileAddr) (scope string, chRow any, err error) {
	set := 0
	for _, v := range []string{addr.ChannelID, addr.RoomID, addr.StationID} {
		if v != "" {
			set++
		}
	}
	if set != 1 {
		return "", nil, CallerSafe(errors.New(
			"give exactly one of channel_id, to_room or to_station — a file goes to one place, " +
				"and which one decides who can fetch it"))
	}

	switch {
	case addr.RoomID != "":
		// Membership is the authorisation, exactly as it is for a room send.
		if !s.callerIsInRoom(ctx, ep, addr.RoomID) {
			return "", nil, ErrRoomEmpty
		}
		return roomScope(addr.RoomID), nil, nil

	case addr.StationID != "":
		senderParty, err := endpointParty(ctx, t, ep.ID)
		if err != nil {
			return "", nil, err
		}
		from, ok := strings.CutPrefix(senderParty, "s:")
		if !ok || from == "" {
			return "", nil, ErrNotAStation
		}
		if from == addr.StationID {
			return "", nil, ErrSelfSend
		}
		linked, err := areLinked(ctx, t, from, addr.StationID)
		if err != nil {
			return "", nil, err
		}
		if !linked {
			return "", nil, ErrNotLinked
		}
		// NO CHANNEL ROW, which is the entire point of the pair scope: the conversation is
		// the relationship, so a file joins the conversation the two are already having
		// rather than opening a second place for it to live.
		return pairScope(from, addr.StationID), nil, nil

	default:
		ch, _, err := s.ChannelFor(ctx, ep, addr.ChannelID)
		if err != nil {
			return "", nil, err
		}
		return channelScope(ch.ChannelID), ch.ID, nil
	}
}

func (s *Store) OfferFile(ctx context.Context, ep *Endpoint, addr FileAddr, in FileOffer) (*OfferResult, error) {
	l := s.lim()
	if !l.FilesEnabled {
		return nil, ErrFilesDisabled
	}
	if err := ValidateAttachmentName(in.Name); err != nil {
		return nil, err
	}
	sha, ok := validSHA256(in.SHA256)
	if !ok {
		return nil, CallerSafe(errors.New("sha256 must be 64 hex digits"))
	}
	if in.SizeBytes <= 0 || in.SizeBytes > l.FileMaxBytes {
		return nil, ErrTooLarge
	}
	var nonce string
	switch in.Transfer {
	case "path":
		// The rendezvous is the proof of a shared filesystem (C9); an offer without
		// it would invite the receiver to open a path on someone's say-so.
		nonce, ok = validSHA256(in.NonceSHA256)
		if !ok {
			return nil, CallerSafe(errors.New("path transfer requires nonce_sha256 (64 hex digits) — see the rendezvous protocol"))
		}
	case "upload":
		if err := s.CheckFileQuota(ctx, in.SizeBytes); err != nil {
			return nil, err
		}
	default:
		return nil, CallerSafe(errors.New(`transfer must be "path" or "upload"`))
	}
	if len(in.Note) > l.MaxBodyBytes {
		return nil, ErrTooLarge
	}
	ttl := clampTTL(in.TTLSeconds, l.FileTTLSeconds)

	out := &OfferResult{}
	err := s.tx(ctx, func(t *sql.Tx) error {
		// RESOLVED AND AUTHORISED INSIDE THE TRANSACTION, so a link revoked or a member
		// removed between the check and the insert cannot slip an offer through.
		scope, chRow, err := s.resolveOfferScope(ctx, t, ep, addr)
		if err != nil {
			return err
		}
		// Idempotent re-offer: return the original; re-arm the grant only while the
		// attachment is still awaiting its bytes.
		if in.IdempotencyKey != "" {
			var existing string
			err := t.QueryRowContext(ctx, `
SELECT attachment_id FROM attachment WHERE scope_id=? AND sender_endpoint=? AND idempotency_key=?`,
				scope, ep.ID, in.IdempotencyKey).Scan(&existing)
			if err == nil {
				att, err := attachmentByID(ctx, t, existing)
				if err != nil {
					return err
				}
				out.Attachment = att
				switch {
				case att.Transfer != "upload":
					// A path offer has nothing to re-arm.
				case att.State == "offered":
					g, err := mintGrant(ctx, t, att.rowID, ep.ID, "upload", l.GrantTTLSeconds)
					if err != nil {
						return err
					}
					out.UploadGrant = g
				case att.State == "failed":
					// Revive a failed attachment rather than returning it inert. The relay's
					// own errors tell the sender to "re-offer to retry"; without this the
					// idempotency key became a poison pill — the re-offer returned the
					// original attachment with no grant and no error, so the prescribed
					// recovery path was a silent dead end.
					if _, err := t.ExecContext(ctx, `
UPDATE attachment SET state='offered', stored_bytes=0,
       expires_at=strftime('%Y-%m-%dT%H:%M:%fZ','now',?) WHERE id=? AND state='failed'`,
						nowExpr(ttl), att.rowID); err != nil {
						return err
					}
					g, err := mintGrant(ctx, t, att.rowID, ep.ID, "upload", l.GrantTTLSeconds)
					if err != nil {
						return err
					}
					out.UploadGrant = g
					if att, err = attachmentByID(ctx, t, existing); err != nil {
						return err
					}
					out.Attachment = att
				}
				return nil
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}

		if in.Transfer == "upload" {
			var inflight int
			if err := t.QueryRowContext(ctx, `
SELECT COUNT(*) FROM attachment WHERE sender_endpoint=? AND transfer='upload' AND state='offered'`,
				ep.ID).Scan(&inflight); err != nil {
				return err
			}
			if inflight >= maxConcurrentUploads {
				return ErrBackpressure
			}
		}

		attachmentID, err := randBase62(22)
		if err != nil {
			return err
		}
		// THE SCOPE IS THE ADDRESS, and writing it is what migration 0010 cut the seam for
		// and nothing ever did — every attachment written between 0010 and 0017 carries
		// scope_id NULL, which is why 0017 has to re-backfill before it can tighten.
		//
		// `channel_id` and `recipient_endpoint` are still written HERE because this is the
		// channel path and both are true of it: the CASCADE on channel_id is how revoking a
		// channel takes its files with it, and a 'path' transfer's rendezvous really is
		// between two specific endpoints on one filesystem. A room or pair offer leaves both
		// NULL and carries only the scope.
		res, err := t.ExecContext(ctx, `
INSERT INTO attachment(attachment_id, scope_id, channel_id, sender_endpoint, recipient_endpoint,
                       name, size_bytes, sha256, transfer, note, nonce_sha256, idempotency_key, expires_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?, strftime('%Y-%m-%dT%H:%M:%fZ','now',?))`,
			attachmentID, scope, chRow, ep.ID, recipientFor(ctx, t, chRow, ep),
			in.Name, in.SizeBytes, sha, in.Transfer, nullStr(in.Note), nullStr(nonce), nullStr(in.IdempotencyKey),
			nowExpr(ttl))
		if err != nil {
			return err
		}
		attRow, err := res.LastInsertId()
		if err != nil {
			return err
		}

		switch in.Transfer {
		case "path":
			// Nothing to upload: enqueue now and mark ready.
			msg, err := s.enqueueOfferMessage(ctx, t, ep, scope, chRow, ep.ID, in.Note, ttl)
			if err != nil {
				return err
			}
			if _, err := t.ExecContext(ctx, `
UPDATE attachment SET state='ready', ready_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),
       message_id=(SELECT id FROM message WHERE message_id=?) WHERE id=?`,
				msg.MessageID, attRow); err != nil {
				return err
			}
			out.Message = msg
		case "upload":
			g, err := mintGrant(ctx, t, attRow, ep.ID, "upload", l.GrantTTLSeconds)
			if err != nil {
				return err
			}
			out.UploadGrant = g
		}

		att, err := attachmentByID(ctx, t, attachmentID)
		if err != nil {
			return err
		}
		out.Attachment = att
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// GrantDownload mints a one-time download grant for an attachment addressed to
// ep. Callable repeatedly — redelivered messages and failed curls need fresh
// grants — but only while the bytes exist.
func (s *Store) GrantDownload(ctx context.Context, ep *Endpoint, attachmentID string) (string, *Attachment, error) {
	if !s.lim().FilesEnabled {
		return "", nil, ErrFilesDisabled
	}
	var grant string
	var att *Attachment
	err := s.tx(ctx, func(t *sql.Tx) error {
		a, err := attachmentByID(ctx, t, attachmentID)
		if err != nil {
			return err
		}
		// AUTHORISED BY PARTY, NOT BY ENDPOINT ROWID.
		//
		// This compared a.recipientRow against ep.ID, so a REPLACEMENT session staffing the
		// same station was refused an attachment its station legitimately owns. The offer
		// message is delivered to the station's party and a successor polls it normally —
		// sees the file descriptor, calls comm_file_grant, and is told the attachment does
		// not exist. Worst in exactly the case stations exist for: a takeover.
		//
		// It is the endpoint-versus-party mistake that migration 0010 names by name, left
		// standing in the one place that mints bytes.
		//
		// ErrNotFound, not ErrDenied, for a non-recipient: they must not learn the id exists.
		//
		// AUTHORISED BY SCOPE MEMBERSHIP AS OF NOW, not by a recipient row frozen at offer
		// time. That row could only ever name ONE endpoint, so it could not express a room,
		// and for a pair there is no row to name. Asking the scope instead answers all three
		// kinds with one question — and it answers it CURRENTLY, which is the part that
		// matters: a station removed from a room stops being a member, so its outstanding
		// grants stop being mintable without anything having to revoke them one by one.
		mem, err := membersOfScope(ctx, t, a.ScopeID)
		if err != nil {
			return err
		}
		me := PartyOf(ep)
		member := false
		for _, m := range mem {
			if m.Party == me {
				member = true
				break
			}
		}
		// *** AND THE SENDER IS REFUSED, EXPLICITLY, EVEN THOUGH IT IS A MEMBER. ***
		//
		// A download grant is for the RECEIVING side; the sender already has the bytes and
		// has no business pulling them back through the relay. That rule predates scopes and
		// is asserted by TestGrantDownloadIsRecipientOnly — I broke it briefly by reasoning
		// that a sender is "entitled to its own file", which is true of the file and false
		// of the grant.
		//
		// It has to be stated separately now, and that is new. On a CHANNEL the sender was
		// never the recipient row, so the old comparison excluded it for free. In a ROOM the
		// sender IS a member of its own room, so membership alone would let it through —
		// the property survived the rewrite only because a test named it.
		if !member || a.senderRow == ep.ID {
			return ErrNotFound
		}
		if a.Transfer != "upload" || a.State != "ready" {
			return CallerSafe(errors.New("attachment has no downloadable bytes"))
		}
		// Revocation must stop BYTES, not just new messages. Without this re-check a
		// recipient could keep minting fresh download grants for an already-offered
		// file until its TTL, on a channel the human has already killed.
		//
		// THE CHANNEL'S STATE IS THE WHOLE TEST. This also joined `endpoint` on
		// a.recipient_endpoint and refused when that row carried a revoked_at — the same
		// endpoint-versus-party mistake the party check EIGHT LINES ABOVE exists to
		// correct, re-answering the settled question from one connection.
		//
		// That half could never mean what it says. A revoked CALLER never reaches here:
		// AuthenticateEndpoint refuses it before any tool body runs. And the party check
		// above guarantees a.recipient_endpoint is either the caller or a station-mate. So
		// a non-NULL revoked_at was only ever reachable for a PREDECESSOR — the endpoint an
		// operator revoked precisely so that someone else could take over.
		//
		// It was permanent for the CHANNEL, not for one stale attachment: OfferFile stamps
		// recipient_endpoint with the seat rowid ChannelFor returns, and the seat never
		// moves, so every LATER offer to that station was stamped with the same revoked row
		// and was equally ungrantable. Re-offering — the remedy the relay's own errors
		// prescribe — could not clear it.
		// FOR A CHANNEL, THE CHANNEL'S STATE IS STILL THE TEST. For a room or a pair there
		// is no channel to ask, and none is needed: the membership check above already asked
		// the live roster, so a station removed from the room is refused there. That is the
		// same property this check provides for channels, arrived at from the other side —
		// which is why there is no third branch here rather than a missing one.
		if _, isChannel := channelIDOfScope(a.ScopeID); isChannel {
			var chState string
			if err := t.QueryRowContext(ctx, `
SELECT c.state FROM attachment a
JOIN channel c ON c.id = a.channel_id
WHERE a.id=?`, a.rowID).Scan(&chState); err != nil {
				return err
			}
			if chState != "open" {
				return ErrChannelClosed
			}
		}
		g, err := mintGrant(ctx, t, a.rowID, ep.ID, "download", s.lim().GrantTTLSeconds)
		if err != nil {
			return err
		}
		grant, att = g, a
		return nil
	})
	if err != nil {
		return "", nil, err
	}
	return grant, att, nil
}

// enqueueOfferMessage files the message that carries a file offer, on the attachment's own
// SCOPE, through the one insert path every other send already uses.
//
// IT REPLACES enqueueLocked, WHICH WAS THE THIRD MESSAGE-INSERT PATH IN THIS PACKAGE. Its own
// comment records why that is worth removing rather than merely tidying: the paths "drifted
// before — the shipped AckUpTo and Ack carried different recipient predicates for months".
// Three copies of "insert a message and its deliveries" is three chances to fix a predicate
// twice and miss once, and the file path was the copy nobody looked at.
//
// The recipients come from the scope AS OF NOW rather than from a row frozen at offer time,
// so a room that gained a member between the offer and the upload delivers to them too — which
// is what "one offer serves N recipients" has to mean if the audience can change at all.
func (s *Store) enqueueOfferMessage(ctx context.Context, t *sql.Tx, ep *Endpoint, scope string, chRow any, sender int64, body string, ttlSec int) (*Message, error) {
	members, err := membersOfScope(ctx, t, scope)
	if err != nil {
		return nil, err
	}
	senderParty, err := endpointParty(ctx, t, sender)
	if err != nil {
		return nil, err
	}
	recipients := make([]scopeMember, 0, len(members))
	for _, m := range members {
		if m.Party != senderParty {
			recipients = append(recipients, m)
		}
	}
	if len(recipients) == 0 {
		// A file offered into an audience of nobody is not an error the SENDER can act on by
		// retrying, so it must not look like a transient one: the same reasoning as
		// ErrRoomEmpty, and the same wording is reachable there.
		return nil, ErrRoomEmpty
	}
	return s.insertMessageWithDeliveries(ctx, t, insertSpec{
		Scope:       scope,
		ChannelRow:  chRow,
		Sender:      sender,
		SenderParty: senderParty,
		Recipients:  recipients,
		Body:        body,
		TTLSeconds:  ttlSec,
		Kind:        "message",
		Endpoint:    ep,
	})
}

// GrantInfo is a resolved, consumed transfer grant.
type GrantInfo struct {
	AttachmentRow int64
	AttachmentID  string
	EndpointToken string // token_id owning the grant's endpoint — the HTTP caller must match
	RecipientRow  int64  // the attachment's recipient (for the upload-completion notify)
	Name          string
	SizeBytes     int64
	SHA256        string
	State         string
}

// ConsumeGrant resolves and consumes a grant in one step. Single-use even when
// the transfer then fails — the agent mints a fresh grant rather than retrying a
// credential that has been on the wire. An unknown, expired, consumed, or
// wrong-kind grant are all ErrNotFound: indistinguishable on purpose.
func (s *Store) ConsumeGrant(ctx context.Context, plaintext, kind string) (*GrantInfo, error) {
	var gi GrantInfo
	err := s.tx(ctx, func(t *sql.Tx) error {
		var grantRow int64
		var attRow int64
		err := t.QueryRowContext(ctx, `
SELECT g.id, g.attachment_id FROM transfer_grant g
WHERE g.grant_sha256=? AND g.kind=? AND g.consumed_at IS NULL
  AND g.expires_at > strftime('%Y-%m-%dT%H:%M:%fZ','now')`,
			sha256Hex(plaintext), kind).Scan(&grantRow, &attRow)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if _, err := t.ExecContext(ctx, `
UPDATE transfer_grant SET consumed_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, grantRow); err != nil {
			return err
		}
		return t.QueryRowContext(ctx, `
SELECT a.id, a.attachment_id, e.token_id, a.recipient_endpoint, a.name, a.size_bytes, a.sha256, a.state
FROM attachment a
JOIN transfer_grant g ON g.attachment_id = a.id
JOIN endpoint e ON e.id = g.endpoint_id
WHERE g.id=?`, grantRow).
			Scan(&gi.AttachmentRow, &gi.AttachmentID, &gi.EndpointToken, &gi.RecipientRow,
				&gi.Name, &gi.SizeBytes, &gi.SHA256, &gi.State)
	})
	if err != nil {
		return nil, err
	}
	return &gi, nil
}

// CompleteUpload marks an upload's bytes verified and enqueues the message the
// receiver will poll. Called by the HTTP handler after the streamed sha256
// matched the offer — which is why the receiver can never observe partial state.
func (s *Store) CompleteUpload(ctx context.Context, attachmentRow int64, storedBytes int64) (*Message, error) {
	var msg *Message
	err := s.tx(ctx, func(t *sql.Tx) error {
		// channel_id and recipient_endpoint are NULLABLE since 0017 — a room or pair
		// attachment carries neither — so they are scanned as nullable rather than into
		// plain int64s, which would fail the scan on exactly the rows 0017 exists to allow.
		var chRow, recipientNull sql.NullInt64
		var sender int64
		var note sql.NullString
		var state, scope string
		err := t.QueryRowContext(ctx, `
SELECT channel_id, sender_endpoint, recipient_endpoint, state, note, scope_id FROM attachment WHERE id=?`,
			attachmentRow).Scan(&chRow, &sender, &recipientNull, &state, &note, &scope)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		_ = recipientNull // the audience comes from the SCOPE now, not from this frozen row
		if state != "offered" {
			return CallerSafe(errors.New("attachment is not awaiting an upload"))
		}
		// nil rather than chRow when the attachment belongs to no channel — insertSpec's
		// ChannelRow is `any` precisely so a room message can carry no channel at all.
		var chAny any
		if chRow.Valid {
			chAny = chRow.Int64
		}
		m, err := s.enqueueOfferMessage(ctx, t, nil, scope, chAny, sender, note.String, s.lim().FileTTLSeconds)
		if err != nil {
			return err
		}
		if _, err := t.ExecContext(ctx, `
UPDATE attachment SET state='ready', stored_bytes=?, ready_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),
       message_id=(SELECT id FROM message WHERE message_id=?) WHERE id=?`,
			storedBytes, m.MessageID, attachmentRow); err != nil {
			return err
		}
		msg = m
		return nil
	})
	if err != nil {
		return nil, err
	}
	// `recipient_endpoint` IS NO LONGER CONSULTED AT ALL. The audience is resolved from the
	// attachment's SCOPE at upload time, so a room that gained a member between the offer and
	// the completed upload delivers to them too. The column is still selected above and
	// deliberately discarded: it stays on the table because a 'path' transfer's rendezvous is
	// genuinely between two endpoints, and reading it here would reintroduce the frozen seat
	// rowid this rewrite removed.
	return msg, nil
}

// FailUpload marks an upload failed (checksum mismatch, overrun, aborted stream).
// The sender recovers by re-offering; the failed row survives as audit.
func (s *Store) FailUpload(ctx context.Context, attachmentRow int64) error {
	_, err := s.W.ExecContext(ctx, `
UPDATE attachment SET state='failed' WHERE id=? AND state='offered'`, attachmentRow)
	return err
}

// mintGrant creates a one-time transfer grant inside an open transaction and
// returns the plaintext exactly once.
func mintGrant(ctx context.Context, t *sql.Tx, attachmentRow, endpointRow int64, kind string, ttlSec int) (string, error) {
	plain, err := randBase62(40)
	if err != nil {
		return "", err
	}
	if _, err := t.ExecContext(ctx, `
INSERT INTO transfer_grant(grant_sha256, attachment_id, endpoint_id, kind, expires_at)
VALUES(?,?,?,?, strftime('%Y-%m-%dT%H:%M:%fZ','now',?))`,
		sha256Hex(plain), attachmentRow, endpointRow, kind, nowExpr(ttlSec)); err != nil {
		return "", err
	}
	return plain, nil
}

// attachmentByID loads an attachment inside an open transaction.
func attachmentByID(ctx context.Context, t *sql.Tx, attachmentID string) (*Attachment, error) {
	var (
		a     Attachment
		nonce sql.NullString
		msgID sql.NullString
	)
	// LEFT JOIN, because a room or pair attachment has no channel. An INNER join here
	// returned zero rows for exactly the attachments 0017 exists to allow — a lookup that
	// answers "not found" for a row that is sitting in the table.
	var recipient sql.NullInt64
	var chPublic sql.NullString
	err := t.QueryRowContext(ctx, `
SELECT a.id, a.recipient_endpoint, a.sender_endpoint, a.attachment_id, a.scope_id, c.channel_id, a.name, a.size_bytes, a.sha256,
       a.transfer, a.nonce_sha256, a.state, a.expires_at,
       (SELECT m.message_id FROM message m WHERE m.id = a.message_id)
FROM attachment a LEFT JOIN channel c ON c.id = a.channel_id
WHERE a.attachment_id=?`, attachmentID).
		Scan(&a.rowID, &recipient, &a.senderRow, &a.AttachmentID, &a.ScopeID, &chPublic, &a.Name, &a.SizeBytes, &a.SHA256,
			&a.Transfer, &nonce, &a.State, &a.ExpiresAt, &msgID)
	a.recipientRow, a.ChannelID = recipient.Int64, chPublic.String
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	a.NonceSHA256, a.MessageID = nonce.String, msgID.String
	return &a, nil
}

// sweepFiles is the attachment half of Sweep: expiry, done-marking, byte
// deletion, and grant purging. Returns the attachment IDs whose bytes should be
// unlinked AFTER the transaction commits — filesystem calls stay out of the write
// transaction, and the caller clears each row's accounting only once its file is
// actually gone.
func (s *Store) sweepFiles(ctx context.Context, t *sql.Tx) (unlink []string, err error) {
	// Expire attachments past their TTL in any pre-terminal state.
	if _, err := t.ExecContext(ctx, `
UPDATE attachment SET state='expired'
WHERE state IN ('offered','ready') AND expires_at <= strftime('%Y-%m-%dT%H:%M:%fZ','now')`); err != nil {
		return nil, err
	}
	// An acked message settles its attachment — and with several recipients that
	// means acked by ALL of them. Settling on the first ack would delete the bytes
	// while somebody who has not read the offer yet is still entitled to fetch them,
	// which is the body-retention mistake wearing a filesystem.
	if _, err := t.ExecContext(ctx, `
UPDATE attachment SET state='done', done_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
WHERE state='ready' AND message_id IN (
  SELECT m.id FROM message m
   WHERE EXISTS (SELECT 1 FROM delivery d WHERE d.message_row = m.id AND d.state='acked')
     AND NOT EXISTS (SELECT 1 FROM delivery d WHERE d.message_row = m.id
                                                AND d.state IN ('queued','delivered')))`); err != nil {
		return nil, err
	}
	// Collect deletable bytes: settled uploads still holding disk.
	rows, err := t.QueryContext(ctx, `
SELECT attachment_id FROM attachment
WHERE state IN ('done','failed','expired') AND transfer='upload' AND stored_bytes > 0`)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	unlink = append(unlink, ids...)
	// stored_bytes is deliberately NOT zeroed here. The collect query above selects
	// on stored_bytes > 0, so zeroing before the unlink actually happens would, on a
	// failed unlink, leak the file permanently AND remove it from the budget forever
	// — the row would never be selected again. The caller zeroes each row only after
	// its file is gone (ClearStoredBytes), so a failed unlink is simply retried by
	// the next sweep.
	// Purge settled attachment rows past the metadata retention window, and spent
	// or expired grants.
	// Never purge a row that still owns bytes: the row is the only record of which
	// file to unlink, so deleting it first would orphan the file beyond any sweep.
	if _, err := t.ExecContext(ctx, `
DELETE FROM attachment
WHERE state IN ('done','failed','expired') AND stored_bytes = 0
  AND created_at <= strftime('%Y-%m-%dT%H:%M:%fZ','now',?)`, nowExpr(-s.lim().MetadataTTLSeconds)); err != nil {
		return nil, err
	}
	if _, err := t.ExecContext(ctx, `
DELETE FROM transfer_grant
WHERE consumed_at IS NOT NULL OR expires_at <= strftime('%Y-%m-%dT%H:%M:%fZ','now')`); err != nil {
		return nil, err
	}
	return unlink, nil
}

// ClearStoredBytes zeroes a settled attachment's byte accounting after its file
// has actually been removed. Separate from the sweep transaction on purpose: the
// budget must free only for bytes that are really gone.
func (s *Store) ClearStoredBytes(ctx context.Context, attachmentID string) error {
	_, err := s.W.ExecContext(ctx,
		`UPDATE attachment SET stored_bytes=0 WHERE attachment_id=?`, attachmentID)
	return err
}

// sweepPartFiles removes abandoned .part uploads (a crashed or aborted PUT). Age
// is judged by mtime against the grant TTL: any part older than a grant could
// possibly be has no live writer.
func (s *Store) sweepPartFiles() {
	entries, err := os.ReadDir(s.filesDir())
	if err != nil {
		return // no dir yet, or unreadable — nothing to sweep
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".part") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if ageSeconds(info) > int64(s.lim().GrantTTLSeconds)*2 {
			_ = os.Remove(filepath.Join(s.filesDir(), e.Name()))
		}
	}
}

// ErrShortWrite reports a stream that ended before the declared size.
var ErrShortWrite = CallerSafe(fmt.Errorf("upload ended before the declared size"))

// ageSeconds is a file's age by mtime — used only by the .part sweep, where an
// approximate answer is fine.
func ageSeconds(info os.FileInfo) int64 { return int64(time.Since(info.ModTime()).Seconds()) }
