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
	return sess.InitializeResult().Instructions
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

	for _, want := range []string{
		"attribute it to the identity that will still exist when someone reads the entry — the STATION",
		"Take from_station_name and from_station_id off the polled message",
		"WHEN from_station_id IS EMPTY the sender holds no station, and from_endpoint_id is all there is",
		"never record an outcome or assert verification on another session's behalf",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the connect-time block no longer carries %q.\n"+
				"A session captures this once, at conversation start, and can never be sent a "+
				"correction — it will attribute peer knowledge this way for the whole conversation.", want)
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
