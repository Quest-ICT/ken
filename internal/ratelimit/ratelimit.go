// Package ratelimit is Ken's application-layer abuse defense: a per-IP token-bucket
// "first filter" (allowlist bypass, auto-block for repeat offenders, 429 +
// Retry-After otherwise) plus a reusable per-key bucket the MCP layer uses for
// per-token limits. This is app-layer abuse control — NOT volumetric L3/4 DDoS
// mitigation, which belongs upstream at the proxy / kernel / edge.
package ratelimit

import (
	"log"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Quest-ICT/ken/internal/clientip"
)

const maxKeys = 8192 // per-map cap; idle keys are reclaimed by a sweep at this size

// Bucket is a token-bucket rate limiter keyed by an arbitrary string (an IP key or
// a token id). It refills at rate tokens/sec up to burst. Safe for concurrent use;
// a nil *Bucket allows everything (so callers can hold an optional limiter).
type Bucket struct {
	mu    sync.Mutex
	rate  float64 // tokens per second
	burst float64
	keys  map[string]*bkt
}

type bkt struct {
	tokens float64
	last   time.Time
	seen   time.Time
}

// NewBucket builds a limiter allowing ~perMinute requests/minute with the given
// burst capacity. A non-positive perMinute or burst disables it (Allow always ok).
func NewBucket(perMinute, burst int) *Bucket {
	return &Bucket{
		rate:  float64(perMinute) / 60,
		burst: float64(burst),
		keys:  map[string]*bkt{},
	}
}

// Allow consumes one token for key: (true, 0) when allowed, or (false, retryAfter)
// with the time until the next token is available (always > 0 when denied).
func (b *Bucket) Allow(key string) (bool, time.Duration) {
	if b == nil || b.rate <= 0 || b.burst <= 0 {
		return true, 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	e := b.keys[key]
	if e == nil {
		if len(b.keys) >= maxKeys {
			b.sweepLocked(now) // reclaim idle keys before deciding to fail open
		}
		if len(b.keys) >= maxKeys {
			return true, 0 // still full after sweep — fail open rather than lock out a fresh key
		}
		e = &bkt{tokens: b.burst, last: now}
		b.keys[key] = e
	}
	e.tokens += now.Sub(e.last).Seconds() * b.rate
	if e.tokens > b.burst {
		e.tokens = b.burst
	}
	e.last, e.seen = now, now
	if e.tokens >= 1 {
		e.tokens--
		return true, 0
	}
	retry := time.Duration((1 - e.tokens) / b.rate * float64(time.Second))
	if retry <= 0 {
		retry = time.Second
	}
	return false, retry
}

func (b *Bucket) sweepLocked(now time.Time) {
	idle := time.Duration(b.burst/b.rate*float64(time.Second)) + time.Minute // full-refill time + slack
	for k, e := range b.keys {
		if now.Sub(e.seen) > idle {
			delete(b.keys, k)
		}
	}
}

// Config is the resolved rate-limit configuration.
type Config struct {
	Enabled     bool
	IPPerMin    int
	IPBurst     int
	TokenPerMin int
	TokenBurst  int
	BlockAfter  int           // consecutive over-limit rejections (since the last allowed request) before an IP is auto-blocked
	Lockout     time.Duration // how long an auto-block lasts
	Allow       []*net.IPNet  // extra allowlisted CIDRs (loopback is always allowed)

	// OnReject/OnBlock are optional observability hooks fired when the guard
	// throttles (429) or refuses an auto-blocked IP (403). Kept as callbacks so
	// this package stays decoupled from the metrics package.
	OnReject func()
	OnBlock  func()
}

// IPGuard is the outermost middleware: it resolves the client IP, bypasses the
// allowlist (loopback + configured CIDRs) and the /healthz probe, rejects an
// auto-blocked IP with 403, throttles the rest with a per-IP token bucket
// (429 + Retry-After) and auto-blocks repeat offenders. IP keys are normalized by
// network (IPv6 -> /64) so address rotation cannot evade the limit.
type IPGuard struct {
	resolver   *clientip.Resolver
	allow      []*net.IPNet
	bucket     *Bucket
	blockAfter int
	lockout    time.Duration

	onReject func()
	onBlock  func()

	mu     sync.Mutex
	strike map[string]*strikeInfo
}

type strikeInfo struct {
	denied int
	until  time.Time
	seen   time.Time
}

// NewIPGuard builds the per-IP guard, or nil when rate limiting is disabled.
func NewIPGuard(c Config, resolver *clientip.Resolver) *IPGuard {
	if !c.Enabled {
		return nil
	}
	return &IPGuard{
		resolver:   resolver,
		allow:      c.Allow,
		bucket:     NewBucket(c.IPPerMin, c.IPBurst),
		blockAfter: c.BlockAfter,
		lockout:    c.Lockout,
		onReject:   c.OnReject,
		onBlock:    c.OnBlock,
		strike:     map[string]*strikeInfo{},
	}
}

// Wrap installs the guard in front of next. A nil *IPGuard returns next unchanged.
func (g *IPGuard) Wrap(next http.Handler) http.Handler {
	if g == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { g.serve(w, r, next) })
}

