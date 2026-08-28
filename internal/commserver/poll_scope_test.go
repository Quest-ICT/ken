package commserver

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Quest-ICT/ken/internal/comm"
)

// THE SCOPE FILTER IS A PROPERTY OF THE TOOL, SO IT IS TESTED AT THE TOOL.
//
// A store-level test of PollScoped proves the SQL narrows. It proves nothing about
// whether the HANDLER passes the argument — and this handler calls the store TWICE, once
// before parking and once after the wait elapses. Dropping the scope from the second call
// leaves every non-parking test green while a hub using a long wait silently receives
// other conversations' mail. Nor does a store test prove the SDK will accept the argument
// at all: jsonschema-go infers `required` from the ABSENCE of omitempty, which has already
// rejected one new optional field before any handler ran.
func TestPollScopeFilterDrainsOneConversationAndHidesTheRest(t *testing.T) {
	sess, _, ctx := dirHarness(t)

	// TWO SCOPES, BOTH ADDRESSED TO THE CALLER'S PARTY: a pairing-code channel and a
	// room. One scope cannot show a filter working — narrowing to the only thing there is
	// looks identical to not narrowing at all.
	seedRoom(t, "ops", "s:"+dirStation, "s:sender-station")
	sender := stationBoundEndpoint(t, "sender-station")

	me, err := dirMailbox(t, ctx)
	if err != nil {
		t.Fatal(err)
	}
	ch := openChannel(t, dirComm, me, sender, "hub<->sender")
	if !ch.Open() {
		t.Fatalf("setup: channel state %q — nothing below would be testing what it claims", ch.State)
	}
	chScope := "ch:" + ch.ChannelID

	chMsg, err := dirComm.Send(ctx, sender, ch.ChannelID, "channel one", comm.SendOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dirComm.SendToRoom(ctx, sender, "ops", "room one", comm.SendOpts{}); err != nil {
		t.Fatal(err)
	}

	// POSITIVE CONTROL. Both messages are deliverable to this caller at this instant, so a
	// filtered poll returning one is NARROWING rather than merely failing.
	all := callPoll(t, sess, ctx, nil)
	if len(all.Messages) != 2 {
		t.Fatalf("an unfiltered poll returned %d messages, want 2 — the fixture does not hold two "+
			"scopes, so every assertion below would pass for the wrong reason", len(all.Messages))
	}
	if all.ScopeFilter != "" {
		t.Errorf("scope_filter = %q on an unfiltered poll, want empty", all.ScopeFilter)
	}

	// THE CHANNEL, ALONE.
	got := callPoll(t, sess, ctx, map[string]any{"scope": chScope})
	if len(got.Messages) != 1 || got.Messages[0].Body != "channel one" {
		t.Fatalf("scope=%q returned %d messages %s, want exactly the channel message",
			chScope, len(got.Messages), bodiesOf(got))
	}
	if got.ScopeFilter != chScope {
		t.Errorf("scope_filter = %q, want %q — a caller cannot otherwise tell a server that applied "+
			"the filter from one that ignored it", got.ScopeFilter, chScope)
	}

	// THE ROOM, ALONE. Two arms, not one: a filter hardcoded to either namespace, or one
	// that happens to return the first row, passes with a single arm.
	got = callPoll(t, sess, ctx, map[string]any{"scope": "r:ops"})
	if len(got.Messages) != 1 || got.Messages[0].Body != "room one" {
		t.Fatalf("scope=r:ops returned %d messages %s, want exactly the room message",
			len(got.Messages), bodiesOf(got))
	}

	// THE SECOND STORE CALL — THE ONE AFTER THE WAIT — MUST CARRY THE FILTER TOO.
	//
	// With the channel message acked, the only mail left is the room's. A scoped poll that
	// parks and then re-reads UNFILTERED hands that room message back; a correct one returns
	// empty. This is the arm that fails when in.Scope is dropped from the post-wait call, and
	// no non-parking test can reach it.
	if n, err := dirComm.Ack(ctx, me, chMsg.MessageID); err != nil || n == 0 {
		t.Fatalf("setup: ack settled %d deliveries (%v) — without it this arm cannot isolate the wait path", n, err)
	}
	parked := callPoll(t, sess, ctx, map[string]any{"scope": chScope, "wait_seconds": 1})
	if !parked.Waited {
		t.Fatal("the scoped poll did not park, so it never reached the second store call — " +
			"this arm proves nothing until it does")
	}
	if len(parked.Messages) != 0 {
		t.Fatalf("a poll scoped to the drained channel returned %s after parking — the post-wait "+
			"read dropped the filter", bodiesOf(parked))
	}
	// CONTROL FOR THAT ARM: the room message was deliverable the whole time it was hidden.
	after := callPoll(t, sess, ctx, nil)
	if len(after.Messages) != 1 || after.Messages[0].Body != "room one" {
		t.Fatalf("unfiltered poll after the parked one returned %s, want the room message — if it "+
			"is empty, the arm above passed because there was nothing left to hide", bodiesOf(after))
	}
}

// A FILTER THE SERVER CANNOT PARSE MUST REFUSE, NOT RETURN NOTHING.
//
// An empty list is the same answer as "your inbox is empty", so a mistyped filter that
// silently matched nothing would read as silence. The refusal is only worth anything if it
// can be told apart from a build where scoped polling never returns anything at all —
// hence the corrected-scope arm immediately after it.
func TestPollRefusesAScopeThatNamesNoNamespaceAndAcceptsTheCorrectedOne(t *testing.T) {
	sess, _, ctx := dirHarness(t)
	seedRoom(t, "ops", "s:"+dirStation, "s:sender-station")
	sender := stationBoundEndpoint(t, "sender-station")
	if _, err := dirComm.SendToRoom(ctx, sender, "ops", "room one", comm.SendOpts{}); err != nil {
		t.Fatal(err)
	}

	// The mistake a hub actually makes: passing the bare id it is holding.
	res := rawPoll(t, sess, ctx, map[string]any{"scope": "ops"})
	if !res.IsError {
		t.Fatal("comm_poll accepted scope=\"ops\" — an untagged id matches no scope_id, so the caller " +
			"would read an empty result as an empty inbox")
	}
	txt := toolResultText(res)
	for _, want := range []string{"ch:<channel_id>", "r:<room_id>"} {
		if !strings.Contains(txt, want) {
			t.Errorf("the refusal does not name %q, so it does not say how to fix it.\ngot: %s", want, txt)
		}
	}

	// POSITIVE CONTROL: the corrected scope returns the mail.
	got := callPoll(t, sess, ctx, map[string]any{"scope": "r:ops"})
	if len(got.Messages) != 1 {
		t.Fatalf("scope=r:ops returned %d messages, want 1 — the refusal above proves nothing if the "+
			"accepted form returns nothing either", len(got.Messages))
	}

	// A WELL-FORMED SCOPE THAT NAMES NOTHING IS NOT AN ERROR. Refusing here would make the
	// tool an existence oracle for channel ids.
	got = callPoll(t, sess, ctx, map[string]any{"scope": "ch:no-such-channel"})
	if len(got.Messages) != 0 {
		t.Fatalf("a scope naming no channel returned %d messages", len(got.Messages))
	}
	if got.ScopeFilter != "ch:no-such-channel" {
		t.Errorf("scope_filter = %q, want the filter echoed even when it matched nothing", got.ScopeFilter)
	}
}

// `scope_filter` MUST BE PRESENT ON EVERY RESULT, INCLUDING UNFILTERED ONES.
//
// It is the only way an ALREADY-RUNNING session can tell a server that honoured its scope
// from one that predates the argument and ignored it: tool descriptions freeze at
// conversation start, so such a session learned about `scope` from somewhere other than the
// schema and has no other evidence the server understood it. Asserted on the RAW JSON,
// because decoding into a struct makes an absent key and an empty string identical — which
// is precisely the distinction under test.
func TestPollAlwaysEchoesScopeFilter(t *testing.T) {
	sess, _, ctx := dirHarness(t)
	res := rawPoll(t, sess, ctx, nil)
	if res.IsError {
		t.Fatalf("comm_poll errored: %s", toolResultText(res))
	}
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["scope_filter"]; !ok {
		t.Fatal("the `scope_filter` key is ABSENT from an unfiltered poll result. A session that passes " +
			"scope to an older server receives an unfiltered inbox and no signal; the key's presence IS " +
			"the signal, so it must not be omitempty")
	}
}

// The argument must tell a caller how to detect a server that ignored it, and that an empty
// scoped result is not an empty inbox. Asserted by reflection on the shipped struct tag, so
// it tests the schema a client actually receives rather than the source.
func TestScopeArgumentNamesItsOwnEscapeHatches(t *testing.T) {
	f, ok := reflect.TypeOf(pollIn{}).FieldByName("Scope")
	if !ok {
		t.Fatal("pollIn has no Scope field")
	}
	desc := f.Tag.Get("jsonschema")
	for _, want := range []string{"scope_filter", "comm_channels", "HIDDEN"} {
		if !strings.Contains(desc, want) {
			t.Errorf("the scope description does not mention %q — a caller is given a filter with no way "+
				"to learn it was ignored, or that other scopes are hidden rather than empty.\ngot: %s", want, desc)
		}
	}
	// OPTIONAL IN THE SCHEMA, NOT JUST IN THE PROSE. jsonschema-go infers `required` from the
	// absence of omitempty, and a required `scope` would reject every existing caller before
	// the handler ran.
	if !strings.Contains(f.Tag.Get("json"), "omitempty") {
		t.Error("the scope json tag has no omitempty — the SDK would infer it as REQUIRED and reject " +
			"every poll that does not pass it")
	}
}

// --- helpers ---------------------------------------------------------------------

// rawPoll calls comm_poll over the real MCP session and returns the result unexamined, so a
// test can assert on a refusal. Defaults to wait_seconds=-1 so nothing parks unless a test
// asks it to; `extra` is merged last and may override that.
func rawPoll(t *testing.T, sess *mcp.ClientSession, ctx context.Context, extra map[string]any) *mcp.CallToolResult {
	t.Helper()
	args := dirCreds()
	args["wait_seconds"] = -1
	for k, v := range extra {
		args[k] = v
	}
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "comm_poll", Arguments: args})
	if err != nil {
		t.Fatalf("comm_poll was rejected before the handler ran: %v.\n"+
			"That is the jsonschema-go shape a new optional field fails in — check pollIn's tag carries omitempty.", err)
	}
	return res
}

// callPoll is rawPoll plus "this must have succeeded", decoded into the shipped struct.
func callPoll(t *testing.T, sess *mcp.ClientSession, ctx context.Context, extra map[string]any) pollOut {
	t.Helper()
	res := rawPoll(t, sess, ctx, extra)
	if res.IsError {
		t.Fatalf("comm_poll errored: %s", toolResultText(res))
	}
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var out pollOut
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func toolResultText(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// bodiesOf renders the bodies in a result: "want 1, got 2" does not say WHICH conversation
// leaked, and that is the whole question when a filter is wrong.
func bodiesOf(o pollOut) string {
	out := make([]string, 0, len(o.Messages))
	for _, m := range o.Messages {
		out = append(out, m.Body)
	}
	return "[" + strings.Join(out, ", ") + "]"
}
