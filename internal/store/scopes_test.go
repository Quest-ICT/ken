package store

import "testing"

// Every scope the console or the CLI can offer must be in the vocabulary, or it mints a token
// carrying a scope nothing will ever check.
func TestMintableScopesAreAllValid(t *testing.T) {
	for _, s := range []string{"read", "write-draft", "propose", "curate",
		"comm", "comm-file", "station", "station-locker"} {
		if !ValidScopes[s] {
			t.Errorf("%q is offered somewhere but is not in ValidScopes", s)
		}
	}
	for s := range CommScopes {
		if !ValidScopes[s] {
			t.Errorf("comm scope %q is not in ValidScopes", s)
		}
	}
	for s := range StationScopes {
		if !ValidScopes[s] {
			t.Errorf("station scope %q is not in ValidScopes", s)
		}
	}
}
