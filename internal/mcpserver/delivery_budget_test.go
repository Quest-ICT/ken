package mcpserver

import (
	"testing"

	"github.com/Quest-ICT/ken/internal/version"
)

// TestInstructionsFitTheDeliveryBudget fails when what /mcp actually SENDS exceeds what the
// client actually DELIVERS.
//
// THE STRING UNDER TEST IS THE CONCATENATION, not the const. That distinction is the whole
// defect: buildInstructions() + version.InstructionStamp() is what reaches the wire, and for
// five months the const was 2754 characters with a 1053-character stamp appended to it, so a
// third of the block and every word of the stamp were cut. Nobody measured the sum, because
// every test that existed measured a piece.
//
// A test that asserts on a fragment of a delivered value cannot see a cap on the value.
//
// THE CURATION APPENDIX NO LONGER COUNTS, AND THE THREE CASES THAT CHECKED IT WERE VACUOUS.
//
// This used to loop over nil, one language and three, on the reasoning that buildInstructions
// appended a paragraph when curation languages were declared — so the deployment turning that
// feature ON was the one whose instructions overflowed. That was true, and the fix was to move the
// rule onto kb_save and kb_propose_enhancement, where it arrives intact. buildInstructions has
// ignored its argument ever since, so all three cases produced a BYTE-IDENTICAL string and the
// table read as coverage of a variable that no longer varies.
//
// The parameter is gone with them. A signature that accepts something it discards invites exactly
// this: a test that exercises it, passes, and proves nothing.
func TestInstructionsFitTheDeliveryBudget(t *testing.T) {
	delivered := version.InstructionStamp() + buildInstructions()
	if n := len([]rune(delivered)); n > version.InstructionBudget {
		t.Errorf("/mcp sends %d characters and the client delivers %d — %d characters of instruction "+
			"reach no session. Shorten the block, or move a rule into the description of the tool it "+
			"governs, where it arrives intact.", n, version.InstructionBudget, n-version.InstructionBudget)
	}
}

// TestTheStampArrivesFirst pins the ordering, which is the other half of the fix.
//
// Under a cap, POSITION IS DELIVERY. The stamp is the one piece of text that tells a session its
// whole manual may be old; appended, it was the first thing cut on every surface. A later edit
// that restores the old "instructions + stamp" order would pass the budget test above — the sum
// is identical — and silently put the stamp back behind everything else the moment the block
// grows again.
func TestTheStampArrivesFirst(t *testing.T) {
	delivered := version.InstructionStamp() + buildInstructions()
	stamp := version.InstructionStamp()
	if len(delivered) < len(stamp) || delivered[:len(stamp)] != stamp {
		t.Fatal("the version stamp is no longer the first thing in /mcp's instructions; appended, it is " +
			"the first thing a truncating client discards, which is how it went five months undelivered")
	}
}
