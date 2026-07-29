package commserver

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Quest-ICT/ken/internal/store"
)

func newKB(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "ken.db"))
	if err != nil {
		t.Fatalf("open kb: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(); err != nil {
		t.Fatalf("migrate kb: %v", err)
	}
	return st
}

// mintToken issues an API token with the given scopes and returns it.
func mintToken(t *testing.T, st *store.Store, actor string, scopes ...string) string {
	t.Helper()
	ctx := context.Background()
	id, err := st.FindOrCreateActor(ctx, "ai", actor)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := st.IssueToken(ctx, id, scopes, "test")
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// The COMM endpoint accepts exactly one token shape: a ken_ API token carrying
// the comm scope. This is the security property that made duplicating the
// knowledge base's authentication worthwhile rather than sharing it.
func TestAuthRequiresACommScopedAPIToken(t *testing.T) {
	ctx := context.Background()
	st := newKB(t)

	commTok := mintToken(t, st, "comm-agent", "comm")
	kbTok := mintToken(t, st, "kb-agent", "read", "write-draft", "propose")

	p, err := authenticate(ctx, st, commTok, ScopeComm)
	if err != nil {
		t.Fatalf("a comm-scoped token must authenticate: %v", err)
	}
	if p.TokenID == "" || p.ActorID == 0 || p.SpaceID == 0 {
		t.Fatalf("principal not fully resolved: %+v", p)
	}

	if _, err := authenticate(ctx, st, kbTok, ScopeComm); err == nil {
		t.Fatal("a knowledge-base token was accepted on the comm endpoint")
	}
}

// A cloud-hosted connector is the worst possible holder of "reach into the
// sessions on my machines", and its scope set is hard-coded rather than
// operator-chosen. An OAuth-shaped bearer must not authenticate here at all.
func TestAuthRejectsOAuthShapedTokens(t *testing.T) {
	ctx := context.Background()
	st := newKB(t)

	// OAuth access tokens are base62 with no underscore.
	if _, err := authenticate(ctx, st, "abc123DEFopaqueoauthtokenvalue", ScopeComm); err == nil {
		t.Fatal("an OAuth-shaped token was accepted on the comm endpoint")
	}
	if err := func() error { _, e := authenticate(ctx, st, "abc123DEFopaque", ScopeComm); return e }(); err == nil ||
		!strings.Contains(err.Error(), "dedicated ken_ API token") {
		t.Fatalf("want the dedicated-token message, got %v", err)
	}
}

// The dev-token bypass has an empty token id and therefore escapes per-token rate
// accounting, so it must not reach COMM even when set.
func TestAuthRejectsTheDevTokenBypass(t *testing.T) {
	ctx := context.Background()
	st := newKB(t)
	t.Setenv("KEN_DEV_TOKEN", "dev-secret")

	if _, err := authenticate(ctx, st, "dev-secret", ScopeComm); err == nil {
		t.Fatal("KEN_DEV_TOKEN was accepted on the comm endpoint")
	}
}

func TestAuthRejectsRevokedAndMalformedTokens(t *testing.T) {
	ctx := context.Background()
	st := newKB(t)

	tok := mintToken(t, st, "revoked-agent", "comm")
	tokenID := strings.SplitN(tok, "_", 3)[1]
	if err := st.RevokeToken(ctx, tokenID); err != nil {
		t.Fatal(err)
	}
	if _, err := authenticate(ctx, st, tok, ScopeComm); err == nil {
		t.Fatal("a revoked token authenticated")
	}

	for _, bad := range []string{"", "ken_", "ken_only-two-parts", "ken_unknownid_secret"} {
		if _, err := authenticate(ctx, st, bad, ScopeComm); err == nil {
			t.Fatalf("malformed token %q authenticated", bad)
		}
	}

	// A correct token id with the wrong secret must fail.
	good := mintToken(t, st, "secret-agent", "comm")
	parts := strings.SplitN(good, "_", 3)
	if _, err := authenticate(ctx, st, "ken_"+parts[1]+"_wrongsecret", ScopeComm); err == nil {
		t.Fatal("a wrong secret authenticated")
	}
}

// A wait that ties or exceeds the client's tool timeout turns a successful empty
// poll into a tool ERROR, so the ceiling is clamped server-side no matter what an
// operator configures — including when they change it live from the settings page.
func TestPollWaitIsClampedServerSide(t *testing.T) {
	cases := []struct {
		name      string
		configMax int
		requested int
		want      time.Duration
	}{
		{"default when unset", 0, 0, defaultPollWait * time.Second},
		{"requested under the max", 20, 5, 5 * time.Second},
		{"requested over the max is capped", 20, 999, 20 * time.Second},
		{"operator max above the hard ceiling is capped", 3600, 999, hardMaxPollWait * time.Second},
		{"operator max above the hard ceiling caps a modest request too", 3600, 120, hardMaxPollWait * time.Second},
		{"a live update takes effect", 10, 0, 10 * time.Second},
		{"negative means do not park", 20, -1, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := &Handler{w: newWaiters()}
			h.SetMaxPollWait(c.configMax)
			got := h.pollWait(c.requested)
			if got != c.want {
				t.Fatalf("pollWait(%d) with max %d = %v, want %v", c.requested, c.configMax, got, c.want)
			}
		})
	}
}

