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

// THE test for S5, and the one that was missing entirely: a voucher must not be
// redeemable by whoever happens to hold the string.
//
// As shipped, redemption checked the hash, the single-use flag, the expiry and the
// station's state — and nothing about the redeemer. So the value alone bound any
// endpoint to the station's inbox, and the only control was a human remembering
// "never send a voucher over COMM, never write it to a file". That rule was
// load-bearing security. This asserts the code now carries the load.
//
// Note what this test does NOT do: it never asserts "redemption failed". A wrong
// voucher, a typo'd hash and a broken query all fail too, and each would let this
// pass while proving nothing. It pins the SAME voucher failing for the intruder and
// succeeding for the owner, so the refusal can only be about identity.
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

	voucher, err := s.IssueBindingVoucher(ctx, st.StationID, "kens_dev", owner, 1)
	if err != nil {
		t.Fatal(err)
	}

	// The leak: the intruder holds the exact string, unexpired and unredeemed.
	if _, _, err := s.RedeemBindingVoucher(ctx, voucher, "ep-intruder", intruder); !errors.Is(err, ErrVoucherNotYours) {
		t.Fatalf("a voucher issued to actor %d was redeemed by actor %d (err=%v) — "+
			"the voucher is still a bearer capability and anything holding the string can join this station's inbox",
			owner, intruder, err)
	}

	// CONTROL: the very same voucher, for the actor it was issued to. Without this
	// the assertion above would pass against a redemption path that refuses
	// everybody, which is a different bug wearing the same green tick.
	gotStation, gotKey, err := s.RedeemBindingVoucher(ctx, voucher, "ep-owner", owner)
	if err != nil {
		t.Fatalf("the actor the voucher was issued to could not redeem it: %v", err)
	}
	if gotStation != st.StationID || gotKey != "kens_dev" {
		t.Fatalf("redeemed to station %q via key %q, want %q / %q", gotStation, gotKey, st.StationID, "kens_dev")
	}
}

// A failed redemption by the wrong actor must not consume the voucher.
//
// If the UPDATE had matched on the hash alone and rejected afterwards, an intruder
// could burn every voucher the moment it was issued — turning a confidentiality bug
// into a denial of service against binding, which is the operation a session cannot
// complete any other way.
func TestRejectedRedemptionDoesNotBurnTheVoucher(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	st, owner := mkStationAndKey(t, s, "dev", "dev-session")
	intruder, err := s.FindOrCreateActor(ctx, "ai", "someone-else")
	if err != nil {
		t.Fatal(err)
	}
	voucher, err := s.IssueBindingVoucher(ctx, st.StationID, "kens_dev", owner, 1)
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 3; i++ {
		if _, _, err := s.RedeemBindingVoucher(ctx, voucher, "ep-intruder", intruder); !errors.Is(err, ErrVoucherNotYours) {
			t.Fatalf("attempt %d: want ErrVoucherNotYours, got %v", i+1, err)
		}
	}
	if _, _, err := s.RedeemBindingVoucher(ctx, voucher, "ep-owner", owner); err != nil {
		t.Fatalf("the owner's voucher was destroyed by %d rejected attempts: %v", 3, err)
	}
}

// The setup error must be distinguishable from the expiry race.
//
// ErrVoucherInvalid deliberately collapses unknown/used/expired, so this asserts the
// one case that is deliberately NOT collapsed. An actor mismatch is a configuration
// the deployment can sit in for months with no symptom; reported as "not valid" it
// reads as an expiry race and the operator mints fresh vouchers forever, each
// failing identically.
func TestActorMismatchIsDistinguishableFromAnInvalidVoucher(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	st, owner := mkStationAndKey(t, s, "dev", "dev-session")
	intruder, err := s.FindOrCreateActor(ctx, "ai", "someone-else")
	if err != nil {
		t.Fatal(err)
	}
	voucher, err := s.IssueBindingVoucher(ctx, st.StationID, "kens_dev", owner, 1)
	if err != nil {
		t.Fatal(err)
	}

	_, _, mismatch := s.RedeemBindingVoucher(ctx, voucher, "ep", intruder)
	_, _, unknown := s.RedeemBindingVoucher(ctx, "a-voucher-that-was-never-issued", "ep", intruder)

	if errors.Is(mismatch, ErrVoucherInvalid) {
		t.Fatal("an actor mismatch reports as ErrVoucherInvalid — the operator sees an expiry race and cannot reach the real cause")
	}
	if !errors.Is(unknown, ErrVoucherInvalid) {
		t.Fatalf("an unknown voucher reports %v, want ErrVoucherInvalid", unknown)
	}
	if mismatch.Error() == unknown.Error() {
		t.Fatal("the two refusals are textually identical, so the distinction exists only in the type and no operator will ever see it")
	}
}

// Single-use, and the second attempt is by the SAME actor — otherwise this would be
// re-testing the actor check rather than the redeemed_at flag.
func TestVoucherIsSingleUse(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	st, owner := mkStationAndKey(t, s, "dev", "dev-session")
	voucher, err := s.IssueBindingVoucher(ctx, st.StationID, "kens_dev", owner, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.RedeemBindingVoucher(ctx, voucher, "ep-1", owner); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.RedeemBindingVoucher(ctx, voucher, "ep-2", owner); !errors.Is(err, ErrVoucherInvalid) {
		t.Fatalf("a voucher redeemed twice: second attempt returned %v, want ErrVoucherInvalid", err)
	}
}

// A voucher minted before migration 0014 has a NULL issued_to_actor and must refuse,
// rather than being grandfathered in as a bearer capability by the very change that
// closes the bearer hole.
//
// The NULL is not written by any code path — it can only arrive from a row that
// predates the column — so the test forges one directly. It also proves the refusal
// comes from the NULL and not from some incidental difference, by redeeming an
// identically-shaped row that HAS an actor.
func TestPreMigrationVoucherIsRefusedRatherThanGrandfathered(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	st, owner := mkStationAndKey(t, s, "dev", "dev-session")

	// A row exactly as 0013 would have written it: no issuing identity at all.
	legacy := "a-voucher-from-before-the-column-existed"
	if _, err := s.W.ExecContext(ctx, `
INSERT INTO station_binding_voucher(voucher_sha256, station_id, token_id, expires_at)
VALUES(?,?,?, strftime('%Y-%m-%dT%H:%M:%fZ','now','+5 minutes'))`,
		voucherHash(legacy), st.StationID, "kens_dev"); err != nil {
		t.Fatal(err)
	}

	if _, _, err := s.RedeemBindingVoucher(ctx, legacy, "ep", owner); !errors.Is(err, ErrVoucherInvalid) {
		t.Fatalf("a pre-0014 voucher redeemed with err=%v — the bearer hole survives the migration that closes it", err)
	}

	// CONTROL: the same shape, issued properly, still works — so the refusal above
	// is about the NULL and not about anything else this test happens to do.
	fresh, err := s.IssueBindingVoucher(ctx, st.StationID, "kens_dev", owner, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.RedeemBindingVoucher(ctx, fresh, "ep", owner); err != nil {
		t.Fatalf("control: a properly-issued voucher failed too (%v), so the assertion above proves nothing", err)
	}
}

// An archived station's voucher must not bind, even for the right actor: S3 stops an
// archived station's keys from binding, and honouring a voucher minted before the
// archive would be a hole straight through that.
func TestVoucherForAnArchivedStationRefuses(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	st, owner := mkStationAndKey(t, s, "dev", "dev-session")
	voucher, err := s.IssueBindingVoucher(ctx, st.StationID, "kens_dev", owner, 1)
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
