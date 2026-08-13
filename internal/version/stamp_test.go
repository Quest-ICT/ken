package version

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// THE STAMP AND THE TOOL MUST NAME THE SAME VERSION, or the check they exist to enable
// is a comparison between two things that were never comparable.
//
// This is the whole mechanism in one assertion: the instructions say which version wrote
// them, the tool says which is running, and a session compares two strings. If they are
// derived from different sources they can disagree while the server is perfectly
// consistent, and the session would chase a drift that is not there.
func TestTheStampAndTheToolReportTheSameVersion(t *testing.T) {
	stamp := InstructionStamp()
	if !strings.Contains(stamp, Version) {
		t.Fatalf("the instruction stamp does not name the running version (%s):\n%s", Version, stamp)
	}
	info := Current()
	if info.Version != Version {
		t.Fatalf("ken_version reports %q while the build is %q", info.Version, Version)
	}
	if !strings.Contains(info.HowToCheck, Version) {
		t.Error("the how-to-check line does not name the version to compare against, so a session " +
			"is told to compare and not told what with")
	}
}

// THE STAMP MUST SAY WHAT TO DO, not merely state a number.
//
// A version in a wall of instructions is a number a session reads past. The three things
// that make it actionable are the ones a session cannot work out for itself, and two of
// them are counter-intuitive: the text does not refresh on RECONNECT, and stale text is
// normal rather than a fault.
func TestTheStampTellsASessionWhatToDoWithADiscrepancy(t *testing.T) {
	stamp := strings.ToLower(InstructionStamp())
	for _, want := range []string{
		"ken_version",  // what to call
		"reconnect",    // that reconnecting does NOT help — the counter-intuitive part
		"tool descrip", // that descriptions are pinned too, not just this text
		"result",       // that results ARE current, so something is still trustworthy
	} {
		if !strings.Contains(stamp, want) {
			t.Errorf("the stamp never mentions %q — a session reading it cannot act on it:\n%s", want, InstructionStamp())
		}
	}
	// And it must NOT read as an alarm. Stale instructions are the ordinary condition of
	// a long conversation; a session that treats it as breakage will reconnect, which
	// does nothing, or escalate to its human over normal operation.
	if !strings.Contains(stamp, "nothing is broken") {
		t.Error("the stamp does not say the condition is normal, so a session meeting it will treat " +
			"an ordinary state as a fault")
	}
}

// EVERY SURFACE REGISTERS THE TOOL AND STAMPS ITS INSTRUCTIONS.
//
// A session may hold a credential for exactly one of the three, and "what am I talking
// to" is the same question on all of them. A surface that answers it and a surface that
// does not is worse than neither, because the session that most needs the answer — one
// bound to a single endpoint — is the one likeliest to be on the surface that was
// forgotten.
//
// Source-level, and deliberately so: this asserts REGISTRATION, which is what a
// behavioural test on one surface would silently not cover for the other two.
func TestEverySurfaceOffersTheVersionToolAndStampsItsInstructions(t *testing.T) {
	surfaces := map[string]string{
		"knowledge base": filepath.Join("..", "mcpserver", "server.go"),
		"comm":           filepath.Join("..", "commserver", "commserver.go"),
		"stations":       filepath.Join("..", "stationserver", "stationserver.go"),
	}
	for name, path := range surfaces {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		src := string(body)
		// Matched on the REGISTRATION, not on a mention: the name appears in comments
		// explaining the mechanism, and a test that cannot tell a use from an
		// explanation eventually forces the explanation out.
		if !regexp.MustCompile(`Name:\s+"ken_version"`).MatchString(src) {
			t.Errorf("the %s surface does not register ken_version — a session holding only that "+
				"credential cannot ask what it is talking to", name)
		}
		if !strings.Contains(src, "version.InstructionStamp()") {
			t.Errorf("the %s surface does not stamp its instructions, so a session there has nothing "+
				"to compare the tool's answer against", name)
		}
	}
}
