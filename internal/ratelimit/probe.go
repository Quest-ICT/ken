package ratelimit

import (
	"log"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Quest-ICT/ken/internal/clientip"
)

// ProbeGuard protects ONE unauthenticated endpoint — the mail-waiting check,
// POST /comm/waiting — and nothing else.
//
// WHY A SECOND GUARD AND NOT THE IPGuard. The IPGuard is Ken's outermost middleware and
// its block covers every surface: MCP, comm, files, the console. Every Claude Code
// session on a workstation calls from the same address, so if a lookup of a station that
// does not exist fed that block, one session with a stale station id in a hook would cut
// every session on the machine off Ken for the whole lockout, MCP included. This guard's
// block covers this endpoint only. For the same reason the endpoint does not draw on the
// IPGuard's shared per-IP budget — the budget the 2026-10-26 rejections came out of — so
// checking for mail can never spend the budget for reading it.
//
// TWO INDEPENDENT CONTROLS:
//
//   - a per-IP token bucket (429 + Retry-After), which bounds load, and
//   - a MISS counter: the handler calls Miss for every request that is not a well-formed
//     lookup of an existing station. MissLimit misses inside MissWindow block the address
//     from this endpoint for Lockout (403). Vlad, 2026-11-17: asking for stations that do
//     not exist is what gets an address blocked.
//
// A WINDOW, NOT A STREAK. The IPGuard counts consecutive rejections and resets on an
// allowed request. Here a hit does not reset the count: if it did, a prober holding one
// real id could interleave it to keep its miss count at zero forever.
//
// Allowlisted addresses (loopback and rl_allow_cidrs) bypass both controls, exactly as
// they bypass the IPGuard. State lives in memory and is lost when the guard is rebuilt
// (a rate-limit setting changed, or a restart): the same trade the IPGuard makes.
type ProbeGuard struct {
	resolver   *clientip.Resolver
	allow      []*net.IPNet
	bucket     *Bucket
	missLimit  int
	missWindow time.Duration
	lockout    time.Duration
	onReject   func()
	onBlock    func()
	now        func() time.Time // time.Now; replaced only by tests

	mu     sync.Mutex
	misses map[string]*missInfo
}

type missInfo struct {
	count       int
	windowStart time.Time
	until       time.Time // zero unless blocked
	seen        time.Time
}

// ProbeConfig configures a ProbeGuard.
type ProbeConfig struct {
	Enabled    bool
	PerMin     int
	Burst      int
	MissLimit  int           // misses inside MissWindow before a block; 0 = never block
	MissWindow time.Duration // the window misses are counted in
	Lockout    time.Duration // how long a block lasts
	Allow      []*net.IPNet  // extra allowlisted CIDRs (loopback is always allowed)
	OnReject   func()        // optional: a 429 was returned
	OnBlock    func()        // optional: a 403 was returned
}

// Verdict is what a ProbeGuard decided about one request.
type Verdict struct {
	// Status is 0 when the request may proceed, else the status to refuse it with
	// (http.StatusForbidden or http.StatusTooManyRequests).
	Status int
	// RetryAfter is set with a 429.
	RetryAfter time.Duration
	// Key identifies the caller for Miss. Empty for an allowlisted caller, whose misses
	// are not counted.
	Key string
}

// NewProbeGuard builds the guard, or nil when rate limiting is disabled. A nil guard
// admits everything and counts nothing.
func NewProbeGuard(c ProbeConfig, resolver *clientip.Resolver) *ProbeGuard {
	if !c.Enabled {
		return nil
	}
	return &ProbeGuard{
		resolver:   resolver,
		allow:      c.Allow,
		bucket:     NewBucket(c.PerMin, c.Burst),
		missLimit:  c.MissLimit,
		missWindow: c.MissWindow,
		lockout:    c.Lockout,
		onReject:   c.OnReject,
		onBlock:    c.OnBlock,
		now:        time.Now,
		misses:     map[string]*missInfo{},
	}
}

