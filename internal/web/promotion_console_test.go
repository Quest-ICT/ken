package web

import (
	"net/url"
	"strings"
	"testing"

	"github.com/Quest-ICT/ken/internal/store"
)

// A promotion request must reach the human, end to end.
//
// `station_note_promote` has written `station_promotion` rows since stations shipped.
// Its tool description told every session it "asks your human to convert a page". And
// nothing read the table — no store function, no route, no template — so every request
// went into a drawer nobody could open while the session was told it had asked.
//
// That is the third shape of this defect found in one week: a flag with no reader
// (`published`), a store function with no caller (`RevokeStationLink`), and now a whole
// table with neither. This test drives the console rather than the store, because the
// store was never the missing half.
func TestAPromotionRequestReachesTheConsoleAndCanBeResolved(t *testing.T) {
	st, ctx, cli, base, actor := stationsHarness(t)

	station, err := st.CreateStation(ctx, "prod-ops", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.WriteStationNote(ctx, store.DefaultStationNoteLimits(), station.StationID,
		"the-finding", "Retention keys on acked_at", "A body worth promoting.",
		nil, "replace", 0, "tok", actor, false); err != nil {
		t.Fatal(err)
	}
	promoID, err := st.PromoteStationNote(ctx, station.StationID, "the-finding")
	if err != nil {
		t.Fatal(err)
	}

	page := get(t, cli, base+"/stations")

	// The request, the page it points at, and the BODY — a console that shows a
	// request without its content just moves the dead end somewhere prettier.
	for _, want := range []string{"the-finding", "A body worth promoting.", "prod-ops"} {
		if !strings.Contains(page, want) {
			t.Fatalf("the promotion request does not reach the console: %q is absent.\n"+
				"A session was told it had asked its human, and the human cannot see it.", want)
		}
	}
	if !strings.Contains(page, "/stations/promotions/"+promoID+"/resolve") {
		t.Fatal("the request is displayed with no way to answer it — a request that cannot be closed keeps asking forever")
	}

	// Resolve it, and it must leave the pending view.
	csrf := extract(t, cli, base+"/stations", `name="csrf" value="([^"]+)"`)
	postForm(t, cli, base+"/stations/promotions/"+promoID+"/resolve",
		url.Values{"csrf": {csrf}, "decision": {"converted"}, "slug": {"retention-keys-on-acked-at"}})

	after := get(t, cli, base+"/stations")
	if strings.Contains(after, "/stations/promotions/"+promoID+"/resolve") {
		t.Fatal("the request is still pending after being answered")
	}

	// And the decision is recorded, with the trail from note to entry.
	var state, slug string
	if err := st.R.QueryRowContext(ctx,
		`SELECT state, COALESCE(entry_slug,'') FROM station_promotion WHERE promotion_id=?`, promoID).Scan(&state, &slug); err != nil {
		t.Fatal(err)
	}
	if state != "converted" {
		t.Errorf("state is %q, want converted", state)
	}
	if slug != "retention-keys-on-acked-at" {
		t.Errorf("entry slug not recorded (%q) — the trail from the note to the knowledge is lost", slug)
	}
}

// Resolving twice must not re-decide. Two console tabs, or a double click, would
// otherwise race to opposite answers on a settled request.
func TestResolvingAPromotionTwiceIsRefused(t *testing.T) {
	st, ctx, cli, base, actor := stationsHarness(t)

	station, err := st.CreateStation(ctx, "dev", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.WriteStationNote(ctx, store.DefaultStationNoteLimits(), station.StationID,
		"p", "t", "b", nil, "replace", 0, "tok", actor, false); err != nil {
		t.Fatal(err)
	}
	promoID, err := st.PromoteStationNote(ctx, station.StationID, "p")
	if err != nil {
		t.Fatal(err)
	}

	csrf := extract(t, cli, base+"/stations", `name="csrf" value="([^"]+)"`)
	postForm(t, cli, base+"/stations/promotions/"+promoID+"/resolve",
		url.Values{"csrf": {csrf}, "decision": {"discarded"}})

	csrf = extract(t, cli, base+"/stations", `name="csrf" value="([^"]+)"`)
	postForm(t, cli, base+"/stations/promotions/"+promoID+"/resolve",
		url.Values{"csrf": {csrf}, "decision": {"converted"}})

	var state string
	if err := st.R.QueryRowContext(ctx,
		`SELECT state FROM station_promotion WHERE promotion_id=?`, promoID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "discarded" {
		t.Errorf("a settled request was re-decided to %q — the first answer must stand", state)
	}
}
