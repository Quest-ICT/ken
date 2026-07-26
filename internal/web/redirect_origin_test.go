package web

import "testing"

// TestRedirectURIOriginRejectsCSPInjection locks the fix for the consent-page CSP:
// url.Parse leaves ';' ',' quotes '*' … in u.Host, so redirectURIOrigin must reject
// any host carrying CSP-significant characters before it is spliced into form-action.
func TestRedirectURIOriginRejectsCSPInjection(t *testing.T) {
	cases := map[string]string{
		"https://claude.ai/callback":    "https://claude.ai",
		"https://claude.ai:8443/cb":     "https://claude.ai:8443",
		"http://127.0.0.1:1455/cb":      "http://127.0.0.1:1455",
		"https://evil.com;default-src*": "", // CSP metachars -> refuse to widen form-action
		"https://evil.com,script-src*":  "",
		"https://ev'il.com/cb":          "",
		"ftp://host/x":                  "",
		"/relative":                     "",
		"":                              "",
	}
	for raw, want := range cases {
		if got := redirectURIOrigin(raw); got != want {
			t.Errorf("redirectURIOrigin(%q) = %q, want %q", raw, got, want)
		}
	}
}
