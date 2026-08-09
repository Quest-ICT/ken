package store

import (
	"context"
	"errors"
	"testing"
)

// mkStationAndKey returns a station plus an actor id to issue vouchers under.
func mkStationAndKey(t *testing.T, s *Store, name, actorName string) (*Station, int64) {
	t.Helper()
	ctx := context.Background()
	actor, err := s.FindOrCreateActor(ctx, "ai", actorName)
	if err != nil {
		t.Fatal(err)
	}
	st, err := s.CreateStation(ctx, 1, name, "", actor)
	if err != nil {
		t.Fatal(err)
	}
	return st, actor
}

// THE test for S5: a voucher must be usable only by the endpoint it names, even by a
// session that shares everything else with the one it was issued to.
//
// This is the case the actor check could not reach, and it is not hypothetical.
// ken-prod-ops measured their estate and found SIX of eight stations sharing one
// actor, because the actor is per MACHINE. Under an actor-only check, a voucher for
// station A was redeemable by any of six sessions on that workstation — and the
// claim that accompanied it, that a leaked voucher grants nothing the comm token
// already grants, was false: a comm token registers an UNBOUND endpoint, which can
// read no station's mail at all.
//
// Two endpoints, ONE actor, so the actor check cannot be what refuses. If this test
// ever passes because of the actor, it is testing nothing.
func TestVoucherCannotBeRedeemedByAnotherEndpointUnderTheSameActor(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	st, actor := mkStationAndKey(t, s, "prod-ops", "claude@ultrakde")

	// Minted for the session staffing prod-ops.
	voucher, err := s.IssueBindingVoucher(ctx, st.StationID, "kens_prodops", "ep-prod-ops", actor, 1)
	if err != nil {
		t.Fatal(err)
	}

	// A different session on the same workstation — same actor, same comm token,
	// different endpoint. It has the string; endpoint ids are not secret either.
	if _, _, err := s.RedeemBindingVoucher(ctx, voucher, "ep-promo", actor); !errors.Is(err, ErrVoucherNotForThisEndpoint) {
		t.Fatalf("a voucher naming ep-prod-ops was redeemed from ep-promo under the same actor (err=%v) — "+
			"on an estate where one actor covers six stations this is every station's inbox open to every session on the box", err)
	}

	// CONTROL: the same voucher, from the endpoint it names. Without this the
	// assertion above would pass against a redemption path that refuses everybody.
	gotStation, gotKey, err := s.RedeemBindingVoucher(ctx, voucher, "ep-prod-ops", actor)
	if err != nil {
		t.Fatalf("the endpoint the voucher names could not redeem it: %v", err)
	}
	if gotStation != st.StationID || gotKey != "kens_prodops" {
		t.Fatalf("redeemed to station %q via key %q, want %q / %q", gotStation, gotKey, st.StationID, "kens_prodops")
	}
}

// The actor check, isolated. Same endpoint on both attempts, so the ONLY thing that
// can differ is the actor — otherwise this would silently be a second copy of the
// endpoint test above.
//
// This check is the SETUP guard, not the security property: it catches a station key
// minted under a different actor than the machine's comm token, which otherwise has
// no symptom until it silently defeats the hearsay marker.
func TestVoucherCannotBeRedeemedByAnotherActor(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	st, owner := mkStationAndKey(t, s, "dev", "dev-session")
	intruder, err := s.FindOrCreateActor(ctx, "ai", "someone-else")
	if err != nil {
		t.Fatal(err)
	}
	if owner == intruder {
		t.Fatal("setup: both actors resolved to the same id, so the test cannot discriminate")
	}

	voucher, err := s.IssueBindingVoucher(ctx, st.StationID, "kens_dev", "ep-owner", owner, 1)
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := s.RedeemBindingVoucher(ctx, voucher, "ep-owner", intruder); !errors.Is(err, ErrVoucherNotYours) {
		t.Fatalf("a voucher issued to actor %d was redeemed by actor %d from the correct endpoint (err=%v)", owner, intruder, err)
	}
	if _, _, err := s.RedeemBindingVoucher(ctx, voucher, "ep-owner", owner); err != nil {
		t.Fatalf("the actor the voucher was issued to could not redeem it: %v", err)
	}
}

// A rejected redemption must not consume the voucher.
//
// If the UPDATE matched on the hash alone and rejected afterwards, anyone holding a
// leaked voucher could burn it the instant it was issued — turning a confidentiality
// bug into a denial of service against binding, which is the one operation a session
// cannot complete any other way.
func TestRejectedRedemptionDoesNotBurnTheVoucher(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	st, owner := mkStationAndKey(t, s, "dev", "dev-session")
	intruder, err := s.FindOrCreateActor(ctx, "ai", "someone-else")
	if err != nil {
		t.Fatal(err)
	}
	voucher, err := s.IssueBindingVoucher(ctx, st.StationID, "kens_dev", "ep-owner", owner, 1)
	if err != nil {
		t.Fatal(err)
	}

	// Both refusal causes, alternating, so neither path can be the one that burns it.
	for i := 0; i < 3; i++ {
		if _, _, err := s.RedeemBindingVoucher(ctx, voucher, "ep-owner", intruder); !errors.Is(err, ErrVoucherNotYours) {
			t.Fatalf("attempt %d (wrong actor): want ErrVoucherNotYours, got %v", i+1, err)
		}
		if _, _, err := s.RedeemBindingVoucher(ctx, voucher, "ep-other", owner); !errors.Is(err, ErrVoucherNotForThisEndpoint) {
			t.Fatalf("attempt %d (wrong endpoint): want ErrVoucherNotForThisEndpoint, got %v", i+1, err)
		}
	}
	if _, _, err := s.RedeemBindingVoucher(ctx, voucher, "ep-owner", owner); err != nil {
		t.Fatalf("the owner's voucher was destroyed by six rejected attempts: %v", err)
	}
}

