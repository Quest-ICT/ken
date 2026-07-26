// Package clientip resolves the real client IP of an HTTP request. X-Forwarded-For
// is honored ONLY when the direct peer is a configured trusted proxy, so a client
// cannot spoof XFF to forge its address for the login guard or the rate limiter.
package clientip

import (
	"net"
	"net/http"
	"strings"
)

// Resolver derives a request's client IP against a set of trusted-proxy CIDRs.
type Resolver struct {
	trusted []*net.IPNet
}

// NewResolver builds a Resolver from a comma-separated CIDR list (env
// KEN_TRUSTED_PROXIES). An empty/blank list means "no proxy" — RemoteAddr is
// always used and XFF is ignored.
func NewResolver(cidrs string) *Resolver { return &Resolver{trusted: ParseCIDRs(cidrs)} }

// ParseCIDRs parses a comma-separated CIDR list, skipping blank/invalid entries.
func ParseCIDRs(s string) []*net.IPNet {
	var out []*net.IPNet
	for _, c := range strings.Split(s, ",") {
		if c = strings.TrimSpace(c); c == "" {
			continue
		}
		if _, n, err := net.ParseCIDR(c); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// IP returns the request's client IP. When the direct peer is a trusted proxy the
// rightmost X-Forwarded-For hop that is a valid IP and not itself a trusted proxy
// is used (across all XFF header lines); otherwise RemoteAddr's host is returned
// and XFF is ignored.
func (r *Resolver) IP(req *http.Request) string {
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		host = req.RemoteAddr
	}
	if len(r.trusted) == 0 || !r.contains(host) {
		return host
	}
	// Join every XFF line the proxy left (a proxy may append its own as a new line).
	parts := strings.Split(strings.Join(req.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(parts) - 1; i >= 0; i-- {
		if ip := normalizeIP(parts[i]); ip != "" && !r.contains(ip) {
			return ip
		}
	}
	return host
}

// TrustedPeer reports whether the request's direct peer (RemoteAddr) is one of the
// configured trusted proxies — i.e. whether this request's X-Forwarded-* headers may
// be believed. With no trusted proxies configured it is always false.
func (r *Resolver) TrustedPeer(req *http.Request) bool {
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		host = req.RemoteAddr
	}
	return len(r.trusted) > 0 && r.contains(host)
}

func (r *Resolver) contains(ip string) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	for _, n := range r.trusted {
		if n.Contains(parsed) {
			return true
		}
	}
	return false
}

// normalizeIP trims an XFF entry (optional port, brackets, spaces) and returns the
// canonical IP string, or "" if it is not a valid IP (e.g. a proxy "unknown" token).
func normalizeIP(s string) string {
	if s = strings.TrimSpace(s); s == "" {
		return ""
	}
	if h, _, err := net.SplitHostPort(s); err == nil {
		s = h
	}
	s = strings.Trim(s, "[]")
	if ip := net.ParseIP(s); ip != nil {
		return ip.String()
	}
	return ""
}

// Key normalizes an IP to a rate-limit / lockout key: an IPv4 address maps to
// itself (/32), an IPv6 address maps to its /64 network — so an attacker cannot
// rotate addresses within a single (typically /64) allocation to mint unbounded
// distinct keys and evade per-IP limits or the auto-block.
func Key(ip string) string {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return ip
	}
	if v4 := parsed.To4(); v4 != nil {
		return v4.String()
	}
	return parsed.Mask(net.CIDRMask(64, 128)).String() + "/64"
}
