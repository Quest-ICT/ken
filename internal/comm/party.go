package comm

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
)

// Party and scope — the two identities the delivery split introduced, and the only
// two the message layer is allowed to reason about (migration 0009).
//
//	SCOPE   where a message lives.     'ch:<channel_id>' | 'r:<room_id>'
//	PARTY   who a message is for.      's:<station_id>'  | 'e:<endpoint rowid>'
//
// Both are tagged strings, and the tag is load-bearing rather than decorative: a
// station whose id is the decimal string of some endpoint's rowid would otherwise
// share that endpoint's inbox, and a room named like a channel would share its
// sequence. That collision never shows up in testing and is unrecoverable once it
// does — 0007 learned it on the sender side and this is the same rule applied to
// every axis.
//
// The reason a party is not just an endpoint: an endpoint is a CONNECTION and a
// station is a POST. A session that reconnects gets a new endpoint and must still
// find the mail its station was sent, which the poll predicate has always tried to
// do at read time. Storing the party makes it true at WRITE time, which is what a
// third participant needs.

// scopePrefixChannel and scopePrefixRoom tag the two scope namespaces. Rooms arrive
// with slice 5; the constant is here because the parser must reject an unknown tag
// rather than treat it as a channel.
const (
	scopePrefixChannel = "ch:"
	scopePrefixRoom    = "r:"
)

// channelScope is the scope id for a two-party channel.
func channelScope(channelID string) string { return scopePrefixChannel + channelID }

// channelIDOfScope returns the channel id inside a 'ch:' scope, and false for any
// other shape. Callers that still need a channel row (the file surface, the console)
// use it to bridge; a room scope answers false and they refuse rather than guess.
func channelIDOfScope(scope string) (string, bool) {
	if rest, ok := strings.CutPrefix(scope, scopePrefixChannel); ok && rest != "" {
		return rest, true
	}
	return "", false
}

// endpointParty is the party key of an endpoint row: its station when it has one,
// its own rowid when it does not.
//
// Resolved INSIDE the caller's transaction so it cannot observe a binding that
// changes underneath it — a session binding mid-send would otherwise get a message
// filed under one identity and a delivery under another.
func endpointParty(ctx context.Context, q rowQuerier, endpointRow int64) (string, error) {
	var station sql.NullString
	err := q.QueryRowContext(ctx, `SELECT station_id FROM endpoint WHERE id=?`, endpointRow).Scan(&station)
	if errors.Is(err, sql.ErrNoRows) {
		// The endpoint is gone mid-call. The rowid form is the conservative answer:
		// it can only ever under-share an inbox, never widen one.
		return endpointPartyKey(endpointRow), nil
	}
	if err != nil {
		return "", err
	}
	if station.Valid && station.String != "" {
		return "s:" + station.String, nil
	}
	return endpointPartyKey(endpointRow), nil
}

// endpointPartyKey is the unbound form, without a lookup.
func endpointPartyKey(endpointRow int64) string { return "e:" + strconv.FormatInt(endpointRow, 10) }

// stationParty is the bound form.
func stationParty(stationID string) string { return "s:" + stationID }

// scopeMember is one addressable seat in a scope: the party that owns it, and the
// endpoint that currently occupies it, if any.
//
// Endpoint is kept only for the audit column on `delivery` and for the legacy
// claim-once machinery. Nothing about ADDRESSING may depend on it, or the split
// buys nothing.
type scopeMember struct {
	Party    string
	Endpoint sql.NullInt64
}

// membersOfScope lists the parties a message in this scope is delivered to.
//
// For a channel that is its two seats — and it reads them from `channel` rather than
// from the sender's point of view, because a fan-out has no "peer". A seat that has
// not been joined yet (endpoint_b before the second join) contributes nothing, which
// is what makes a send into a pending channel a no-op rather than an error at this
// layer; membership and openness are enforced above.
func membersOfScope(ctx context.Context, t *sql.Tx, scope string) ([]scopeMember, error) {
	if roomID, ok := roomIDOfScope(scope); ok {
		return roomMembers(ctx, t, roomID)
	}
	chID, ok := channelIDOfScope(scope)
	if !ok {
		// Refusing loudly beats defaulting to "nobody", which would accept a send and
		// deliver it to no one — a success that delivered nothing is the worst answer
		// available here.
		return nil, errors.New("unknown scope kind: " + scope)
	}
	rows, err := t.QueryContext(ctx, `
SELECT e.id, e.station_id
  FROM channel c
  JOIN endpoint e ON e.id IN (c.endpoint_a, c.endpoint_b)
 WHERE c.channel_id = ?`, chID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []scopeMember
	for rows.Next() {
		var id int64
		var station sql.NullString
		if err := rows.Scan(&id, &station); err != nil {
			return nil, err
		}
		m := scopeMember{Endpoint: sql.NullInt64{Int64: id, Valid: true}}
		if station.Valid && station.String != "" {
			m.Party = stationParty(station.String)
		} else {
			m.Party = endpointPartyKey(id)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// nextScopeSeq allocates the next sequence number for a scope.
//
// ONE counter per scope, spanning every sender — which is the change from
// channel_seq's per-(channel, sender) keying. Two interleaved sequences in one
// channel meant `ack_up_to_seq`, which is a RANGE, could settle mail from the other
// direction that nobody had read. With a room the old scheme has no meaning at all:
// there is no "direction" among five participants.
//
// The high-water mark lives in its own table rather than being derived as
// MAX(scope_seq)+1, because message rows are swept: once history was purged the
// derived counter RESET to 1, breaking the ascending promise and letting a retried
// cumulative ack settle brand-new messages reissued the same low numbers.
//
// Safe as upsert-then-read under the single-writer discipline.
func nextScopeSeq(ctx context.Context, t *sql.Tx, scope string) (int64, error) {
	if _, err := t.ExecContext(ctx, `
INSERT INTO scope_counter(scope_id, next_seq) VALUES(?, 2)
ON CONFLICT(scope_id) DO UPDATE SET next_seq = next_seq + 1`, scope); err != nil {
		return 0, err
	}
	var next int64
	if err := t.QueryRowContext(ctx,
		`SELECT next_seq FROM scope_counter WHERE scope_id=?`, scope).Scan(&next); err != nil {
		return 0, err
	}
	return next - 1, nil
}

// roomScope is the scope id for a room.
func roomScope(roomID string) string { return scopePrefixRoom + roomID }

// roomIDOfScope returns the room id inside an 'r:' scope.
func roomIDOfScope(scope string) (string, bool) {
	if rest, ok := strings.CutPrefix(scope, scopePrefixRoom); ok && rest != "" {
		return rest, true
	}
	return "", false
}

// roomMembers reads a room's parties from the MIRROR.
//
// Every member is a station, so there is no endpoint to attach: the Endpoint field
// stays null and `delivery.recipient_endpoint` is NULL for room mail. That column is
// audit-only and already nullable for exactly this reason — a room message is addressed
// to a post, and which connection happens to read it is decided later, at poll time.
//
// An empty room is not an error here. Send refuses it, with a message that says so,
// because "delivered to nobody" and "no such room" are different problems and an
// operator needs to tell them apart.
func roomMembers(ctx context.Context, t *sql.Tx, roomID string) ([]scopeMember, error) {
	rows, err := t.QueryContext(ctx,
		`SELECT party_key FROM room_member_mirror WHERE room_id=? ORDER BY party_key`, roomID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []scopeMember
	for rows.Next() {
		var party string
		if err := rows.Scan(&party); err != nil {
			return nil, err
		}
		out = append(out, scopeMember{Party: party})
	}
	return out, rows.Err()
}
