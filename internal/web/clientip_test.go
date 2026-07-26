package web

import (
	"net/http"
	"testing"
)

func TestClientIPTrustedProxy(t *testing.T) {
	a := newApp(Deps{TrustedProxies: "10.0.0.0/8"})

	// Direct (untrusted) peer: X-Forwarded-For is ignored, RemoteAddr wins.
	r := &http.Request{RemoteAddr: "203.0.113.9:1234", Header: http.Header{}}
	r.Header.Set("X-Forwarded-For", "1.2.3.4")
	if got := a.clientIP(r); got != "203.0.113.9" {
		t.Fatalf("untrusted peer must use RemoteAddr, got %q", got)
	}

	// Trusted proxy peer: use the rightmost X-Forwarded-For hop that isn't itself a proxy.
	r2 := &http.Request{RemoteAddr: "10.1.2.3:443", Header: http.Header{}}
	r2.Header.Set("X-Forwarded-For", "198.51.100.7, 10.9.9.9")
	if got := a.clientIP(r2); got != "198.51.100.7" {
		t.Fatalf("trusted proxy must resolve the real client hop, got %q", got)
	}
}
