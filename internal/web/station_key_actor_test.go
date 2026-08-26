package web

import (
	"strings"
	"testing"
)

// The console must show which actor each station key belongs to, and flag the one
// that cannot bind.
//
// This is the operator's only view of a property that is otherwise invisible until
// it fails, in a different surface, possibly months later: a station key minted
// under an actor that holds no COMM token authenticates perfectly and drives every
// station tool, and then refuses at binding — because RedeemBindingVoucher requires
// the redeeming endpoint's actor to be the one the voucher was issued to.
//
// ErrVoucherNotYours tells the operator to "check the /stations console", so this
// test is what stops that sentence becoming a lie. A store field nothing renders is
// the same kind of unfinished as a store function nothing calls.
func TestStationsConsoleShowsKeyActorAndFlagsTheOneThatCannotBind(t *testing.T) {
	st, ctx, cli, base, curator := stationsHarness(t)

	station, err := st.CreateStation(ctx, "dev", "", curator)
	if err != nil {
		t.Fatal(err)
	}

	// Two actors: one holds this machine's COMM token, one does not. That is exactly
	// the misconfiguration — the CLI used to default station keys to the human actor
	// while COMM tokens default to an `ai` one.
	withComm, err := st.FindOrCreateActor(ctx, "ai", "dev-session")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.IssueToken(ctx, withComm, []string{"read", "comm"}, "this machine"); err != nil {
		t.Fatal(err)
	}
	withoutComm, err := st.FindOrCreateActor(ctx, "human", "vlad")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := st.IssueStationKey(ctx, withComm, station.StationID, "good-key", []string{"station"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.IssueStationKey(ctx, withoutComm, station.StationID, "cannot-bind", []string{"station"}); err != nil {
		t.Fatal(err)
	}

	page := get(t, cli, base+"/stations")

	// Both actors named. Without this the warning below could be rendering on a page
	// that shows no actor information at all.
	for _, want := range []string{"ai:dev-session", "human:vlad"} {
		if !strings.Contains(page, want) {
			t.Fatalf("the keys table does not name the actor %q — the operator cannot see which key can bind", want)
		}
	}

	// And the flag, which is the part that turns a name into a diagnosis.
	if !strings.Contains(page, ">no COMM token</span>") {
		t.Fatal("a station key under an actor with no COMM token carries no warning — it looks identical to a working key and fails only at bind time")
	}

	// CONTROL: exactly one key is flagged. If the badge rendered unconditionally the
	// assertion above would pass while telling the operator nothing, and the good key
	// would be labelled broken.
	// Counted on the rendered element, not the bare phrase: the phrase also occurs
	// inside the badge's own tooltip, so counting it double-counts each badge.
	if n := strings.Count(page, ">no COMM token</span>"); n != 1 {
		t.Fatalf("the warning badge appears %d times for 1 unbindable key of 2 — it is not keyed on the actor's COMM token", n)
	}
}
