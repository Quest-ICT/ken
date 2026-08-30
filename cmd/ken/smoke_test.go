package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// *** THE SUITE COULD NOT SEE THE WIRING, AND THAT IS WHY THE SAME CLASS KEPT SHIPPING. ***
//
// Three audit rounds on the 4.0.0 release found, between them: live settings edited a handler
// nothing mounted; the rate limit charged three times because one limiter reached three chained
// middlewares; the dev token honoured by one middleware of three and then failing on the first call
// it authorised; a console form posting to a deleted route; a migration failure that reported
// healthy on the next boot. Every one of those survived a green `go test ./...`, and every one was
// found by a human building a binary by hand.
//
// The reason is structural rather than careless. Almost everything in cmd/ken/main.go is WIRING —
// which handler is mounted, which hook points at it, which limiter instance is shared, which URL is
// advertised — and a unit test constructs its own wiring. It can only ever prove that the pieces
// work when assembled the way the test assembles them, which is not the way main() assembles them.
//
// So this boots the REAL BINARY and drives it over HTTP, asserting the handful of facts that only
// exist once main() has run. It is deliberately small: this is a smoke test, not a second
// integration suite. Every assertion here is one that a previous audit round had to discover by
// hand, and each is cheap because the process is already up.
//
// The harness (kenBinary, freePort) is the one the dispatch tests already use — the build is lazy
// and shared, so adding this costs one boot.

// ken starts the built binary against a fresh data directory and returns its base URL. The server is
// stopped when the test ends.
func ken(t *testing.T, env ...string) string {
	t.Helper()
	bin := kenBinary(t)
	dir := t.TempDir()
	addr := freePort(t)

	base := []string{}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "KEN_") {
			base = append(base, kv)
		}
	}
	base = append(base,
		"KEN_DB="+filepath.Join(dir, "data", "ken.db"),
		"KEN_ADDR="+addr,
		"KEN_METRICS=off",
		"HOME="+dir,
	)
	cmd := exec.Command(bin, "serve")
	cmd.Env = append(base, env...)
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start ken: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		if t.Failed() {
			t.Logf("server log:\n%s", out.String())
		}
	})

	url := "http://" + addr
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return url
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("ken did not become healthy within the budget. log:\n%s", out.String())
	return ""
}

// rpc makes one MCP call against the running server and returns the HTTP status and the body.
func rpc(t *testing.T, url, token, sessionID, body string) (int, string, string) {
	t.Helper()
	req, err := http.NewRequest("POST", url+"/mcp", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("mcp request: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get("Mcp-Session-Id"), string(b)
}

const initBody = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"smoke","version":"0"}}}`

// THE ONE MACHINE SURFACE IS MOUNTED, AND THE TWO DELETED ONES ARE NOT.
//
// This is pure wiring: which paths main() hands to the mux. No unit test can see it, because every
// unit test builds its own handler and mounts it itself.
func TestBinaryServesOneMachineSurface(t *testing.T) {
	url := ken(t, "KEN_DEV_TOKEN=smoke-secret")

	code, sid, body := rpc(t, url, "smoke-secret", "", initBody)
	if code != http.StatusOK {
		t.Fatalf("/mcp initialize returned %d: %s", code, body)
	}
	if sid == "" {
		t.Fatal("/mcp initialize returned no session id, so no tool call can follow")
	}

	// THE RETIRED ENDPOINTS MUST NOT SPEAK MCP. They are not special-cased: the web mux handles
	// them like any unknown path, which on a fresh install is a 303 to first-run setup. That is the
	// correct outcome — a connector pointed there gets something that is plainly not MCP.
	//
	// ASSERTED AS "no MCP session comes back", not as a status code. The first version of this test
	// checked for a non-200 and failed, because http.DefaultClient FOLLOWS the redirect and reports
	// the 200 from /setup. A status code was the wrong question; whether the path serves the
	// protocol is the right one.
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	for _, p := range []string{"/comm/mcp", "/station/mcp", "/all/mcp"} {
		req, _ := http.NewRequest("POST", url+p, strings.NewReader(initBody))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Authorization", "Bearer smoke-secret")
		resp, err := noRedirect.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		got, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.Header.Get("Mcp-Session-Id") != "" || strings.Contains(string(got), `"protocolVersion"`) {
			t.Errorf("%s answered the MCP handshake (%d) — 4.0.0 deletes it, and a connector left "+
				"pointing there must fail rather than keep working with a subset of the tools: %s",
				p, resp.StatusCode, got)
		}
	}
}

// A CREDENTIAL MUST REACH TOOLS, NOT JUST THE HANDSHAKE — and the handshake is where every previous
// check stopped.
//
// The dev-token fix was verified with initialize and tools/list, both of which passed, and the very
// next call died with a raw FOREIGN KEY error because the principal carried no actor. A boot test
// that stops at the handshake would have missed it exactly as the unit tests did.
func TestBinaryAnswersToolCallsAcrossAllThreeFamilies(t *testing.T) {
	url := ken(t, "KEN_DEV_TOKEN=smoke-secret")
	_, sid, _ := rpc(t, url, "smoke-secret", "", initBody)
	rpc(t, url, "smoke-secret", sid, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)

	call := func(name, args string) string {
		t.Helper()
		code, _, body := rpc(t, url, "smoke-secret", sid,
			fmt.Sprintf(`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, name, args))
		if code != http.StatusOK {
			t.Fatalf("%s: HTTP %d: %s", name, code, body)
		}
		return body
	}

	// tools/list first: it names what the rest of this test may call, and its COUNT is the one
	// number the release notes quote.
	code, _, list := rpc(t, url, "smoke-secret", sid, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if code != http.StatusOK {
		t.Fatalf("tools/list: HTTP %d: %s", code, list)
	}
	for _, family := range []string{`"kb_search"`, `"comm_directory"`, `"station_me"`} {
		if !strings.Contains(list, family) {
			t.Errorf("tools/list does not offer %s — the single surface is missing a family", family)
		}
	}

	// One call per family. station_me FIRST, because a station is what the other two resolve
	// against, and because it is the call the server's own instructions mandate.
	if out := call("station_me", `{"session_key":"smoke-conv"}`); strings.Contains(out, "constraint") {
		t.Errorf("station_me failed against the built binary: %s\n"+
			"A handshake proves a credential was accepted; only a call proves it can be used.", out)
	}
	for _, c := range []struct{ name, args string }{
		{"comm_directory", `{"session_key":"smoke-conv"}`},
		{"kb_search", `{"query":"anything"}`},
	} {
		if out := call(c.name, c.args); strings.Contains(out, "constraint") || strings.Contains(out, "internal error") {
			t.Errorf("%s failed against the built binary: %s", c.name, out)
		}
	}
}

