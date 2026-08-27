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

// roomAsker stands a real station surface up and returns a client already holding a station key,
// plus the store behind it. Driven through a real MCP client on purpose: a store-level test proves
// nothing about a tool, and this project has shipped both a store method with no caller and a route
// with no button.
func roomAsker(t *testing.T) (*store.Store, context.Context, *mcp.ClientSession, string) {
	t.Helper()
	ctx := context.Background()
	st := newKB(t)
	actor, err := st.FindOrCreateActor(ctx, "human", "admin")
	if err != nil {
		t.Fatal(err)
	}
	mine, err := st.CreateStation(ctx, "asker", "", actor)
	if err != nil {
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
	return st, ctx, sess, mine.StationID
}

func askForRoom(t *testing.T, ctx context.Context, sess *mcp.ClientSession, reason, hint string) (bool, string) {
	t.Helper()
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name:      "station_room_request",
		Arguments: map[string]any{"reason": reason, "name_hint": hint},
	})
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	var msg string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			msg += tc.Text
		}
	}
	return res.IsError, msg
}

// *** THE TOOL EXISTS, FILES, AND TELLS THE SESSION TO SPEAK. ***
//
// Vlad decided on 2026-08-06 that "sessions may REQUEST, human approves". This is the session half,
// asserted where a session actually stands.
func TestStationRoomRequestFilesAndAnswersPending(t *testing.T) {
	st, ctx, sess, _ := roomAsker(t)

	isErr, msg := askForRoom(t, ctx, sess, "three peers to coordinate", "ops")
	if isErr {
		t.Fatalf("the tool refused a well-formed ask: %s", msg)
	}
	if !strings.Contains(msg, "pending") {
		t.Errorf("the session is not told the ask is pending: %s", msg)
	}
	// THE RELAY INSTRUCTION IS THE FEATURE. A tool result reaches nobody unless the session speaks
	// it — this project's most-repeated lesson, and the reason station_me carries the same line.
	if !strings.Contains(msg, "Tell them in words") {
		t.Errorf("the result does not tell the session to relay it, so the human never hears about "+
			"a decision that is theirs to make: %s", msg)
	}

	pending, err := st.PendingStationRequests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, r := range pending {
		if r.Kind == "room" {
			found = true
			if r.NameHint != "ops" {
				t.Errorf("the name hint %q did not reach the human's queue", r.NameHint)
			}
		}
	}
	if !found {
		t.Fatal("the tool answered pending and filed NOTHING — the session waits for a decision " +
			"nobody will ever see")
	}
}

// A REASON IS REQUIRED, and the refusal must name what is missing. The session reads this message,
// not the store's.
func TestStationRoomRequestRefusesAnEmptyReason(t *testing.T) {
	_, ctx, sess, _ := roomAsker(t)
	for _, blank := range []string{"", "   "} {
		isErr, msg := askForRoom(t, ctx, sess, blank, "ops")
		if !isErr {
			t.Errorf("reason %q was accepted", blank)
		} else if !strings.Contains(msg, "reason") {
			t.Errorf("the refusal does not name the missing field: %s", msg)
		}
	}
}

// *** A MUTED RE-ASK MUST BE INDISTINGUISHABLE FROM A FILED ONE. ***
//
// The silent drop exists so a persistent session cannot probe its human's past refusals one request
// at a time. If the TOOL answered differently for a dropped ask, the store's silence would be
// undone at the only layer a session can see.
func TestAMutedRoomRequestAnswersExactlyLikeAFiledOne(t *testing.T) {
	st, ctx, sess, _ := roomAsker(t)

	firstErr, firstMsg := askForRoom(t, ctx, sess, "first ask", "")
	if firstErr {
		t.Fatalf("the first ask was refused: %s", firstMsg)
	}
	pending, _ := st.PendingStationRequests(ctx)
	var id string
	for _, r := range pending {
		if r.Kind == "room" {
			id = r.RequestID
		}
	}
	if id == "" {
		t.Fatal("nothing was filed, so there is nothing to deny and this test proves nothing")
	}
	actor, _ := st.FindOrCreateActor(ctx, "human", "admin")
	if err := st.DenyStationRequest(ctx, id, "not now", actor); err != nil {
		t.Fatal(err)
	}

	mutedErr, mutedMsg := askForRoom(t, ctx, sess, "asking again", "")
	if mutedErr {
		t.Fatalf("a muted re-ask was REFUSED, which tells the session its human said no: %s", mutedMsg)
	}
	if mutedMsg != firstMsg {
		t.Errorf("a muted re-ask answers differently:\n dropped: %s\n filed:   %s\n"+
			"A session that can tell them apart can probe its human's decisions.", mutedMsg, firstMsg)
	}
	after, _ := st.PendingStationRequests(ctx)
	for _, r := range after {
		if r.Kind == "room" {
			t.Error("the muted re-ask reached the human's queue")
		}
	}
}

// THE TOOL IS ON THE STATION SURFACE'S TOOL LIST. A tool that exists in code and is never
// registered is the same failure as a store method with no caller.
func TestStationRoomRequestIsAdvertised(t *testing.T) {
	_, ctx, sess, _ := roomAsker(t)
	tools, err := sess.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var found *mcp.Tool
	for _, tl := range tools.Tools {
		if tl.Name == "station_room_request" {
			found = tl
		}
	}
	if found == nil {
		t.Fatal("station_room_request is not in the tool list, so no session can ever call it")
	}
	// It must say the session does NOT choose the members — that is the property migration 0017's
	// surviving argument turns on, and the description is where a session learns it.
	if !strings.Contains(found.Description, "human decides membership") {
		t.Errorf("the description does not tell the session its human decides who is in the room: %q",
			found.Description)
	}
}
