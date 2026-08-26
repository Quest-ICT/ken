package web

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/Quest-ICT/ken/internal/store"
)

// *** THE MARKER AND THE ENDPOINT MUST AGREE, OR THE PAGE RELOADS FOREVER. ***
//
// app.js reloads whenever /stations/count disagrees with the value the page was rendered
// with. So a drift between them is not cosmetic — it is an infinite reload loop, worse than
// the silence it replaces. Both now come from one function; this proves it through the two
// SURFACES rather than by trusting that they share a callee.
//
// /stations/count had ZERO tests before this, which is how it could count one of the two
// things that arrive here for as long as it did.
func TestTheLiveMarkerAndTheCountEndpointAgree(t *testing.T) {
	st, ctx, cli, base, actor := stationsHarness(t)
	s, err := st.CreateStation(ctx, "prod-ops", "", actor)
	if err != nil {
		t.Fatal(err)
	}

	check := func(what string) int {
		t.Helper()
		body := get(t, cli, base+"/stations")
		m := regexp.MustCompile(`data-live-refresh="(\d+)"`).FindStringSubmatch(body)
		if m == nil {
			t.Fatalf("%s: no live-refresh marker rendered", what)
		}
		rendered, _ := strconv.Atoi(m[1])
		served := strings.TrimSpace(get(t, cli, base+"/stations/count"))
		cm := regexp.MustCompile(`"count":\s*(\d+)`).FindStringSubmatch(served)
		if cm == nil {
			t.Fatalf("%s: count endpoint returned %q", what, served)
		}
		n, _ := strconv.Atoi(cm[1])
		if rendered != n {
			t.Fatalf("%s: marker says %d, endpoint says %d — app.js will reload forever", what, rendered, n)
		}
		return n
	}

	base0 := check("empty")

	// A REQUEST ARRIVES — the thing the count already handled.
	if _, err := st.CreateStationRequest(ctx, "tok", "", "handoff", "a new post"); err != nil {
		t.Fatal(err)
	}
	if n := check("after a request"); n != base0+1 {
		t.Fatalf("a pending request did not move the count: %d -> %d", base0, n)
	}

	// A PROMOTION ARRIVES — the thing it did not count. This is the case that left the page
	// asserting "last checked <now>" on a timer with the item sitting invisible below it.
	if _, err := st.WriteStationNote(ctx, store.DefaultStationNoteLimits(), s.StationID,
		"handoff", "Handoff", "where things stand", nil, "replace", 0, "tok", actor, false); err != nil {
		t.Fatalf("note fixture: %v", err)
	}
	if _, err := st.PromoteStationNote(ctx, s.StationID, "handoff"); err != nil {
		t.Fatalf("promotion fixture: %v", err)
	}
	if n := check("after a promotion"); n != base0+2 {
		t.Fatalf("a pending promotion did not move the count: want %d, got %d", base0+2, n)
	}
}
