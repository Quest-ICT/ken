package main

import (
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// THE MAIL-WAITING CHECK, THROUGH THE BINARY THAT SHIPS. The handler and its guard have unit
// tests; what only main() decides is the ROUTING: POST /comm/waiting must go around the outer
// IPGuard (its own budget, its own block) while every other path still goes through it, and
// its block must not reach the rest of Ken. Every Claude Code session on a workstation shares
// one address, so either mistake would let one session's stale hook cut the machine off Ken.
//
// Loopback is exempt from both guards, so the test puts Ken behind a "proxy" at 127.0.0.1 and
// presents outside addresses through X-Forwarded-For.
func TestBinaryRoutesTheMailCheckAroundTheSharedGuard(t *testing.T) {
	url := ken(t,
		"KEN_DEV_TOKEN=smoke-secret",
		"KEN_TRUSTED_PROXIES=127.0.0.1/32",
		"KEN_RATELIMIT_IP_RPM=60", "KEN_RATELIMIT_IP_BURST=2", // the shared guard: tiny
		"KEN_RATELIMIT_WAITING_MISS_LIMIT=3",
	)

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
	stationID := func(out string) string {
		t.Helper()
		m := regexp.MustCompile(`\\?"station_id\\?":\s*\\?"([0-9A-Za-z]{16})`).FindStringSubmatch(out)
		if m == nil {
			t.Fatalf("no 16-character station_id in station_me:\n%s", out)
		}
		return m[1]
	}
	a := stationID(call("station_me", `{"session_key":"wait-a","station_label":"wait-a"}`))
	b := stationID(call("station_me", `{"session_key":"wait-b","station_label":"wait-b"}`))

	check := func(from, id string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, url+"/comm/waiting", strings.NewReader(`{"station":"`+id+`"}`))
		req.Header.Set("X-Forwarded-For", from)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, strings.TrimSpace(string(out))
	}
	page := func(from string) int {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, url+"/", nil)
		req.Header.Set("X-Forwarded-For", from)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	// Real answers, end to end over the real stores.
	if c, out := check("203.0.113.5", b); c != http.StatusOK || out != `{"mail":false}` {
		t.Fatalf("b before any mail: %d %s", c, out)
	}
	call("comm_send", `{"session_key":"wait-a","to_everyone":true,"body":"hello b","idempotency_key":"wait-smoke-1"}`)
	if c, out := check("203.0.113.5", b); c != http.StatusOK || out != `{"mail":true}` {
		t.Fatalf("b after a broadcast: %d %s, want {\"mail\":true}", c, out)
	}
	call("comm_poll", `{"session_key":"wait-b","wait_seconds":0}`)
	if c, out := check("203.0.113.5", b); c != http.StatusOK || out != `{"mail":false}` {
		t.Fatalf("b after polling: %d %s — delivered mail is not waiting mail", c, out)
	}

	// AROUND THE SHARED GUARD: far more checks than its burst of 2, all answered.
	for i := 0; i < 10; i++ {
		if c, out := check("203.0.113.5", a); c != http.StatusOK {
			t.Fatalf("check %d from one address: %d %s — the mail check is drawing on the shared per-IP budget", i, c, out)
		}
	}
	// ...and the shared guard still guards everything else (the control: without it, the loop
	// above proves nothing about routing).
	var got []int
	for i := 0; i < 4; i++ {
		got = append(got, page("203.0.113.5"))
	}
	if got[len(got)-1] != http.StatusTooManyRequests {
		t.Fatalf("GET / from one address 4 times with a shared burst of 2: %v — the control failed, "+
			"so the shared guard is not in front of the mux", got)
	}

	// THE MISS BLOCK COVERS THE MAIL CHECK ONLY.
	for i := 0; i < 3; i++ {
		check("203.0.113.7", "ZZZZZZZZZZZZZZZ9")
	}
	if c, out := check("203.0.113.7", a); c != http.StatusForbidden {
		t.Fatalf("after 3 lookups of a missing station: %d %s, want 403", c, out)
	}
	if c := page("203.0.113.7"); c != http.StatusOK && c != http.StatusFound && c != http.StatusSeeOther {
		t.Fatalf("the mail-check block reached the rest of Ken: GET / answered %d", c)
	}
}
