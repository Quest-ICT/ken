package commserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Quest-ICT/ken/internal/comm"
	"github.com/Quest-ICT/ken/internal/tooldoc"
)

// deliveredCommInstructions returns the instruction block a real client receives in
// its initialize result, over real HTTP through NewHTTPHandler.
//
// The layer matters. The defect this file pins is in WHAT A SESSION IS TOLD AT
// CONNECT, and the const is one layer below that: an edit that fixed the const while
// dropping it from ServerOptions would leave every session with no rule at all, and a
// const-only assertion would still pass. So the value under test is the one the
// client reads back.
func deliveredCommInstructions(t *testing.T) string {
	t.Helper()
	st := newKB(t)
	cs, err := comm.Open(filepath.Join(t.TempDir(), "comm.db"), comm.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	if err := cs.Migrate(); err != nil {
		t.Fatal(err)
	}
	tok := mintToken(t, st, "hearsay-agent", "comm")

	srv := httptest.NewServer(NewHTTPHandler(Deps{Comm: cs, Store: st}))
	t.Cleanup(srv.Close)

	cli := mcp.NewClient(&mcp.Implementation{Name: "hearsay", Version: "0"}, nil)
	sess, err := cli.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:             srv.URL,
		HTTPClient:           &http.Client{Transport: dirRT{token: tok, base: http.DefaultTransport}},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })

	// EVERYTHING THE CLIENT ACTUALLY RECEIVES: the connect-time block plus every tool
	// description, read off the live session rather than out of the source.
	//
	// The block alone stopped being the right corpus when the instructions were refitted under
	// version.InstructionBudget: per-tool rules moved into the descriptions of the tools they
	// govern, because the client truncates the instructions field and does not truncate these.
	// A test that kept reading only the block would fail for every rule that moved — and, worse,
	// would keep passing for any rule left sitting past the cut, which is the state this whole
	// refit existed to end.
	var sb strings.Builder
	sb.WriteString(sess.InitializeResult().Instructions)
	tools, err := sess.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) < 10 {
		t.Fatalf("only %d tools listed; the corpus is broken, not the text", len(tools.Tools))
	}
	for _, tl := range tools.Tools {
		sb.WriteString("\n")
		sb.WriteString(tl.Description)
		// AND THE RULES BEHIND EACH DESCRIPTION, which is where per-tool detail moved a second
		// time. It went instructions -> descriptions when the client was found to truncate the
		// instructions field; it went descriptions -> ken_instructions{tool:"…"} when the deeper
		// problem was faced, which is that a description pins at conversation start and can never
		// be corrected. That is the very sentence this test's failure message used to end with.
		//
		// So the corpus is still "everything a session can receive", and what changed is that some
		// of it now arrives through a CALL. The brief carries a pointer to it, and that the pointer
		// is present on every tool is asserted in allserver — here it is taken as given, so this
		// test can keep asking its own question: does the rule exist anywhere a session can read it.
		if full, ok := tooldoc.Full(tl.Name); ok {
			sb.WriteString("\n")
			sb.WriteString(full)
		}
	}
	return sb.String()
}

// THE HEARSAY RULE MUST NAME THE IDENTITY THAT OUTLIVES THE CONVERSATION.
//
// Endpoint rows are deleted by the idle sweep (internal/comm/message.go:1105, default
// 7 days) and the knowledge base has no TTL, so an entry attributed to an endpoint
// cites a row that will not exist — and three sessions of one correspondent read as
// three unrelated strangers. The durable name is the station, and comm_poll already
// hands the reader both halves of it.
func TestTheHearsayRuleNamesTheDurableIdentity(t *testing.T) {
	got := deliveredCommInstructions(t)

	// POSITIVE CONTROL, asserted FIRST. Every check below is "the text contains X" or
	// "does not contain Y", and an empty Instructions string satisfies every negative
	// one of them. So prove the block arrived before concluding anything about what is
	// in it.
	const anchor = "Ken COMM — inter-session messaging between AI sessions."
	if !strings.Contains(got, anchor) {
		t.Fatalf("the initialize result carried %d characters of instructions and not the opening line.\n"+
			"Nothing below this point would be evidence of anything: an absent block passes every "+
			"\"must not contain\" check in this test.", len(got))
	}

	// EACH CHECK NAMES A PROPERTY AND ACCEPTS ANY WORDING THAT CARRIES IT.
	//
	// These were four exact sentences, pinned against the connect-time block. Three of them now
	// live in comm_directory's and comm_bind's descriptions, because the block is truncated at
	// version.InstructionBudget and per-tool rules were moved to where they arrive intact. Pinning
	// the SENTENCE would have forced the text back into the field that cuts it — the test would
	// have been the reason the defect returned.
	//
	// So the contract is the meaning, and the corpus is everything the client receives. A rewrite
	// that keeps the rule passes; one that drops it does not.
	for _, c := range []struct {
		property string
		anyOf    []string
	}{
		{"that the durable identity is the STATION, not the endpoint",
			[]string{"the STATION", "sending STATION", "not an endpoint"}},
		{"which fields carry it off a polled message",
			[]string{"from_station_name and from_station_id"}},
		{"what to do when the sender has no station at all",
			[]string{"from_station_id is empty", "from_station_id IS EMPTY", "holds no station"}},
		{"that a session never records an outcome for a peer",
			[]string{"never record an outcome or assert verification on another session's behalf"}},
	} {
		found := false
		for _, w := range c.anyOf {
			if strings.Contains(got, w) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("nothing a session can receive says %s.\n"+
				"Attribution is not recoverable after the fact: an entry filed against an endpoint "+
				"cites a row the idle sweep will delete, and the knowledge base has no TTL to notice.", c.property)
		}
	}

	// AND THE OLD RULE IS GONE. Asserting only the new sentences would pass with both
	// present, which is the likeliest regression: an edit that adds the correction and
	// leaves the contradiction sitting above it.
	if strings.Contains(got, "attribute the sending endpoint") {
		t.Error("the connect-time block still tells sessions to attribute the sending ENDPOINT.\n" +
			"Endpoint rows are deleted by the idle sweep and the knowledge base has no expiry, so " +
			"that attribution cites a row that will not exist.")
	}
}

// AND THE FIELDS IT NAMES MUST BE FIELDS comm_poll ACTUALLY RETURNS.
//
// The instruction is frozen at connect; the result shape is not. A rename or a
// dropped field on messageView would leave every running session hunting for a key
// that no longer arrives, with nothing to tell it why.
func TestTheFieldsTheHearsayRuleNamesExistOnThePollResult(t *testing.T) {
	tags := map[string]bool{}
	rt := reflect.TypeOf(messageView{})
	for i := 0; i < rt.NumField(); i++ {
		name, _, _ := strings.Cut(rt.Field(i).Tag.Get("json"), ",")
		tags[name] = true
	}
	// POSITIVE CONTROL: a reflection walk that found nothing would report every field
	// as present-and-fine by reporting nothing at all.
	if len(tags) < 5 {
		t.Fatalf("the walk over messageView found %d json fields — it is not looking at the poll result", len(tags))
	}
	for _, f := range []string{"from_station_name", "from_station_id", "from_endpoint_id"} {
		if !tags[f] {
			t.Errorf("the connect-time hearsay rule tells every session to record %q, and comm_poll "+
				"does not return it. The instruction is frozen at connect; the field is not.", f)
		}
	}
}
