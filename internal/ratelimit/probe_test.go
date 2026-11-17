package ratelimit

import (
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/Quest-ICT/ken/internal/clientip"
)

// fakeClock is a settable clock for the miss window and the lockout.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func probeGuard(t *testing.T, c ProbeConfig) (*ProbeGuard, *fakeClock) {
	t.Helper()
	c.Enabled = true
	if c.PerMin == 0 {
		c.PerMin, c.Burst = 1_000_000, 1_000_000 // the bucket is not under test unless asked
	}
	g := NewProbeGuard(c, clientip.NewResolver(""))
	clk := &fakeClock{t: time.Date(2026, 11, 17, 17, 0, 0, 0, time.UTC)}
	g.now = clk.now
	return g, clk
}

func probeReq(remoteAddr string) *http.Request {
	r, _ := http.NewRequest(http.MethodPost, "http://x/comm/waiting", nil)
	r.RemoteAddr = remoteAddr
	return r
}

// miss admits one request from addr and records it as a miss, as the handler does.
func miss(t *testing.T, g *ProbeGuard, addr string) {
	t.Helper()
	v := g.Admit(probeReq(addr))
	if v.Status != 0 {
		t.Fatalf("miss from %s was refused with %d before it could be counted", addr, v.Status)
	}
	g.Miss(v.Key)
}

func TestProbeGuardBlocksAtTheMissLimitAndOnlyThatAddress(t *testing.T) {
	g, _ := probeGuard(t, ProbeConfig{MissLimit: 3, MissWindow: 10 * time.Minute, Lockout: 15 * time.Minute})
	const a, b = "203.0.113.5:1", "203.0.113.9:1"
	miss(t, g, a)
	miss(t, g, a)
	if v := g.Admit(probeReq(a)); v.Status != 0 {
		t.Fatalf("2 misses of a limit of 3 must not block, got %d", v.Status)
	}
	miss(t, g, a) // the third
	if v := g.Admit(probeReq(a)); v.Status != http.StatusForbidden {
		t.Fatalf("3 misses of a limit of 3 must block with 403, got %d", v.Status)
	}
	if v := g.Admit(probeReq(b)); v.Status != 0 {
		t.Fatalf("another address must be unaffected by a's block, got %d", v.Status)
	}
}

// A WINDOW, NOT A STREAK: a hit in between must not reset the count. If it did, a prober
// holding one real id could interleave it and never be blocked.
func TestProbeGuardHitsDoNotResetTheMissCount(t *testing.T) {
	g, _ := probeGuard(t, ProbeConfig{MissLimit: 3, MissWindow: 10 * time.Minute, Lockout: 15 * time.Minute})
	const a = "203.0.113.5:1"
	for i := 0; i < 3; i++ {
		miss(t, g, a)
		if i < 2 {
			if v := g.Admit(probeReq(a)); v.Status != 0 { // a hit: admitted, no Miss
				t.Fatalf("hit %d refused with %d", i, v.Status)
			}
		}
	}
	if v := g.Admit(probeReq(a)); v.Status != http.StatusForbidden {
		t.Fatalf("misses interleaved with hits must still block, got %d", v.Status)
	}
}

func TestProbeGuardMissesOutsideTheWindowStartOver(t *testing.T) {
	g, clk := probeGuard(t, ProbeConfig{MissLimit: 3, MissWindow: 10 * time.Minute, Lockout: 15 * time.Minute})
	const a = "203.0.113.5:1"
	miss(t, g, a)
	miss(t, g, a)
	clk.advance(10*time.Minute + time.Second)
	miss(t, g, a) // a new window: this is miss 1, not 3
	if v := g.Admit(probeReq(a)); v.Status != 0 {
		t.Fatalf("misses from an elapsed window must not count, got %d", v.Status)
	}
	miss(t, g, a)
	miss(t, g, a)
	if v := g.Admit(probeReq(a)); v.Status != http.StatusForbidden {
		t.Fatalf("3 misses inside the new window must block, got %d", v.Status)
	}
}

