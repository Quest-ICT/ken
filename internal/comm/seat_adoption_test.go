package comm

import (
	"context"
	"testing"
)

// BINDING ADOPTS THE SEATS THE ENDPOINT ALREADY OCCUPIES.
//
// channel.station_a/b is written when a seat is FILLED. A session that joined a
// pairing-code channel while unbound and bound afterwards left NULL there forever — nothing
// revisited it. The consequence is not cosmetic: the pair predicate is snapshot-only, so the
// channel is invisible to the blast-radius count shown before a link revocation, to the
// revocation sweep, and to OpenLinkedChannel's reuse lookup.
func TestBindingAdoptsAChannelSeatJoinedWhileUnbound(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()

	// Both sides join a pairing-code channel while UNBOUND — the ordinary case before
	// either session staffs a station.
	epA, secA, err := st.RegisterEndpoint(ctx, owner("tok-a"), "a", "")
	if err != nil {
		t.Fatal(err)
	}
	epB, secB, err := st.RegisterEndpoint(ctx, owner("tok-b"), "b", "")
	if err != nil {
		t.Fatal(err)
	}
	boundA, err := st.AuthenticateEndpoint(ctx, epA.EndpointID, secA)
	if err != nil {
		t.Fatal(err)
	}
	boundB, err := st.AuthenticateEndpoint(ctx, epB.EndpointID, secB)
	if err != nil {
		t.Fatal(err)
	}
	code, err := st.MintPairingCode(ctx, 1, 1, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.JoinChannel(ctx, boundA, code); err != nil {
		t.Fatal(err)
	}
	ch, err := st.JoinChannel(ctx, boundB, code)
	if err != nil {
		t.Fatal(err)
	}

	pair := func() (a, b any) {
		t.Helper()
		if err := st.R.QueryRowContext(ctx,
			`SELECT station_a, station_b FROM channel WHERE channel_id=?`, ch.ChannelID).Scan(&a, &b); err != nil {
			t.Fatal(err)
		}
		return
	}

	// POSITIVE CONTROL: both seats must start NULL, or "they are filled after binding"
	// would be true of a channel that was never in the broken state.
	if a, b := pair(); a != nil || b != nil {
		t.Fatalf("setup: seats are not NULL before binding (%v, %v) — this fixture is not the "+
			"adopt-after-join case the fix is about", a, b)
	}

	if err := st.BindEndpointToStation(ctx, epA.EndpointID, "st-alpha", "kens_a"); err != nil {
		t.Fatal(err)
	}
	a, b := pair()
	if a != "st-alpha" {
		t.Errorf("seat A was not adopted on bind: station_a = %v, want st-alpha", a)
	}
	if b != nil {
		t.Errorf("binding endpoint A filled seat B (%v) — it must only adopt seats it occupies", b)
	}

	if err := st.BindEndpointToStation(ctx, epB.EndpointID, "st-beta", "kens_b"); err != nil {
		t.Fatal(err)
	}
	if a, b = pair(); a != "st-alpha" || b != "st-beta" {
		t.Errorf("after both binds the pair is (%v, %v), want (st-alpha, st-beta)", a, b)
	}

	// AND THE CONSEQUENCE THAT MATTERS: the channel is now visible to link revocation,
	// which is what a human is shown before they click.
	n, err := st.CountOpenChannelsBetweenStations(ctx, "st-alpha", "st-beta")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("the adopted channel is invisible to the blast-radius count (%d, want 1) — a "+
			"human revoking this link would be told it ends no traffic", n)
	}
}

// AN EXISTING SNAPSHOT IS NEVER REWRITTEN.
//
// Migration 0008's warning is that the CURRENT binding is "exactly the value that may
// already have drifted". Filling NULL at the moment of binding cannot drift; overwriting a
// pair recorded at seat-fill time with a later binding is precisely the mistake it names.
func TestBindingNeverOverwritesAPairRecordedAtSeatFill(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()

	sender := stationEndpoint(t, st, "tok-s", "st-original")
	peer := stationEndpoint(t, st, "tok-p", "st-peer")
	ch, err := st.OpenLinkedChannel(ctx, sender, peer, 1, "orig <-> peer")
	if err != nil {
		t.Fatal(err)
	}

	var before any
	if err := st.R.QueryRowContext(ctx,
		`SELECT station_a FROM channel WHERE channel_id=?`, ch.ChannelID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if before == nil {
		t.Fatal("setup: a linked channel recorded no pair, so there is nothing to protect")
	}

	// Unbind and rebind the same endpoint to a DIFFERENT station. The seat's recorded pair
	// must not follow it.
	if err := st.UnbindEndpointFromStation(ctx, sender.EndpointID); err != nil {
		t.Fatal(err)
	}
	if err := st.BindEndpointToStation(ctx, sender.EndpointID, "st-moved", "kens_m"); err != nil {
		t.Fatal(err)
	}
	var after any
	if err := st.R.QueryRowContext(ctx,
		`SELECT station_a FROM channel WHERE channel_id=?`, ch.ChannelID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("the authorising pair moved with a rebind: %v -> %v. Authorisation is a fact "+
			"about the past and must not be re-derived from state that has since changed", before, after)
	}
}
