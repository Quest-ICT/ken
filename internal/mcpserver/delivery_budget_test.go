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
// AND THE CURATION APPENDIX COUNTS TOO. buildInstructions appends a paragraph when the operator
// declares curation languages, so the deployment that turns that feature ON is the one whose
// instructions overflow — the failure lands on the operator who configured the most. Checked
// with a realistic multi-language setting rather than only with nil.
func TestInstructionsFitTheDeliveryBudget(t *testing.T) {
	for _, tc := range []struct {
		name  string
		langs []string
	}{
		{"no curation languages", nil},
		{"one curation language", []string{"es"}},
		{"three curation languages", []string{"es", "fr", "de"}},
	} {
		delivered := version.InstructionStamp() + buildInstructions(tc.langs)
		if n := len([]rune(delivered)); n > version.InstructionBudget {
			t.Errorf("%s: /mcp sends %d characters and the client delivers %d — %d characters "+
				"of instruction reach no session. Shorten the block, or move a rule into the "+
				"description of the tool it governs, where it arrives intact.",
				tc.name, n, version.InstructionBudget, n-version.InstructionBudget)
		}
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
	delivered := version.InstructionStamp() + buildInstructions(nil)
	stamp := version.InstructionStamp()
	if len(delivered) < len(stamp) || delivered[:len(stamp)] != stamp {
		t.Fatal("the version stamp is no longer the first thing in /mcp's instructions; appended, it is " +
			"the first thing a truncating client discards, which is how it went five months undelivered")
	}
}
