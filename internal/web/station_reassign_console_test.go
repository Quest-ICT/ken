package web

import (
	"net/url"
	"strings"
	"testing"
)

// *** THE HUMAN'S HALF OF WORKSPACE RECOVERY, THROUGH THE MUX. ***
//
// A session can adopt a workspace only while it has no owning conversation, deliberately — so a
// workspace whose conversation is gone can be re-staffed by NOTHING except a person. That makes
// this console form the only door back in, and a store method with no button would leave the door
// bolted (the recurring defect this project has a dedicated gate for).
//
// The flow it has to support, in full: a chat session invents a conversation key and states it in
// its reply, the human pastes that string here, the session's next station_me lands in the
// recovered workspace. No token, no voucher, no file, nothing secret on screen.
func TestReassigningAWorkspaceFromTheConsole(t *testing.T) {
	st, ctx, cli, base, actor := stationsHarness(t)
	abandoned, _, err := st.ClaimStationForSession(ctx, "conv-dead", "old laptop", actor, "")
	if err != nil {
		t.Fatal(err)
	}

	csrf := extract(t, cli, base+"/stations", `name="csrf" value="([^"]+)"`)
	postForm(t, cli, base+"/stations/"+abandoned.StationID+"/reassign",
		url.Values{"csrf": {csrf}, "session_key": {"conv-took-over"}})

	// ASSERTED THROUGH THE CLAIM PATH, not through the column. The column changing proves the
	// form works; the claim landing proves the RECOVERY works, and that is the feature.
	got, created, err := st.ClaimStationForSession(ctx, "conv-took-over", "chat", actor, "")
	if err != nil {
		t.Fatal(err)
	}
	if created || got.StationID != abandoned.StationID {
		t.Fatalf("the session landed in %q (created=%v), want the recovered workspace %q",
			got.StationID, created, abandoned.StationID)
	}
}

// THE FORM SHOWS WHO HOLDS THE POST, and clearing it is how a workspace goes back to the pool.
// Without the prefill an operator has no way to see which conversation a workspace answers to,
// and "empty means release" would be an undocumented gesture rather than an obvious one.
func TestTheReassignFormShowsTheCurrentConversation(t *testing.T) {
	st, ctx, cli, base, actor := stationsHarness(t)
	if _, _, err := st.ClaimStationForSession(ctx, "conv-visible-key", "held", actor, ""); err != nil {
		t.Fatal(err)
	}
	body := get(t, cli, base+"/stations")
	if !strings.Contains(body, "conv-visible-key") {
		t.Error("the stations page never shows the conversation key that owns a workspace, so an " +
			"operator cannot tell a held post from an abandoned one — which is the first thing " +
			"they need to know before reassigning anything")
	}
}

// RELEASING THROUGH THE FORM. An empty box is the undo for a reassignment, and without it a
// workspace pointed at the wrong conversation is stuck to it — the dead end this feature exists
// to open, re-created by the fix for it.
func TestReleasingAWorkspaceFromTheConsole(t *testing.T) {
	st, ctx, cli, base, actor := stationsHarness(t)
	held, _, err := st.ClaimStationForSession(ctx, "conv-holder", "held", actor, "")
	if err != nil {
		t.Fatal(err)
	}
	csrf := extract(t, cli, base+"/stations", `name="csrf" value="([^"]+)"`)
	postForm(t, cli, base+"/stations/"+held.StationID+"/reassign",
		url.Values{"csrf": {csrf}, "session_key": {""}})

	got, err := st.StationByID(ctx, held.StationID)
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionKey != "" {
		t.Errorf("the workspace still answers to %q after being released through the form", got.SessionKey)
	}
}
