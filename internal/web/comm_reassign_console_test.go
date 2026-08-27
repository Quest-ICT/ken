package web

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/Quest-ICT/ken/internal/comm"
)

// *** THE COMM HALF OF WORKSPACE RECOVERY, THROUGH THE MUX. ***
//
// Recovering a workspace without its mailbox is half a recovery: the new session inherits the
// station's bound endpoints and cannot open them, because a claimed one answers to the DEAD
// conversation's key. Rotate was the only way in and it hands the session a secret to write to
// disk — which a claude.ai chat cannot do, and which is the exact ceremony 3.36.0 removed.
func TestReassigningAnEndpointFromTheConsole(t *testing.T) {
	_, _, cli, base, _ := stationsHarnessWithComm(t)
	cs := commOf(t)
	ctx := context.Background()

	abandoned, _, err := cs.ClaimEndpointForSession(ctx,
		comm.Owner{TokenID: "tok-x", ActorID: 7}, "conv-dead", "old laptop", "")
	if err != nil {
		t.Fatal(err)
	}

	csrf := extract(t, cli, base+"/comm", `name="csrf" value="([^"]+)"`)
	postForm(t, cli, base+"/comm/endpoints/"+abandoned.EndpointID+"/reassign",
		url.Values{"csrf": {csrf}, "session_key": {"conv-took-over"}})

	// ASSERTED THROUGH THE AUTH PATH. The column changing proves the form posts; the key
	// AUTHENTICATING proves the mailbox is reachable, and that is the feature.
	got, err := cs.AuthenticateEndpointBySessionKey(ctx, "conv-took-over")
	if err != nil {
		t.Fatalf("the recovered mailbox does not authenticate by its new key: %v", err)
	}
	if got.EndpointID != abandoned.EndpointID {
		t.Errorf("the key drives %q, want the recovered mailbox %q", got.EndpointID, abandoned.EndpointID)
	}
}

// THE PAGE SHOWS WHICH CONVERSATION DRIVES EACH MAILBOX. Scoped to the endpoints table, because an
// assertion satisfied by a different region of the page measures the page rather than the thing —
// this file's own header records what that cost the first time.
func TestTheCommPageShowsWhichConversationDrivesAnEndpoint(t *testing.T) {
	_, _, cli, base, _ := stationsHarnessWithComm(t)
	cs := commOf(t)
	if _, _, err := cs.ClaimEndpointForSession(context.Background(),
		comm.Owner{TokenID: "tok-x", ActorID: 7}, "conv-visible-here", "held", ""); err != nil {
		t.Fatal(err)
	}
	body := endpointsBlock(t, get(t, cli, base+"/comm"))
	if !strings.Contains(body, "conv-visible-here") {
		t.Error("the endpoints table never shows the conversation key driving a mailbox, so an " +
			"operator cannot tell a live one from an abandoned one before reassigning it")
	}
}
