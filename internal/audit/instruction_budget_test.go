package audit

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/Quest-ICT/ken/internal/version"
)

// TestNoToolDescriptionExceedsTheDeliveryBudget fails when a tool description grows past what the
// MCP client delivers.
//
// TOOL DESCRIPTIONS ARE WHERE THE INSTRUCTION BLOCKS' CONTENT WENT, so the budget follows it
// there. The refit moved per-tool rules out of three truncated instruction blocks and into the
// descriptions of the tools they govern, on the measured basis that all 43 arrive intact — the
// largest was comm_poll at 1578 characters against a 2048 budget. That headroom is now spent
// deliberately rather than accidentally, and this is what stops the next edit from re-creating
// the original defect one level down.
//
// The failure mode being prevented is exact: a rule moved somewhere safe, then quietly pushed
// past the cut by the next paragraph appended after it, with nothing failing and no session ever
// reading it again.
func TestNoToolDescriptionExceedsTheDeliveryBudget(t *testing.T) {
	files, err := os.ReadDir("..")
	if err != nil {
		t.Fatal(err)
	}
	desc := regexp.MustCompile(`Name:\s*"([a-z_0-9]+)"[^}]*?Description:\s*((?:"(?:[^"\\]|\\.)*"\s*\+?\s*)+)`)
	lit := regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`)

	seen := 0
	for _, dir := range files {
		if !dir.IsDir() || !strings.HasSuffix(dir.Name(), "server") {
			continue
		}
		entries, _ := os.ReadDir("../" + dir.Name())
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			b, err := os.ReadFile("../" + dir.Name() + "/" + e.Name())
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range desc.FindAllStringSubmatch(string(b), -1) {
				var sb strings.Builder
				for _, p := range lit.FindAllStringSubmatch(m[2], -1) {
					sb.WriteString(strings.ReplaceAll(strings.ReplaceAll(p[1], `\n`, "\n"), `\"`, `"`))
				}
				seen++
				if n := len([]rune(sb.String())); n > version.InstructionBudget {
					t.Errorf("%s's description is %d characters against a %d-character delivery budget — "+
						"%d characters of it reach no session. It is the tool's own rules that get cut, "+
						"at the moment the tool is used.", m[1], n, version.InstructionBudget, n-version.InstructionBudget)
				}
			}
		}
	}
	// POSITIVE CONTROL: a regexp that stops matching would pass by measuring nothing, which is
	// the failure this whole family of checks exists to prevent.
	if seen < 35 {
		t.Fatalf("only %d tool descriptions parsed; the scanner is broken, not the descriptions", seen)
	}
}
