package stationserver

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Quest-ICT/ken/internal/store"
)

// THE FIGURE HAS TO REACH THE RESULT, and that is a different layer from the one that
// computes it. buildBriefing copies the store's briefing into briefingView field by field, so
// a field left out of that literal compiles clean, passes every store test, and serialises as
// 0 forever. This reads the WIRE KEY out of a real station_me call rather than a Go struct, so
// a wrong json tag fails here too.
//
// It is also the only channel that reaches a session already running: tool descriptions and
// server instructions pin at connect; results never do.
func TestStationMeResultCarriesTheAgeOfEveryOpenTask(t *testing.T) {
	st, srv, key, station := harness(t)
	ctx := context.Background()
	lim := store.DefaultStationTaskLimits()

	mine, _, err := st.AddStationTask(ctx, lim,
		store.StationTask{StationID: station.StationID, Text: "read the 1.5.1 and 1.5.2 promo briefs", BlockedOn: "self"},
		"tok", 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.W.ExecContext(ctx,
		`UPDATE station_task SET created_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','-21 days') WHERE task_id=?`,
		mine.TaskID); err != nil {
		t.Fatal(err)
	}
	// POSITIVE CONTROL: a recent human-blocked item, so the briefing carries TWO different
	// non-zero ages and neither figure can pass by being the other one — or by being absent.
	theirs, _, err := st.AddStationTask(ctx, lim,
		store.StationTask{StationID: station.StationID, Text: "decide the release date", BlockedOn: "human"},
		"tok", 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.W.ExecContext(ctx,
		`UPDATE station_task SET created_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','-3 days') WHERE task_id=?`,
		theirs.TaskID); err != nil {
		t.Fatal(err)
	}

	cli := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	sess, err := cli.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             srv.URL,
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
	var out struct {
		Tasks map[string]any `json:"tasks"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	num := func(k string) float64 {
		t.Helper()
		v, ok := out.Tasks[k]
		if !ok {
			t.Fatalf("the briefing result carries no %q key at all: %s", k, raw)
		}
		f, ok := v.(float64)
		if !ok {
			t.Fatalf("%s = %v (%T), want a number", k, v, v)
		}
		return f
	}
	// CAST truncates, so 21 days ago reads 20 once any wall time has passed.
	if got := num("oldest_open_task_days"); got < 20 || got > 21 {
		t.Fatalf("oldest_open_task_days = %v in the station_me RESULT, want 20 or 21.\n"+
			"The store computes it; this is the field-by-field copy into briefingView, where a dropped "+
			"line is a silent zero that no store test can see.", got)
	}
	if got := num("oldest_blocked_on_human_days"); got < 2 || got > 3 {
		t.Fatalf("oldest_blocked_on_human_days = %v, want 2 or 3 — positive control: the fixture is live, "+
			"the briefing is populated, and the two ages are distinct numbers.", got)
	}
}
