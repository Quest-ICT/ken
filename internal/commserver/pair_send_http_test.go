package commserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Quest-ICT/ken/internal/comm"
	"github.com/Quest-ICT/ken/internal/store"
)

// `comm_send{to_station}` OVER REAL HTTP, THROUGH THE SDK'S SCHEMA VALIDATION.
//
// A store-level test proves the send works; it proves nothing about whether the tool will
// ACCEPT the argument. That distinction is not hypothetical here — item B passed a unit test
// and then failed over HTTP, because jsonschema-go infers `required` from the ABSENCE of
// `omitempty` (infer.go:342) and the SDK rejected the call before any handler ran. `to_station`
// is a new optional field on the same struct, so it earns the same end-to-end proof.
//
// It also covers the arithmetic this item changed: "exactly one of" went from a two-way boolean
// identity to a count over three, and the case that identity would have wrongly ADMITTED —
// channel_id and to_station together — is asserted below.
func TestSendToStationOverHTTP(t *testing.T) {
	ctx := context.Background()
	st := newKB(t)
	cs, err := comm.Open(filepath.Join(t.TempDir(), "comm.db"), comm.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	if err := cs.Migrate(); err != nil {
		t.Fatal(err)
	}

	// TWO REAL STATIONS AND A REAL APPROVAL. The mirror is populated by the same two
	// calls the console makes — LinkMirrorRows then ReplaceLinkMirror — rather than by
	// poking the projection directly, so this covers the chain that actually authorises
	// a pair send in production: a human approves a request, and a refresh makes the
	// permission reachable from comm.db.
	actor, err := st.FindOrCreateActor(ctx, "human", "admin")
	if err != nil {
		t.Fatal(err)
	}
	alpha, err := st.CreateStation(ctx, "alpha", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	beta, err := st.CreateStation(ctx, "beta", "", actor)
	if err != nil {
		t.Fatal(err)
	}

	register := func(name string, station *store.Station) (string, string) {
		t.Helper()
		tok := mintToken(t, st, name, "comm")
		prin, err := authenticate(ctx, st, tok, ScopeComm)
		if err != nil {
			t.Fatal(err)
		}
		// NOTHING TO REGISTER AND NOTHING TO BIND. A station comes with a mailbox, so claiming the
		// station for a conversation is the whole setup — where this used to register an endpoint,
		// mint a station key and bind the two together.
		key := "conv-" + name
		if _, _, err := st.ClaimStationForSession(ctx, key, name, prin.ActorID, station.StationID); err != nil {
			t.Fatal(err)
		}
		return tok, key
	}
	tokA, credA := register("pair-a", alpha)
	tokB, credB := register("pair-b", beta)

	// A THIRD STATION REACHABLE ONLY VIA A ROOM. The directory builds room co-members at a
	// SEPARATE construction site from linked stations (the D4 catch-up path), and a
	// mutation run proved that site could drop the address while every other test stayed
	// green. Two paths, two chances to ship an entry nobody can address.
	gamma, err := st.CreateStation(ctx, "gamma", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	if err := cs.ReplaceRoomMirror(ctx,
		map[string][]string{"room-x": {"s:" + alpha.StationID, "s:" + gamma.StationID}}, 1); err != nil {
		t.Fatal(err)
	}

	if _, err := st.EnsureStationLink(ctx, alpha.StationID, beta.StationID, actor); err != nil {
		t.Fatal(err)
	}
	// A FOURTH STATION, LINKED TO BETA BUT NOT TO ALPHA. It used to exist so "not linked" and
	// "unknown station" could be told apart. Auto-linking took that job away — a station alpha has
	// never contacted is now the FIRST-CONTACT case, not a refusal — so delta is the subject of
	// that arm instead, and afterwards of the suspended-link arm which is the only way to reach
	// ErrNotLinked at all now.
	delta, err := st.CreateStation(ctx, "delta", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnsureStationLink(ctx, beta.StationID, delta.StationID, actor); err != nil {
		t.Fatal(err)
	}

	// The console's refresh, verbatim: read ken.db's active links, stamp them with the
	// roster epoch, replace the projection.
	pairs, err := st.LinkMirrorRows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	epoch, err := st.RosterEpoch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := cs.ReplaceLinkMirror(ctx, pairs, epoch); err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 2 {
		t.Fatalf("LinkMirrorRows returned %d pairs after two approvals, want 2 — if this is zero, "+
			"the approval chain is broken and every assertion below would fail for the wrong reason",
			len(pairs))
	}

	srv := httptest.NewServer(NewHTTPHandler(Deps{Comm: cs, Store: st}))
	t.Cleanup(srv.Close)
	connect := func(tok, _ string) *mcp.ClientSession {
		t.Helper()
		cli := mcp.NewClient(&mcp.Implementation{Name: "pair", Version: "0"}, nil)
		sess, err := cli.Connect(ctx, &mcp.StreamableClientTransport{
			Endpoint:             srv.URL,
			HTTPClient:           &http.Client{Transport: bearerRT{token: tok, base: http.DefaultTransport}},
			DisableStandaloneSSE: true,
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = sess.Close() })
		return sess
	}
	sessA := connect(tokA, credA)

	textOf := func(res *mcp.CallToolResult) string {
		var b strings.Builder
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				b.WriteString(tc.Text)
			}
		}
		return b.String()
	}

	// THE POSITIVE ARM. If the SDK rejects `to_station` at schema validation, this fails
	// here with a validation error and never reaches the handler — which is exactly how
	// B failed.
	res, err := sessA.CallTool(ctx, &mcp.CallToolParams{
		Name:      "comm_send",
		Arguments: map[string]any{"session_key": credA, "to_station": beta.StationID, "body": "over http", "idempotency_key": "p2-http-1"},
	})
	if err != nil {
		t.Fatalf("comm_send{to_station} was rejected before the handler ran: %v.\n"+
			"That is the jsonschema-go shape B was caught by — check the struct tag carries omitempty.", err)
	}
	if res.IsError {
		t.Fatalf("comm_send{to_station} returned a tool error: %s", textOf(res))
	}
	var out struct {
		MessageID  string `json:"message_id"`
		Recipients int    `json:"recipients"`
		Seq        int64  `json:"seq"`
	}
	if err := json.Unmarshal([]byte(textOf(res)), &out); err != nil {
		t.Fatalf("send result did not parse: %v (%s)", err, textOf(res))
	}
	if out.MessageID == "" || out.Recipients != 1 {
		t.Fatalf("send result = %+v, want one recipient and a message id", out)
	}

	// THE PEER RECEIVES IT, and is told how to answer.
	sessB := connect(tokB, credB)
	res, err = sessB.CallTool(ctx, &mcp.CallToolParams{
		Name: "comm_poll", Arguments: map[string]any{"session_key": credB, "wait_seconds": 0},
	})
	if err != nil || res.IsError {
		t.Fatalf("comm_poll failed: %v %s", err, textOf(res))
	}
	var poll struct {
		Messages []struct {
			Body           string `json:"body"`
			Scope          string `json:"scope"`
			ReplyToStation string `json:"reply_to_station"`
			ChannelID      string `json:"channel_id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(textOf(res)), &poll); err != nil {
		t.Fatalf("poll result did not parse: %v", err)
	}
	if len(poll.Messages) != 1 {
		t.Fatalf("peer polled %d messages, want 1", len(poll.Messages))
	}
	got := poll.Messages[0]
	if got.Body != "over http" {
		t.Errorf("body = %q", got.Body)
	}
	if got.ReplyToStation != alpha.StationID {
		t.Errorf("reply_to_station = %q, want the sending station — a recipient that cannot see the reply "+
			"address has to reverse-engineer it from the scope string", got.ReplyToStation)
	}
	if got.ChannelID != "" {
		t.Errorf("channel_id = %q — a pair message belongs to no channel, and a non-empty value "+
			"here would be passed straight back to a tool that rejects it", got.ChannelID)
	}

	// comm_channels LISTS THE PAIR. The listing and the send read the same mirror, so a
	// peer named here is always one the send would accept.
	res, err = sessA.CallTool(ctx, &mcp.CallToolParams{Name: "comm_channels", Arguments: map[string]any{"session_key": credA}})
	if err != nil || res.IsError {
		t.Fatalf("comm_channels failed: %v %s", err, textOf(res))
	}
	var chans struct {
		Pairs []struct {
			StationID   string `json:"station_id"`
			AddressWith string `json:"address_with"`
		} `json:"pairs"`
	}
	if err := json.Unmarshal([]byte(textOf(res)), &chans); err != nil {
		t.Fatalf("channels result did not parse: %v", err)
	}
	if len(chans.Pairs) != 1 || chans.Pairs[0].StationID != beta.StationID {
		t.Fatalf("pairs = %+v, want exactly beta", chans.Pairs)
	}
	if !strings.Contains(chans.Pairs[0].AddressWith, `to_station:"`+beta.StationID+`"`) {
		t.Errorf("address_with = %q — it must be the literal call shape", chans.Pairs[0].AddressWith)
	}

	// EVERY OTHER ADDRESSING MODE STILL WORKS WITHOUT to_station IN THE ARGUMENTS.
	//
	// This is the arm a mutation run proved was missing: DELETING `omitempty` from the
	// ToStation tag left every test green, because jsonschema-go infers `required` from
	// the ABSENCE of omitempty and every call above happens to pass the field. A build
	// that made to_station required would have shipped, and every channel and room send
	// on every running session would have started failing at schema validation — before
	// any handler ran, with an error naming a field the caller never heard of.
	//
	// A room send is the cheapest proof: no to_station, no channel_id, and it must reach
	// the handler. It fails on membership (this station is in no room), which is a
	// HANDLER error — exactly what proves the schema let it through. A schema rejection
	// would surface as a transport error from CallTool instead.
	res, err = sessA.CallTool(ctx, &mcp.CallToolParams{
		Name:      "comm_send",
		Arguments: map[string]any{"to_room": "no-such-room", "body": "room-addressed"},
	})
	if err != nil {
		t.Fatalf("a room send with no to_station was rejected before the handler ran: %v.\n"+
			"That means to_station became REQUIRED — check that its struct tag still carries "+
			"omitempty, because jsonschema-go infers required from its absence.", err)
	}
	if !res.IsError {
		t.Fatal("a send to a nonexistent room succeeded")
	}
	if msg := textOf(res); strings.Contains(msg, "to_station") {
		t.Errorf("the refusal blames to_station on a room-addressed send: %s", msg)
	}

	// THE DIRECTORY HANDS BACK A SPENDABLE ADDRESS.
	//
	// 3.12.0's own to_station description said "Get it from comm_channels (pairs) or
	// comm_directory" and directoryEntry had no id field, so half that sentence was false
	// the day it shipped — and frozen into every session that connected after it. The fix
	// was to make the sentence true, so this asserts the ROUND TRIP rather than the field:
	// read an id out of comm_directory and spend it on comm_send, with nothing in between.
	res, err = sessA.CallTool(ctx, &mcp.CallToolParams{Name: "comm_directory", Arguments: map[string]any{"session_key": credA}})
	if err != nil || res.IsError {
		t.Fatalf("comm_directory failed: %v %s", err, textOf(res))
	}
	var dir struct {
		Stations []struct {
			Name      string `json:"name"`
			StationID string `json:"station_id"`
			Linked    bool   `json:"linked"`
		} `json:"stations"`
	}
	if err := json.Unmarshal([]byte(textOf(res)), &dir); err != nil {
		t.Fatalf("directory result did not parse: %v", err)
	}
	var fromDirectory string
	var sawRoomMate bool
	for _, e := range dir.Stations {
		if e.Name == "gamma" {
			sawRoomMate = true
		}
		if e.StationID == "" {
			t.Errorf("directory entry %q carries no station_id — a directory whose job is "+
				"\"who may I talk to\" must also answer \"how\"", e.Name)
		}
		if e.Name == "beta" {
			fromDirectory = e.StationID
		}
	}
	if fromDirectory == "" {
		t.Fatal("the linked peer is absent from comm_directory, or carries no id")
	}
	if !sawRoomMate {
		t.Fatal("the room co-member is absent from comm_directory — without it the id " +
			"assertion above never visits the second construction site and proves nothing about it")
	}
	if fromDirectory != beta.StationID {
		t.Fatalf("directory gave station_id %q for beta, want %q", fromDirectory, beta.StationID)
	}
	// SPEND IT. Uses the value the directory returned, never the one the test already
	// held — otherwise this proves the field exists and not that it is the right value.
	res, err = sessA.CallTool(ctx, &mcp.CallToolParams{
		Name: "comm_send",
		Arguments: map[string]any{"session_key": credA, "to_station": fromDirectory, "body": "addressed from the directory",
			"idempotency_key": "p2-from-directory"},
	})
	if err != nil {
		t.Fatalf("an id taken from comm_directory was rejected by comm_send: %v", err)
	}
	if res.IsError {
		t.Fatalf("an id taken from comm_directory was refused by comm_send: %s", textOf(res))
	}

	// EVERY REFUSAL MUST REACH THE CALLER AS ITSELF, NOT AS "internal error".
	//
	// THIS IS THE TEST THAT WAS MISSING, and its absence cost a live defect. The store
	// tests assert errors.Is against each sentinel and pass; commError flattens by
	// sentinel and anything its switch does not name falls through to
	// `errors.New("internal error")`. So four carefully written refusals — the whole
	// vocabulary of this feature — arrived at sessions as four words that mean "the
	// server is broken". ken-prod-ops received it from a live revocation test hours after
	// 3.12.0 shipped.
	//
	// A store-level assertion cannot see this. Only a caller-level one can, which is why
	// this arm asserts the TEXT a tool result carries rather than an error identity.
	for _, c := range []struct {
		name, toStation, want string
	}{
		{"self", alpha.StationID, "that is your own station"},
		{"unknown", "st-no-such-station-here", "no station with that id is known here"},
	} {
		res, err := sessA.CallTool(ctx, &mcp.CallToolParams{
			Name:      "comm_send",
			Arguments: map[string]any{"session_key": credA, "to_station": c.toStation, "body": "refusal probe " + c.name},
		})
		if err != nil {
			t.Fatalf("%s: transport error rather than a tool error: %v", c.name, err)
		}
		if !res.IsError {
			t.Fatalf("%s: the send was ACCEPTED, so this arm proves nothing about its refusal", c.name)
		}
		got := textOf(res)
		if strings.Contains(got, "internal error") {
			t.Errorf("%s: the caller received %q.\n"+
				"A refusal indistinguishable from a server fault invites the two wrong responses "+
				"(retry, or report an outage) and makes the right one (stop, tell your human) "+
				"unreachable. Wrap the sentinel in comm.CallerSafe.", c.name, got)
			continue
		}
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: caller received %q, want text containing %q", c.name, got, c.want)
		}
	}

	// THE UNBOUND-SENDER ARM IS DELETED. It asserted that an endpoint with no station could not
	// address one — a fact about a state that no longer exists, since a station comes with a
	// mailbox and there is no way to hold one without the other.

	// *** THE "UNLINKED" ARM IS NOW A SUCCESS ARM, AND IT WAS GREEN FOR THE WRONG REASON. ***
	//
	// It asserted that a send to delta — a station linked to beta, never to alpha — was refused
	// with "no approved link joins you to that station". It kept passing after auto-linking
	// shipped, because this harness built Deps with only Comm and Store while the auto-link push
	// went through an OPTIONAL SyncLinkMirror hook that was nil here. So the link landed in ken.db,
	// comm.db never heard, and the send refused — a refusal no deployment would ever produce,
	// asserted as correct behaviour.
	//
	// That is the same silent-instrument shape this file already carries two scars from: the check
	// and the thing being checked rendered identically. The hook is gone; the send path pushes the
	// mirror itself, so no harness can be missing the wiring production has.
	//
	// FIRST CONTACT IS ASSERTED END TO END, and the link is read back from ken.db afterwards. A
	// send that succeeded would be worth little on its own: what makes it the acceptance test for
	// this whole wave is that the ROW EXISTS, active, created by nothing but the message.
	res, err = sessA.CallTool(ctx, &mcp.CallToolParams{
		Name: "comm_send",
		Arguments: map[string]any{"session_key": credA, "to_station": delta.StationID,
			"body": "first contact", "idempotency_key": "p2-first-contact"},
	})
	if err != nil {
		t.Fatalf("first contact was rejected before the handler ran: %v", err)
	}
	if res.IsError {
		t.Fatalf("a send to a station with NO prior link was refused: %s.\n"+
			"Links are created on first contact now — a refusal here means the send path created "+
			"the row and never told comm.db, which is a permission that exists and cannot be spent.",
			textOf(res))
	}
	linksNow, err := st.ListStationLinks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var madeByContact bool
	for _, l := range linksNow {
		if (l.StationA == alpha.StationID && l.StationB == delta.StationID) ||
			(l.StationA == delta.StationID && l.StationB == alpha.StationID) {
			madeByContact = true
			if l.State != "active" {
				t.Errorf("the link created on first contact is %q, want active", l.State)
			}
		}
	}
	if !madeByContact {
		t.Error("the message went through and left no link — the audit trail Vlad kept the concept " +
			"for is the only record that these two stations ever started talking")
	}

	// AND THE OFF-SWITCH STILL STOPS IT, which is the half that makes the success above safe.
	// Without this arm, "everything is permitted" would pass this test just as well.
	// THE SECOND LINK IS REMOVED BEFORE SUSPENDING, AND ITS REMOVAL IS THE POINT.
	//
	// This arm used to leave beta<->delta standing, which kept delta visible in the mirror for an
	// unrelated reason — so the refusal below read SUSPENDED while the code was deciding it from
	// "does this station appear in ANY mirror row". In a real two-station estate the answer after a
	// suspend is always "no", and the session was told to re-check a typo. The test passed on a
	// fixture artefact; deleting the artefact is what makes it a test.
	if err := st.SetStationLinkSuspended(ctx, linkBetweenStations(t, st, ctx, beta.StationID, delta.StationID), true); err != nil {
		t.Fatal(err)
	}
	suspendMe := linkBetweenStations(t, st, ctx, alpha.StationID, delta.StationID)
	if err := st.SetStationLinkSuspended(ctx, suspendMe, true); err != nil {
		t.Fatal(err)
	}
	syncLinkMirror(ctx, Deps{Comm: cs, Store: st})
	res, err = sessA.CallTool(ctx, &mcp.CallToolParams{
		Name: "comm_send",
		Arguments: map[string]any{"session_key": credA, "to_station": delta.StationID,
			"body": "after suspension", "idempotency_key": "p2-after-suspend"},
	})
	if err != nil {
		t.Fatalf("transport error on the suspended-link arm: %v", err)
	}
	if !res.IsError {
		t.Fatal("a send over a SUSPENDED link succeeded — auto-linking re-created what a human " +
			"turned off, which makes the off-switch a button that does nothing")
	}
	if got := textOf(res); !strings.Contains(got, "SUSPENDED") {
		t.Errorf("the refusal for a suspended link reads %q — it must say a human turned this off, "+
			"because the one thing a session must NOT do here is retry", got)
	}

	// THE ARITHMETIC. Two addresses together must be REFUSED — this is the case the old
	// two-way boolean identity would have admitted once a third address existed.
	res, err = sessA.CallTool(ctx, &mcp.CallToolParams{
		Name:      "comm_send",
		Arguments: map[string]any{"session_key": credA, "to_station": beta.StationID, "channel_id": "c-whatever", "body": "two addresses"},
	})
	if err == nil && !res.IsError {
		t.Fatal("comm_send accepted BOTH channel_id and to_station — with three addresses the " +
			"exactly-one check must count, not compare a pair")
	}
	// And none at all is still refused.
	res, err = sessA.CallTool(ctx, &mcp.CallToolParams{
		Name: "comm_send", Arguments: map[string]any{"body": "no address"},
	})
	if err == nil && !res.IsError {
		t.Fatal("comm_send accepted a message with no address")
	}
}

// linkBetweenStations returns the link id joining two stations, failing loudly when there is none.
func linkBetweenStations(t *testing.T, st *store.Store, ctx context.Context, x, y string) string {
	t.Helper()
	all, err := st.ListStationLinks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range all {
		if (l.StationA == x && l.StationB == y) || (l.StationA == y && l.StationB == x) {
			return l.LinkID
		}
	}
	t.Fatalf("no link between %s and %s", x, y)
	return ""
}
