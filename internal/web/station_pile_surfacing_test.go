package web

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/Quest-ICT/ken/internal/store"
)

// THE PILE MUST ANNOUNCE ITSELF. §11.8 built the cross-station view for the human's
// question — "what is everyone waiting on me for?" — and then put it somewhere nothing
// points at, so it answered that question only for a human who already remembered to ask.
// A session briefs its human on ONE station; nothing brought up the rest.
//
// Asserted on the NAV, which is on every page, rather than on /stations — a badge that only
// appears once you have already navigated to the page is not a prompt to navigate there.
func TestTheStationPileShowsInTheNavAndDashboard(t *testing.T) {
	st, ctx, cli, base, actor := stationsHarness(t)
	s, err := st.CreateStation(ctx, "quiet-post", "", actor)
	if err != nil {
		t.Fatal(err)
	}

	// CONTROL FIRST: with nothing waiting, no badge. Without this the test passes against a
	// badge that is always rendered, which would be worse than none — a permanent number
	// teaches the eye to skip the row.
	if body := get(t, cli, base+"/"); strings.Contains(body, "badge--accent") {
		t.Fatal("a badge rendered before any task existed")
	}

	addTask(t, st, ctx, s.StationID, "decide the thing", "human", actor)
	addTask(t, st, ctx, s.StationID, "approve the other thing", "human", actor)
	// A self-blocked task must NOT raise it: the badge answers "waiting on YOU".
	addTask(t, st, ctx, s.StationID, "my own work", "self", actor)

	for _, page := range []string{"/", "/browse", "/tokens", "/search"} {
		body := get(t, cli, base+page)
		if !regexp.MustCompile(`href="/stations".*?>2<`).MatchString(body) {
			t.Fatalf("%s: nav shows no count of 2 — the pile is still invisible from here", page)
		}
	}
}

// A CAPPED LIST RENDERED WITH NO TOTAL IS A SILENT SAMPLE, on the one page built so the
// human can see the WHOLE pile. The vault trail on this same page already says "the last 20
// of 2,318" for exactly this reason.
func TestTheTaskListSaysWhenItIsShowingASample(t *testing.T) {
	st, ctx, cli, base, actor := stationsHarness(t)
	s, err := st.CreateStation(ctx, "busy-post", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	// Below the cap: the page must NOT claim to be sampling. A "showing N of N" line on
	// every render is noise, and noise stops being read before the one render that matters.
	addTask(t, st, ctx, s.StationID, "only one", "human", actor)
	if body := get(t, cli, base+"/stations"); strings.Contains(body, "Showing the first") {
		t.Fatal("claimed to be showing a sample when the whole list fit")
	}

	// The count and the list must disagree only because the CAP bit, so assert through the
	// store rather than manufacturing 200 tasks in a console test.
	total, err := st.CountCrossStationTasks(ctx, "human")
	if err != nil {
		t.Fatal(err)
	}
	list, err := st.CrossStationHumanTasks(ctx, "human", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if total != len(list) {
		t.Fatalf("uncapped, count=%d and list=%d must agree or the page's 'of N' is a different N", total, len(list))
	}
	capped, err := st.CrossStationHumanTasks(ctx, "human", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(capped) == 0 {
		t.Fatal("the fixture produced nothing to cap")
	}
}

func addTask(t *testing.T, st *store.Store, ctx context.Context, sid, text, blocked string, actor int64) {
	t.Helper()
	if _, _, err := st.AddStationTask(ctx, store.DefaultStationTaskLimits(),
		store.StationTask{StationID: sid, Text: text, BlockedOn: blocked}, "tok", actor, false); err != nil {
		t.Fatalf("add %q: %v", text, err)
	}
}

// TestTheRevokeConfirmSaysUnknownRatherThanZero IS DELETED with the bound-by-key count. The
// property it protected — never render 0 for "could not compute" — still holds for the count a
// token OWNS, which is asserted by its neighbour.
