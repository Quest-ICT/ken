package stationserver

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Quest-ICT/ken/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// meVia calls station_me over a real MCP session and returns the decoded briefing.
func meVia(t *testing.T, srv string, key string) meOut {
	t.Helper()
	ctx := context.Background()
	cli := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	sess, err := cli.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             srv,
		HTTPClient:           &http.Client{Transport: stnRT{token: key, base: http.DefaultTransport}},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "station_me", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("station_me errored: %+v", res.Content)
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var out meOut
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// *** THE CASE THIS FIELD EXISTS FOR: nothing here, something there. ***
//
// A session staffs one station and its briefing stops at that boundary — but the human does
// not have that boundary. Without this, a session whose own list is empty tells its human
// "nothing is waiting on you" while another station's pile grows unmentioned, and that
// answer is worse than silence because it is confidently wrong.
func TestBriefingReportsWhatIsWaitingOnOtherStations(t *testing.T) {
	st, srv, key, mine := harness(t)
	ctx := context.Background()
	lim := store.DefaultStationTaskLimits()

	other, err := st.CreateStation(ctx, 1, "other-post", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	third, err := st.CreateStation(ctx, 1, "third-post", "", 1)
	if err != nil {
		t.Fatal(err)
	}

	// CONTROL: with nothing anywhere, the field is absent — not zero. "Nothing elsewhere"
	// and "this deployment cannot answer" would otherwise look identical, and only one is
	// worth chasing.
	if b := meVia(t, srv.URL, key); b.Elsewhere != nil {
		t.Fatalf("reported %+v before any task existed anywhere", b.Elsewhere)
	}

	add := func(sid, text, blocked string) {
		t.Helper()
		if _, _, err := st.AddStationTask(ctx, lim,
			store.StationTask{StationID: sid, Text: text, BlockedOn: blocked}, "tok", 1, false); err != nil {
			t.Fatal(err)
		}
	}
	add(other.StationID, "approve the thing", "human")
	add(third.StationID, "decide the other thing", "human")
	add(third.StationID, "and this one", "human")
	add(third.StationID, "not this one", "self") // not blocked on the human
	_ = mine

	b := meVia(t, srv.URL, key)
	if b.Elsewhere == nil {
		t.Fatal("three tasks wait on the human across two other stations and the briefing said nothing")
	}
	if b.Elsewhere.Tasks != 3 || b.Elsewhere.Stations != 2 {
		t.Fatalf("got %d tasks across %d stations, want 3 across 2", b.Elsewhere.Tasks, b.Elsewhere.Stations)
	}
	// IT CARRIES NO CONTENTS. S6: a station key does not let its holder read another
	// station's assets. Two integers are not assets; a task's text is.
	blob, _ := json.Marshal(b)
	for _, leak := range []string{"approve the thing", "decide the other thing", "other-post", "third-post", other.StationID, third.StationID} {
		if strings.Contains(string(blob), leak) {
			t.Fatalf("the briefing leaked %q from a station this key does not staff", leak)
		}
	}
	// THE RELAY MUST SAY IT. The count reaches nobody if the session is not told to speak
	// it — that is the whole design of this field, and the reason `relay_to_human` exists.
	if b.Relay == "" {
		t.Fatal("nothing waits on this station, three wait elsewhere, and the relay is empty")
	}
	if !strings.Contains(b.Relay, "/stations") {
		t.Fatalf("the relay does not point at the console, which is the only place the pile is readable: %q", b.Relay)
	}
}

// THE OWN PILE IS NOT DOUBLE-COUNTED. The briefing already reports blocked_on_human for this
// station; if `elsewhere` included it too, every session would hand its human a number larger
// than the truth, on the one figure it is instructed to say out loud.
func TestElsewhereExcludesTheCallersOwnStation(t *testing.T) {
	st, srv, key, mine := harness(t)
	ctx := context.Background()
	lim := store.DefaultStationTaskLimits()
	for _, txt := range []string{"mine one", "mine two"} {
		if _, _, err := st.AddStationTask(ctx, lim,
			store.StationTask{StationID: mine.StationID, Text: txt, BlockedOn: "human"}, "tok", 1, false); err != nil {
			t.Fatal(err)
		}
	}
	b := meVia(t, srv.URL, key)
	if b.Tasks.BlockedOnHuman != 2 {
		t.Fatalf("own blocked_on_human = %d, want 2 — the fixture is wrong", b.Tasks.BlockedOnHuman)
	}
	if b.Elsewhere != nil {
		t.Fatalf("the caller's own %d tasks were counted as elsewhere: %+v", b.Tasks.BlockedOnHuman, b.Elsewhere)
	}
}
