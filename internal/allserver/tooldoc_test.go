package allserver

import (
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Quest-ICT/ken/internal/commserver"
	"github.com/Quest-ICT/ken/internal/mcpserver"
	"github.com/Quest-ICT/ken/internal/stationserver"
	"github.com/Quest-ICT/ken/internal/tooldoc"
)

// EVERY TOOL'S ONE-LINE DESCRIPTION MUST STAND ALONE, and this is where that is checked.
//
// The rules moved out of tool descriptions into ken_instructions{tool:"…"} because a description
// freezes at conversation start and a result does not. The cost of that move is that the tool LIST
// — the only thing a session sees before it decides what to call — is now one computed sentence per
// tool. If that sentence is empty, truncated, or says nothing, a session cannot find the tool it
// needs and the pointer to the full rules never gets followed.
//
// So the extraction is asserted against every registered tool rather than spot-checked: it is a
// mechanical rule applied to ~45 hand-written texts, and the ones it handles badly are exactly the
// ones nobody would think to look at.
func TestEveryToolBriefStandsAlone(t *testing.T) {
	names := registerEverythingForDocs(t)
	if len(names) < 35 {
		t.Fatalf("only %d tools registered; this gate is meant to cover the whole surface", len(names))
	}
	for _, n := range names {
		full, ok := tooldoc.Full(n)
		if !ok {
			t.Errorf("%s: registered by name with no rules behind it", n)
			continue
		}
		brief := tooldoc.Brief(n, full)
		lead := strings.TrimSpace(strings.Split(brief, " — FULL RULES:")[0])
		switch {
		case lead == "":
			t.Errorf("%s: the one-line description is empty — the tool list would show a bare pointer", n)
		// The floor is low on purpose. Two tools genuinely say everything they need to in under
		// forty characters ("Remove a file from the locker."), and a floor that rejected them would
		// push someone to pad real prose to satisfy a test. What it still catches is the failure
		// worth catching: a fragment, or an extractor that split on an abbreviation and left a
		// stub — both of which still render as a description, which is why a human would not spot
		// them in a diff.
		case len(lead) < 24:
			t.Errorf("%s: one-line description is %d chars and says nothing useful: %q", n, len(lead), lead)
		// AND IT MUST NOT END MID-THOUGHT. The length floor catches a stub; it cannot catch a
		// fragment, because a fragment renders exactly like a sentence — kb_diff shipped 66
		// characters ending in "e.g." and passed every check here. tooldoc.firstSentence now
		// refuses to break on a known abbreviation; this asserts the outcome rather than trusting
		// the list to be complete.
		case strings.HasSuffix(lead, "e.g.") || strings.HasSuffix(lead, "i.e.") ||
			strings.HasSuffix(lead, "etc.") || strings.HasSuffix(lead, "vs."):
			t.Errorf("%s: the one-line description ends on an abbreviation and is a fragment: %q", n, lead)
		case len(lead) > 400:
			t.Errorf("%s: the extractor found no sentence break in %d chars, so the whole rules are back in "+
				"the description and nothing was shortened: %q…", n, len(lead), lead[:120])
		}
		// The pointer must name THIS tool. A copy-paste or a shared constant naming another tool
		// sends every session to the wrong rules, and the description still reads as correct.
		if !strings.Contains(brief, `ken_instructions{tool:"`+n+`"}`) {
			t.Errorf("%s: its pointer does not name it", n)
		}
	}
}

// registerEverythingForDocs registers all three packages' tools against a throwaway server, which
// is what populates the tooldoc registry, and returns every documented name.
//
// ZERO-VALUE DEPS ARE ENOUGH because registration never calls a handler — it records a name, a
// schema and a description. Building real stores here would test the stores, not the descriptions.
func registerEverythingForDocs(t *testing.T) []string {
	t.Helper()
	s := mcp.NewServer(&mcp.Implementation{Name: "doc-probe", Version: "0"}, nil)
	mcpserver.RegisterTools(s, mcpserver.Deps{})
	commserver.RegisterTools(s, commserver.Deps{}, &commserver.Handler{})
	stationserver.RegisterTools(s, stationserver.Deps{})
	return tooldoc.Names()
}
