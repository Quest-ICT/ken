package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// NO PART OF KEN IS OPTIONAL. This asserts that nothing can switch a feature off.
//
// The history is the argument. COMM and stations shipped behind KEN_COMM_ENABLED
// and KEN_STATION_ENABLED, opt-in and off by default. They were then made core with
// the variables INVERTED into opt-outs, which felt like the careful choice and was
// not: it left every document, every tool description and every session instruction
// hedging about a feature that might be missing, and Ken then shipped a release whose
// COMM instruction still opened "opt-in; off by default" — false the moment it was
// released, in the one place read by a machine on every connection. A switch nobody
// is expected to use still costs a hedge everywhere, and hedges rot.
//
// KEN_OAUTH_ENABLED was worse: it defaulted to OFF, and OAuth is how a human registers
// Ken once on their account and reaches it from every client. A fresh install could
// not be connected the documented way until the operator found a variable nothing
// pointed them at.
//
// This is a SOURCE test rather than a behavioural one because there is no behaviour
// left to observe — that is the point. What can regress is someone reintroducing a
// gate, and the only way to catch that is to look for it.
func TestNoFeatureCanBeSwitchedOff(t *testing.T) {
	// Retired gates. Reintroducing any of these means a feature became optional again.
	retired := []string{
		"KEN_COMM_ENABLED",
		"KEN_STATION_ENABLED",
		"KEN_OAUTH_ENABLED",
	}

	src := readGoSources(t, ".")
	if len(src) < 4 {
		t.Fatalf("only %d Go sources read from cmd/ken — the scan is finding nothing and this test proves nothing", len(src))
	}

	// Looks for a READ, not a mention. The invariant is that nothing consults a feature
	// gate — not that the name may never appear. Ken records reversals rather than
	// deleting them, so the comments explaining why these variables are gone contain
	// the very strings this test hunts, and a test that cannot tell an explanation from
	// a use would force the explanation out. That is backwards: the comment is why the
	// next person does not reintroduce it.
	reads := func(body, gate string) bool {
		for _, form := range []string{
			`os.Getenv("` + gate + `")`,
			`envBoolDefault("` + gate + `"`,
			`envTrue("` + gate + `")`,
			`envOr("` + gate + `"`,
		} {
			if strings.Contains(body, form) {
				return true
			}
		}
		return false
	}

	for name, body := range src {
		for _, gate := range retired {
			if reads(body, gate) {
				t.Errorf("%s READS %s.\n"+
					"That variable was removed because no part of Ken is optional. If a feature needs to be\n"+
					"absent, every doc, tool description and connect-time instruction has to hedge about it —\n"+
					"and Ken has already shipped a release whose instruction text hedged about this exact\n"+
					"variable, incorrectly, to every session that connected.", name, gate)
			}
		}
	}
}

// The degraded state is NOT a switch, and removing the switches must not have removed
// it. An unopenable comm.db has to leave the knowledge base running: that is what makes
// "COMM may fail; the KB stays UP" true rather than aspirational, and it is the one
// path by which COMM is legitimately absent at runtime.
//
// Asserted on the source for the same reason as above — the alternative is a test that
// corrupts a database file to watch a log line, which is slow, flaky, and tests the
// SQLite driver more than it tests Ken.
func TestAnUnopenableCommDatabaseDegradesRatherThanAborting(t *testing.T) {
	body, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)

	if !strings.Contains(s, "COMM: DEGRADED") {
		t.Error("main.go no longer logs a DEGRADED line for an unopenable comm.db — an operator whose messaging is down has nothing to find")
	}
	// The failure path must not be fatal — log.Fatal there would let an expendable
	// database take the durable knowledge base down with it.
	//
	// Asserted on the CALL, not by scanning nearby lines. The first version searched
	// the 400 characters before the log line for "log.Fatal" and failed, because the
	// comment immediately above it explains that a log.Fatal there would be wrong. That
	// is the same use-versus-explanation confusion as the gate scan above, made twice
	// in one file: a source test that cannot tell code from prose about code will
	// eventually force the prose out, and the prose is what stops the regression.
	if !strings.Contains(s, `log.Printf("COMM: DEGRADED`) {
		t.Error("the unopenable-comm.db path does not report through log.Printf — if it aborts instead, " +
			"an expendable database takes the durable knowledge base down with it")
	}
}

// readGoSources returns every non-test Go file in a directory, keyed by name.
func readGoSources(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			t.Fatal(err)
		}
		out[n] = string(b)
	}
	return out
}
