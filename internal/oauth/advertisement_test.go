package oauth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Quest-ICT/ken/internal/store"
)

// *** WHAT IS IMPLEMENTED MUST BE ADVERTISED, OR IT DOES NOT EXIST. ***
//
// ken-prod-ops measured this against the live deployment after §10 step 2 shipped:
//
//	POST /station/mcp                                  401, NO www-authenticate at all
//	/.well-known/oauth-protected-resource/station/mcp  404
//	scopes_supported                                   ["read","write","offline_access"]
//
// Three walls, each sufficient alone, between a correct client and a workspace. And the third
// survives fixing the other two: with no ken: scope advertised, a client that asks for exactly
// what the metadata offers lands in the legacy branch BY CONSTRUCTION and is refused, correctly,
// at the end of a flow that could never have produced anything else. Their estate proved it — 8
// grants, every one `read write offline_access`, because nothing ever offered more.
//
// **The capability was implemented, tested, and unreachable.** Same family as the instruction
// block nobody received and the bulk re-point with no button, but worse: there is no button to add
// and no text to shorten. The advertisement IS the interface.
func TestTheKenScopesAreAdvertised(t *testing.T) {
	for _, want := range []string{store.ScopeKB, store.ScopeCommSet, store.ScopeStation} {
		found := false
		for _, got := range scopesSupported {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Errorf("scopes_supported does not offer %q, so no client can ask for it and every grant "+
				"lands in the legacy branch — knowledge-base-only, correctly, forever", want)
		}
	}
	// The OAuth-level tokens stay: clients send them, and offline_access is what makes a client
	// request a refresh token.
	for _, want := range []string{"read", "write", "offline_access"} {
		found := false
		for _, got := range scopesSupported {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Errorf("scopes_supported dropped %q, which clients send", want)
		}
	}
}

// EVERY MCP SURFACE DESCRIBES ITSELF, not always /mcp.
//
// RFC 9728 puts the resource's path after the well-known prefix. Ken serves three surfaces and
// this document answered for one, so a client following the spec to the metadata for the surface
// it wanted got a 404 — or, worse, a 200 describing a different resource.
func TestProtectedResourceMetadataNamesTheSurfaceAskedFor(t *testing.T) {
	s := New(nil, func(*http.Request) string { return "https://kb.example" }, Config{})
	for _, tc := range []struct{ path, want string }{
		{"/.well-known/oauth-protected-resource", "https://kb.example/mcp"},
		{"/.well-known/oauth-protected-resource/mcp", "https://kb.example/mcp"},
		{"/.well-known/oauth-protected-resource/comm/mcp", "https://kb.example/comm/mcp"},
		{"/.well-known/oauth-protected-resource/station/mcp", "https://kb.example/station/mcp"},
	} {
		rec := httptest.NewRecorder()
		s.HandlePRMetadata(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		var doc map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
			t.Fatalf("%s: %v", tc.path, err)
		}
		if doc["resource"] != tc.want {
			t.Errorf("%s describes resource %v, want %s — a client is told the metadata for the "+
				"surface it asked about belongs to a different one", tc.path, doc["resource"], tc.want)
		}
		scopes, _ := json.Marshal(doc["scopes_supported"])
		if !strings.Contains(string(scopes), store.ScopeStation) {
			t.Errorf("%s does not advertise %q", tc.path, store.ScopeStation)
		}
	}
}

// AND THE CHALLENGE POINTS AT THE RIGHT DOCUMENT.
//
// The 401 is the only thing that starts discovery. Worth recording why this cannot be left to the
// client to work out — the session on the far side told Vlad exactly what it could see: "a
// 401-without-WWW-Authenticate is indistinguishable from a 401 with one: both render as the same
// 'needs authorization' notice. The diagnostic detail that would let someone fix the server is not
// propagated to me at all."
func TestEachSurfaceChallengesWithItsOwnMetadata(t *testing.T) {
	s := New(nil, func(*http.Request) string { return "https://kb.example" }, Config{})
	for _, surface := range []string{"/comm/mcp", "/station/mcp"} {
		got := s.ResourceMetadataURLFor(surface)(httptest.NewRequest(http.MethodPost, surface, nil))
		want := "https://kb.example/.well-known/oauth-protected-resource" + surface
		if got != want {
			t.Errorf("%s challenges with %q, want %q", surface, got, want)
		}
	}
}
