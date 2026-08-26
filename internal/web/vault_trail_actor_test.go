package web

import (
	"regexp"
	"strings"
	"testing"

	"github.com/Quest-ICT/ken/internal/store"
)

// WHO read the secret is the one question the trail is kept for, and the console dropped
// it at the last step: the row carried by_actor_id into the view model and the template
// printed name, via and time.
//
// This drives the RENDERED PAGE, because the render is where the defect lived. A store
// test asserting that StationVaultReads returns an actor would have passed against every
// build of the broken console, and reverting either half of the fix — the join or the
// template line — has to fail here.
func TestTheVaultTrailNamesWhoReadTheSecret(t *testing.T) {
	st, ctx, cli, base, actor := stationsHarness(t)
	lim := store.DefaultStationVaultLimits()

	station, err := st.CreateStation(ctx, "prod-ops", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.PutStationVaultSecret(ctx, lim, station.StationID,
		"deploy-key", vaultSecret, "", "tok", actor); err != nil {
		t.Fatal(err)
	}

	// A name that cannot be confused with anything else the page renders. The logged-in
	// human is "admin" and it is on every load, so asserting on THAT would pass with the
	// trail naming nobody.
	const reader = "night-shift-relay-7f3c"
	readerID, err := st.FindOrCreateActor(ctx, "ai", reader)
	if err != nil {
		t.Fatal(err)
	}

	// NEGATIVE CONTROL. The name must be absent BEFORE the read, or "the page contains
	// the name" proves only that the page lists actors.
	if strings.Contains(get(t, cli, base+"/stations"), reader) {
		t.Fatalf("%q is already on the stations page before it read anything — the assertion below would prove nothing", reader)
	}

	if _, err := st.GetStationVaultSecret(ctx, lim, station.StationID,
		"deploy-key", "station", "tok-7", readerID); err != nil {
		t.Fatal(err)
	}

	page := get(t, cli, base+"/stations")

	// POSITIVE CONTROL. Isolate the trail line first, so a fixture that never produced a
	// read reports THAT rather than being read as "the reader is missing".
	line := regexp.MustCompile(`(?s)<li class="help"><span class="mono">deploy-key</span>.*?</li>`).FindString(page)
	if line == "" {
		t.Fatal("no read trail rendered at all — the fixture failed, not the naming")
	}
	if !strings.Contains(line, "ai:"+reader) {
		t.Fatalf("the trail line is %q — it does not say WHO read the secret, which is the only question an audit trail is kept to answer", line)
	}
}
