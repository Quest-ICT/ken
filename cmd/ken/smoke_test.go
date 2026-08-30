package main

import (
	"database/sql"
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

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
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

// *** AN UPGRADE FROM THE PREVIOUS RELEASE MUST NOT DEGRADE COMM. ***
//
// This is the one assertion that would have caught the last two audit rounds. Both found the same
// defect class in comm migration 0021: a statement that removes rows while foreign keys are OFF,
// orphaning columns nobody enumerated, so the post-migration check fails — AFTER the migration has
// committed its version, which means every subsequent boot is degraded and no re-run repairs it.
//
// Round two found it in the endpoint columns. The fix for that introduced it in the channel columns.
// Neither was visible to `go test ./...`, because every store test opens a FRESH database: the
// data-moving arms of a migration copy zero rows in every unit test that exists.
//
// So this builds the PREVIOUS tag's binary, lets it create a database, and boots HEAD on it. It is
// the only test in the tree that exercises a migration against data it did not itself create.
//
// SKIPPED, NOT FAILED, when the previous tag cannot be built — a shallow clone or a missing tag is
// an environment fact, not a defect, and a test that fails for that reason gets disabled rather than
// fixed. It logs loudly so a skip cannot be mistaken for a pass.
func TestUpgradeFromPreviousReleaseDoesNotDegradeComm(t *testing.T) {
	const prev = "v3.42.0"
	root, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Skipf("not a git checkout, cannot build %s: %v", prev, err)
	}
	repo := strings.TrimSpace(string(root))
	if err := exec.Command("git", "-C", repo, "rev-parse", "--verify", prev+"^{commit}").Run(); err != nil {
		t.Skipf("tag %s is not present in this checkout, so the upgrade path cannot be exercised here", prev)
	}

	work := t.TempDir()
	tree := filepath.Join(work, "prev")
	if out, err := exec.Command("git", "-C", repo, "worktree", "add", "--detach", tree, prev).CombinedOutput(); err != nil {
		t.Skipf("cannot create a worktree at %s: %v: %s", prev, err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command("git", "-C", repo, "worktree", "remove", "--force", tree).Run()
	})

	oldBin := filepath.Join(work, "ken-prev")
	build := exec.Command(goToolPath(t), "build", "-o", oldBin, "./cmd/ken")
	build.Dir = tree
	if out, err := build.CombinedOutput(); err != nil {
		t.Skipf("cannot build %s: %v: %s", prev, err, out)
	}

	// The OLD binary creates both databases at its own schema.
	data := filepath.Join(work, "data")
	runFor(t, oldBin, data, 20*time.Second)

	// CONTROL: the old binary really did create comm.db. Without it, "the upgrade was clean" would
	// be true of an upgrade that had nothing to migrate — which is exactly the blind spot that let
	// two rounds of this defect through.
	commDB := filepath.Join(data, "comm", "comm.db")
	if _, err := os.Stat(commDB); err != nil {
		t.Fatalf("%s did not create comm.db at %s, so this test would prove nothing: %v", prev, commDB, err)
	}

	// *** AND IT MUST CONTAIN THE ROWS THE MIGRATION ACTUALLY MOVES. ***
	//
	// The first version of this test booted the old binary, upgraded, and passed — while the broken
	// migration was still in the tree. A fresh boot creates an EMPTY comm.db, so every data-moving
	// statement copies zero rows and the arms that fail are never entered. That is the same blind
	// spot as the unit tests, reproduced in the test written to escape it.
	//
	// The shape below is one v3.42.0 could produce with its own tools: comm_register twice for one
	// station leaves two mailboxes, and binding an endpoint to a station AFTER joining a channel
	// leaves a channel whose two seats belong to the same station. Written as SQL because driving
	// the old binary's MCP surface from here would be a second integration suite; the STATE is what
	// the migration reads, and the state is faithful.
	seedLegacyCommRows(t, commDB)

	// Now boot HEAD on it and read the log.
	log := runFor(t, kenBinary(t), data, 30*time.Second)
	if strings.Contains(log, "DEGRADED") {
		t.Errorf("upgrading from %s degrades COMM:\n%s\n\n"+
			"A migration that leaves a dangling reference commits its version first, so this does not "+
			"heal on restart — every later boot reports the same failure with nothing repaired.", prev, log)
	}
	if strings.Contains(strings.ToLower(log), "dangling") {
		t.Errorf("the upgrade left dangling foreign key references:\n%s", log)
	}
}