func TestWaiterIsWokenByNotify(t *testing.T) {
	w := newWaiters()
	done := make(chan bool, 1)
	go func() { done <- w.wait(context.Background(), 42, 5*time.Second) }()

	// Let the waiter park before notifying, so this tests the wakeup rather than
	// the immediate re-read.
	for i := 0; i < 100 && w.parked() == 0; i++ {
		time.Sleep(time.Millisecond)
	}
	w.notify(42)

	select {
	case woken := <-done:
		if !woken {
			t.Fatal("waiter returned without being woken")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("notify did not wake the parked waiter")
	}
	if w.parked() != 0 {
		t.Fatalf("waiter not deregistered: %d still parked", w.parked())
	}
}

func TestWaiterTimesOutAndDeregisters(t *testing.T) {
	w := newWaiters()
	if woken := w.wait(context.Background(), 1, 10*time.Millisecond); woken {
		t.Fatal("wait reported a wakeup on timeout")
	}
	if w.parked() != 0 {
		t.Fatalf("waiter leaked after timeout: %d parked", w.parked())
	}
}

// A notify for one endpoint must not wake another endpoint's poll.
func TestNotifyIsPerEndpoint(t *testing.T) {
	w := newWaiters()
	done := make(chan bool, 1)
	go func() { done <- w.wait(context.Background(), 1, 150*time.Millisecond) }()
	for i := 0; i < 100 && w.parked() == 0; i++ {
		time.Sleep(time.Millisecond)
	}
	w.notify(2) // a different endpoint

	select {
	case woken := <-done:
		if woken {
			t.Fatal("a notify for endpoint 2 woke a waiter on endpoint 1")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiter never returned")
	}
}

// Drain must wake everyone: the shutdown budget is shorter than a long poll, so
// without it every deploy severs parked connections mid-response.
func TestDrainWakesEveryWaiterAndRefusesNewOnes(t *testing.T) {
	w := newWaiters()
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int64) { defer wg.Done(); w.wait(context.Background(), id, 10*time.Second) }(int64(i))
	}
	for i := 0; i < 200 && w.parked() < 5; i++ {
		time.Sleep(time.Millisecond)
	}

	w.drain()
	waited := make(chan struct{})
	go func() { wg.Wait(); close(waited) }()
	select {
	case <-waited:
	case <-time.After(3 * time.Second):
		t.Fatal("drain did not wake all parked waiters")
	}

	// After draining, a new wait returns immediately rather than parking.
	start := time.Now()
	if woken := w.wait(context.Background(), 99, 5*time.Second); woken {
		t.Fatal("a waiter parked after drain")
	}
	if time.Since(start) > time.Second {
		t.Fatal("wait blocked after drain instead of returning immediately")
	}
}

// Waiters are capped because nothing outside the process bounds them: no write
// timeout on the server, no task limit in the unit file.
func TestWaiterCapsAreEnforced(t *testing.T) {
	w := newWaiters()
	var wg sync.WaitGroup
	for i := 0; i < maxWaitersPerEndpoint; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); w.wait(context.Background(), 7, 2*time.Second) }()
	}
	for i := 0; i < 200 && w.parked() < maxWaitersPerEndpoint; i++ {
		time.Sleep(time.Millisecond)
	}

	// One past the per-endpoint cap must return immediately, not park.
	start := time.Now()
	if woken := w.wait(context.Background(), 7, 5*time.Second); woken {
		t.Fatal("over-cap waiter reported a wakeup")
	}
	if time.Since(start) > time.Second {
		t.Fatal("over-cap waiter parked instead of returning immediately")
	}

	w.drain()
	wg.Wait()
}

// A cancelled request must release its waiter rather than holding the slot.
func TestWaiterReleasesOnContextCancel(t *testing.T) {
	w := newWaiters()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan bool, 1)
	go func() { done <- w.wait(ctx, 5, 10*time.Second) }()
	for i := 0; i < 200 && w.parked() == 0; i++ {
		time.Sleep(time.Millisecond)
	}
	cancel()

	select {
	case woken := <-done:
		if woken {
			t.Fatal("cancelled wait reported a wakeup")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not release the waiter")
	}
	if w.parked() != 0 {
		t.Fatalf("waiter leaked after cancel: %d parked", w.parked())
	}
}

// A parked long-poll is a wait, not latency: comm_poll must be excluded from the
// tool-duration histogram, while every other comm_* tool is recorded. Bucketing a
// 30s wait as "latency" would drown the real work time of every other tool.
func TestPollExcludedFromDurationHistogram(t *testing.T) {
	if recordsDuration("comm_poll") {
		t.Fatal("comm_poll (a long-poll) must NOT be recorded in the latency histogram")
	}
	for _, tool := range []string{"comm_register", "comm_join", "comm_channels", "comm_send", "comm_ack", "comm_file_offer", "comm_file_grant"} {
		if !recordsDuration(tool) {
			t.Fatalf("%s should be recorded — it does not block", tool)
		}
	}
}

// TestInstructionsTellTheModelWhereToKeepTheSecret pins the fix for a real outage:
// a dev session lost its endpoint_secret to context compaction, could not
// reconnect, and work stopped for a day waiting for a human to mint a fresh
// pairing code.
//
// The old text said "KEEP the endpoint_id and endpoint_secret". That is advice a
// human client follows by writing a config file and an AI client follows by
// remembering — and remembering is precisely the thing that fails, silently and by
// design. The instruction is only useful if it names the DESTINATION, so this test
// asserts the destination and the irreversibility, not merely that the words
// "endpoint_secret" appear.
func TestInstructionsTellTheModelWhereToKeepTheSecret(t *testing.T) {
	for _, want := range []string{
		"WRITE the endpoint_id and endpoint_secret TO A FILE ON DISK",
		"context compaction is routine and silent",
		"never be re-read, re-derived or reset",
	} {
		if !strings.Contains(instructions, want) {
			t.Errorf("connect-time instructions no longer carry %q — a session that loses its\n"+
				"secret cannot reconnect at all, so this guidance is load-bearing", want)
		}
	}
}
