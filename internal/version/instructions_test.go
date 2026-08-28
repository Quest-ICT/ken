package version

import (
	"strings"
	"testing"

	"github.com/Quest-ICT/ken/internal/tooldoc"
)

// TestInstructionsResultCarriesWhatTheConnectTimeCopyCannot.
//
// THE POINT OF THE TOOL IS THAT A RESULT IS NEITHER OLD NOR CUT. Everything else about instruction
// delivery has one of those two problems: the connect-time field is captured when a conversation
// begins and never refreshes, and the client truncates it at InstructionBudget characters. A tool
// result has neither property, which makes it the only channel that can carry the full current
// text to a session that already exists.
//
// Suggested by Vlad on 2026-08-25, hours after we found an MCP registration on two machines
// serving pre-3.22.0 instructions and pre-3.22.0 tool descriptions against a fully patched server,
// in conversations that began AFTER the upgrade — while its ken_version RESULT came back
// completely current. No Ken release can reach that captured text. This can.
func TestInstructionsResultCarriesWhatTheConnectTimeCopyCannot(t *testing.T) {
	// A block deliberately LONGER than what a client delivers, because that is the case the
	// tool exists for: the result must not be trimmed to the budget the connect-time copy obeys.
	long := strings.Repeat("x", InstructionBudget+500)
	// A registered tool, so the `tools` assertion below has something to find. This package holds
	// the registry's only other reader; nothing else here populates it.
	tooldoc.Register("kb_search", "Search the knowledge base. Longer rules follow this sentence.")
	got := InstructionsFor(long)

	if got.Instructions != long {
		t.Fatalf("the result carries %d characters of a %d-character block — a result that truncates "+
			"is the connect-time field with extra steps", len(got.Instructions), len(long))
	}
	if got.Version != Version {
		t.Errorf("version = %q, want %q — the number is what a session compares against its captured copy", got.Version, Version)
	}
	// THE SURFACE AND SURFACES FIELDS ARE GONE, with the endpoints they distinguished. /comm/mcp
	// and /station/mcp were deleted; there is one machine surface and it carries every tool, so a
	// field whose only job was telling several apart would go on answering a question no session
	// can ask. What replaced them is the TOOL list, which is the thing a session now needs to be
	// told: descriptions were shortened to one sentence and a pointer, so this is how it learns
	// what it may ask about in full.
	if len(got.Tools) == 0 {
		t.Error("the answer names no tools; with per-tool rules behind ken_instructions{tool:\"…\"}, " +
			"a session with no list has no way to discover what it can ask for")
	}
	// AND IT MUST SAY WHAT TO DO WITH A DIFFERENCE. A session that finds one and is told nothing
	// reconnects, which does not help — the counter-intuitive fact the stamp exists to carry.
	for _, want := range []string{"CURRENT", "Reconnecting does not"} {
		if !strings.Contains(got.Note, want) {
			t.Errorf("the note never says %q, so a session that finds a difference has no instruction attached", want)
		}
	}
}

// TestTheRefetchPathIsReachableFromAFrozenSession.
//
// TWO DOORS, DELIBERATELY, because they reach different populations and neither covers both:
//
//	ken_instructions   — the obvious one, and it does NOT travel. A tool registered after a
//	                     conversation began is absent from that conversation's list forever.
//	ken_version's      — reaches sessions frozen BEFORE that tool existed, because parameters do
//	include_instructions travel: the server validates what ARRIVES, not the captured schema.
//	                     ken-prod-ops proved that by passing `to_room` to a comm_send schema with
//	                     no such property.
//
// So the argument is not a convenience duplicate of the tool. It is the only door for the exact
// population this feature was built for, and deleting it as redundant would close that door
// silently. This pins both, and pins the reason.
func TestTheRefetchPathIsReachableFromAFrozenSession(t *testing.T) {
	if !strings.Contains(InstructionsToolDescription, "truncat") {
		t.Error("ken_instructions' description never says the connect-time copy is truncated — that is " +
			"half of why a session should call it, and the half nobody guesses")
	}
	if !strings.Contains(strings.ToLower(ToolDescription), "whole tools do not") {
		t.Error("ken_version's description no longer explains that whole tools do not travel; without it " +
			"a frozen session has no reason to try an argument its schema does not list")
	}
	// The argument must stay OPTIONAL. ken_version is the cheap call a session makes to discover
	// it is stale; making it always carry kilobytes is how sessions stop making it.
	var in InstructionsIn
	if in.Wants() {
		t.Error("include_instructions defaults to true; ken_version must stay cheap or it stops being called")
	}

	// *** IT MUST ACCEPT WHAT A SCHEMA-LESS CLIENT SENDS, WHICH IS A STRING. ***
	//
	// ken-prod-ops ran this from inside the population the argument exists for and it failed:
	// their client had no schema for the property, so it serialized true as "true", and schema
	// validation rejected the call on type. The argument crossed the freeze — the premise holds —
	// and then died at the door.
	//
	// A client with no schema for a property CANNOT type that property correctly. That is the
	// definition of the case, not a client bug, so a boolean-only contract is wrong for exactly
	// the callers this serves.
	for _, yes := range []any{true, "true", "TRUE", " true ", "yes", "1", "on", float64(1)} {
		if !(InstructionsIn{IncludeInstructions: yes}).Wants() {
			t.Errorf("%#v does not read as yes — a frozen session sending it gets no instructions and no "+
				"reason why", yes)
		}
	}
	// AND LENIENT IN ONE DIRECTION ONLY. Guessing wrong toward yes costs a few kilobytes in one
	// result; toward no it silently withholds the fix a session just asked for.
	for _, no := range []any{false, "false", "no", "0", "", nil, float64(0), []string{"true"}} {
		if (InstructionsIn{IncludeInstructions: no}).Wants() {
			t.Errorf("%#v reads as yes; ken_version is the cheap call sessions make to notice they are "+
				"stale, and it must stay cheap by default", no)
		}
	}
}
