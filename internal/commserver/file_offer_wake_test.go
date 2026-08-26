package commserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Quest-ICT/ken/internal/comm"
	"github.com/Quest-ICT/ken/internal/store"
)

// A FILE OFFER MUST WAKE THE STATION'S LIVE READER, NOT THE SEAT FROZEN INTO THE CHANNEL.
//
// THIS TEST IS AT THE COMMSERVER LAYER ON PURPOSE, AND THE REASON IS THE LESSON. The same
// defect was fixed here on 2026-08-20 and covered by a test in `internal/comm` that asserts
// WakeTargetsFor returns the right ids — and the mutant that reverts THIS call site to
// `w.notify(res.RecipientRow)` survived it, because the defect is not in what WakeTargetsFor
// returns, it is in which value the handler passes to notify. A test one layer below a
// call-site defect cannot observe it. That is the third time in one day that exact mistake
// was made, after four refusals shipped as "internal error" and after a security fix whose
// wiring went untested.
//
// Deterministic rather than timed: `w.wait` reports whether it was WOKEN, so this asserts a
// boolean, not a duration.
func TestFileOfferWakesTheStationsLiveReaderNotTheFrozenSeat(t *testing.T) {
	ctx := context.Background()
	st := newKB(t)
	lim := comm.DefaultLimits()
	lim.FilesEnabled = true
	cs, err := comm.Open(filepath.Join(t.TempDir(), "comm.db"), lim)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	if err := cs.Migrate(); err != nil {
		t.Fatal(err)
	}

	actor, err := st.FindOrCreateActor(ctx, "human", "admin")
	if err != nil {
		t.Fatal(err)
	}
	sender, err := st.CreateStation(ctx, "sender", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := st.CreateStation(ctx, "receiver", "", actor)
	if err != nil {
		t.Fatal(err)
	}

	reg := func(name string, station *store.Station) (string, *comm.Endpoint, string) {
		t.Helper()
		tok := mintToken(t, st, name, "comm", "comm-file")
		prin, err := authenticate(ctx, st, tok, ScopeComm)
		if err != nil {
			t.Fatal(err)
		}
		ep, secret, err := cs.RegisterEndpoint(ctx,
			comm.Owner{TokenID: prin.TokenID, ActorID: prin.ActorID}, name, "")
		if err != nil {
			t.Fatal(err)
		}
		keyStr, err := st.IssueStationKey(ctx, actor, station.StationID, name, []string{"station"})
		if err != nil {
			t.Fatal(err)
		}
		keyID := strings.Split(strings.TrimPrefix(keyStr, "kens_"), "_")[0]
		if err := cs.BindEndpointToStation(ctx, ep.EndpointID, station.StationID, keyID); err != nil {
			t.Fatal(err)
		}
		bound, err := cs.AuthenticateEndpoint(ctx, ep.EndpointID, secret)
		if err != nil {
			t.Fatal(err)
		}
		return tok, bound, secret
	}
	tokS, epSender, secS := reg("wake-sender", sender)
	_, epSeat, _ := reg("wake-seat", receiver)

	// The channel freezes epSeat as the recipient rowid.
	if _, err := cs.OpenLinkedChannel(ctx, epSender, epSeat, actor, "sender <-> receiver"); err != nil {
		t.Fatal(err)
	}
	chs, err := cs.ListChannels(ctx, epSender)
	if err != nil || len(chs) == 0 {
		t.Fatalf("setup: %d channels (err=%v)", len(chs), err)
	}
	channelID := chs[0].ChannelID

	// A SECOND session now staffs the same station. It is the live reader; the seat is not.
	_, epLive, _ := reg("wake-live", receiver)
	if epLive.ID == epSeat.ID {
		t.Fatal("setup: the live reader and the frozen seat are the same endpoint, so this test " +
			"cannot distinguish them")
	}

	h := NewHTTPHandler(Deps{Comm: cs, Store: st})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	// Park a waiter on the LIVE reader, using the handler's own waiters.
	woken := make(chan bool, 1)
	go func() { woken <- h.w.wait(context.Background(), epLive.ID, 8*time.Second) }()
	for i := 0; i < 400 && h.ParkedWaiters() < 1; i++ {
		time.Sleep(time.Millisecond)
	}
	if h.ParkedWaiters() < 1 {
		t.Fatal("setup: no waiter parked, so a wakeup could not be observed either way")
	}

	cli := mcp.NewClient(&mcp.Implementation{Name: "wake", Version: "0"}, nil)
	sess, err := cli.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: srv.URL,
		HTTPClient: &http.Client{Transport: hdrRT{
			token: tokS, epID: epSender.EndpointID, epSecret: secS, base: http.DefaultTransport,
		}},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })

	res, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name: "comm_file_offer",
		Arguments: map[string]any{
			"channel_id": channelID, "name": "report.pdf", "size_bytes": 12,
			"sha256":       "0000000000000000000000000000000000000000000000000000000000000000",
			"nonce_sha256": "1111111111111111111111111111111111111111111111111111111111111111",
			"transfer":     "path",
		},
	})
	if err != nil {
		t.Fatalf("comm_file_offer: %v", err)
	}
	if res.IsError {
		var b strings.Builder
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				b.WriteString(tc.Text)
			}
		}
		t.Fatalf("comm_file_offer refused: %s", b.String())
	}

	select {
	case got := <-woken:
		if !got {
			t.Fatal("the station's LIVE reader was not woken by a file offer — the handler " +
				"notified the frozen channel seat instead, so a successor waits out its whole " +
				"poll window for a file while every other message arrives at once")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the parked waiter neither woke nor returned")
	}
}
