package audit

import (
	"os"
	"strings"
	"testing"
)

// TestEverySurfaceOffersTheInstructionRefetch fails when a surface can serve instructions but
// cannot hand them back.
//
// The three registrations of ken_version already had to be kept in step by hand; adding a second
// shared tool doubles the chance one is forgotten, and a surface missing it fails INVISIBLY — a
// session on that endpoint simply never learns the door exists, which is the same shape as the
// stamp nobody could read. Both doors are checked, per surface, because they reach different
// populations: the tool serves conversations that begin from now on, the argument reaches those
// that began before it existed.
func TestEverySurfaceOffersTheInstructionRefetch(t *testing.T) {
	surfaces := map[string]string{
		"/mcp":         "../mcpserver/server.go",
		"/comm/mcp":    "../commserver/commserver.go",
		"/station/mcp": "../stationserver/stationserver.go",
	}
	for name, path := range surfaces {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		src := string(b)
		if !strings.Contains(src, `Name:        "ken_instructions"`) {
			t.Errorf("%s does not register ken_instructions — a session there can never re-read its "+
				"own instructions, and will not know that is even possible", name)
		}
		if !strings.Contains(src, "in.Wants()") {
			t.Errorf("%s's ken_version ignores include_instructions — sessions frozen before "+
				"ken_instructions existed have no way in at all, and they are the population it is for", name)
		}
		if !strings.Contains(src, `version.InstructionsFor("`+name+`"`) {
			t.Errorf("%s does not build its answer for its own surface name; a session holding several "+
				"endpoints could not tell which text it got back", name)
		}
	}
}
