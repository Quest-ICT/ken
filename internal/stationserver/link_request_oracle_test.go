package stationserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Quest-ICT/ken/internal/store"
)

// A STATION THE CALLER CANNOT SEE MUST BE INDISTINGUISHABLE FROM ONE THAT DOES NOT EXIST.
//
// station_link_request resolved its to_station argument through StationByName, whose own
// contract reserves it for the console and CLI — "a name is not an address, and no
// agent-facing path may route by it (S3)". That query filters on name alone, so a
// name that existed produced a FILED REQUEST and one that did not produced a refusal. Two
// distinguishable outcomes is an enumeration oracle over every station name in the space,
// including the ones deliberately withheld from station_directory.
//
// The filed request was the worse half: a correct guess put an agent-authored ask for an
// unpublished post in front of its human — the unsolicited approach publication exists to
// prevent.
//
// This asserts the PROPERTY, not the query: same answer, and no row.
func TestLinkRequestCannotEnumerateHiddenStations(t *testing.T) {
	ctx := context.Background()
	st := newKB(t)

	actor, err := st.FindOrCreateActor(ctx, "human", "admin")
	if err != nil {
		t.Fatal(err)
	}
	mine, err := st.CreateStation(ctx, "mine", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	// Published: the caller can see it, so asking for a link is legitimate.
	visible, err := st.CreateStation(ctx, "visible-peer", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetStationPublished(ctx, visible.StationID, true); err != nil {
		t.Fatal(err)
	}
	// Never published, never linked: absent from this caller's directory entirely.
	hidden, err := st.CreateStation(ctx, "hidden-peer", "", actor)
	if err != nil {
		t.Fatal(err)
	}

	resolve := func(name string) error {
		_, err := st.StationByNameVisibleTo(ctx, mine.StationID, name)
		return err
	}

	// POSITIVE CONTROL FIRST. If a visible station does not resolve, every "refused"
	// assertion below is true for the wrong reason and this test certifies nothing.
	if err := resolve("visible-peer"); err != nil {
		t.Fatalf("a PUBLISHED station did not resolve: %v — the refusals below would prove nothing", err)
	}

	hiddenErr := resolve("hidden-peer")
	absentErr := resolve("no-station-has-this-name")
	if hiddenErr == nil {
		t.Fatal("an UNPUBLISHED, unlinked station resolved by name — a session can enumerate " +
			"every station in the space by guessing, and a correct guess files a request its " +
			"human never invited")
	}
	if absentErr == nil {
		t.Fatal("a nonexistent name resolved")
	}
	// INDISTINGUISHABLE. Not merely "both fail" — the same error, because a caller that
	// could tell them apart has the oracle back in a subtler form.
	if hiddenErr.Error() != absentErr.Error() {
		t.Errorf("hidden station gives %q and a nonexistent name gives %q — a caller that can "+
			"tell these apart can still enumerate", hiddenErr, absentErr)
	}

	// AND THE SIDE EFFECT THAT MATTERS MOST: no request row for a guess. The read being
	// safe is only half of it; the old defect FILED something.
	pending, err := st.PendingStationRequests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range pending {
		if strings.Contains(r.ToName, "hidden") {
			t.Fatalf("a request naming the hidden station was filed: %+v", r)
		}
	}
	if len(pending) != 0 {
		t.Errorf("%d pending requests after only failed resolutions", len(pending))
	}

	// An ARCHIVED station is hidden too — publication is not the only way to be invisible.
	if err := st.SetStationPublished(ctx, visible.StationID, true); err != nil {
		t.Fatal(err)
	}
	if err := st.ArchiveStation(ctx, visible.StationID, true); err != nil {
		t.Fatal(err)
	}
	if err := resolve("visible-peer"); err == nil {
		t.Error("an ARCHIVED station still resolves — a dormant post is not a link target")
	}
	_ = hidden
}

// A LINKED-BUT-UNPUBLISHED PEER MUST STILL RESOLVE, or the fix has broken the ordinary case:
// a station you already talk to is one you can obviously see.
func TestLinkRequestStillResolvesALinkedPeer(t *testing.T) {
	ctx := context.Background()
	st := newKB(t)
	actor, err := st.FindOrCreateActor(ctx, "human", "admin")
	if err != nil {
		t.Fatal(err)
	}
	mine, err := st.CreateStation(ctx, "mine", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	peer, err := st.CreateStation(ctx, "quiet-peer", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	// Deliberately NOT published.
	reqID, err := st.CreateStationLinkRequest(ctx, "tok", mine.StationID, peer.StationID, "r", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ApproveLinkRequest(ctx, reqID, actor); err != nil {
		t.Fatal(err)
	}
	if _, err := st.StationByNameVisibleTo(ctx, mine.StationID, "quiet-peer"); err != nil {
		t.Fatalf("an approved-linked peer no longer resolves: %v — the fix has narrowed past the defect", err)
	}
	var _ = store.Station{}
}

// AND THE SAME PROPERTY THROUGH THE TOOL, WHICH IS THE ONE THAT COUNTS.
//
// The store-level test above passed while station_link_request was still wired to the
// UNSAFE resolver: a mutation reverting that one line survived it. That is the identical
// mistake that let four refusals ship as "internal error" — asserting the layer below the
// one where the defect lives. A resolver that is safe and a caller that does not use it
// is the same shape as a function with no callers.
//
// So this drives the real tool over HTTP and asserts BOTH halves: the refusal is identical
// for hidden and nonexistent, and nothing is filed either way.
func TestLinkRequestToolCannotEnumerateHiddenStations(t *testing.T) {
	ctx := context.Background()
	st := newKB(t)
	actor, err := st.FindOrCreateActor(ctx, "human", "curator")
	if err != nil {
		t.Fatal(err)
	}
	mine, err := st.CreateStation(ctx, "asker", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	visible, err := st.CreateStation(ctx, "visible-peer", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetStationPublished(ctx, visible.StationID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateStation(ctx, "hidden-peer", "", actor); err != nil {
		t.Fatal(err)
	}
	key, err := st.IssueStationKey(ctx, actor, mine.StationID, "test", []string{ScopeStation})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewHTTPHandler(Deps{Store: st}))
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
	t.Cleanup(func() { sess.Close() })

	ask := func(name string) (bool, string) {
		t.Helper()
		res, err := sess.CallTool(ctx, &mcp.CallToolParams{
			Name:      "station_link_request",
			Arguments: map[string]any{"to_station": name, "reason": "probe"},
		})
		if err != nil {
			t.Fatalf("%s: transport error: %v", name, err)
		}
		var msg string
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				msg += tc.Text
			}
		}
		return res.IsError, msg
	}

	// POSITIVE CONTROL: a published peer must still be requestable, or every refusal
	// below is true because the tool is broken rather than because it is safe.
	if isErr, msg := ask("visible-peer"); isErr {
		t.Fatalf("a PUBLISHED station was refused: %s — the refusals below would prove nothing", msg)
	}

	hiddenErr, hiddenMsg := ask("hidden-peer")
	absentErr, absentMsg := ask("no-station-has-this-name")
	if !hiddenErr {
		t.Fatal("station_link_request ACCEPTED a name withheld from the directory — a session " +
			"can enumerate the space by guessing, and a correct guess files a request its human " +
			"never invited")
	}
	if !absentErr {
		t.Fatal("station_link_request accepted a nonexistent name")
	}
	if hiddenMsg != absentMsg {
		t.Errorf("hidden gives %q and nonexistent gives %q — distinguishable answers are the "+
			"oracle in a subtler form", hiddenMsg, absentMsg)
	}

	// NOTHING FILED for either guess — only the legitimate one.
	pending, err := st.PendingStationRequests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range pending {
		if strings.Contains(r.ToName, "hidden") {
			t.Fatalf("a request naming the hidden station reached the human: %+v", r)
		}
	}
	if len(pending) != 1 {
		t.Errorf("%d pending requests, want exactly the one for visible-peer", len(pending))
	}
}