// Admit decides whether r may proceed. A blocked address is refused before it spends
// any of the bucket, so a blocked prober costs one map lookup per request.
func (g *ProbeGuard) Admit(r *http.Request) Verdict {
	if g == nil {
		return Verdict{}
	}
	ip := g.resolver.IP(r)
	if ipAllowed(ip, g.allow) {
		return Verdict{}
	}
	key := clientip.Key(ip)
	if g.blocked(key, g.now()) {
		if g.onBlock != nil {
			g.onBlock()
		}
		return Verdict{Status: http.StatusForbidden, Key: key}
	}
	if ok, retry := g.bucket.Allow(key); !ok {
		if g.onReject != nil {
			g.onReject()
		}
		return Verdict{Status: http.StatusTooManyRequests, RetryAfter: retry, Key: key}
	}
	return Verdict{Key: key}
}

// Miss records one request from key that was not a well-formed lookup of an existing
// station. Reaching MissLimit inside the window blocks key for Lockout and logs it once,
// so a stale id in one of our own hooks shows up in the log instead of failing silently.
// An empty key (allowlisted caller) and a nil guard are no-ops.
func (g *ProbeGuard) Miss(key string) {
	if g == nil || key == "" || g.missLimit <= 0 {
		return
	}
	now := g.now()
	g.mu.Lock()
	defer g.mu.Unlock()
	m := g.misses[key]
	if m == nil {
		if len(g.misses) >= maxKeys {
			g.sweepLocked(now)
		}
		if len(g.misses) >= maxKeys {
			return // still full of live entries after a sweep — bound memory
		}
		m = &missInfo{}
		g.misses[key] = m
	}
	if !m.until.IsZero() {
		if now.Before(m.until) {
			m.seen = now
			return // already blocked; Admit refuses before the handler runs
		}
		*m = missInfo{} // a lockout elapsed: start clean
	}
	if m.windowStart.IsZero() || now.Sub(m.windowStart) > g.missWindow {
		m.count, m.windowStart = 0, now
	}
	m.count++
	m.seen = now
	if m.count >= g.missLimit {
		m.until = now.Add(g.lockout)
		log.Printf("ratelimit: /comm/waiting blocked %s for %s after %d lookups that named no station (or were malformed) within %s",
			key, g.lockout, m.count, g.missWindow)
	}
}

func (g *ProbeGuard) blocked(key string, now time.Time) bool {
	if g.missLimit <= 0 {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	m := g.misses[key]
	return m != nil && !m.until.IsZero() && now.Before(m.until)
}

func (g *ProbeGuard) sweepLocked(now time.Time) {
	for key, m := range g.misses {
		blockedNow := !m.until.IsZero() && now.Before(m.until)
		if !blockedNow && now.Sub(m.seen) > g.missWindow {
			delete(g.misses, key)
		}
	}
}

// ReloadableProbeGuard lets the guard be swapped when settings change. A nil current
// guard admits everything.
type ReloadableProbeGuard struct{ p atomic.Pointer[ProbeGuard] }

// Store installs g (nil disables the guard).
func (rg *ReloadableProbeGuard) Store(g *ProbeGuard) { rg.p.Store(g) }

// Admit delegates to the current guard.
func (rg *ReloadableProbeGuard) Admit(r *http.Request) Verdict { return rg.p.Load().Admit(r) }

// Miss delegates to the current guard.
func (rg *ReloadableProbeGuard) Miss(key string) { rg.p.Load().Miss(key) }

// ipAllowed is the allowlist rule shared with the IPGuard: loopback, then the CIDRs.
func ipAllowed(ip string, allow []*net.IPNet) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	if parsed.IsLoopback() {
		return true
	}
	for _, n := range allow {
		if n.Contains(parsed) {
			return true
		}
	}
	return false
}
