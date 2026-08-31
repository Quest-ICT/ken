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
	"regexp"
	"strconv"
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

// A CREDENTIAL THAT CANNOT DO EVERYTHING IS REFUSED AT THE TRANSPORT — the case the test above is
// named for and does not cover.
//
// TestBinaryRefusesAPartialCredential checks an UNKNOWN bearer and an ABSENT one. Neither is
// partial: both are refused by the ordinary "who are you" path that has existed since 1.x. The
// documented 4.0.0 behaviour is different and stronger — /mcp carries every tool, so a credential
// holding SOME capabilities is refused rather than admitted with a reduced tool list, on the
// grounds that admitting it would turn the tool list into a reconnaissance surface.
//
// That claim was in UPGRADING.md and in the release notes with nothing exercising it. A named test
// that covers a neighbouring case reads as coverage from the outside, which is the whole reason
// the deleted-test audit that found this was worth running.
func TestBinaryRefusesACredentialThatHoldsOnlySomeCapabilities(t *testing.T) {
	bin := kenBinary(t)
	dir := t.TempDir()
	db := filepath.Join(dir, "data", "ken.db")

	env := []string{}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "KEN_") {
			env = append(env, kv)
		}
	}
	env = append(env, "KEN_DB="+db, "HOME="+dir)

	// A knowledge-base-only token: exactly the shape a connector approved before /mcp carried
	// everything ends up with.
	mint := exec.Command(bin, "token", "add", "--actor", "kb-only-probe", "--label", "kb-only", "--scopes", "read")
	mint.Env = env
	out, err := mint.CombinedOutput()
	if err != nil {
		t.Skipf("could not mint a scoped token, so this cannot be exercised here: %v\n%s", err, out)
	}
	tok := ""
	for _, f := range strings.Fields(string(out)) {
		if strings.HasPrefix(f, "ken_") {
			tok = strings.TrimRight(f, ".,")
			break
		}
	}
	if tok == "" {
		t.Skipf("no ken_ token in the mint output, so there is nothing to present:\n%s", out)
	}

	addr := freePort(t)
	srv := exec.Command(bin, "serve")
	srv.Env = append(env, "KEN_ADDR="+addr, "KEN_METRICS=off")
	var log strings.Builder
	srv.Stdout, srv.Stderr = &log, &log
	if err := srv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = srv.Process.Kill(); _ = srv.Wait() }()

	url := "http://" + addr
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}

	code, _, body := rpc(t, url, tok, "", initBody)
	if code == http.StatusOK {
		t.Errorf("a token holding only 'read' was ADMITTED to /mcp (HTTP 200).\n%s\n"+
			"UPGRADING.md and the 4.0.0 release notes both say a partial credential is refused at "+
			"the transport, because admitting it would make the tool list a reconnaissance surface. "+
			"Either the behaviour or the documentation is wrong.", body)
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
// A DEV PRINCIPAL MUST BE CHARGED LIKE ANY OTHER TOKEN — the half its neighbour cannot see.
//
// TestBinaryChargesTheRateLimitOncePerRequest above catches OVER-charging: a shared limiter
// divides the burst, so too few requests get through. It is silent on the opposite defect, and
// the opposite defect is the one this project actually shipped. A dev principal carrying no
// token identity is charged to no bucket, never 429s at all, and sails through that test with
// ok == burst*2 — comfortably past its `ok < burst-1` floor.
//
// That is not hypothetical. v3.42.0 had TestAuthRejectsTheDevTokenBypass for exactly this
// reason; the 4.0.0 wave deleted it and replaced it with nothing. Blanking TokenID on any of
// the three auth paths (mcpserver, commserver, stationserver) leaves `go test ./...` green.
//
// So this asserts the 429 ARRIVES. Loopback is exempt from per-IP limiting, so the only bucket
// that can produce one is the per-token bucket, which is precisely the thing under test.
func TestBinaryChargesTheDevPrincipalLikeAnyOtherToken(t *testing.T) {
	const burst = 4
	url := ken(t,
		"KEN_DEV_TOKEN=smoke-secret",
		"KEN_RATELIMIT_TOKEN_RPM=60",
		fmt.Sprintf("KEN_RATELIMIT_TOKEN_BURST=%d", burst),
	)

	sent, limited := 0, false
	for i := 0; i < burst*4; i++ {
		code, _, _ := rpc(t, url, "smoke-secret", "", initBody)
		sent++
		if code == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Errorf("%d requests on a burst of %d and never a 429 — the dev principal is charged to "+
			"no token bucket, so it is exempt from a limit every other credential pays.\n"+
			"Check TokenID on the dev principal in internal/{mcpserver,commserver,stationserver}/auth.go: "+
			"an empty one accounts to nothing and is invisible to every other test in this tree.",
			sent, burst)
	}
}

// EVERY STATION TOOL MUST WORK FROM session_key ALONE, ON A CONNECTION THAT NEVER BOUND.
//
// station_me, comm_poll, comm_send and comm_directory took a session_key. The other nineteen
// station tools did not DECLARE the field, so `additionalProperties:false` rejected it at the
// schema, and they fell back to a map keyed on the MCP session id that station_me writes.
//
// ken-prod-ops measured that map failing on a client which re-initialises between messages:
// station_me succeeded, and a station_note_write seconds later in the SAME conversation with the
// SAME key was refused "this connection has not said which station it is". Notebook, tasks,
// locker and vault were behind whether a connection happened to persist.
//
// IT HAS TO BE TESTED OVER THE WIRE. Half the defect was the SCHEMA refusing the argument, which
// no unit test on requireStation can see: such a test passes a Go string to a Go function and
// never meets the JSON schema that was doing the rejecting. So this drives real MCP calls, and
// deliberately uses a SECOND initialize — a different session id, nothing bound — to be the
// re-initialising client rather than a simulation of one.
func TestEveryStationToolWorksFromTheSessionKeyAlone(t *testing.T) {
	url := ken(t, "KEN_DEV_TOKEN=smoke-secret")
	const key = "smoke-conversation-key"

	call := func(sid, tool, args string) string {
		t.Helper()
		body := fmt.Sprintf(
			`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":%q,"arguments":%s}}`,
			tool, args)
		code, _, out := rpc(t, url, "smoke-secret", sid, body)
		if code != http.StatusOK {
			t.Fatalf("%s: HTTP %d: %s", tool, code, out)
		}
		return out
	}

	// Connection ONE claims a station with the key.
	code, sid1, body := rpc(t, url, "smoke-secret", "", initBody)
	if code != http.StatusOK {
		t.Fatalf("initialize: %d %s", code, body)
	}
	call(sid1, "station_me", fmt.Sprintf(`{"session_key":%q,"station_label":"smoke"}`, key))

	// Connection TWO is a fresh MCP session: it has bound nothing, which is exactly the state a
	// re-initialising client is in on its second message.
	code, sid2, body := rpc(t, url, "smoke-secret", "", initBody)
	if code != http.StatusOK {
		t.Fatalf("second initialize: %d %s", code, body)
	}
	if sid2 == sid1 {
		t.Fatal("the second initialize reused the first session id, so this proves nothing about " +
			"an unbound connection")
	}

	// One tool per shape: a lister that took a bare struct{}, the notebook, the task list, the
	// locker and the vault. Each is a different input type, and the field had to be added to all.
	for _, c := range []struct{ tool, args string }{
		{"station_note_list", `{}`},
		{"station_note_write", `{"key":"handoff","title":"t","body":"b","mode":"replace"}`},
		{"station_task_list", `{"state":"open"}`},
		{"station_locker_list", `{}`},
		{"station_vault_list", `{}`},
		{"station_directory", `{}`},
	} {
		withKey := c.args[:len(c.args)-1]
		if len(withKey) > 1 {
			withKey += ","
		}
		withKey += fmt.Sprintf(`"session_key":%q}`, key)

		out := call(sid2, c.tool, withKey)
		if strings.Contains(out, "has not said which station") {
			t.Errorf("%s refused a call carrying session_key on an unbound connection:\n%s\n"+
				"That is the whole defect: the key names the station and the connection is "+
				"irrelevant.", c.tool, out)
		}
		if strings.Contains(out, "additionalProperties") || strings.Contains(out, "unknown field") {
			t.Errorf("%s REJECTED session_key at the schema:\n%s\n"+
				"The input type is missing the field, so no caller can send it however correct "+
				"the resolver is.", c.tool, out)
		}
	}
}

// A SELF-DESCRIPTION SENT WITH A session_key MUST SURVIVE THE CALL THAT ACCEPTED IT.
//
// station_me wrote it at the BOTTOM of the handler, and both session_key branches — station
// created, and station already claimed — return before reaching that line. So the recommended way
// to call the tool was the one way the fields were silently discarded: a station created with
// self_described_about set echoed self_described_about:"" in the same result.
//
// Over the wire, because the discard was a control-flow path in the handler, and the value has to
// come back out of a SECOND call to prove it was STORED rather than merely echoed.
func TestASelfDescriptionSentWithASessionKeyIsKept(t *testing.T) {
	url := ken(t, "KEN_DEV_TOKEN=smoke-secret")
	const key = "smoke-selfdesc-key"
	const about = "what this station is responsible for"

	code, sid, body := rpc(t, url, "smoke-secret", "", initBody)
	if code != http.StatusOK {
		t.Fatalf("initialize: %d %s", code, body)
	}
	me := func(args string) string {
		t.Helper()
		c, _, out := rpc(t, url, "smoke-secret", sid, fmt.Sprintf(
			`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"station_me","arguments":%s}}`, args))
		if c != http.StatusOK {
			t.Fatalf("station_me: %d %s", c, out)
		}
		return out
	}

	// The station is CREATED by this call, which is the path that dropped the value.
	first := me(fmt.Sprintf(`{"session_key":%q,"self_described_about":%q}`, key, about))

	// Read it back on a separate call: an echo proves nothing about what was stored.
	second := me(fmt.Sprintf(`{"session_key":%q}`, key))
	if !strings.Contains(second, about) {
		t.Errorf("self_described_about did not survive the call that accepted it.\ncreate: %s\nread back: %s\n"+
			"station_me applies the self-description only after requireStation, and both session_key "+
			"branches return before reaching that line.", first, second)
	}
}

// A NEW STATION ON AN OLD DEPLOYMENT MUST SAY SO, AND THE FIRST ONE MUST NOT.
//
// station_me knows it just created a station and then reports 0 tasks exactly as it would for one
// that genuinely has nothing outstanding. On a first run that is right. On a deployment where this
// conversation used to have a station, it is a session telling its human "nothing is waiting on
// you" about a post that no longer exists — collector-proxy-prod read exactly that after the estate
// was rebuilt, having lost 34 tasks and a notebook.
//
// BOTH ARMS MATTER AND THE SECOND IS THE ONE THAT KEEPS IT HONEST. A warning on every first call
// would be noise on a genuinely fresh install, and noise is what gets instructions ignored — so
// this asserts the FIRST station stays quiet as firmly as it asserts the second one speaks.
func TestANewStationOnAnOldDeploymentSaysSo(t *testing.T) {
	url := ken(t, "KEN_DEV_TOKEN=smoke-secret")

	code, sid, body := rpc(t, url, "smoke-secret", "", initBody)
	if code != http.StatusOK {
		t.Fatalf("initialize: %d %s", code, body)
	}
	me := func(key string) string {
		t.Helper()
		c, _, out := rpc(t, url, "smoke-secret", sid, fmt.Sprintf(
			`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"station_me","arguments":{"session_key":%q}}}`, key))
		if c != http.StatusOK {
			t.Fatalf("station_me: %d %s", c, out)
		}
		return out
	}

	// THE FIRST STATION ON AN EMPTY DEPLOYMENT. Nothing was lost, so nothing is claimed.
	first := me("smoke-first-conversation")
	if !strings.Contains(first, `"station_just_created":true`) {
		t.Fatalf("the first call did not create a station, so neither arm below is testing anything:\n%s", first)
	}
	if strings.Contains(first, "already has other stations") {
		t.Errorf("the FIRST station on a fresh deployment was warned that others exist:\n%s\n"+
			"That is noise on an ordinary first run, and noise is how a warning gets ignored.", first)
	}

	// A SECOND CONVERSATION. Now the deployment has a history this session is not part of.
	second := me("smoke-second-conversation")
	if !strings.Contains(second, `"station_just_created":true`) {
		t.Fatalf("the second key did not get its own station:\n%s", second)
	}
	for _, want := range []string{
		"relay_to_human", // it must be in the field a session is told to say out loud
		"already has other stations",
		"NEW",
	} {
		if !strings.Contains(second, want) {
			t.Errorf("a new station on a deployment that already had others does not say %q:\n%s\n"+
				"An empty briefing reads as reassurance; that is the whole defect.", want, second)
		}
	}
	// IT MUST NOT ASSERT A LOSS KEN CANNOT SEE. Ken does not know whether this conversation ever
	// had a station — only that others exist and this one is new.
	for _, forbidden := range []string{"was deleted", "was removed and", "your data is gone"} {
		if strings.Contains(second, forbidden) {
			t.Errorf("the relay claims %q, which Ken cannot know:\n%s", forbidden, second)
		}
	}
}

// *** TWO CONVERSATIONS ON ONE CONNECTION MUST NOT WRITE TO EACH OTHER'S STATION. ***
//
// This is a production data-integrity defect, reproduced. The connection binding is keyed on the
// MCP session id, which is per CONNECTION; Claude Desktop holds ONE connection for the whole
// application, so every conversation in it shares one row in that map:
//
//	conversation A: station_me{session_key:A}  -> map[conn] = stationA
//	conversation B: station_me{session_key:B}  -> map[conn] = stationB   (OVERWRITES)
//	conversation A: station_note_write{}       -> Bound(conn) = stationB -> A's note lands on B
//
// ken-prod-ops read the rows on 2026-08-31: one session's notes in another's notebook, a third
// party's task and handoff filed under the first, and a mode=replace stopped one call short of
// destroying a live handoff. It appeared the hour the estate went from 4 stations to 13, because
// that was the first time two conversations shared a connection — the map had been accidentally
// correct for as long as every session was alone on one.
//
// THE FIX IS A REFUSAL, NOT A REPAIR. Ken cannot tell which conversation a keyless call belongs to
// once two have claimed the connection, and guessing is what produced the misrouting. So the
// binding is abandoned for that connection permanently, and the caller is told the one thing that
// does work.
//
// OVER THE WIRE, ON ONE Mcp-Session-Id, because that identifier IS the defect. A unit test would
// have to fabricate the collision it exists to detect.
func TestTwoConversationsOnOneConnectionDoNotCrossStations(t *testing.T) {
	url := ken(t, "KEN_DEV_TOKEN=smoke-secret")

	code, sid, body := rpc(t, url, "smoke-secret", "", initBody)
	if code != http.StatusOK {
		t.Fatalf("initialize: %d %s", code, body)
	}
	call := func(tool, args string) string {
		t.Helper()
		c, _, out := rpc(t, url, "smoke-secret", sid, fmt.Sprintf(
			`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, tool, args))
		if c != http.StatusOK {
			t.Fatalf("%s: HTTP %d: %s", tool, c, out)
		}
		return out
	}

	// Two conversations claim the SAME connection, exactly as two Desktop chats do.
	call("station_me", `{"session_key":"conversation-A","station_label":"A"}`)
	call("station_me", `{"session_key":"conversation-B","station_label":"B"}`)

	// A KEYLESS CALL MUST NOW BE REFUSED. Before the guard it resolved to whichever station bound
	// last — conversation B's — and wrote there.
	keyless := call("station_note_write", `{"key":"handoff","body":"from conversation A","mode":"replace"}`)
	if !strings.Contains(keyless, "shared by more than one conversation") {
		t.Errorf("a keyless write on a connection claimed by two stations was not refused:\n%s\n\n"+
			"That is the production defect: the binding hands back whichever station bound most "+
			"recently, so one conversation's write lands in another's notebook.", keyless)
	}
	// The refusal must name the remedy, and must NOT send them to station_me — they have already
	// called it, successfully, and calling it again does not help.
	if !strings.Contains(keyless, "session_key") {
		t.Errorf("the refusal does not name session_key, which is the only thing that fixes it:\n%s", keyless)
	}

	// AND THE KEY STILL WORKS, on the same poisoned connection. A guard that broke both paths would
	// be an outage rather than a fix.
	withKey := call("station_note_write",
		`{"key":"handoff","body":"from conversation A","mode":"replace","session_key":"conversation-A"}`)
	if strings.Contains(withKey, "shared by more than one conversation") ||
		strings.Contains(withKey, "has not said which station") {
		t.Errorf("a call carrying session_key was refused on a shared connection:\n%s\n\n"+
			"The key resolves the station directly and never touches the binding.", withKey)
	}

	// *** AND IT LANDED ON A's STATION, NOT B's. *** The refusal above is only half the fix; this
	// is the half that proves nothing crossed. Read back through B's key: B's handoff must be
	// untouched, which it cannot be if A's write went to B.
	bNotes := call("station_note_read", `{"key":"handoff","session_key":"conversation-B"}`)
	if strings.Contains(bNotes, "from conversation A") {
		t.Errorf("conversation A's note is in conversation B's notebook:\n%s\n\n"+
			"This is the row-level shape ken-prod-ops measured on production.", bNotes)
	}
}

// *** THE REACH A SESSION IS SHOWN MUST BE THE REACH A BROADCAST ACTUALLY GETS. ***
//
// THIS IS THE TEST THE CODEBASE DID NOT HAVE, AND ITS ABSENCE COST A DAY. There WAS a test called
// TestTheAdvertisedBroadcastReachMatchesWhatSendingWouldDo, and it compared BroadcastAudience
// against Broadcast — two hand-copied queries over the SAME room mirror. It proved the two copies
// agreed and nothing about whether either described the world. On production, thirteen stations
// and zero rooms made both of them 0: it reported "advertised 0, delivered 0, matched", stayed
// green, and an operator could not send an estate-wide "stop writing" advisory during a live
// data-integrity incident. Six stations were never warned.
//
// It has to live HERE, against the built binary, because the invariant now spans both databases:
// the reach is read from ken.db's station roster and the deliveries land in comm.db. No package
// below this one holds both handles — internal/comm must never import internal/store (S7) — so
// there is no unit-level seam where this can be checked. That is a cost of the design and it is
// the right cost: the thing being asserted is end-to-end behaviour.
//
// VERIFY BY DELETION: point the dispatch at anything other than store.BroadcastRoster — the old
// room-mirror self-join, a subset, a cached list — and the counts disagree and this goes RED.
func TestAnEstateBroadcastReachesEveryStationTheDirectoryLists(t *testing.T) {
	url := ken(t, "KEN_DEV_TOKEN=smoke-secret")

	code, sid, body := rpc(t, url, "smoke-secret", "", initBody)
	if code != http.StatusOK {
		t.Fatalf("initialize: %d %s", code, body)
	}
	call := func(tool, args string) string {
		t.Helper()
		c, _, out := rpc(t, url, "smoke-secret", sid, fmt.Sprintf(
			`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, tool, args))
		if c != http.StatusOK {
			t.Fatalf("%s: HTTP %d: %s", tool, c, out)
		}
		return out
	}

	// FOUR STATIONS AND DELIBERATELY NO ROOM. This is production's shape on 2026-08-31, and the
	// shape every fresh deployment has: rooms are human-created and a fresh Ken has none.
	for _, k := range []string{"conv-a", "conv-b", "conv-c", "conv-d"} {
		call("station_me", fmt.Sprintf(`{"session_key":%q,"station_label":%q}`, k, k))
	}

	dir := call("comm_directory", `{"session_key":"conv-a"}`)
	reach := jsonNumber(t, dir, "broadcast_reaches")
	if reach != 3 {
		t.Fatalf("broadcast_reaches = %d on a four-station instance with NO rooms, want 3.\n%s\n\n"+
			"Zero here is the production defect verbatim: the audience came from room membership, "+
			"so an estate with no rooms could not address itself.", reach, dir)
	}

	// Both spellings, and they must agree. to_room:"all" is the older one and is kept forever,
	// because a session live across this upgrade holds a schema that has never heard of
	// to_everyone.
	for _, addr := range []string{`"to_everyone":true`, `"to_room":"all"`} {
		out := call("comm_send", fmt.Sprintf(
			`{"session_key":"conv-a","body":"stop writing","idempotency_key":"advisory-%s",%s}`,
			strings.NewReplacer(`"`, "", ":", "-").Replace(addr), addr))
		got := jsonNumber(t, out, "recipients")
		if got != reach {
			t.Errorf("comm_directory advertised a reach of %d and a broadcast via %s delivered %d.\n%s\n\n"+
				"A session cannot plan against a number that is not the one it gets, and an operator "+
				"sending a safety advisory cannot tell 3-of-13 from 13-of-13 by reading a count.",
				reach, addr, got, out)
		}
		// AND THE NAMES, because a count is not checkable. "reached 3" and "reached 13" read
		// identically to a session that never knew which was right.
		for _, name := range []string{"conv-b", "conv-c", "conv-d"} {
			if !strings.Contains(out, name) {
				t.Errorf("recipient_stations does not name %s after a broadcast via %s:\n%s",
					name, addr, out)
			}
		}
		if strings.Contains(out, `"conv-a"`) {
			t.Errorf("the sender is in its own broadcast audience via %s:\n%s", addr, out)
		}
	}

	// AND THE MAIL IS REALLY THERE, flagged as a broadcast. The flag is what tells a session a
	// reply reaches a scope rather than a person, and it keyed on audience_size > 1 until 5.2.0 —
	// so on a two-station Ken an estate advisory arrived looking like an ordinary direct message.
	got := call("comm_poll", `{"session_key":"conv-d","wait_seconds":0}`)
	if !strings.Contains(got, "stop writing") {
		t.Errorf("a station that shares no room with the sender did not receive the estate broadcast:\n%s", got)
	}
	if !strings.Contains(got, `"broadcast":true`) {
		t.Errorf("estate mail arrived without the broadcast flag:\n%s", got)
	}
}

// TestATwoStationEstateStillFlagsABroadcast is the two-station case on its own, because it is the
// one every fresh deployment starts in and the one the old `audience_size > 1` test could not see:
// with a single recipient the audience size is 1, so the flag was false and a session read
// "everyone stop writing" as a note addressed to it alone.
func TestATwoStationEstateStillFlagsABroadcast(t *testing.T) {
	url := ken(t, "KEN_DEV_TOKEN=smoke-secret")
	code, sid, body := rpc(t, url, "smoke-secret", "", initBody)
	if code != http.StatusOK {
		t.Fatalf("initialize: %d %s", code, body)
	}
	call := func(tool, args string) string {
		t.Helper()
		c, _, out := rpc(t, url, "smoke-secret", sid, fmt.Sprintf(
			`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, tool, args))
		if c != http.StatusOK {
			t.Fatalf("%s: HTTP %d: %s", tool, c, out)
		}
		return out
	}
	call("station_me", `{"session_key":"only-a","station_label":"only-a"}`)
	call("station_me", `{"session_key":"only-b","station_label":"only-b"}`)

	call("comm_send", `{"session_key":"only-a","to_everyone":true,"body":"everyone stop writing","idempotency_key":"two-station-advisory"}`)
	got := call("comm_poll", `{"session_key":"only-b","wait_seconds":0}`)
	if !strings.Contains(got, `"broadcast":true`) {
		t.Errorf("on a TWO-station estate the broadcast flag is missing:\n%s\n\n"+
			"audience_size excludes the sender, so it is 1 here — keyed on `> 1` alone this is the "+
			"case that silently reads as an ordinary directed message.", got)
	}
}

// jsonNumber pulls an integer field out of a tool result, which arrives as JSON embedded in a
// JSON-RPC envelope's text content. Deliberately a regex over the raw body rather than a decode:
// the point of these tests is what a CLIENT sees on the wire, and a struct with the field in it
// would pass even if the server stopped sending it.
func jsonNumber(t *testing.T, body, field string) int {
	t.Helper()
	m := regexp.MustCompile(`\\?"` + field + `\\?":\s*(-?\d+)`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no %q field in:\n%s", field, body)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("%s = %q: %v", field, m[1], err)
	}
	return n
}

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
// IT NOW COVERS THE WHOLE UPGRADE STORY, because 5.0.0 moved the rewrite out of the server.
// Ken creates a database from schema/*.sql and otherwise checks the recorded version, so the
// path from 4.x is: HEAD REFUSES, an operator runs upgrade/comm-4.x-to-5.0.0.sql with stock
// sqlite3, HEAD starts. Every step of that is asserted here against the real previous binary and
// the real script — the alternative is a procedure that exists only in a document.
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
// THE BOOT MUST SAY WHAT IT DID TO THE SCHEMA, AND SAY THE INTEGRITY CHECK RAN.
//
// ken-prod-ops measured the boot that took the live database from ken 24 -> 26 and comm 19 -> 21:
// thirteen log lines, and `grep -icE "migrat|foreign|fk|integrity|schema"` returned ZERO. Two
// migrations ran and the foreign-key check ran and passed, invisibly. They could establish the
// databases were sound only by opening them and checking by hand.
//
// A check whose success is indistinguishable from its absence is this project's own defect class,
// aimed at its riskiest operation. An operator reading that log cannot tell "the integrity check
// passed" from "there is no integrity check", and on the day it matters they will assume the
// second.
//
// Asserted on BOTH boots, because the two paths print from different branches: the first migrates,
// the second finds nothing pending and must still report that the check ran.
func TestTheBootSaysWhatItDidToTheSchema(t *testing.T) {
	data := t.TempDir()
	bin := kenBinary(t)

	first := runFor(t, bin, data, 30*time.Second)
	for _, want := range []string{
		"schema: ken.db is empty — creating it", "schema: comm.db is empty — creating it",
		"foreign_key_check clean",
	} {
		if !strings.Contains(first, want) {
			t.Errorf("the migrating boot never said %q:\n%s", want, first)
		}
	}

	second := runFor(t, bin, data, 30*time.Second)
	for _, want := range []string{
		"schema: ken.db at version 26, as required", "schema: comm.db at version 22, as required",
		"foreign_key_check clean",
	} {
		if !strings.Contains(second, want) {
			t.Errorf("a boot with nothing pending never said %q, so a reader cannot tell the "+
				"integrity check ran from there being none:\n%s", want, second)
		}
	}

	// ken-prod-ops' own command, which returned 0 on the boot that migrated two databases.
	if n := strings.Count(strings.ToLower(first), "schema:"); n < 4 {
		t.Errorf("only %d schema lines on a boot that created two databases; prod's grep for "+
			"migration evidence is what returned zero and started this", n)
	}
}

// prevRelease is the tag the upgrade tests build. Bump it with every release: pointing at a tag
// older than the one operators are actually on tests a journey nobody takes, and pointing it at
// HEAD makes both tests vacuous while staying green.
const prevRelease = "v4.0.1"

// repoRoot locates the checkout, or SKIPS. A shallow clone is an environment fact, not a defect,
// and a test that FAILS for it gets disabled rather than fixed.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Skipf("not a git checkout, so a previous release cannot be built: %v", err)
	}
	return strings.TrimSpace(string(root))
}

// buildPreviousRelease builds the named tag in a throwaway worktree and returns the binary.
func buildPreviousRelease(t *testing.T, repo, tag string) string {
	t.Helper()
	if err := exec.Command("git", "-C", repo, "rev-parse", "--verify", tag+"^{commit}").Run(); err != nil {
		t.Skipf("tag %s is not present in this checkout, so the upgrade path cannot be exercised here", tag)
	}
	work := t.TempDir()
	tree := filepath.Join(work, "prev")
	if out, err := exec.Command("git", "-C", repo, "worktree", "add", "--detach", tree, tag).CombinedOutput(); err != nil {
		t.Skipf("cannot create a worktree at %s: %v: %s", tag, err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command("git", "-C", repo, "worktree", "remove", "--force", tree).Run()
	})
	bin := filepath.Join(work, "ken-prev")
	build := exec.Command(goToolPath(t), "build", "-o", bin, "./cmd/ken")
	build.Dir = tree
	if out, err := build.CombinedOutput(); err != nil {
		t.Skipf("cannot build %s: %v: %s", tag, err, out)
	}
	return bin
}

func TestAPreviousReleasesDatabaseIsRefusedThenUpgradedByTheScript(t *testing.T) {
	prev := prevRelease
	repo := repoRoot(t)
	oldBin := buildPreviousRelease(t, repo, prev)

	// The OLD binary creates both databases at its own schema.
	data := t.TempDir()
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
	seedReleaseCommRows(t, commDB)

	// *** HEAD MUST REFUSE THIS DATABASE, AND THEN THE UPGRADE SCRIPT MUST MAKE IT BOOT. ***
	//
	// 5.0.0 stopped migrating databases. Ken creates one from schema/*.sql and otherwise checks the
	// recorded version, so a 4.x comm.db — schema 21 against a binary requiring 22 — has to stop the
	// boot. Both halves are asserted here because either alone is worthless: a refusal that nothing
	// can clear is an outage, and an upgrade nobody was told to run is the silent-start this
	// replaces.
	refused := bootAndCapture(t, kenBinary(t), data, 30*time.Second)
	if !strings.Contains(refused, "schema version") {
		t.Fatalf("HEAD started against a %s database instead of refusing it:\n%s\n\n"+
			"That is the failure the version check exists to prevent: a binary opening a database "+
			"whose shape it does not know, writing columns that are not there.", prev, refused)
	}
	// The refusal has to name the way out. An operator who cannot act on it will delete something.
	if !strings.Contains(refused, "UPGRADING-THE-DATABASE.md") {
		t.Errorf("the refusal does not name the upgrade procedure:\n%s", refused)
	}

	// APPLY THE SCRIPT THE WAY AN OPERATOR DOES: the file that ships in upgrade/, run against the
	// database from outside the process. If this stops working, so does the only supported path
	// from 4.x — and it would fail here rather than on somebody's deployment.
	applySQLFile(t, commDB, filepath.Join(repo, "upgrade", "comm-4.x-to-5.0.0.sql"))

	log := bootAndCapture(t, kenBinary(t), data, 30*time.Second)
	if !strings.Contains(log, "COMM:") {
		t.Fatalf("after the upgrade HEAD still did not come up:\n%s", log)
	}
	// PROVE THE LOG ARRIVED BEFORE TRUSTING WHAT IT DOES NOT SAY. The checks below are negative, so
	// an empty log satisfies every one of them.
	if !strings.Contains(log, "comm.db at version 22") {
		t.Errorf("the boot does not report comm.db at the version the upgrade set:\n%s", log)
	}
	if strings.Contains(log, "DEGRADED") || strings.Contains(strings.ToLower(log), "dangling") {
		t.Errorf("the upgraded database does not hold together:\n%s", log)
	}
	if !strings.Contains(log, "foreign_key_check clean") {
		t.Errorf("the boot never says the integrity check ran, so its silence proves nothing:\n%s", log)
	}
}

// AN UPGRADED DATABASE AND A FRESH ONE MUST END UP WITH THE SAME SCHEMA.
//
// This is the claim the whole no-migration design rests on: schema/comm.sql and
// upgrade/comm-4.x-to-5.0.0.sql are two routes to one shape, written by hand and by generation
// respectively, and nothing but this test forces them to agree. If they drift, an upgraded
// deployment runs on a schema no fresh install has ever been tested against — and it would look
// completely healthy, because every version check would still pass.
//
// ken-prod-ops verifies exactly this from outside, by hashing the DDL of both databases before and
// after. That they can is not an accident: the files are plain SQLite, readable by stock sqlite3
// from another process, which is a property to protect rather than one to optimise away.
func TestAnUpgradedDatabaseMatchesAFreshOne(t *testing.T) {
	repo := repoRoot(t)
	oldBin := buildPreviousRelease(t, repo, prevRelease)

	// Route one: the previous release creates it, the script upgrades it.
	upgraded := t.TempDir()
	runFor(t, oldBin, upgraded, 20*time.Second)
	applySQLFile(t, filepath.Join(upgraded, "comm", "comm.db"),
		filepath.Join(repo, "upgrade", "comm-4.x-to-5.0.0.sql"))

	// Route two: HEAD creates it from the schema file.
	fresh := t.TempDir()
	runFor(t, kenBinary(t), fresh, 30*time.Second)

	a := commDDL(t, filepath.Join(upgraded, "comm", "comm.db"))
	b := commDDL(t, filepath.Join(fresh, "comm", "comm.db"))
	if a != b {
		t.Errorf("an upgraded comm.db and a fresh one have DIFFERENT schemas.\n"+
			"upgraded:\n%s\n\nfresh:\n%s\n\n"+
			"The upgrade script and schema/comm.sql have drifted. Every version check would still "+
			"pass, so nothing else in this tree would notice.", a, b)
	}
}

// commDDL reads a database's whole schema, ordered, the way an external checker would.
func commDDL(t *testing.T, path string) string {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT type, name, COALESCE(sql,'') FROM sqlite_master
	    WHERE name NOT LIKE 'sqlite_%' ORDER BY type, name`)
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	defer rows.Close()
	var sb strings.Builder
	for rows.Next() {
		var typ, name, ddl string
		if err := rows.Scan(&typ, &name, &ddl); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&sb, "%s %s\n%s\n\n", typ, name, strings.TrimSpace(ddl))
	}
	return sb.String()
}

// bootAndCapture starts the binary and returns everything it logged, whether it came up or died.
//
// runFor FAILS the test when the process never becomes healthy, which is correct for every other
// caller and exactly wrong here: the first half of the upgrade test WANTS a binary that refuses to
// start, and needs to read why.
func bootAndCapture(t *testing.T, bin, dataDir string, budget time.Duration) string {
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

	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()

	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		select {
		case <-done:
			// It exited on its own — a refusal. Its output is complete because Wait joined the
			// copiers.
			return out.String()
		default:
		}
		resp, err := http.Get("http://" + addr + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				_ = cmd.Process.Kill()
				<-done
				return out.String()
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	<-done
	return out.String()
}

// applySQLFile runs one .sql file against a database the way an operator's sqlite3 would.
func applySQLFile(t *testing.T, dbPath, sqlPath string) {
	t.Helper()
	body, err := os.ReadFile(sqlPath)
	if err != nil {
		t.Fatalf("read %s: %v", sqlPath, err)
	}
	db, err := sql.Open("sqlite3", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open %s: %v", dbPath, err)
	}
	defer db.Close()
	if _, err := db.Exec(string(body)); err != nil {
		t.Fatalf("applying %s: %v", filepath.Base(sqlPath), err)
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

	// STOP THE PROCESS BEFORE READING THE LOG, AND JOIN ITS COPIERS.
	//
	// exec fills `out` from goroutines it owns, so reading out.String() while the child is alive is
	// a data race — and the earlier shape did exactly that behind a 200ms "let the startup lines
	// flush" sleep. The race is the smaller half of the problem. The caller's assertion is
	// NEGATIVE ("the log must not contain DEGRADED"), so a log that is merely SHORT passes it: a
	// flush that lost the race made this test green by having nothing to read. That is the failure
	// mode this whole file exists to catch, reproduced inside the instrument.
	//
	// cmd.Wait() joins the stdout/stderr copiers, so after it returns `out` is complete and reading
	// it is race-free. No sleep, and nothing to tune.
	stopped := false
	stop := func() string {
		if !stopped {
			stopped = true
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		return out.String()
	}
	defer func() { _ = stop() }()

	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + addr + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return stop()
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s did not become healthy within %s:\n%s", bin, budget, stop())
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

// seedReleaseCommRows writes traffic into a database built by the PREVIOUS RELEASE, so the
// upgrade above runs over rows it did not create: two stations with one mailbox each, a channel
// between them, and a message, delivery and attachment on that channel.
//
// Every column here is one an audit round found unhandled while these tables were being rewritten.
// Keep them all: the value of this fixture is that it is the union of what those rounds found by
// hand, and the columns outlived the migration that made them interesting.
func seedReleaseCommRows(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+path+"?_pragma=foreign_keys(off)")
	if err != nil {
		t.Fatalf("open released comm.db: %v", err)
	}
	defer db.Close()

	// THE SHAPE A RELEASED DATABASE ACTUALLY HOLDS, not one the current schema forbids.
	// The previous version of this seeder built two mailboxes for one station, which is what
	// 3.x could produce and what comm 0021 exists to collapse. A v4.0.0 database already has
	// 0021's partial UNIQUE index, so that seed is REJECTED at insert — correctly. One mailbox
	// per station is now the invariant, so the rows below respect it and the interesting case
	// becomes a channel and its traffic surviving untouched.
	stmts := []string{
		`INSERT INTO endpoint(endpoint_id,secret_sha256,token_id,actor_id,label,station_id,bound_at)
		   VALUES('rel-a','x','tok',1,'rel-a','st-a',strftime('%Y-%m-%dT%H:%M:%fZ','now'))`,
		`INSERT INTO endpoint(endpoint_id,secret_sha256,token_id,actor_id,label,station_id,bound_at)
		   VALUES('rel-b','x','tok',1,'rel-b','st-b',strftime('%Y-%m-%dT%H:%M:%fZ','now'))`,
		`INSERT INTO channel(channel_id,owner_actor_id,endpoint_a,endpoint_b,state)
		   SELECT 'rel-ch',1,
		          (SELECT id FROM endpoint WHERE endpoint_id='rel-a'),
		          (SELECT id FROM endpoint WHERE endpoint_id='rel-b'),'open'`,
		`INSERT INTO message(message_id,channel_id,scope_id,scope_seq,sender_endpoint,sender_party,
		                     body,body_bytes,body_sha256,expires_at)
		   SELECT 'rel-m1',(SELECT id FROM channel WHERE channel_id='rel-ch'),'ch:rel-ch',1,
		          (SELECT id FROM endpoint WHERE endpoint_id='rel-b'),
		          'e:'||(SELECT id FROM endpoint WHERE endpoint_id='rel-b'),
		          'hi',2,'abc',strftime('%Y-%m-%dT%H:%M:%fZ','now','+1 day')`,
		`INSERT INTO delivery(message_row,party_key,recipient_endpoint,state)
		   SELECT (SELECT id FROM message WHERE message_id='rel-m1'),
		          'e:'||(SELECT id FROM endpoint WHERE endpoint_id='rel-a'),
		          (SELECT id FROM endpoint WHERE endpoint_id='rel-a'),'queued'`,
		`INSERT INTO attachment(attachment_id,channel_id,scope_id,sender_endpoint,name,size_bytes,
		                        sha256,state,transfer,expires_at)
		   SELECT 'rel-at1',(SELECT id FROM channel WHERE channel_id='rel-ch'),'ch:rel-ch',
		          (SELECT id FROM endpoint WHERE endpoint_id='rel-b'),'f.bin',3,'abc','offered',
		          'upload',strftime('%Y-%m-%dT%H:%M:%fZ','now','+1 day')`,
	}
	for i, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("seed released row %d: %v", i+1, err)
		}
	}
}
