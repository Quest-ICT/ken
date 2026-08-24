package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// StationKeyStation IS THE GATE FOR A BINDING RE-POINT, so its refusals are tested as their own
// contract rather than only through the console.
//
// The re-point statement's WHERE independently rejects the results of a wrong answer here — a
// non-station token resolves to no station, and no endpoint's `station_id` matches an empty
// one — so a console test cannot tell a correct refusal from a lucky one. Mutation proved it:
// deleting the station-key check entirely left every console test passing, because the UPDATE
// caught what the validator let through, and the operator would have been told "not found"
// instead of which credential could not have bound anything. Defence in depth is not a reason
// to leave the outer layer untested; it is the reason a test through the inner layer cannot
// stand in for one.
func TestStationKeyStationRefusesEverythingThatCouldNotHaveBound(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	actor, err := st.FindOrCreateActor(ctx, "ai", "tester")
	if err != nil {
		t.Fatal(err)
	}
	station, err := st.CreateStation(ctx, 1, "prod-ops", "", actor)
	if err != nil {
		t.Fatal(err)
	}

	live, err := st.IssueStationKey(ctx, actor, station.StationID, "laptop", []string{"station"})
	if err != nil {
		t.Fatal(err)
	}
	liveID := strings.Split(strings.TrimPrefix(live, "kens_"), "_")[0]

	dead, _ := st.IssueStationKey(ctx, actor, station.StationID, "dead", []string{"station"})
	deadID := strings.Split(strings.TrimPrefix(dead, "kens_"), "_")[0]
	if err := st.RevokeToken(ctx, deadID); err != nil {
		t.Fatal(err)
	}
	commTok, _ := st.IssueToken(ctx, actor, []string{"comm"}, "a machine")
	commID := strings.SplitN(strings.TrimPrefix(commTok, "ken_"), "_", 2)[0]

	for _, tc := range []struct{ name, id string }{
		{"a revoked station key", deadID},
		{"a comm token, which binds nothing", commID},
		{"a token that does not exist", "no-such-token"},
	} {
		got, err := st.StationKeyStation(ctx, tc.id)
		if !errors.Is(err, ErrNotAStationKey) {
			t.Errorf("%s: err = %v, want ErrNotAStationKey", tc.name, err)
		}
		if got != "" {
			t.Errorf("%s: returned station %q — a refusal must not name one", tc.name, got)
		}
	}

	// CONTROL: the live key resolves, and to the RIGHT station. Returning the wrong station
	// would be worse than refusing — the re-point statement trusts this value as the station
	// the endpoint must already be on, so a wrong one silently moves nothing while every
	// message says it should have worked.
	got, err := st.StationKeyStation(ctx, liveID)
	if err != nil {
		t.Fatalf("a live station key was refused: %v", err)
	}
	if got != station.StationID {
		t.Fatalf("station = %q, want %q", got, station.StationID)
	}
}
