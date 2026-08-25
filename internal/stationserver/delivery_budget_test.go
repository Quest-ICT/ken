package stationserver

import (
	"testing"

	"github.com/Quest-ICT/ken/internal/version"
)

// TestInstructionsFitTheDeliveryBudget: what /station/mcp SENDS against what the client DELIVERS.
//
// STATION delivered 4714 characters plus a 1053-character stamp as 2048 — and the block never
// located itself at all: it said nothing about /station, /mcp, endpoints, registration or scopes,
// so a session reaching it was never told it is a separate registration from the other two.
func TestInstructionsFitTheDeliveryBudget(t *testing.T) {
	delivered := version.InstructionStamp() + instructions
	if n := len([]rune(delivered)); n > version.InstructionBudget {
		t.Errorf("/station/mcp sends %d characters and the client delivers %d — %d characters reach no "+
			"session. Move a rule into the description of the tool it governs.",
			n, version.InstructionBudget, n-version.InstructionBudget)
	}
}
