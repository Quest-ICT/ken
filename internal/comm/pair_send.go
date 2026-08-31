package comm

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// Station-addressed send — Batch 6 item P2, and the piece that makes
// `comm_open_channel` redundant.
//
// THE SHAPE, IN ONE SENTENCE: a human approves a link between two stations, and from
// then on either side writes to the other by NAME, with no pairing code, no channel row
// and no second human step.
//
// It is a third entry point beside Send and SendToRoom rather than a flag on either,
// for the reason room_send.go already states: each authorises differently, and folding
// them together means a chain of conditionals in which the wrong branch is a security
// answer given for the wrong reason. A channel authorises by MEMBERSHIP OF A PAIR a
// pairing code created; a room by membership of a set a human filled; a pair scope by
// an APPROVED LINK. Everything after addressing — numbering, idempotency, backpressure,
// the message and its deliveries — is insertMessageWithDeliveries, shared by all three.

// THE FOUR REFUSALS BELOW ARE WRAPPED IN CallerSafe, AND THAT WRAP IS THE WHOLE POINT OF WRITING
// THEM. Without it every one reaches the caller as the literal string "internal error": commError
// flattens by sentinel and anything its switch does not name falls through to that default.
// ken-prod-ops received exactly that from a live revocation test on 2026-08-19, hours after this
// file shipped.
//
// It is worse than a wording bug, and the reason is worth stating once. A session whose link was
// revoked cannot distinguish a PERMISSION DECISION from a SERVER FAULT. "Internal error" invites a
// retry, or a report that Ken is down; the correct response — stop, and tell your human the link is
// gone — is the one answer the text makes unreachable. The revocation path was built to be reliable
// and is; it was simply unreadable.
//
// Wrapped AT DECLARATION rather than at each raise site, deliberately: a raise site added later
// inherits the wrap instead of having to remember it, which is exactly what was forgotten here.
// CallerSafe preserves the chain, so errors.Is against these still matches.
//
// Each clears the bar at comm.go:94-96 — the text reveals nothing the caller could not already
// establish. Three are facts about the CALLER's own state. ErrUnknownStation is the one that needed
// thought: it is raised only for an id absent from the link mirror, which projects links in the
// caller's own instance, and it separates "you typed an id nobody here is linked to" from "you are not
// linked to it" — a typo the session fixes in one call versus a human approval it cannot retry into
// existence.

// ErrNotAStation refuses a pair send from a mailbox with no station.
//
// IT SHOULD NO LONGER BE REACHABLE, and it is kept anyway. A mailbox is now created by
// MailboxFor against a resolved station, so there is no supported way to hold one without a
// station behind it — but the party key is a string in a row, and a guard that reads it is
// cheaper than the alternative: an unbound sender would otherwise take the linked path and
// address a peer as nobody. Kept as an assertion, not as a workflow.
//
// ITS TEXT NAMED comm_bind AND THE X-Ken-Workspace HEADER UNTIL 2026-08-27, both deleted in the
// same wave that made this state unreachable. An error telling a session to call a tool that no
// longer exists is worse than no error: it is a confident instruction into a dead end.
var ErrNotAStation = CallerSafe(errors.New("to_station addresses one station from another, and this mailbox has no station behind it. " +
	"That should not be possible — tell your human"))

// ErrNotLinked now means ONE THING: a human turned this relationship off.
//
// It used to mean "no human has approved you yet", and its text told the session to file
// station_link_request and wait. Both halves are gone: links are created on first contact by the
// send path itself, and the tool that filed the ask was deleted with the approval it fed. The only
// way to arrive here is a link a human SUSPENDED — auto-linking will not resurrect one, which is
// the whole point of having an off-switch.
//
// SO THE REMEDY CHANGED SHAPE, and saying so matters more than it looks: under the old text a
// session read "not linked" and retried, or asked again, forever. Under the new one there is
// nothing to retry and nothing to file — a human decided, and the only correct next action is to
// tell them. An error that suggests a retry where none can work is how a session burns a
// conversation on a wall it cannot see.
var ErrNotLinked = CallerSafe(errors.New("that link is SUSPENDED, so nothing was sent. Links are created automatically on " +
	"first contact, so this one exists and was deliberately turned off by your human at Ken's /stations console. " +
	"Do not retry: tell them you tried to reach that station and why, and let them decide whether to resume it"))

// ErrSelfSend refuses a station addressing itself.
//
// A pair scope between one station and itself has one member, so the message would be
// written, delivered to the sender, and returned by its own next poll — a loop that
// looks like the peer answering.
var ErrSelfSend = CallerSafe(errors.New("that is your own station — a message to yourself would come back as mail from a peer"))

// ErrUnknownStation separates "no such station" from "that link is suspended".
//
// The mirror holds links between ACTIVE stations only, so both failures arrive as an absent row
// and the temptation is to answer both the same way. They still need different sentences, and the
// gap between them WIDENED when auto-linking shipped: a suspended link is a human decision the
// session must not retry, while an unknown id is a typo it fixes in one call — and since first
// contact now creates the link itself, an id the session got right essentially cannot land here.
// Which makes this the "you mistyped it" error almost every time it fires.
//
// Distinguished by asking whether ANY link mentions the id, which is the only evidence comm.db
// has. A station that exists but has never been contacted has no row either — so the remedy named
// here is the directory, which lists every live station whether or not a link exists yet.
var ErrUnknownStation = CallerSafe(errors.New("no station with that id is known here — check the id with comm_directory, " +
	"which lists every station you can address and hands back the exact id to use"))