func TestProbeGuardLockoutEndsAndStartsClean(t *testing.T) {
	g, clk := probeGuard(t, ProbeConfig{MissLimit: 2, MissWindow: 10 * time.Minute, Lockout: 15 * time.Minute})
	const a = "203.0.113.5:1"
	miss(t, g, a)
	miss(t, g, a)
	clk.advance(15*time.Minute - time.Second)
	if v := g.Admit(probeReq(a)); v.Status != http.StatusForbidden {
		t.Fatalf("still inside the lockout, got %d", v.Status)
	}
	clk.advance(2 * time.Second)
	if v := g.Admit(probeReq(a)); v.Status != 0 {
		t.Fatalf("the lockout has elapsed, got %d", v.Status)
	}
	miss(t, g, a) // one miss after a lockout is one miss, not the limit again
	if v := g.Admit(probeReq(a)); v.Status != 0 {
		t.Fatalf("a single miss after a lockout must not re-block, got %d", v.Status)
	}
}

func TestProbeGuardZeroMissLimitNeverBlocks(t *testing.T) {
	g, _ := probeGuard(t, ProbeConfig{MissLimit: 0, MissWindow: time.Minute, Lockout: time.Hour})
	for i := 0; i < 50; i++ {
		miss(t, g, "203.0.113.5:1")
	}
	if v := g.Admit(probeReq("203.0.113.5:1")); v.Status != 0 {
		t.Fatalf("miss limit 0 means never block, got %d", v.Status)
	}
}

func TestProbeGuardAllowlistBypassesBothControls(t *testing.T) {
	_, allowed, _ := net.ParseCIDR("198.51.100.0/24")
	g, _ := probeGuard(t, ProbeConfig{PerMin: 1, Burst: 1, MissLimit: 1, MissWindow: time.Minute, Lockout: time.Hour,
		Allow: []*net.IPNet{allowed}})
	for _, addr := range []string{"127.0.0.1:1", "198.51.100.7:1"} {
		for i := 0; i < 5; i++ {
			v := g.Admit(probeReq(addr))
			if v.Status != 0 {
				t.Fatalf("%s is allowlisted, request %d refused with %d", addr, i, v.Status)
			}
			if v.Key != "" {
				t.Fatalf("%s is allowlisted: its misses must not be countable, got key %q", addr, v.Key)
			}
			g.Miss(v.Key)
		}
	}
}

func TestProbeGuardBucketThrottlesWith429(t *testing.T) {
	g, _ := probeGuard(t, ProbeConfig{PerMin: 60, Burst: 2, MissLimit: 10, MissWindow: time.Minute, Lockout: time.Hour})
	const a = "203.0.113.5:1"
	for i := 0; i < 2; i++ {
		if v := g.Admit(probeReq(a)); v.Status != 0 {
			t.Fatalf("request %d inside the burst refused with %d", i, v.Status)
		}
	}
	v := g.Admit(probeReq(a))
	if v.Status != http.StatusTooManyRequests || v.RetryAfter <= 0 {
		t.Fatalf("over the burst must be 429 with a Retry-After, got %d / %s", v.Status, v.RetryAfter)
	}
	// Throttling is not a miss: a busy machine must never be BLOCKED for being busy.
	if g.blocked(v.Key, g.now()) {
		t.Fatal("a 429 must not count toward the miss block")
	}
}

func TestProbeGuardNilAdmitsEverything(t *testing.T) {
	var g *ProbeGuard
	if v := g.Admit(probeReq("203.0.113.5:1")); v.Status != 0 {
		t.Fatalf("a nil guard (rate limiting off) must admit, got %d", v.Status)
	}
	g.Miss("anything") // must not panic
	var rg ReloadableProbeGuard
	if v := rg.Admit(probeReq("203.0.113.5:1")); v.Status != 0 {
		t.Fatalf("an empty reloadable guard must admit, got %d", v.Status)
	}
	rg.Miss("anything")
	if NewProbeGuard(ProbeConfig{Enabled: false}, clientip.NewResolver("")) != nil {
		t.Fatal("a disabled config must build a nil guard")
	}
}
