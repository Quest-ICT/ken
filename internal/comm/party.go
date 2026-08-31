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
//	SCOPE   where a message lives.     'r:<room_id>' | 'p:<station>|<station>' | 'b:<party>'
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

// The scope namespaces. scopePrefixChannel ("ch:") is DELETED with the channel in 5.0.0 — it is
// now simply an unknown tag, which the parser rejects like any other, so a caller still holding an
// old channel scope gets a refusal rather than an empty result it would read as an empty inbox.
const (
	scopePrefixRoom = "r:"
	// PartyPrefixStation is the party key of a STATION, as opposed to the bare-rowid form an
	// unbound endpoint gets. It was a bare "s:" literal in three places until 5.2.0, when a
	// fourth reader appeared: BroadcastTo must REFUSE an audience entry that is not a station,
	// so the grammar it checks against has to be the same one endpointParty writes.
	PartyPrefixStation = "s:"
	// A BROADCAST scope belongs to its sender rather than to a place. There is no
	// standing set of participants to point at — the audience is every active station on
	// this Ken at the moment of the call, supplied by the layer that can read the roster —
	// so the scope names the only stable thing involved, which is who is speaking.
	//
	// It said "computed at send time from the rooms the sender is in" until 5.2.0. That was
	// the whole defect: on an instance with thirteen stations and no rooms it computed an
	// audience of nobody, and reported that as the truth.
	//
	// It is still a real scope with a real sequence, because a recipient needs to be
	// able to ack one and a cumulative ack has to mean something. What it is NOT is a
	// place anyone can reply INTO: a reply goes to a room or to a station, which is the
	// honest shape of "I told everyone" — the answer comes back to somewhere specific.
	scopePrefixBroadcast = "b:"
	// A PAIR scope is the private conversation between two stations, authorised by the
	// station_link a human approved (Batch 6, P2). 'p:<a>|<b>', ids SORTED, so both
	// directions name the same place and one ascending sequence covers the exchange.
	//
	// This is the shape that makes comm_open_channel redundant. A channel is a
	// conversation a PAIRING CODE created and a row records; a pair scope is a
	// conversation the LINK already authorised, which needs no row and no code — the
	// permission is the relationship, and the address is derived from it.
	scopePrefixPair = "p:"
	// The separator between the two ids. PRINTABLE on purpose: station ids are
	// randBase62(16) so '|' cannot occur inside one, and a control character here would
	// be invisible in every log, grep and dashboard that ever has to read a scope.
	// (The project has paid for that mistake once already.)
	pairSep = "|"
)

// orderStations puts two station ids in the canonical order the link tables use.
//
// ken.db's station_link carries CHECK (station_a < station_b) and orders every read
// through its own orderPair; this is the same rule restated where comm can reach it.
// Both halves must agree or a pair authorises in one direction and refuses in the
// other, which reads as a permissions bug on whichever side asked second.
func orderStations(x, y string) (string, string) {
	if x > y {
		return y, x
	}
	return x, y
}

// pairScope is the scope id for the conversation between two stations.
func pairScope(x, y string) string {
	a, b := orderStations(x, y)
	return scopePrefixPair + a + pairSep + b
}

