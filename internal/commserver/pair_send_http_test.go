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
	alpha, err := st.CreateStation(ctx, 1, "alpha", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	beta, err := st.CreateStation(ctx, 1, "beta", "", actor)
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
		ep, secret, err := cs.RegisterEndpoint(ctx,
			comm.Owner{TokenID: prin.TokenID, ActorID: prin.ActorID, SpaceID: prin.SpaceID}, name, "")
		if err != nil {
			t.Fatal(err)
		}
		// A REAL station key. Binding records the key that did it and every later call
		// re-checks whether that key was revoked, so a fabricated id makes every tool
		// call fail at AUTH — a refusal that would look exactly like the one under test.
		keyStr, err := st.IssueStationKey(ctx, actor, station.StationID, name, []string{"station"})
		if err != nil {
			t.Fatal(err)
		}
		keyID := strings.Split(strings.TrimPrefix(keyStr, "kens_"), "_")[0]
		if err := cs.BindEndpointToStation(ctx, ep.EndpointID, station.StationID, keyID); err != nil {
			t.Fatal(err)
		}
		return tok, ep.EndpointID + "|" + secret
	}
	tokA, credA := register("pair-a", alpha)
	tokB, credB := register("pair-b", beta)

	reqID, err := st.CreateStationLinkRequest(ctx, 1, "tok", alpha.StationID, beta.StationID, "because", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ApproveLinkRequest(ctx, reqID, actor); err != nil {
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
	if len(pairs) != 1 {
		t.Fatalf("LinkMirrorRows returned %d pairs after one approval, want 1 — if this is zero, "+
			"the approval chain is broken and every assertion below would fail for the wrong reason",
			len(pairs))
	}

	srv := httptest.NewServer(NewHTTPHandler(Deps{Comm: cs, Store: st}))
	t.Cleanup(srv.Close)
	connect := func(tok, cred string) *mcp.ClientSession {
		t.Helper()
		id, secret, _ := strings.Cut(cred, "|")
		cli := mcp.NewClient(&mcp.Implementation{Name: "pair", Version: "0"}, nil)
		sess, err := cli.Connect(ctx, &mcp.StreamableClientTransport{
			Endpoint: srv.URL,
			HTTPClient: &http.Client{Transport: hdrRT{
				token: tok, epID: id, epSecret: secret, base: http.DefaultTransport,
			}},
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
		Arguments: map[string]any{"to_station": beta.StationID, "body": "over http", "idempotency_key": "p2-http-1"},
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
		Name: "comm_poll", Arguments: map[string]any{"wait_seconds": 0},
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
	res, err = sessA.CallTool(ctx, &mcp.CallToolParams{Name: "comm_channels", Arguments: map[string]any{}})
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

	// THE ARITHMETIC. Two addresses together must be REFUSED — this is the case the old
	// two-way boolean identity would have admitted once a third address existed.
	res, err = sessA.CallTool(ctx, &mcp.CallToolParams{
		Name:      "comm_send",
		Arguments: map[string]any{"to_station": beta.StationID, "channel_id": "c-whatever", "body": "two addresses"},
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