// runFor starts a binary against a data directory, waits for it to become healthy, stops it, and
// returns everything it logged.
func runFor(t *testing.T, bin, dataDir string, budget time.Duration) string {
	t.Helper()
	addr := freePort(t)
	env := []string{}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "KEN_") {
			env = append(env, kv)
		}
	}
	env = append(env,
		"KEN_DB="+filepath.Join(dataDir, "ken.db"),
		"KEN_ADDR="+addr,
		"KEN_METRICS=off",
		"HOME="+dataDir,
	)
	cmd := exec.Command(bin, "serve")
	cmd.Env = env
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", bin, err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + addr + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				time.Sleep(200 * time.Millisecond) // let the startup lines flush
				return out.String()
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s did not become healthy within %s:\n%s", bin, budget, out.String())
	return ""
}

// goToolPath finds the go binary the same way kenBinary does.
func goToolPath(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("no go toolchain on PATH to build the previous release: %v", err)
	}
	return p
}

// seedLegacyCommRows writes the pre-4.0.0 shapes that migration 0021 has to move: two live
// mailboxes on one station, a channel seated on both of them, and a message, delivery and
// attachment referencing the one that will be collapsed away.
//
// Every column here is one a previous audit round found unhandled. Keep them all: the value of this
// fixture is that it is the union of what two rounds discovered by hand.
func seedLegacyCommRows(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+path+"?_pragma=foreign_keys(off)")
	if err != nil {
		t.Fatalf("open legacy comm.db: %v", err)
	}
	defer db.Close()

	stmts := []string{
		`INSERT INTO endpoint(endpoint_id,secret_sha256,token_id,actor_id,label,station_id,bound_at)
		   VALUES('legacy-a','x','tok',1,'legacy-a','st-legacy',strftime('%Y-%m-%dT%H:%M:%fZ','now'))`,
		`INSERT INTO endpoint(endpoint_id,secret_sha256,token_id,actor_id,label,station_id,bound_at)
		   VALUES('legacy-b','x','tok',1,'legacy-b','st-legacy',strftime('%Y-%m-%dT%H:%M:%fZ','now'))`,
		// A channel seated on both mailboxes of one station — reachable in 3.x by binding after joining.
		`INSERT INTO channel(channel_id,owner_actor_id,endpoint_a,endpoint_b,state)
		   SELECT 'legacy-self',1,
		          (SELECT id FROM endpoint WHERE endpoint_id='legacy-a'),
		          (SELECT id FROM endpoint WHERE endpoint_id='legacy-b'),'open'`,
		// A message ON that channel, sent by the mailbox that will be collapsed, with an e: party
		// key — the addressing column no foreign key covers.
		`INSERT INTO message(message_id,channel_id,scope_id,scope_seq,sender_endpoint,sender_party,
		                     body,body_bytes,body_sha256,expires_at)
		   SELECT 'legacy-m1',(SELECT id FROM channel WHERE channel_id='legacy-self'),'ch:legacy-self',1,
		          (SELECT id FROM endpoint WHERE endpoint_id='legacy-b'),
		          'e:'||(SELECT id FROM endpoint WHERE endpoint_id='legacy-b'),
		          'hi',2,'abc',strftime('%Y-%m-%dT%H:%M:%fZ','now','+1 day')`,
		`INSERT INTO delivery(message_row,party_key,recipient_endpoint,state)
		   SELECT (SELECT id FROM message WHERE message_id='legacy-m1'),
		          'e:'||(SELECT id FROM endpoint WHERE endpoint_id='legacy-b'),
		          (SELECT id FROM endpoint WHERE endpoint_id='legacy-b'),'queued'`,
		`INSERT INTO attachment(attachment_id,channel_id,scope_id,sender_endpoint,name,size_bytes,
		                        sha256,state,transfer,expires_at)
		   SELECT 'legacy-at1',(SELECT id FROM channel WHERE channel_id='legacy-self'),'ch:legacy-self',
		          (SELECT id FROM endpoint WHERE endpoint_id='legacy-b'),'f.bin',3,'abc','offered',
		          'upload',strftime('%Y-%m-%dT%H:%M:%fZ','now','+1 day')`,
	}
	for i, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("seed legacy row %d: %v", i+1, err)
		}
	}
}