// SendToStation delivers one body to another station over the pair scope their link
// authorises.
//
// ONE recipient, so it is a "channel" in every sense a caller cares about — but the
// conversation is addressed by WHO rather than by WHICH ROW, and it exists the moment
// the human approves rather than the moment two sessions both happen to be online. That
// second difference is the one P3 discovered the hard way: a link approved while
// neither side was staffed materialised no channel, and the permission the human
// granted had nothing to spend it on until somebody re-ran the dance.
//
// THE RECIPIENT HAS NO ENDPOINT ATTACHED, exactly as a room delivery has none. A pair
// message is addressed to a POST; which connection reads it is decided at poll time, so
// a successor session inherits the conversation without anything being re-pointed.
func (s *Store) SendToStation(ctx context.Context, ep *Endpoint, toStation, body string, opts SendOpts) (*Message, error) {
	if len(body) > s.lim().MaxBodyBytes {
		return nil, ErrTooLarge
	}
	toStation = strings.TrimSpace(toStation)
	if toStation == "" {
		return nil, ErrUnknownStation
	}

	undelivered := s.lim().UndeliveredTTLSeconds
	if undelivered <= 0 || undelivered < s.lim().MessageTTLSeconds {
		undelivered = DefaultLimits().UndeliveredTTLSeconds
	}
	ttl := clampTTL(opts.TTLSeconds, undelivered)
	clampedFrom := 0
	if opts.TTLSeconds > 0 && opts.TTLSeconds != ttl {
		clampedFrom = opts.TTLSeconds
	}

	var out *Message
	err := s.tx(ctx, func(t *sql.Tx) error {
		senderParty, err := endpointParty(ctx, t, ep.ID)
		if err != nil {
			return err
		}
		fromStation, ok := strings.CutPrefix(senderParty, "s:")
		if !ok || fromStation == "" {
			return ErrNotAStation
		}
		if fromStation == toStation {
			return ErrSelfSend
		}

		// AUTHORISATION, INSIDE THE WRITING TRANSACTION. Not because the race is
		// likely — a human revoking a link during this request is rare — but because
		// the alternative is a rule that holds by timing, and the one thing revocation
		// must be is reliable.
		linked, err := areLinked(ctx, t, fromStation, toStation)
		if err != nil {
			return err
		}
		if !linked {
			// Which of the two failures it is, decided from evidence rather than
			// guessed. A station that appears in no link at all is far more likely a
			// mistyped id than a revoked relationship.
			var known bool
			if err := t.QueryRowContext(ctx,
				`SELECT EXISTS(SELECT 1 FROM station_link_mirror WHERE station_a=?1 OR station_b=?1)`,
				toStation).Scan(&known); err != nil {
				return err
			}
			if !known {
				return ErrUnknownStation
			}
			return ErrNotLinked
		}

		out, err = s.insertMessageWithDeliveries(ctx, t, insertSpec{
			Scope:       pairScope(fromStation, toStation),
			Sender:      ep.ID,
			SenderParty: senderParty,
			Recipients:  []scopeMember{{Party: stationParty(toStation)}},
			Body:        body,
			TTLSeconds:  ttl,
			Opts:        opts,
			Kind:        "message",
			Endpoint:    ep,
		})
		if out != nil {
			out.TTLClampedFrom, out.Recipients = clampedFrom, 1
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// PairConversation is one linked peer and what is waiting on that conversation.
type PairConversation struct {
	StationID string
	Scope     string
	Pending   int
}

// PairsFor lists the pair conversations this endpoint's station can address.
//
// LISTED EVEN WHEN EMPTY OF MAIL, because the list answers "who may I write to", which
// is the question a session actually has — and answering it from the same mirror the
// send path consults means the listing can never name a peer the send would refuse.
//
// An endpoint with no station gets an empty list rather than an error: comm_channels is
// a read, and an unbound session asking what it can reach deserves an honest "nothing by
// station" instead of a failed call.
func (s *Store) PairsFor(ctx context.Context, ep *Endpoint) ([]PairConversation, error) {
	var station sql.NullString
	if err := s.R.QueryRowContext(ctx,
		`SELECT station_id FROM endpoint WHERE id=?`, ep.ID).Scan(&station); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if !station.Valid || station.String == "" {
		return nil, nil
	}
	peers, err := s.LinkedStations(ctx, station.String)
	if err != nil {
		return nil, err
	}
	out := make([]PairConversation, 0, len(peers))
	for _, peer := range peers {
		scope := pairScope(station.String, peer)
		// Counted per scope with the SAME predicate the other pending counters use, so
		// Counted per scope with the SAME predicate the other pending counters use — now
		// literally the same string rather than a copy of it, so a pair row cannot
		// disagree with pending_total the way the four channel/room counters did.
		var pending int
		if err := s.R.QueryRowContext(ctx, pendingSQL(`
SELECT COUNT(*)
  FROM delivery d
  JOIN message m ON m.id = d.message_row
 WHERE m.scope_id = ? AND d.party_key = ? AND d.state = 'queued'
   AND %NOTEXPIRED%`),
			scope, stationParty(station.String)).Scan(&pending); err != nil {
			return nil, err
		}
		out = append(out, PairConversation{StationID: peer, Scope: scope, Pending: pending})
	}
	return out, nil
}
