package main

import "testing"

// surface pairs a variable with THE FUNCTION THE SERVER ACTUALLY CALLS.
//
// This indirection is the entire point of the file. The first version of these tests
// asserted envBoolDefault(key, true) — which passes the expected default in as an
// argument, and therefore proves only that envBoolDefault returns what it was given.
// Changing main.go to default either surface OFF would have left every test here
// green while a default install served neither one.
//
// It is the same shape as a totality guard that guards the mapping instead of the
// detector, and as an oracle test where every call failed at authentication so all
// the refusals matched for the wrong reason. Both of those shipped in this project.
// A test has to ask the question the server asks, not a question shaped like it.
type surface struct {
	env string
	on  func() bool
}

func surfaces() []surface {
	return []surface{
		{"KEN_COMM_ENABLED", commEnabled},
		{"KEN_STATION_ENABLED", stationsEnabledFlag},
	}
}

// COMM and stations are CORE: a default install gets both without asking.
//
// They shipped opt-in, so a plain `ken serve` was the curated knowledge base and
// nothing else. That reasoning expired — stations read COMM for the hearsay marker,
// the operator console carries a page for each, and every deployment was expected to
// switch them on, which makes an option in name only.
//
// This test exists because the default IS the behaviour now. Nothing else observes
// it: the wiring reads an environment variable that is absent on every developer
// machine and in CI, so a flipped default produces a green suite and a server that
// quietly serves neither surface.
func TestCommAndStationsAreOnWithoutAnyEnvironment(t *testing.T) {
	for _, s := range surfaces() {
		t.Setenv(s.env, "")
		if !s.on() {
			t.Errorf("%s unset resolves to OFF — a default install would serve neither the MCP endpoint nor the console, and no other test would notice", s.env)
		}
	}
}

// The opt-out is kept deliberately, so it has to keep working.
//
// Ken ALREADY has a runtime "COMM off" state: an unopenable comm.db degrades into it
// on purpose, so an expendable database cannot take the durable knowledge base down.
// Deleting the variable would not remove that state — only the operator's control of
// it, which is their one remedy if COMM misbehaves in production.
func TestEitherSurfaceCanStillBeTurnedOff(t *testing.T) {
	for _, s := range surfaces() {
		for _, off := range []string{"0", "false", "off", "no"} {
			t.Setenv(s.env, off)
			if s.on() {
				t.Errorf("%s=%q did not turn the surface off — an operator who needs it down has no way to put it down", s.env, off)
			}
		}
		// Existing deployments set these to "1" to switch the surfaces ON under the
		// old meaning. That must keep working: an upgrade turning a surface off
		// because its unit file says the old thing would be the worst possible way to
		// deliver "this is now core".
		t.Setenv(s.env, "1")
		if !s.on() {
			t.Errorf("%s=1 turned the surface off — every deployment that opted IN under the old meaning would lose it on upgrade", s.env)
		}
		// A typo must not silently disable a core surface. Failing OPEN is right
		// precisely because these are no longer optional: ignoring a malformed value
		// costs a surface that stays up, honouring it costs a feature that vanishes
		// for a reason the operator cannot see.
		t.Setenv(s.env, "yes-please")
		if !s.on() {
			t.Errorf("%s with an unrecognised value turned the surface off — a typo must not disable core functionality", s.env)
		}
	}
}

// The two are independent, and both defaulting to on is exactly when that gets
// forgotten. STATIONS.md S2 is explicit: the notebook and the task list are valuable
// to a solo session with no peers, so stations must never be gated behind a messaging
// feature they have nothing to do with.
func TestTurningCommOffLeavesStationsOn(t *testing.T) {
	t.Setenv("KEN_COMM_ENABLED", "0")
	t.Setenv("KEN_STATION_ENABLED", "")
	if commEnabled() {
		t.Fatal("setup: COMM did not turn off, so this test cannot discriminate")
	}
	if !stationsEnabledFlag() {
		t.Error("disabling COMM also disabled stations — the notebook and task list are not a messaging feature (S2)")
	}
}
