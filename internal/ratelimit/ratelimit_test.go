package ratelimit

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Quest-ICT/ken/internal/clientip"
)

func TestBucketBurstAndRefill(t *testing.T) {
	b := NewBucket(60, 3) // 1 token/sec, burst 3
	for i := 0; i < 3; i++ {
		if ok, _ := b.Allow("k"); !ok {
			t.Fatalf("request %d within burst should be allowed", i)
		}
	}
	if ok, retry := b.Allow("k"); ok || retry <= 0 {
		t.Fatalf("4th request should be denied with a retry-after; got ok=%v retry=%v", ok, retry)
	}
	if ok, _ := b.Allow("other"); !ok {
		t.Fatal("an independent key must have its own fresh burst")
	}
}

func TestBucketDisabled(t *testing.T) {
	var nilB *Bucket
	if ok, _ := nilB.Allow("k"); !ok {
		t.Fatal("a nil bucket must allow everything")
	}
	if ok, _ := NewBucket(0, 0).Allow("k"); !ok {
		t.Fatal("a zero-config bucket must allow everything")
	}
}

func req(remoteAddr, path string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "http://ken"+path, nil)
	r.RemoteAddr = remoteAddr
	return r
}

func TestIPGuardBypasses(t *testing.T) {
	cfg := Config{
		Enabled: true, IPPerMin: 60, IPBurst: 1, BlockAfter: 3, Lockout: time.Minute,
		Allow: clientip.ParseCIDRs("10.0.0.0/8"),
	}
	g := NewIPGuard(cfg, clientip.NewResolver(""))
	h := g.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))

	// loopback is exempt even far beyond the burst
	for i := 0; i < 5; i++ {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req("127.0.0.1:1", "/x"))
		if rr.Code != 200 {
			t.Fatalf("loopback must bypass, got %d", rr.Code)
		}
	}
	// allowlisted CIDR is exempt
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req("10.1.2.3:1", "/x"))
	if rr.Code != 200 {
		t.Fatalf("allowlisted CIDR must bypass, got %d", rr.Code)
	}
	// the /healthz probe is exempt for any IP
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req("203.0.113.5:1", "/healthz"))
	if rr.Code != 200 {
		t.Fatalf("/healthz must bypass, got %d", rr.Code)
	}
}

func TestIPGuardLimitAndAutoBlock(t *testing.T) {
	cfg := Config{Enabled: true, IPPerMin: 60, IPBurst: 1, BlockAfter: 3, Lockout: time.Minute}
	g := NewIPGuard(cfg, clientip.NewResolver(""))
	h := g.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	const ip = "203.0.113.9:5"

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req(ip, "/x"))
	if rr.Code != 200 {
		t.Fatalf("first request (within burst) should pass, got %d", rr.Code)
	}

	got429, got403 := 0, 0
	for i := 0; i < 5; i++ { // burst exhausted; refill is ~1/s so these fail fast
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req(ip, "/x"))
		switch rr.Code {
		case http.StatusTooManyRequests:
			got429++
			if rr.Header().Get("Retry-After") == "" {
				t.Fatal("a 429 must carry Retry-After")
			}
		case http.StatusForbidden:
			got403++
		default:
			t.Fatalf("unexpected status %d", rr.Code)
		}
	}
	if got429 == 0 {
		t.Fatal("expected some 429s once the burst is spent")
	}
	if got403 == 0 {
		t.Fatal("expected an auto-block (403) after BlockAfter over-limit rejections")
	}
}

func TestIPGuardDisabledIsPassthrough(t *testing.T) {
	g := NewIPGuard(Config{Enabled: false}, clientip.NewResolver(""))
	if g != nil {
		t.Fatal("disabled config must yield a nil guard")
	}
	// nil guard's Wrap returns the handler unchanged and never limits
	h := g.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	for i := 0; i < 100; i++ {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req("203.0.113.1:1", "/x"))
		if rr.Code != 200 {
			t.Fatalf("disabled guard must never limit, got %d", rr.Code)
		}
	}
}

func TestClearStrikeResetsStreak(t *testing.T) {
	g := NewIPGuard(Config{Enabled: true, IPPerMin: 60, IPBurst: 1, BlockAfter: 3, Lockout: time.Minute}, nil)
	const k = "203.0.113.1"
	g.strikeIP(k)
	g.strikeIP(k) // denied=2, below BlockAfter
	if g.blocked(k) {
		t.Fatal("must not be blocked at 2 < 3 strikes")
	}
	g.clearStrike(k) // an allowed request resets the consecutive streak
	g.strikeIP(k)
	g.strikeIP(k) // 2 fresh strikes after reset
	if g.blocked(k) {
		t.Fatal("clearStrike must reset the streak — 2 post-reset strikes should not block")
	}
	g.strikeIP(k) // 3rd consecutive -> block
	if !g.blocked(k) {
		t.Fatal("3 consecutive strikes should auto-block")
	}
}

func TestBucketSweepReclaims(t *testing.T) {
	b := NewBucket(60, 1)
	b.keys["stale"] = &bkt{seen: time.Now().Add(-2 * time.Hour)}
	b.keys["fresh"] = &bkt{seen: time.Now()}
	b.sweepLocked(time.Now())
	if _, ok := b.keys["stale"]; ok {
		t.Fatal("an idle key must be swept")
	}
	if _, ok := b.keys["fresh"]; !ok {
		t.Fatal("a recently-seen key must remain")
	}
}

func TestGuardSweepReclaims(t *testing.T) {
	g := NewIPGuard(Config{Enabled: true, IPPerMin: 60, IPBurst: 1, BlockAfter: 3, Lockout: time.Minute}, nil)
	g.strike["old"] = &strikeInfo{seen: time.Now().Add(-2 * time.Hour)}
	g.strike["new"] = &strikeInfo{seen: time.Now()}
	g.sweepLocked(time.Now())
	if _, ok := g.strike["old"]; ok {
		t.Fatal("an idle strike entry must be swept")
	}
	if _, ok := g.strike["new"]; !ok {
		t.Fatal("a recent strike entry must remain")
	}
}