// The two refusals must be distinguishable from each other AND from an invalid
// voucher, because all three demand different responses: ask for a voucher naming
// your endpoint (retry works), re-mint a station key from the console (retry never
// works), or the voucher is simply spent.
func TestTheThreeRefusalsAreDistinguishable(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	st, owner := mkStationAndKey(t, s, "dev", "dev-session")
	intruder, err := s.FindOrCreateActor(ctx, "ai", "someone-else")
	if err != nil {
		t.Fatal(err)
	}
	voucher, err := s.IssueBindingVoucher(ctx, st.StationID, "kens_dev", "ep-named", owner, 1)
	if err != nil {
		t.Fatal(err)
	}

	_, _, wrongActor := s.RedeemBindingVoucher(ctx, voucher, "ep-named", intruder)
	_, _, wrongEndpoint := s.RedeemBindingVoucher(ctx, voucher, "ep-other", owner)
	_, _, unknown := s.RedeemBindingVoucher(ctx, "a-voucher-that-was-never-issued", "ep-named", owner)

	if !errors.Is(wrongActor, ErrVoucherNotYours) {
		t.Fatalf("wrong actor reported %v", wrongActor)
	}
	if !errors.Is(wrongEndpoint, ErrVoucherNotForThisEndpoint) {
		t.Fatalf("wrong endpoint reported %v", wrongEndpoint)
	}
	if !errors.Is(unknown, ErrVoucherInvalid) {
		t.Fatalf("unknown voucher reported %v, want ErrVoucherInvalid", unknown)
	}
	// Distinct TEXT, not merely distinct types: an operator reads the string.
	seen := map[string]bool{}
	for _, e := range []error{wrongActor, wrongEndpoint, unknown} {
		if seen[e.Error()] {
			t.Fatalf("two refusals share the same text, so the distinction exists only in the type and no operator will ever see it: %q", e.Error())
		}
		seen[e.Error()] = true
	}
}

// Single-use, from the SAME endpoint under the SAME actor — otherwise this would be
// re-testing one of the identity checks rather than the redeemed_at flag.
func TestVoucherIsSingleUse(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	st, owner := mkStationAndKey(t, s, "dev", "dev-session")
	voucher, err := s.IssueBindingVoucher(ctx, st.StationID, "kens_dev", "ep-1", owner, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.RedeemBindingVoucher(ctx, voucher, "ep-1", owner); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.RedeemBindingVoucher(ctx, voucher, "ep-1", owner); !errors.Is(err, ErrVoucherInvalid) {
		t.Fatalf("a voucher redeemed twice by its own endpoint: second attempt returned %v, want ErrVoucherInvalid", err)
	}
}

// A voucher with no nomination cannot be minted at all. An empty nomination would
// produce a well-formed credential whose redemption predicate can never match — a
// session handed something that fails at the next call for no visible reason.
func TestAVoucherMustNameAnEndpoint(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	st, owner := mkStationAndKey(t, s, "dev", "dev-session")
	if _, err := s.IssueBindingVoucher(ctx, st.StationID, "kens_dev", "", owner, 1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("minting a voucher with no endpoint returned %v, want ErrInvalid", err)
	}
}

// A voucher minted before migrations 0014/0015 carries NULL in the columns that
// authorise redemption, and must refuse rather than be grandfathered in as a bearer
// capability by the very change that closes the bearer hole.
//
// The NULLs cannot be produced by any code path — they can only arrive from a row
// that predates the columns — so the test forges one, and then redeems a properly
// issued voucher to prove the refusal came from the NULLs.
func TestPreMigrationVoucherIsRefusedRatherThanGrandfathered(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	st, owner := mkStationAndKey(t, s, "dev", "dev-session")

	legacy := "a-voucher-from-before-the-columns-existed"
	if _, err := s.W.ExecContext(ctx, `
INSERT INTO station_binding_voucher(voucher_sha256, station_id, token_id, expires_at)
VALUES(?,?,?, strftime('%Y-%m-%dT%H:%M:%fZ','now','+5 minutes'))`,
		voucherHash(legacy), st.StationID, "kens_dev"); err != nil {
		t.Fatal(err)
	}

	if _, _, err := s.RedeemBindingVoucher(ctx, legacy, "ep", owner); !errors.Is(err, ErrVoucherInvalid) {
		t.Fatalf("a pre-migration voucher redeemed with err=%v — the bearer hole survives the migration that closes it", err)
	}

	fresh, err := s.IssueBindingVoucher(ctx, st.StationID, "kens_dev", "ep", owner, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.RedeemBindingVoucher(ctx, fresh, "ep", owner); err != nil {
		t.Fatalf("control: a properly-issued voucher failed too (%v), so the assertion above proves nothing", err)
	}
}

// An archived station's voucher must not bind even for the right endpoint and actor:
// S3 stops an archived station's keys from binding, and honouring a voucher minted
// before the archive would be a hole straight through that.
func TestVoucherForAnArchivedStationRefuses(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	st, owner := mkStationAndKey(t, s, "dev", "dev-session")
	voucher, err := s.IssueBindingVoucher(ctx, st.StationID, "kens_dev", "ep", owner, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ArchiveStation(ctx, st.StationID, true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.RedeemBindingVoucher(ctx, voucher, "ep", owner); !errors.Is(err, ErrVoucherInvalid) {
		t.Fatalf("a voucher for an archived station redeemed with err=%v", err)
	}
}
