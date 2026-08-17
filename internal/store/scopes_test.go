package store

import "testing"

// THE ONE-FAMILY RULE, now that BOTH mint paths reach it.
//
// Until 3.10.0 this lived in `package main` and only `ken token add` enforced it. The console
// could not violate it because its menu offered three knowledge-base scopes and silently dropped
// everything else — safety by accident, which stopped being good enough the moment the console
// learned to mint comm tokens.
//
// The permitted-pair arm is not decoration. Without it, a rule that refused EVERY combination
// would pass every other case here, and the failure would surface as a station session unable to
// hold the two scopes it legitimately needs.
func TestCheckScopeMix(t *testing.T) {
	for _, c := range []struct {
		name   string
		scopes []string
		refuse bool
	}{
		{"knowledge base alone", []string{"read", "write-draft", "propose"}, false},
		{"comm alone", []string{"comm"}, false},
		{"comm with its reserved sibling", []string{"comm", "comm-file"}, false},
		{"station alone", []string{"station", "station-locker"}, false},
		{"THE PERMITTED PAIR: a station that talks", []string{"station", "comm"}, false},
		{"knowledge base + comm", []string{"read", "comm"}, true},
		{"knowledge base + station", []string{"propose", "station"}, true},
		{"curate + comm", []string{"curate", "comm"}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := CheckScopeMix(c.scopes)
			if c.refuse && err == nil {
				t.Errorf("%v was allowed — a token that can both read working notes and write "+
					"knowledge is exactly the mixing this rule exists to prevent, and API tokens "+
					"have no expiry, so every already-copied instance would gain it retroactively",
					c.scopes)
			}
			if !c.refuse && err != nil {
				t.Errorf("%v was refused: %v", c.scopes, err)
			}
		})
	}
}

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
