package comm

import (
	"context"
	"errors"
	"testing"
)

// A RE-POINT MUST CHANGE WHO MAY DRIVE AN ENDPOINT AND NOTHING ELSE.
//
// Everything asserted here is something a naive implementation destroys — most of them by
// copying a pattern that is correct for a DIFFERENT operation. Unbind, revoke and sever all
// release claims and clear bindings, each for a stated reason: "a severed reader is never
// coming back to ack". A re-pointed reader is coming back.
func TestRepointPreservesEverythingButTheOwner(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	a := stationEndpoint(t, st, "tok-old", "st-alpha")
	b := stationEndpoint(t, st, "tok-b", "st-beta")
	linkFixture(t, st, [2]string{"st-alpha", "st-beta"})

	// Give it mail under BOTH party forms and a live claim, which is the state a re-point
	// is most likely to damage.
	if _, err := st.SendToStation(ctx, b, "st-alpha", "for the station", SendOpts{}); err != nil {
		t.Fatal(err)
	}
	before, err := st.Poll(ctx, a, 10)
	if err != nil || len(before) != 1 {
		t.Fatalf("setup poll: %d msgs, err %v", len(before), err)
	}
	var claimedBefore, claimExpBefore string
	if err := st.R.QueryRowContext(ctx,
		`SELECT COALESCE(claimed_by_endpoint,''), COALESCE(claim_expires_at,'') FROM delivery LIMIT 1`).
		Scan(&claimedBefore, &claimExpBefore); err != nil {
		t.Fatal(err)
	}
	if claimedBefore == "" {
		t.Fatal("fixture: no claim was taken, so this test cannot prove claims survive")
	}

	snap := func() (station, boundKey, boundAt, secret string) {
		t.Helper()
		if err := st.R.QueryRowContext(ctx, `
SELECT COALESCE(station_id,''), COALESCE(bound_by_station_key_id,''), COALESCE(bound_at,''), secret_sha256
FROM endpoint WHERE endpoint_id=?`, a.EndpointID).Scan(&station, &boundKey, &boundAt, &secret); err != nil {
			t.Fatal(err)
		}
		return
	}
	s0, k0, t0, sec0 := snap()

	if err := st.RepointEndpointOwner(ctx, a.EndpointID, "tok-old",
		Owner{TokenID: "tok-new", ActorID: 42}); err != nil {
		t.Fatalf("repoint: %v", err)
	}

	// 1. THE OWNER TUPLE MOVED, ALL OF IT. Moving token_id alone leaves actor_id stale, and a
	//    later binding voucher compares issued_to_actor against it — so the endpoint could
	//    never be re-bound, failing forever with an error that blames the voucher.
	var tok string
	var actor int64
	if err := st.R.QueryRowContext(ctx,
		`SELECT token_id, actor_id FROM endpoint WHERE endpoint_id=?`, a.EndpointID).
		Scan(&tok, &actor); err != nil {
		t.Fatal(err)
	}
	if tok != "tok-new" || actor != 42 {
		t.Fatalf("owner tuple = (%s, %d), want (tok-new, 42)", tok, actor)
	}

	// 2. THE STATION BINDING IS BYTE-IDENTICAL. Clearing station_id would silently unbind a
	//    session from the post it staffs, and the mail it inherits is filed under that station.
	s1, k1, t1, sec1 := snap()
	if s1 != s0 || k1 != k0 || t1 != t0 {
		t.Fatalf("binding moved: station %q->%q key %q->%q at %q->%q", s0, s1, k0, k1, t0, t1)
	}
	// 3. THE SECRET IS UNTOUCHED — a re-point is not a rotation, and a session that was
	//    working must keep working without being handed anything.
	if sec1 != sec0 {
		t.Fatal("the secret changed: a re-point must not force a session to re-read a credential")
	}

	// 4. THE CLAIM SURVIVES, holder and expiry. This is the one a reflex copy of
	//    UnbindEndpointFromStation destroys, handing in-flight mail to a sibling reader.
	var claimedAfter, claimExpAfter string
	if err := st.R.QueryRowContext(ctx,
		`SELECT COALESCE(claimed_by_endpoint,''), COALESCE(claim_expires_at,'') FROM delivery LIMIT 1`).
		Scan(&claimedAfter, &claimExpAfter); err != nil {
		t.Fatal(err)
	}
	if claimedAfter != claimedBefore || claimExpAfter != claimExpBefore {
		t.Fatalf("claim moved: holder %q->%q expiry %q->%q", claimedBefore, claimedAfter, claimExpBefore, claimExpAfter)
	}

	// 5. THE MAIL IS STILL REACHABLE, through the same endpoint, after the owner changed.
	again, err := st.Poll(ctx, a, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != len(before) {
		t.Fatalf("polled %d messages after the re-point, %d before", len(again), len(before))
	}
}

// CONDITIONAL, NEVER CHECK-THEN-ACT. A stale console page must fail loudly rather than move a
// row someone else already moved, and a revoked endpoint must not be resurrected.
func TestRepointIsConditionalAndRefusesRevoked(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	ep := stationEndpoint(t, st, "tok-old", "st-alpha")
	to := Owner{TokenID: "tok-new", ActorID: 42}

	// A wrong `from` is refused and changes nothing — the idempotency property.
	if err := st.RepointEndpointOwner(ctx, ep.EndpointID, "tok-WRONG", to); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale from-token: got %v, want ErrNotFound", err)
	}
	var tok string
	if err := st.R.QueryRowContext(ctx,
		`SELECT token_id FROM endpoint WHERE endpoint_id=?`, ep.EndpointID).Scan(&tok); err != nil {
		t.Fatal(err)
	}
	if tok != "tok-old" {
		t.Fatalf("a refused re-point moved the row anyway: %q", tok)
	}

	// CONTROL: the right `from` works, or the check above proves nothing.
	if err := st.RepointEndpointOwner(ctx, ep.EndpointID, "tok-old", to); err != nil {
		t.Fatalf("correct from-token was refused: %v", err)
	}
	// And re-running it is now a no-op refusal rather than a second move.
	if err := st.RepointEndpointOwner(ctx, ep.EndpointID, "tok-old", to); !errors.Is(err, ErrNotFound) {
		t.Fatalf("replaying the same re-point: got %v, want ErrNotFound", err)
	}

	// A REVOKED ENDPOINT IS REFUSED — re-pointing it would resurrect a capability an operator
	// deliberately destroyed, which is the argument rotation already makes for itself.
	dead := stationEndpoint(t, st, "tok-old", "st-gamma")
	if err := st.RevokeEndpoint(ctx, dead.EndpointID); err != nil {
		t.Fatal(err)
	}
	if err := st.RepointEndpointOwner(ctx, dead.EndpointID, "tok-old", to); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked endpoint: got %v, want ErrNotFound", err)
	}
}

