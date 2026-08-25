package store

import "testing"

// *** §10 STEP 2, STATED AS THE PROPERTY IT IS: ONE IDENTITY SPANS ALL THREE SURFACES. ***
//
// docs/IDENTITY.md: "Make one identity span /comm and /station. This is the condition §9.2 names,
// and it is what unlocks the voucher chain. auth.go:200 discarding the OAuth grant's scope is the
// blocker: until OAuth can express comm and station, no session can hold one identity across both."
//
// The three authenticators each read this same function now, so the test that the step happened is
// that a fully-granted connector resolves to capabilities covering all three — and that a session
// therefore needs ONE approval rather than three credentials minted three different ways, which is
// Vlad's standing requirement: "all sessions without exception must have full access to all Ken
// features and it should not require numerous keys, tokens, vouchers, approvals, etc. but just an
// actor (computer) registration ... and one approval."
//
// WHAT THIS DOES NOT ASSERT, deliberately: that the connector has a STATION. It does not. An OAuth
// principal reaches /station/mcp with no station id, which is the state station_request exists to
// serve. Turning that request into a station is the next piece of work.
func TestOneGrantSpansAllThreeSurfaces(t *testing.T) {
	full := ScopeKB + " " + ScopeCommSet + " " + ScopeStation
	caps := map[string]bool{}
	for _, c := range GrantedCapabilities("read write offline_access " + full) {
		caps[c] = true
	}
	for _, surface := range []struct{ name, capability string }{
		{"/mcp — search and fetch", "read"},
		{"/mcp — propose", "propose"},
		{"/comm/mcp — messaging", "comm"},
		{"/comm/mcp — the file relay", "comm-file"},
		{"/station/mcp — the working identity", "station"},
		{"/station/mcp — its locker", "station-locker"},
	} {
		if !caps[surface.capability] {
			t.Errorf("a fully-granted connector cannot reach %s (missing %q). One approval must cover "+
				"every surface, or a session is back to three credentials minted three ways.",
				surface.name, surface.capability)
		}
	}
	if caps["curate"] {
		t.Error("and it must never carry curate, on any path")
	}
}
