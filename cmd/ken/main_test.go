package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Quest-ICT/ken/internal/clientip"
)

// TestMetricsAllowed locks the scrape gate: loopback and allowlisted CIDRs pass
// unconditionally; every other peer needs the exact bearer token; a spoofable
// X-Forwarded-For must never grant access; and behind a *declared* trusted proxy
// the gate authorizes on the real (validated) client IP, not the proxy's loopback.
func TestMetricsAllowed(t *testing.T) {
	allow := clientip.ParseCIDRs("10.0.0.0/8")
	noProxy := clientip.NewResolver("")               // XFF never trusted
	withProxy := clientip.NewResolver("127.0.0.1/32") // trusts a co-located loopback proxy
	req := func(remote, auth, xff string) *http.Request {
		r := httptest.NewRequest("GET", "/metrics", nil)
		r.RemoteAddr = remote
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		return r
	}

	cases := []struct {
		name     string
		r        *http.Request
		token    string
		resolver *clientip.Resolver
		expect   bool
	}{
		{"ipv4 loopback", req("127.0.0.1:5000", "", ""), "secret", noProxy, true},
		{"ipv6 loopback", req("[::1]:5000", "", ""), "secret", noProxy, true},
		{"allowlisted cidr", req("10.1.2.3:5000", "", ""), "secret", noProxy, true},
		{"remote no token", req("203.0.113.5:5000", "", ""), "secret", noProxy, false},
		{"remote correct token", req("203.0.113.5:5000", "Bearer secret", ""), "secret", noProxy, true},
		{"remote wrong token", req("203.0.113.5:5000", "Bearer nope", ""), "secret", noProxy, false},
		{"no token configured, remote denied", req("203.0.113.5:5000", "Bearer secret", ""), "", noProxy, false},
		{"spoofed XFF ignored without trusted proxy", req("203.0.113.5:5000", "", "127.0.0.1"), "secret", noProxy, false},
		// Behind a declared proxy: an external client is NOT treated as loopback.
		{"proxy forwards real client ip → denied", req("127.0.0.1:5000", "", "203.0.113.9"), "secret", withProxy, false},
		{"proxy's own loopback probe → allowed", req("127.0.0.1:5000", "", ""), "secret", withProxy, true},
	}
	for _, c := range cases {
		if got := metricsAllowed(c.r, c.token, allow, c.resolver); got != c.expect {
			t.Errorf("%s: metricsAllowed = %v, want %v", c.name, got, c.expect)
		}
	}
}
