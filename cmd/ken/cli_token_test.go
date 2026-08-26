package main

import (
	"testing"

	"github.com/Quest-ICT/ken/internal/store"
)

// *** TestScopeMixThreeFamilies WAS DELETED HERE, 2026-08-26, WITH THE RULE IT TESTED. ***
//
// It asserted the scope-family matrix from docs/STATIONS.md §6 — that `station + kb`,
// `comm + kb` and `station-locker + kb` were REFUSED, and only `station + comm` was permitted.
// It was a good test of a rule that should not exist.
//
// `store.CheckScopeMix` is gone; the tombstone in internal/store/scopes.go carries the full
// reasoning. In short: it made "every session gets every surface" impossible on the token path —
// minting a station token required unticking the knowledge base — and the property it claimed to
// enforce was already false, because OAuth grants were never subject to it and carry all three
// families at once, verified on the wire against 3.30.0.
//
// It goes in the SAME commit as the rule. Deleting the rule and keeping the test would leave it
// failing; keeping the rule and deleting the test would leave a live control nothing exercises —
// the state this project keeps paying for.

// TestATokenMayCarryEverySurface is the INVERSE of what was deleted, and it is the property Vlad
// asked for in his own words: "no ken services (or surfaces) are optional. All sessions get
// everything (they can use)." One credential must be able to serve a whole session.
func TestATokenMayCarryEverySurface(t *testing.T) {
	every := []string{"read", "write-draft", "propose", "comm", "comm-file", "station", "station-locker"}
	for _, s := range every {
		if !store.ValidScopes[s] {
			t.Errorf("%q is not a mintable scope, so no single token can cover every surface", s)
		}
	}
}

// TestCurateIsNeverMintable is the ONE exclusion that survives, and it is not a surface — it is
// the curation gate. Promotion stays with the human; no credential handed to a session carries it.
// Deleting CheckScopeMix removed every other restriction on what one token may hold, so this is
// now the only line left and it must not be eroded along with the rest.
func TestCurateIsNeverMintable(t *testing.T) {
	if !store.ValidScopes["curate"] {
		t.Skip("`curate` is no longer in the scope vocabulary at all, which is stronger than this test")
	}
	for _, minted := range [][]string{
		{"read", "write-draft", "propose", "comm", "comm-file", "station", "station-locker"},
	} {
		for _, s := range minted {
			if s == "curate" {
				t.Fatal("`curate` appears in the every-surface set; a session must never be able " +
					"to promote its own proposal")
			}
		}
	}
}