// pairStationsOfScope returns the two station ids inside a 'p:' scope.
//
// Refuses a scope with an empty half or no separator rather than returning one id and
// a blank: a blank station id would match nothing and produce a delivery addressed to
// "s:", which is a party no endpoint can ever hold — mail that vanishes without error.
func pairStationsOfScope(scope string) (string, string, bool) {
	rest, ok := strings.CutPrefix(scope, scopePrefixPair)
	if !ok {
		return "", "", false
	}
	a, b, found := strings.Cut(rest, pairSep)
	if !found || a == "" || b == "" {
		return "", "", false
	}
	return a, b, true
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
		return PartyPrefixStation + station.String, nil
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
	// A PAIR's members are its NAME. Both station ids are in the scope string, so this
	// arm needs no table at all — which is the whole reason a pair conversation costs no
	// row. What the string cannot say is whether the two are PERMITTED to talk; that is
	// the link mirror's job and it is checked by the send path, not here. Keeping the
	// two apart matters: this function answers "who would receive this", and answering
	// it for an unauthorised pair is what lets the caller refuse with a useful error
	// instead of a silent empty audience.
	if a, b, ok := pairStationsOfScope(scope); ok {
		return []scopeMember{{Party: stationParty(a)}, {Party: stationParty(b)}}, nil
	}
	// EVERY SURVIVING NAMESPACE IS HANDLED ABOVE, so anything reaching here names none of them —
	// including a 'ch:' scope written before 5.0.0, which is now simply an unknown tag.
	//
	// Refusing loudly beats defaulting to "nobody", which would accept a send and deliver it to no
	// one: a success that delivered nothing is the worst answer available here.
	return nil, CallerSafe(errors.New("unknown scope kind: " + scope))
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

// broadcastScope is the scope a sender's broadcasts live in.
func broadcastScope(senderParty string) string { return scopePrefixBroadcast + senderParty }

// IsBroadcastScope states the 'b:' grammar ONCE.
//
// validScope's broadcast arm, the backpressure branch in room_send.go, the cumulative-ack arm in
// message.go and commserver's `broadcast` flag all ask the same question, and three of them used
// to answer it by hand. Exported because commserver must ask it too: a recipient's `broadcast`
// flag keyed on `audience_size > 1`, so on a TWO-station Ken an estate-wide advisory arrived
// flagged as an ordinary directed message — audience_size excludes the sender, so it was 1.
func IsBroadcastScope(scope string) bool {
	rest, ok := strings.CutPrefix(scope, scopePrefixBroadcast)
	return ok && rest != ""
}

// validScope reports whether s names one of the four scope namespaces.
//
// It exists for the POLL FILTER, and the reason it refuses rather than shrugging is the
// same reason membersOfScope refuses an unknown kind: a filter that matches nothing
// returns an empty list, and an empty list is byte-identical to "no mail is waiting".
// A hub draining a backlog cannot act on that answer, and nothing in the result would
// tell it the filter was the problem.
//
// Deliberately built from the four existing parsers rather than a prefix table, so a
// namespace can never be accepted here in a shape those parsers would reject — 'p:' with
// no separator being the case that actually differs.
func validScope(s string) bool {
	if _, ok := roomIDOfScope(s); ok {
		return true
	}
	if _, _, ok := pairStationsOfScope(s); ok {
		return true
	}
	if rest, ok := strings.CutPrefix(s, scopePrefixBroadcast); ok && rest != "" {
		return true
	}
	return false
}

// WakeTargetsFor returns the endpoint rowids that should have a parked poll woken because
// this message was just written for them.
//
// ROOM AND BROADCAST SENDS WOKE NOBODY. The wakeup is keyed by endpoint rowid, and a room
// delivery is addressed to a PARTY with recipient_endpoint NULL — rooms hold stations, and
// which endpoint is staffing one is not decided until somebody polls. So the send handler had
// no rowid to notify and simply did not, on both room paths.
//
// The cost is latency rather than loss: a poll re-reads the database when its wait elapses, so
// nothing was ever dropped. But it is bounded by the granted wait (15 s by default) on every
// room message and immediate on every channel message, and "rooms feel dead compared to
// channels" is a belief that has already shipped once. A wakeup is an optimisation; an
// optimisation that applies to one addressing mode and not the other is a difference users read
// as capability.
//
// Resolves the party to CURRENT live endpoints rather than to whatever polled last: the whole
// point of a station inbox is that a successor session inherits it, and waking the endpoint
// that has gone away would be waking the wrong one.
func (s *Store) WakeTargetsFor(ctx context.Context, messageID string) ([]int64, error) {
	rows, err := s.R.QueryContext(ctx, `
SELECT DISTINCT e.id
  FROM delivery d
  JOIN message m ON m.id = d.message_row
  JOIN endpoint e
    ON (d.party_key = 's:' || COALESCE(e.station_id,'')  AND e.station_id IS NOT NULL)
    OR (d.party_key = 'e:' || e.id)
 WHERE m.message_id = ?
   AND d.state = 'queued'
   AND e.revoked_at IS NULL`, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
