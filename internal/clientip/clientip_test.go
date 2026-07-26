package clientip

import (
	"net/http"
	"testing"
)

func TestResolverTrustedProxy(t *testing.T) {
	r := NewResolver("10.0.0.0/8")

	// Untrusted direct peer: X-Forwarded-For is ignored, RemoteAddr wins.
	req := &http.Request{RemoteAddr: "203.0.113.9:1234", Header: http.Header{}}
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	if got := r.IP(req); got != "203.0.113.9" {
		t.Fatalf("untrusted peer must use RemoteAddr, got %q", got)
	}

	// Trusted proxy peer: rightmost XFF hop that is not itself a trusted proxy.
	req2 := &http.Request{RemoteAddr: "10.1.2.3:443", Header: http.Header{}}
	req2.Header.Set("X-Forwarded-For", "198.51.100.7, 10.9.9.9")
	if got := r.IP(req2); got != "198.51.100.7" {
		t.Fatalf("trusted proxy must resolve the real client hop, got %q", got)
	}

	// No proxies configured: XFF is never consulted.
	if got := NewResolver("").IP(req2); got != "10.1.2.3" {
		t.Fatalf("with no trusted proxies, RemoteAddr must win, got %q", got)
	}
}

func TestKeyNormalizesIPv6(t *testing.T) {
	if got := Key("1.2.3.4"); got != "1.2.3.4" {
		t.Fatalf("IPv4 must be keyed unchanged, got %q", got)
	}
	a := Key("2001:db8:1:2:3:4:5:6")
	b := Key("2001:db8:1:2:ffff:ffff:ffff:ffff")
	if a != b {
		t.Fatalf("two addresses in the same /64 must share a key: %q vs %q", a, b)
	}
	if Key("2001:db8:1:3::1") == a {
		t.Fatal("addresses in different /64s must not share a key")
	}
	if got := Key("not-an-ip"); got != "not-an-ip" {
		t.Fatalf("a non-IP must pass through unchanged, got %q", got)
	}
}

func TestResolverXFFEdgeCases(t *testing.T) {
	r := NewResolver("10.0.0.0/8")

	// Multiple X-Forwarded-For header lines are all considered.
	req := &http.Request{RemoteAddr: "10.0.0.1:1", Header: http.Header{}}
	req.Header.Add("X-Forwarded-For", "203.0.113.7")
	req.Header.Add("X-Forwarded-For", "10.9.9.9")
	if got := r.IP(req); got != "203.0.113.7" {
		t.Fatalf("multi-line XFF must resolve the real client, got %q", got)
	}

	// A non-IP trailing token is skipped, not returned.
	req2 := &http.Request{RemoteAddr: "10.0.0.1:1", Header: http.Header{}}
	req2.Header.Set("X-Forwarded-For", "203.0.113.8, unknown")
	if got := r.IP(req2); got != "203.0.113.8" {
		t.Fatalf("an invalid XFF token must be skipped, got %q", got)
	}
}
