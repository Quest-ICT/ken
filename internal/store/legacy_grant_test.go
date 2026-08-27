package store

import "testing"

// *** THE BADGE AND THE AUTHENTICATOR MUST AGREE, OR THE BADGE IS WORSE THAN NOTHING. ***
//
// This exists because of a real detour on the live estate: Vlad removed three connectors, re-added
// one, and saw only kb_* tools. Deleting a connector revokes nothing, so his reconnect silently
// reused a grant from 2026-08-11 with no ken: scopes — he was KB-only BY GRANT while debugging it
// as a URL mistake. The tool list was the symptom, the grant was the cause, and nothing connected
// them. `Legacy` on the console row is that connection, so it has to be right: an operator with a
// WRONG answer is worse off than one with none.
func TestLegacyMatchesWhatTheAuthenticatorActuallyGrants(t *testing.T) {
	for _, c := range []struct {
		name, scope string
		wantLegacy  bool
	}{
		{"the grant that caused the detour", "read write offline_access", true},
		{"empty scope", "", true},
		{"a current full grant", "read write offline_access ken:kb ken:comm ken:station", false},
		// NARROWED IS NOT LEGACY, and this is the case a naive "grants exactly the knowledge base"
		// check gets wrong: it produces the same three capabilities as a legacy grant, but a human
		// chose it and re-approving would not widen it. Telling them to revoke would be advice to
		// undo their own decision.
		{"a grant narrowed to the knowledge base", "read ken:kb", false},
		{"station only", "read ken:station", false},
		{"an unrelated scope alongside nothing ken", "read write offline_access openid", true},
	} {
		if got := IsLegacyGrant(c.scope); got != c.wantLegacy {
			t.Errorf("%s: IsLegacyGrant(%q) = %v, want %v", c.name, c.scope, got, c.wantLegacy)
		}
		// THE TIE TO THE AUTHENTICATOR. A legacy grant must get exactly the pre-scope capability
		// set; anything else means the two have drifted and the badge is describing a different
		// server than the one answering calls.
		caps := GrantedCapabilities(c.scope)
		if c.wantLegacy {
			if len(caps) != 3 || caps[0] != "read" || caps[1] != "write-draft" || caps[2] != "propose" {
				t.Errorf("%s: a legacy grant yields %v, want exactly the pre-scope set", c.name, caps)
			}
		}
	}
}

// A REDIRECT THAT WILL NOT PARSE COMES BACK EMPTY, NOT RAW. This column is the one field on the
// connector row that carries trust — the application name is self-reported, and ken-prod-ops found
// a client called `ken-identity-verification` on the live estate that Ken ships nowhere, plausible
// only because its redirect was loopback. Printing an unparseable string here would invite reading
// it as a host, which is the exact misreading the column exists to prevent.
func TestRedirectHostIsAHostOrNothing(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"http://127.0.0.1:9876/callback", "127.0.0.1:9876"},
		{"https://claude.ai/api/mcp/auth_callback", "claude.ai"},
		{"", ""},
		{"not a url at all", ""},
		{"urn:ietf:wg:oauth:2.0:oob", ""},
		{"://%%%", ""},
	} {
		if got := redirectHostOf(c.in); got != c.want {
			t.Errorf("redirectHostOf(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
