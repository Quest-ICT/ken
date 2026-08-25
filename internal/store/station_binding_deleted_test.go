package store

import (
	"os"
	"strings"
	"testing"
)

// TestTheVoucherChainStaysDeleted.
//
// *** WHY A TEST FOR AN ABSENCE. *** §10 step 3 said to delete the chain "in one change, with its
// tests, saying why" — so `station_voucher_test.go` went with it, 63 references across ten cases
// covering wrong-endpoint, wrong-actor, single-use, no-nomination, pre-migration NULLs and
// archived-station redemption. Deleting tests is how a mechanism quietly comes back: nothing then
// objects when someone reintroduces the "safer" version of a thing the design removed on purpose.
//
// It also closes an open item rather than abandoning it. Task t-laLaMYzb recorded that VoucherTTL
// was effectively untested — no test in that file ever advanced a clock, so deleting the
// `expires_at > now` predicate left every case green while the hourly janitor silently widened the
// window from five minutes to up to an hour. Its instruction was explicit: **"either write the
// clock test or delete the mechanism, but do not leave it in this state."** The mechanism is
// deleted. That is the honest resolution, not the convenient one.
//
// WHAT REPLACES THE VOUCHER IS NOT A SMALLER CREDENTIAL, IT IS NO CREDENTIAL: comm_bind reads the
// workspace off a header that authorises nothing, and binds with an EMPTY
// bound_by_station_key_id. If a future change needs a credential there again, §9.2 is the thing to
// re-read first — "if that id ever gains authority, this control comes straight back."
func TestTheVoucherChainStaysDeleted(t *testing.T) {
	b, err := os.ReadFile("station_binding.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	// POSITIVE CONTROL: the file must still hold the thing it is now for, or this test is
	// asserting absences in a file that no longer exists in the shape it thinks.
	if !strings.Contains(src, "func (s *Store) IsStationKeyRevoked") {
		t.Fatal("station_binding.go no longer defines IsStationKeyRevoked; this test is looking at " +
			"the wrong file and every assertion below is vacuous")
	}
	for _, gone := range []string{
		"func (s *Store) IssueBindingVoucher",
		"func (s *Store) RedeemBindingVoucher",
		"func (s *Store) SweepBindingVouchers",
		// DECLARATIONS, not mentions: the banner comment above names all six so the next
		// reader knows what left and why, and a bare-name grep would fire on the explanation
		// itself — a check that forbids describing its own subject.
		"var ErrVoucherInvalid",
		"var ErrVoucherNotForThisEndpoint",
		"var ErrVoucherNotYours",
	} {
		if strings.Contains(src, gone) {
			t.Errorf("%s is back. The voucher existed SOLELY so a station key never crossed to the "+
				"comm surface as a tool argument (§9.2); one identity spans both surfaces now and the "+
				"workspace id authorises nothing, so there is nothing for it to carry. If this is "+
				"deliberate, re-read §9.2 and delete this test in the same change.", gone)
		}
	}
}