// serve is the per-request guard logic (shared by Wrap and ReloadableGuard).
func (g *IPGuard) serve(w http.ResponseWriter, r *http.Request, next http.Handler) {
	if r.URL.Path == "/healthz" { // never throttle the trivial liveness probe
		next.ServeHTTP(w, r)
		return
	}
	// NOTE: /health (a real readiness check: DB ping + data-dir write) and /metrics
	// are deliberately NOT exempt — they must be throttleable. Loopback + allow-CIDR
	// callers (a local Prometheus/probe) still bypass the guard via the allowlist.
	ip := g.resolver.IP(r)
	if g.allowed(ip) {
		next.ServeHTTP(w, r)
		return
	}
	key := clientip.Key(ip) // IPv6 -> /64 so address rotation can't evade
	if g.blocked(key) {
		if g.onBlock != nil {
			g.onBlock()
		}
		reject(w, http.StatusForbidden, 0)
		return
	}
	if ok, retry := g.bucket.Allow(key); !ok {
		g.strikeIP(key)
		if g.onReject != nil {
			g.onReject()
		}
		reject(w, http.StatusTooManyRequests, retry)
		return
	}
	g.clearStrike(key) // an allowed request resets the consecutive-denial streak
	next.ServeHTTP(w, r)
}

// ReloadableGuard is a per-IP guard that can be swapped live (when settings change)
// behind a stable http.Handler. A nil current guard passes through.
type ReloadableGuard struct{ p atomic.Pointer[IPGuard] }

// Store swaps in a new guard (may be nil to disable).
func (rg *ReloadableGuard) Store(g *IPGuard) { rg.p.Store(g) }

// Wrap returns a stable handler that dispatches to the current guard each request.
func (rg *ReloadableGuard) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if g := rg.p.Load(); g != nil {
			g.serve(w, r, next)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Limiter is the per-token limit the MCP layer applies (satisfied by *Bucket and
// *ReloadableBucket).
type Limiter interface {
	Allow(key string) (bool, time.Duration)
}

// ReloadableBucket wraps a token Bucket that can be swapped live.
type ReloadableBucket struct{ p atomic.Pointer[Bucket] }

// Store swaps in a new bucket (may be nil to disable — Allow then passes through).
func (rb *ReloadableBucket) Store(b *Bucket) { rb.p.Store(b) }

// Allow delegates to the current bucket (a nil bucket is nil-safe -> allowed).
func (rb *ReloadableBucket) Allow(key string) (bool, time.Duration) { return rb.p.Load().Allow(key) }

func (g *IPGuard) allowed(ip string) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	if parsed.IsLoopback() {
		return true
	}
	for _, n := range g.allow {
		if n.Contains(parsed) {
			return true
		}
	}
	return false
}

func (g *IPGuard) blocked(key string) bool {
	if g.blockAfter <= 0 {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	s := g.strike[key]
	return s != nil && s.denied >= g.blockAfter && time.Now().Before(s.until)
}

func (g *IPGuard) strikeIP(key string) {
	if g.blockAfter <= 0 {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	s := g.strike[key]
	if s == nil {
		if len(g.strike) >= maxKeys {
			g.sweepLocked(now)
		}
		if len(g.strike) >= maxKeys {
			return // still full of active offenders after a sweep — bound memory
		}
		s = &strikeInfo{}
		g.strike[key] = s
	}
	if !s.until.IsZero() && now.After(s.until) {
		*s = strikeInfo{} // a prior lockout elapsed -> fresh streak
	}
	s.denied++
	s.seen = now
	if s.denied == g.blockAfter {
		s.until = now.Add(g.lockout)
		log.Printf("ratelimit: auto-blocked %s for %s after %d over-limit rejections", key, g.lockout, s.denied)
	}
}

// clearStrike drops an IP's denial streak after an allowed request (reached only
// when the IP is not currently blocked), so the auto-block counts *consecutive*
// over-limit rejections and a busy shared/CGNAT IP is not blocked for occasional
// bursts.
func (g *IPGuard) clearStrike(key string) {
	if g.blockAfter <= 0 {
		return
	}
	g.mu.Lock()
	delete(g.strike, key)
	g.mu.Unlock()
}

func (g *IPGuard) sweepLocked(now time.Time) {
	for key, s := range g.strike {
		if (!s.until.IsZero() && now.After(s.until)) || now.Sub(s.seen) > g.lockout {
			delete(g.strike, key)
		}
	}
}

// reject writes a tiny rejection and asks the client to close the connection so a
// flood is shed cheaply.
func reject(w http.ResponseWriter, status int, retry time.Duration) {
	if retry > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
	}
	w.Header().Set("Connection", "close")
	http.Error(w, http.StatusText(status), status)
}
