package store

import (
	"strings"
	"testing"
)

// *** NO GRANT EVER CARRIES `curate`, WHATEVER IT ASKS FOR. ***
//
// The curation gate is Ken's central claim: an agent captures, enhances, flags stale and records
// outcomes, and a HUMAN promotes. Everything else in the product is arrangeable; this is not.
//
// It was enforced by the fact that the OAuth authenticator held a literal three-element list, and
// §10 step 2 replaced that literal with a mapping from a string the CLIENT supplies. That is the
// moment an exclusion-by-construction becomes an exclusion-by-omission, and mutation proved the
// difference: appending "curate" to the returned set left the whole repository green.
//
// A client cannot ask its way past it, because the mapping is a whitelist rather than a filter —
// nothing in the scope string is copied through to the capability set.
func TestNoGrantCanEverCarryCurate(t *testing.T) {
	for _, scope := range []string{
		"read write offline_access",
		ScopeKB + " " + ScopeCommSet + " " + ScopeStation,
		"curate",                       // asked for by name
		"ken:curate",                   // asked for in Ken's own namespace
		ScopeKB + " curate ken:curate", // smuggled beside a legitimate one
		strings.Repeat("curate ", 50),  // asked for insistently
	} {
		for _, got := range GrantedCapabilities(scope) {
			if got == "curate" {
				t.Errorf("scope %q produced the curate capability — a connector could advance the "+
					"curated head, which is the one thing only a human may do", scope)
			}
		}
	}
}

// AND THE MAPPING IS A WHITELIST, not a pass-through: an unknown scope grants nothing.
//
// The failure this prevents is subtle — a future edit that "just copies the ken:* fields through"
// would look tidier and would let a client name any capability Ken ever adds, including ones added
// after the grant was approved.
func TestUnknownScopesGrantNothingExtra(t *testing.T) {
	legacy := GrantedCapabilities("read write offline_access")
	weird := GrantedCapabilities("read write offline_access ken:everything ken:admin station")
	if len(weird) != len(legacy) {
		t.Errorf("unrecognised scopes changed the capability set: %v vs %v — the mapping is copying "+
			"through rather than whitelisting, so a client can name capabilities nobody granted", weird, legacy)
	}
	// Note `station` bare (not ken:station) is in that string deliberately: Ken's internal
	// capability names must not be reachable from the wire vocabulary either.
	for _, c := range weird {
		if c == "station" || c == "station-locker" {
			t.Error("the bare internal capability name `station` was honoured from the wire scope; " +
				"only ken:station may grant it")
		}
	}
}

// A LEGACY GRANT KEEPS EXACTLY WHAT ITS HUMAN AGREED TO.
//
// Every grant approved before step 2 was approved when a connector could reach /mcp and nothing
// else. IDENTITY-CONTROLS.md's warning is that consolidation widens them "invisibly — every
// surface keeps working, better even, and the day a connector is compromised the blast radius has
// quietly grown from the knowledge base to the message bus and the vault."
func TestLegacyGrantsAreNotWidened(t *testing.T) {
	got := GrantedCapabilities("read write offline_access")
	want := map[string]bool{"read": true, "write-draft": true, "propose": true}
	if len(got) != len(want) {
		t.Fatalf("a legacy grant resolves to %v; it must stay the knowledge base alone", got)
	}
	for _, c := range got {
		if !want[c] {
			t.Errorf("a legacy grant gained %q without anyone approving it", c)
		}
	}
}
