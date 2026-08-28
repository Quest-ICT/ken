package allserver

import (
	"regexp"
	"strings"
	"testing"

	"github.com/Quest-ICT/ken/internal/version"
)

// *** THE MERGED INSTRUCTIONS MUST FIT WHAT A CLIENT ACTUALLY DELIVERS. ***
//
// This is the constraint that shaped the whole collapse, and it was measured rather than assumed:
// on 3.35.1 the delivered instructions were 2045 characters on /mcp and 2046 on /comm/mcp, against
// a 2048 budget. Concatenating three surfaces' blocks would have sent ~6100 characters into a 2048
// window and silently destroyed two thirds of the guidance on every surface at once — the exact
// truncation defect 3.26.0 fixed, re-created at triple scale.
//
// The stamp counts too, because it is prepended to the same field.
func TestMergedInstructionsFitTheDeliveryBudget(t *testing.T) {
	delivered := version.InstructionStamp() + Instructions
	if n := len([]rune(delivered)); n > version.InstructionBudget {
		t.Errorf("the unified instructions deliver %d characters against a %d budget — %d over. "+
			"A session reading this endpoint would lose the tail silently, and the tail is where "+
			"ken_instructions is named.", n, version.InstructionBudget, n-version.InstructionBudget)
	}
}

// *** THE THREE SENTENCES THIS BLOCK EXISTS TO CARRY. ***
//
// It is short because it has to be, so what survives the shortening is a decision rather than an
// accident. Each of these was in the block for a measured reason, and an edit that drops one
// should have to say so.
func TestTheMergedInstructionsKeepWhatTheyExistToSay(t *testing.T) {
	for _, c := range []struct{ needle, why string }{
		{"session_key", "without this a session cannot return to its workspace after a restart, " +
			"and the parameter is missing from every tool schema older than 3.35.0"},
		{"DOES NOT LIST IT", "ken-prod-ops watched a session conclude session_key did not exist " +
			"because its schema predated it; this sentence is the only thing that arrives BEFORE a call"},
		{"ken_instructions", "this block is deliberately thin — if it does not point at the full " +
			"guidance, the guidance is unreachable"},
		{"station_me", "it is the call every session must make first, and the only one that " +
			"does not need a workspace already"},
		{"INVENT", "a claude.ai chat CANNOT see its own conversation id — verified 2026-08-26 by " +
			"asking one directly, which reported 'I have no access to a conversation id' and noted " +
			"it can retrieve an id for any PAST conversation but not the one it is in. Without this " +
			"sentence a chat session has no way to hold an identity at all, and every reload strands " +
			"another workspace"},
		{"STATE IT IN YOUR REPLY", "the transcript is the only conversation-scoped thing a chat " +
			"session can persist — its own words. A key that lives only in a tool call is lost on " +
			"reload, so telling it to invent one is useless without telling it where to put one"},
	} {
		if !strings.Contains(Instructions, c.needle) {
			t.Errorf("the merged instructions no longer mention %q: %s", c.needle, c.why)
		}
	}
}

// THE BLOCK MUST NOT REGROW INTO THE THING IT REPLACED. Three surfaces' worth of per-tool detail
// belongs in ken_instructions, where a result is never truncated and never stale; pulling it back
// here is how the budget gets spent without anyone noticing. A generous ceiling, well under the
// hard budget, so ordinary edits pass and a wholesale paste does not.
func TestTheMergedInstructionsStaySmall(t *testing.T) {
	const ceiling = 1600
	if n := len([]rune(Instructions)); n > ceiling {
		t.Errorf("the merged block is %d characters, over the %d ceiling. It is meant to ORIENT and "+
			"POINT: every per-tool rule belongs in ken_instructions{tool:\"…\"}, which is computed per "+
			"call. Growing here is how three blocks became 6100 characters in the first place.", n, ceiling)
	}
}

// EVERY SURFACE MUST BE NAMED. A session arriving here holds all three and has no other way to
// learn that comm_* and station_* exist — there is no second connector to reveal them.
func TestTheMergedInstructionsNameAllThreeSurfaces(t *testing.T) {
	for _, prefix := range []string{"kb_", "comm_", "station_"} {
		if !regexp.MustCompile(regexp.QuoteMeta(prefix)).MatchString(Instructions) {
			t.Errorf("the merged instructions never mention %q, so a session on the unified "+
				"endpoint may never discover that surface exists", prefix)
		}
	}
}