// THE BULK VERB MOVES EVERY LIVE ENDPOINT OF ONE TOKEN AND NOTHING ELSE'S. Eleven on one token
// is the production shape; a half-moved estate is the state nobody has a recovery story for.
func TestRepointTokenMovesOnlyItsOwnLiveEndpoints(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	for _, s := range []string{"st-a", "st-b", "st-c"} {
		stationEndpoint(t, st, "tok-old", s)
	}
	other := stationEndpoint(t, st, "tok-other", "st-d")
	dead := stationEndpoint(t, st, "tok-old", "st-e")
	if err := st.RevokeEndpoint(ctx, dead.EndpointID); err != nil {
		t.Fatal(err)
	}

	n, err := st.RepointEndpointsOfToken(ctx, "tok-old", Owner{TokenID: "tok-new", ActorID: 7})
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("moved %d, want 3 (the revoked one must be left behind)", n)
	}
	var otherTok, deadTok string
	if err := st.R.QueryRowContext(ctx, `SELECT token_id FROM endpoint WHERE endpoint_id=?`, other.EndpointID).Scan(&otherTok); err != nil {
		t.Fatal(err)
	}
	if err := st.R.QueryRowContext(ctx, `SELECT token_id FROM endpoint WHERE endpoint_id=?`, dead.EndpointID).Scan(&deadTok); err != nil {
		t.Fatal(err)
	}
	if otherTok != "tok-other" {
		t.Fatalf("another token's endpoint was moved: %q", otherTok)
	}
	if deadTok != "tok-old" {
		t.Fatalf("a revoked endpoint was moved: %q", deadTok)
	}
	if got, err := st.CountEndpointsByToken(ctx, "tok-new"); err != nil || got != 3 {
		t.Fatalf("CountEndpointsByToken = %d, err %v — want 3", got, err)
	}
}
