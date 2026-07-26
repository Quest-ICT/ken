package web

import (
	"testing"
	"time"
)

func TestLoginGuardDecayAndReset(t *testing.T) {
	g := &loginGuard{fails: map[string]failInfo{}}
	for i := 0; i < loginMaxFails; i++ {
		g.fail("1.2.3.4")
	}
	if !g.blocked("1.2.3.4") {
		t.Fatal("should be blocked after max fails")
	}
	// Simulate the lockout window elapsing.
	g.mu.Lock()
	f := g.fails["1.2.3.4"]
	f.until = time.Now().Add(-time.Second)
	g.fails["1.2.3.4"] = f
	g.mu.Unlock()
	if g.blocked("1.2.3.4") {
		t.Fatal("should be unblocked once the window elapses")
	}
	// A further failure decays the counter to 1 (not 6), so the lockout is not
	// instantly re-armed.
	g.fail("1.2.3.4")
	g.mu.Lock()
	n := g.fails["1.2.3.4"].n
	g.mu.Unlock()
	if n != 1 {
		t.Fatalf("counter should decay to 1 after the window, got %d", n)
	}
	// reset removes the entry entirely.
	g.reset("1.2.3.4")
	g.mu.Lock()
	_, ok := g.fails["1.2.3.4"]
	g.mu.Unlock()
	if ok {
		t.Fatal("reset should remove the entry")
	}
}
