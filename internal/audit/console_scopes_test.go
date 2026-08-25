package audit

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestConsoleCanMintEveryAgentScope fails when a scope the transports accept cannot be minted from
// the console.
//
// *** THIS OMISSION HAS NOW SHIPPED TWICE, AND THE SECOND TIME THE FILE DOCUMENTED THE FIRST. ***
//
// Until 3.10.0 the console could not mint comm scopes at all. `internal/web/app.go` records the
// cost in a comment: *"an operator following it minted a token, handed it to a session, and watched
// comm_register refuse it for a missing scope. Worse, the handler DROPPED the unknown scope
// silently, so nothing said why."*
//
// Four lines below that comment sat `consoleCommScopes = {"comm", "comm-file"}`, excluding the
// station family — justified by "/station/mcp requires a `kens_` key BOUND to a station". **3.27.0
// made that false**: a plain `ken_` token carrying `station` reaches /station/mcp, because the
// client that reported the original deadlock is non-interactive and cannot run an OAuth flow, so
// it was the only door left. The justification was removed by the same commit that left the
// exclusion standing, and Vlad could not mint the credential the fix existed to serve.
// ken-prod-ops found it within the hour.
//
// A comment describing a past instance of a defect is not a guard against the next one. This is.
func TestConsoleCanMintEveryAgentScope(t *testing.T) {
	web, err := os.ReadFile("../web/app.go")
	if err != nil {
		t.Fatal(err)
	}
	mintable := map[string]bool{}
	n := 0
	for _, list := range []string{"agentScopes", "consoleCommScopes"} {
		m := regexp.MustCompile(`var ` + list + ` = \[\]string\{([^}]*)\}`).FindSubmatch(web)
		if m == nil {
			t.Fatalf("cannot find %s in internal/web/app.go; the scanner is broken, not the list", list)
		}
		for _, q := range regexp.MustCompile(`"([a-z-]+)"`).FindAllStringSubmatch(string(m[1]), -1) {
			mintable[q[1]] = true
			n++
		}
	}
	if n < 5 {
		t.Fatalf("only %d mintable scopes parsed; the scanner is broken, not the lists", n)
	}

	// Every scope a transport REQUIRES must be mintable. Read from the servers rather than
	// listed here, so a family added to a surface cannot be missed by this check either.
	required := map[string]string{}
	for _, src := range []struct{ file, note string }{
		{"../commserver/auth.go", "/comm/mcp"},
		{"../stationserver/auth.go", "/station/mcp"},
	} {
		b, err := os.ReadFile(src.file)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range regexp.MustCompile(`Scope[A-Za-z]*\s*=\s*"([a-z-]+)"`).FindAllStringSubmatch(string(b), -1) {
			required[m[1]] = src.note
		}
	}
	if len(required) == 0 {
		t.Fatal("no transport scopes parsed; the scanner is broken, not the servers")
	}

	for scope, surface := range required {
		if !mintable[scope] {
			t.Errorf("%s requires the %q scope and the console cannot mint it.\n"+
				"Ken's posture is that the console is the main method for any operation and the CLI is a "+
				"last resort — so an operator either cannot get there at all, or mints a token that "+
				"authenticates nowhere while looking exactly like a working credential. That has shipped "+
				"twice; add it to consoleCommScopes.", surface, scope)
		}
	}

	// AND THE CURATION GATE STAYS SHUT. The one scope that must never be mintable by any path.
	if mintable["curate"] {
		t.Error("the console offers to mint `curate` — a human promotes, and no agent credential may")
	}
	if !strings.Contains(string(web), "curate") {
		t.Fatal("the word curate does not appear in app.go at all; the check above cannot have been " +
			"looking at the right file")
	}
}
