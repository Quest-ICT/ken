package commserver

import (
	"testing"

	"github.com/Quest-ICT/ken/internal/version"
)

// TestInstructionsFitTheDeliveryBudget: what /comm/mcp SENDS against what the client DELIVERS.
//
// COMM was the worst of the three — 7042 characters plus a 1053-character stamp, of which 2048
// arrived. **Three quarters of this surface's instructions had never been read by any session**,
// including the rule about writing the endpoint secret to a 0600 file, which is the one whose
// absence costs a session its identity.
func TestInstructionsFitTheDeliveryBudget(t *testing.T) {
	delivered := version.InstructionStamp() + instructions
	if n := len([]rune(delivered)); n > version.InstructionBudget {
		t.Errorf("/comm/mcp sends %d characters and the client delivers %d — %d characters reach no "+
			"session. Move a rule into the description of the tool it governs.",
			n, version.InstructionBudget, n-version.InstructionBudget)
	}
}
