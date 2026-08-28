package stationserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Quest-ICT/ken/internal/store"
)

type stnRT struct {
	token string
	base  http.RoundTripper
}

func (a stnRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+a.token)
	return a.base.RoundTrip(r)
}

// dirSession builds a /station endpoint over a KB holding the caller's station, a
// published stranger, a linked-but-unpublished peer and an unpublished stranger.
// staffing is the optional hook: nil models a deployment with COMM off.
func dirSession(t *testing.T, staffing func(context.Context) (map[string]StationStaffing, error)) (*mcp.ClientSession, context.Context, map[string]string) {
	t.Helper()
	ctx := context.Background()
	st := newKB(t)
	actorID, err := st.FindOrCreateActor(ctx, "human", "curator")
	if err != nil {
		t.Fatal(err)
	}
	mk := func(name string, published bool) *store.Station {
		t.Helper()
		s, err := st.CreateStation(ctx, name, "", actorID)
		if err != nil {
			t.Fatal(err)
		}
		if published {
			if err := st.SetStationPublished(ctx, s.StationID, true); err != nil {
				t.Fatal(err)
			}
		}
		return s
	}
	ids := map[string]string{}
	mine := mk("mine", true)
	// THE CALLER'S STATION IS CLAIMED BY THE HARNESS CONVERSATION. A credential no longer names a
	// station, so the fixture declares one the way a session does.
	if _, _, err := st.ClaimStationForSession(ctx, harnessKey, "mine", actorID, mine.StationID); err != nil {
		t.Fatal(err)
	}
	pub := mk("published-stranger", true)
	peer := mk("linked-peer", false)
	hidden := mk("unpublished-stranger", false)
	ids["mine"], ids["published-stranger"] = mine.StationID, pub.StationID
	ids["linked-peer"], ids["unpublished-stranger"] = peer.StationID, hidden.StationID

	reqID, err := st.CreateStationLinkRequest(ctx, "tok", mine.StationID, peer.StationID, "r", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ApproveLinkRequest(ctx, reqID, actorID); err != nil {
		t.Fatal(err)
	}
	key, err := st.IssueToken(ctx, actorID, []string{ScopeStation}, "test")
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(NewHTTPHandler(Deps{Store: st, Staffing: staffing}))
	t.Cleanup(srv.Close)

	cli := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	sess, err := cli.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             srv.URL,
		HTTPClient:           &http.Client{Transport: stnRT{token: key, base: http.DefaultTransport}},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	prime(t, sess)
	t.Cleanup(func() { sess.Close() })
	return sess, ctx, ids
}

func callDir(t *testing.T, sess *mcp.ClientSession, ctx context.Context) dirOut {
	t.Helper()
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "station_directory", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		msg := ""
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				msg += tc.Text
			}
		}
		t.Fatalf("station_directory errored: %s", msg)
	}
	var out dirOut
	b, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// The /station mirror closes a real gap: station_link_request needs a NAME, and until
// now nothing on this surface would tell a session that a station exists. It applies
// the same visibility rule as the COMM side because it calls the same store function.
func TestStationDirectoryMirrorsTheVisibilityRule(t *testing.T) {
	sess, ctx, _ := dirSession(t, nil)
	out := callDir(t, sess, ctx)

	if out.YouAre != "mine" {
		t.Errorf("you_are = %q, want %q", out.YouAre, "mine")
	}
	names := map[string]dirEntry{}
	for _, e := range out.Stations {
		names[e.Name] = e
	}
	if len(out.Stations) != 2 {
		t.Fatalf("got %d stations, want 2: %+v", len(out.Stations), out.Stations)
	}
	if _, ok := names["unpublished-stranger"]; ok {
		t.Error("an unpublished station with no link to me is listed")
	}
	if e, ok := names["linked-peer"]; !ok || !e.Linked {
		t.Error("my established peer is missing or reports linked=false")
	}
	if e := names["published-stranger"]; e.Linked {
		t.Error("a published station I hold no link to reports linked=true")
	}
}

// With COMM off, reachability is UNKNOWN and must be reported as unknown — absent
// fields plus comm_known=false — rather than as everyone being idle. Conflating the
// two would make a session conclude nobody is available when in fact nobody asked.
func TestStationDirectoryOmitsStaffingWhenCommIsOff(t *testing.T) {
	sess, ctx, _ := dirSession(t, nil)
	out := callDir(t, sess, ctx)

	if out.CommKnown {
		t.Error("comm_known is true with no Staffing hook wired")
	}
	for _, e := range out.Stations {
		if e.Staffed != nil {
			t.Errorf("%s reports staffed=%v with COMM off — unknown was rendered as a verdict", e.Name, *e.Staffed)
		}
		if e.LastSeenAt != "" {
			t.Errorf("%s carries last_seen_at with COMM off", e.Name)
		}
	}
}

// And with COMM on the verdict is real, for the station the hook knows and NOT for
// the one it does not. Both halves matter: asserting only the positive would pass if
// every station were reported staffed, and asserting only the negative would pass if
// the hook were ignored entirely.
func TestStationDirectoryReportsStaffingWhenCommIsOn(t *testing.T) {
	staffing := map[string]StationStaffing{}
	sess, ctx, ids := dirSession(t, func(context.Context) (map[string]StationStaffing, error) {
		return staffing, nil
	})
	// Filled after construction, before the call: the hook closes over the map.
	staffing[ids["linked-peer"]] = StationStaffing{Endpoints: 2, LastSeenAt: "2026-08-07T00:00:00.000Z"}

	out := callDir(t, sess, ctx)
	if !out.CommKnown {
		t.Fatal("comm_known is false even though a Staffing hook is wired")
	}
	byName := map[string]dirEntry{}
	for _, e := range out.Stations {
		byName[e.Name] = e
	}
	peer, ok := byName["linked-peer"]
	if !ok {
		t.Fatal("linked-peer missing from the directory")
	}
	if peer.Staffed == nil {
		t.Fatal("linked-peer has no staffing verdict although the hook reported 2 endpoints for it")
	}
	if !*peer.Staffed {
		t.Error("linked-peer reports staffed=false against 2 live endpoints")
	}
	if peer.LastSeenAt != "2026-08-07T00:00:00.000Z" {
		t.Errorf("last_seen_at = %q, want the value the hook supplied", peer.LastSeenAt)
	}
	// The station the hook says nothing about stays UNKNOWN, not false.
	if other, ok := byName["published-stranger"]; ok && other.Staffed != nil {
		t.Errorf("published-stranger got a verdict (%v) from a hook that never mentioned it", *other.Staffed)
	}
}
