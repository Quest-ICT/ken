package comm

import (
	"context"
	"testing"
)

// StationHasMail backs the unauthenticated mail-waiting check (POST /comm/waiting). It must
// say exactly what pending_total says, by station id alone — no endpoint in hand.

func TestStationHasMailAgreesWithPendingTotalInEveryScope(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	alpha := stationEndpoint(t, st, "tok-a", "st-alpha")
	beta := stationEndpoint(t, st, "tok-b", "st-beta")
	roomFixture(t, st, "ops", "s:st-alpha", "s:st-beta")

	if n, err := st.StationHasMail(ctx, "st-alpha"); err != nil || n != 0 {
		t.Fatalf("empty inbox: %d, %v", n, err)
	}
	linkedTo(t, st, alpha, beta)
	if _, err := st.SendToStation(ctx, beta, alpha.StationID, "channel", SendOpts{}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SendToRoom(ctx, beta, "ops", "room", SendOpts{}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.BroadcastTo(ctx, beta, []string{"s:st-alpha"}, "broadcast", SendOpts{}); err != nil {
		t.Fatal(err)
	}
	total, err := st.PendingTotalFor(ctx, alpha)
	if err != nil {
		t.Fatal(err)
	}
	n, err := st.StationHasMail(ctx, "st-alpha")
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || n != total {
		t.Fatalf("StationHasMail = %d, pending_total = %d, want both 3 (channel, room, broadcast)", n, total)
	}
	// The sender's side must agree too. Compared with pending_total rather than with an
	// assumed number, so the test does not encode a belief about who receives a room send.
	betaTotal, err := st.PendingTotalFor(ctx, beta)
	if err != nil {
		t.Fatal(err)
	}
	if nb, err := st.StationHasMail(ctx, "st-beta"); err != nil || nb != betaTotal {
		t.Fatalf("beta: StationHasMail = %d, pending_total = %d", nb, betaTotal)
	}
	if nu, err := st.StationHasMail(ctx, "st-nobody"); err != nil || nu != 0 {
		t.Fatalf("an id naming no station: %d, %v, want 0", nu, err)
	}
}

// BOTH PARTY FORMS: a delivery filed under the endpoint's own 'e:<rowid>' (written while it
// was unbound, or left by a backwards restore) is mail a poll would hand over. Matching
// only 's:<station>' would answer "no mail" over it.
func TestStationHasMailMatchesTheEndpointPartyForm(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	alpha := stationEndpoint(t, st, "tok-a", "st-alpha")
	beta := stationEndpoint(t, st, "tok-b", "st-beta")
	roomFixture(t, st, "ops", "s:st-alpha", "s:st-beta")
	if _, err := st.SendToRoom(ctx, beta, "ops", "filed under the endpoint", SendOpts{}); err != nil {
		t.Fatal(err)
	}
	res, err := st.W.ExecContext(ctx, `UPDATE delivery SET party_key=? WHERE party_key=?`,
		endpointPartyKey(alpha.ID), stationParty("st-alpha"))
	if err != nil {
		t.Fatal(err)
	}
	// PRECONDITION: the re-file must actually move a row. PartyOf(alpha) returns the STATION
	// form for a bound endpoint, so re-filing under it was a no-op and this test passed
	// without ever exercising the endpoint form (found by mutation, 2026-11-17).
	if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("re-filed %d deliveries, want 1 — the test would prove nothing", n)
	}
	if n, err := st.StationHasMail(ctx, "st-alpha"); err != nil || n != 1 {
		t.Fatalf("StationHasMail = %d (%v) for a delivery filed under e:<rowid>, want 1", n, err)
	}
}

// QUEUED ONLY, NOT EXPIRED, AND IT DELIVERS NOTHING. Checked after a poll because before
// one every row is queued and the assertion cannot discriminate.
func TestStationHasMailIsQueuedOnlyAndDeliversNothing(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	alpha := stationEndpoint(t, st, "tok-a", "st-alpha")
	beta := stationEndpoint(t, st, "tok-b", "st-beta")
	roomFixture(t, st, "ops", "s:st-alpha", "s:st-beta")
	if _, err := st.SendToRoom(ctx, beta, "ops", "one", SendOpts{}); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 3; i++ { // asking repeatedly must not change anything
		if n, err := st.StationHasMail(ctx, "st-alpha"); err != nil || n != 1 {
			t.Fatalf("ask %d: %d, %v, want 1", i, n, err)
		}
	}
	var delivered int
	if err := st.R.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM delivery WHERE party_key=? AND first_delivered_at IS NOT NULL`,
		stationParty("st-alpha")).Scan(&delivered); err != nil {
		t.Fatal(err)
	}
	if delivered != 0 {
		t.Fatalf("asking delivered %d messages: the check would start the 24 h expiry clock", delivered)
	}

	if _, err := st.Poll(ctx, alpha, 10); err != nil {
		t.Fatal(err)
	}
	if n, err := st.StationHasMail(ctx, "st-alpha"); err != nil || n != 0 {
		t.Fatalf("after a poll: %d, %v, want 0 — delivered-but-unacked is mail the station already has", n, err)
	}

	// Expired but not yet swept: not deliverable, so not waiting.
	if _, err := st.SendToRoom(ctx, beta, "ops", "two", SendOpts{}); err != nil {
		t.Fatal(err)
	}
	if n, err := st.StationHasMail(ctx, "st-alpha"); err != nil || n != 1 {
		t.Fatalf("positive control: %d, %v, want 1", n, err)
	}
	if _, err := st.W.ExecContext(ctx,
		`UPDATE message SET expires_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','-1 second')`); err != nil {
		t.Fatal(err)
	}
	if n, err := st.StationHasMail(ctx, "st-alpha"); err != nil || n != 0 {
		t.Fatalf("expired but unswept: %d, %v, want 0", n, err)
	}
}
