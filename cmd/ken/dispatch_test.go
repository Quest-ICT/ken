package main

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// The dispatch defect lives in main() itself — which value the switch falls through
// to — so it is tested against the REAL binary. A unit test of any extracted helper
// would leave the one line that matters (the fall-through in main) unproven.

var (
	buildOnce sync.Once
	buildDir  string
	buildPath string
	buildErr  error
)

// TestMain removes the binary the dispatch tests build, and only if one was built:
// the build is lazy, so a run of the other tests in this package pays nothing.
func TestMain(m *testing.M) {
	code := m.Run()
	if buildDir != "" {
		os.RemoveAll(buildDir)
	}
	os.Exit(code)
}

// kenBinary builds cmd/ken once per test binary and returns the path.
func kenBinary(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		goBin := filepath.Join(runtime.GOROOT(), "bin", "go")
		if _, err := os.Stat(goBin); err != nil {
			if p, lerr := exec.LookPath("go"); lerr == nil {
				goBin = p
			} else {
				buildErr = errors.New("no go toolchain found to build cmd/ken")
				return
			}
		}
		dir, err := os.MkdirTemp("", "kenbuild")
		if err != nil {
			buildErr = err
			return
		}
		buildDir = dir
		buildPath = filepath.Join(dir, "ken")
		out, err := exec.Command(goBin, "build", "-o", buildPath, "github.com/Quest-ICT/ken/cmd/ken").CombinedOutput()
		if err != nil {
			buildErr = errors.New("go build cmd/ken: " + err.Error() + ": " + string(out))
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return buildPath
}

// freePort returns a 127.0.0.1 address nothing is listening on.
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

type runResult struct {
	exited   bool // process ended on its own within the budget
	code     int
	stderr   string
	dbExists bool // the serve path creates the database file before it listens
	serving  bool // something accepted a TCP connection on the address we gave it
}

// runKen runs the binary with a clean KEN_* environment and reports what it did.
func runKen(t *testing.T, args ...string) runResult {
	t.Helper()
	bin := kenBinary(t)
	dir := t.TempDir()
	db := filepath.Join(dir, "data", "ken.db")
	addr := freePort(t)

	env := []string{}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "KEN_") {
			env = append(env, kv)
		}
	}
	env = append(env, "KEN_DB="+db, "KEN_ADDR="+addr, "KEN_METRICS=off", "HOME="+dir)

	cmd := exec.Command(bin, args...)
	cmd.Env = env
	var errBuf strings.Builder
	cmd.Stderr = &errBuf
	cmd.Stdout = &errBuf
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %v: %v", args, err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	res := runResult{}
	deadline := time.After(25 * time.Second)
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
wait:
	for {
		select {
		case <-done:
			res.exited = true
			res.code = cmd.ProcessState.ExitCode()
			break wait
		case <-deadline:
			break wait
		case <-tick.C:
			if c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
				c.Close()
				res.serving = true
				break wait
			}
		}
	}
	if !res.exited {
		_ = cmd.Process.Kill()
		<-done
	}
	if _, err := os.Stat(db); err == nil {
		res.dbExists = true
	}
	res.stderr = errBuf.String()
	return res
}

// TestUnknownSubcommandRefusesInsteadOfServing is the defect: `ken lang backfill` on a
// production host fell through to "treat the arguments as serve flags" and started a
// SECOND instance against the same database.
func TestUnknownSubcommandRefusesInsteadOfServing(t *testing.T) {
	got := runKen(t, "lang", "backfill")
	if !got.exited {
		t.Fatalf("`ken lang backfill` did not exit (serving=%v): an unknown verb started a server", got.serving)
	}
	if got.serving {
		t.Fatal("`ken lang backfill` bound the listen address")
	}
	if got.dbExists {
		t.Fatal("`ken lang backfill` opened/created the database — it entered the serve path")
	}
	if got.code != 2 {
		t.Fatalf("exit code = %d, want 2 (stderr: %s)", got.code, got.stderr)
	}
	if !strings.Contains(got.stderr, "unknown subcommand") || !strings.Contains(got.stderr, "lang") {
		t.Fatalf("stderr does not name the bad verb: %q", got.stderr)
	}
}

// TestBareKenStillServes is the POSITIVE CONTROL for the assertions above: it proves the
// harness can SEE a server start, so "no database, no listener" in the test above is an
// observation and not a vacuous pass (an unwritable path or a crashing binary would
// otherwise satisfy it). It is also the requirement in its own right — `ken` with no
// subcommand is the documented default (docs/OPERATION.md §3.2), with or without flags.
func TestBareKenStillServes(t *testing.T) {
	for _, args := range [][]string{{}, {"--demo-seed"}} {
		got := runKen(t, args...)
		if !got.serving {
			t.Fatalf("`ken %v` did not serve (exited=%v code=%d): %s", args, got.exited, got.code, got.stderr)
		}
		if !got.dbExists {
			t.Fatalf("`ken %v` served but created no database — the harness cannot see the serve path", args)
		}
	}
}

// TestKnownSubcommandsUnaffected keeps the refusal from swallowing real verbs, and
// distinguishes "refused with 2" from "every invocation now exits non-zero".
func TestKnownSubcommandsUnaffected(t *testing.T) {
	for _, args := range [][]string{{"version"}, {"help"}, {"--version"}, {"-h"}} {
		got := runKen(t, args...)
		if !got.exited || got.code != 0 {
			t.Fatalf("`ken %v`: exited=%v code=%d, want a clean exit 0: %s", args, got.exited, got.code, got.stderr)
		}
		if got.dbExists || got.serving {
			t.Fatalf("`ken %v` touched the serve path", args)
		}
	}
}

// TestVerbAfterFlagRefuses closes the other half of the same accident: flag parsing
// stops at the first non-flag, so `ken --db … snapshot` used to discard the verb and
// serve. serve takes no positional arguments.
func TestVerbAfterFlagRefuses(t *testing.T) {
	got := runKen(t, "--demo-seed", "snapshot")
	if !got.exited || got.serving {
		t.Fatalf("`ken --demo-seed snapshot` served instead of refusing (exited=%v serving=%v)", got.exited, got.serving)
	}
	if got.dbExists {
		t.Fatal("`ken --demo-seed snapshot` opened the database — it entered the serve path")
	}
	if got.code != 2 {
		t.Fatalf("exit code = %d, want 2 (stderr: %s)", got.code, got.stderr)
	}
	if !strings.Contains(got.stderr, "snapshot") {
		t.Fatalf("stderr does not name the stray argument: %q", got.stderr)
	}
}