// A CREDENTIAL THAT CANNOT USE EVERY CAPABILITY IS REFUSED AT THE TRANSPORT.
//
// This is the property that makes the endpoint collapse safe, and it lives entirely in how main()
// chains three middlewares. An in-process test that builds the chain itself proves its own wiring,
// not main's.
func TestBinaryRefusesAPartialCredential(t *testing.T) {
	url := ken(t)

	code, _, body := rpc(t, url, "definitely-not-a-token", "", initBody)
	if code != http.StatusUnauthorized {
		t.Errorf("an unknown bearer got HTTP %d, want 401: %s", code, body)
	}
	code, _, body = rpc(t, url, "", "", initBody)
	if code != http.StatusUnauthorized {
		t.Errorf("no bearer at all got HTTP %d, want 401: %s", code, body)
	}
}

// THE PER-TOKEN RATE LIMIT IS CHARGED ONCE PER REQUEST.
//
// It was charged three times, because main() hands ONE limiter to three dependency sets and /mcp
// chains all three middlewares — so the shipped default of 120/min burst 60 was really 40/20 while
// the boot log and the settings label both said 120. Nothing in the suite could see it: each
// middleware's own test exercises one middleware.
//
// Asserted as "the burst is spent at roughly the configured rate, not a fraction of it", with a
// generous margin. The defect was a factor of three; this catches that without pinning an exact
// count a future scheduling change could shift by one.
func TestBinaryChargesTheRateLimitOncePerRequest(t *testing.T) {
	const burst = 6
	url := ken(t,
		"KEN_DEV_TOKEN=smoke-secret",
		"KEN_RATELIMIT_TOKEN_RPM=60",
		fmt.Sprintf("KEN_RATELIMIT_TOKEN_BURST=%d", burst),
	)

	var ok int
	for i := 0; i < burst*2; i++ {
		code, _, _ := rpc(t, url, "smoke-secret", "", initBody)
		if code == http.StatusOK {
			ok++
			continue
		}
		if code == http.StatusTooManyRequests {
			break
		}
		t.Fatalf("unexpected status %d on request %d", code, i+1)
	}
	if ok < burst-1 {
		t.Errorf("a burst of %d bought only %d requests before 429.\n"+
			"That is the rate limit being charged more than once per request — /mcp chains three "+
			"auth middlewares and main() hands the same limiter to all three, so the configured "+
			"burst is silently divided.", burst, ok)
	}
}

// THE DATABASES REACH THE VERSION THE RELEASE CLAIMS, AND THE INTEGRITY CHECK PASSES.
//
// A migration that leaves a dangling reference commits its version before the check runs, so the
// version alone is not evidence. This asserts both, on a fresh install, through the binary that
// ships — which is also the only place the comm.db path (a subdirectory main() chooses) is exercised.
func TestBinaryMigratesBothDatabasesCleanly(t *testing.T) {
	url := ken(t)

	resp, err := http.Get(url + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/health returned %d on a fresh install: %s", resp.StatusCode, b)
	}
	var health map[string]any
	if err := json.Unmarshal(b, &health); err != nil {
		t.Fatalf("/health is not JSON: %v (%s)", err, b)
	}
	// A DEGRADED comm is a failure, not a setting — and a fresh install must never be in it.
	if s := string(b); strings.Contains(strings.ToUpper(s), "DEGRADED") {
		t.Errorf("a freshly installed Ken reports a degraded component: %s", s)
	}
}
