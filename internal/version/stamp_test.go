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

// THE STAMP MUST SAY WHAT TO DO, not merely state a number — AND IT MUST FIT.
//
// A version in a wall of instructions is a number a session reads past. The things that make it
// actionable are the ones a session cannot work out for itself, and two of them are
// counter-intuitive: the text does not refresh on RECONNECT, and stale text is normal rather than
// a fault.
//
// *** THE CONTRACT IS NOW SPLIT ACROSS TWO PLACES, AND THAT IS THE POINT OF THIS EDIT. ***
//
// Every phrase below used to be required in the stamp alone, which was 1053 characters appended
// to instruction blocks of 2754, 7042 and 4714. The MCP client cuts the instructions field at
// version.InstructionBudget characters — so the stamp began past the cut on all three surfaces
// and **no session ever received a word of it.** The test passed for five months against text
// nothing could read: it asserted on the Go string, and the Go string was never the delivered one.
//
// So the split is deliberate, and it follows what each half is FOR:
//
//	the STAMP carries what a session needs BEFORE it suspects anything — that this text has a
//	version, what to call, and that neither reconnecting nor waiting refreshes it. It is short
//	because it is prepended, and under a cap position is delivery.
//
//	ken_version's TOOL DESCRIPTION carries the rest — what the freeze does and does not block —
//	because a session reads it at the moment it calls ken_version, which is the moment the
//	discrepancy is actually in front of it. Descriptions are delivered intact: all 43 of Ken's
//	are under the budget, the largest being comm_poll.
//
// The union is asserted, so nothing may quietly leave BOTH.
func TestTheStampTellsASessionWhatToDoWithADiscrepancy(t *testing.T) {
	stamp := strings.ToLower(InstructionStamp())
	desc := strings.ToLower(ToolDescription)

	// In the STAMP, because a session must have them before it knows to look anywhere.
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

	// In ken_version's DESCRIPTION, read when the discrepancy is in hand.
	for _, want := range []string{
		// THE FREEZE BLOCKS DISCOVERY, NOT TRANSMISSION — and ken-prod-ops proved it by
		// doing it. Their comm_send schema predates rooms, has no `to_room` property at
		// all and still marks channel_id required; passing `to_room` anyway WORKED,
		// because the server validates what ARRIVES, not what the client's captured copy
		// of the schema says.
		//
		// I had told them, and Vlad, that a frozen session cannot use what it cannot see.
		// Half right, and I overstated it. Without this sentence a session is told it is
		// stuck when it is only blind.
		"pass it",
		// AND that a whole new TOOL does not travel, which is the sharper limit and the
		// one that bites the mechanism itself: ken_version is a tool added in 3.1.0, so
		// no conversation begun before it can call it — precisely the sessions it exists
		// for. Vlad found that by asking me to use it and watching it not be there.
		"whole tools do not",
	} {
		if !strings.Contains(desc, want) {
			t.Errorf("ken_version's description never mentions %q, and the stamp is too small to carry it:\n%s", want, ToolDescription)
		}
	}

	// And SOMETHING must say the condition is normal. Stale instructions are the ordinary
	// condition of a long conversation; a session that treats it as breakage will reconnect,
	// which does nothing, or escalate to its human over normal operation.
	if !strings.Contains(stamp, "nothing is broken") && !strings.Contains(desc, "nothing is broken") {
		t.Error("neither the stamp nor ken_version's description says the condition is normal, so a " +
			"session meeting it will treat an older manual as breakage")
	}

	// *** AND THE STAMP MUST LEAVE ROOM FOR THE INSTRUCTIONS IT IS PREPENDED TO. ***
	//
	// This is the assertion whose absence let the old stamp consume more than half the budget
	// on the KB surface and all of it twice over on COMM. A stamp that fits is not a nicety:
	// every character it takes is a character of operational instruction that does not arrive.
	if n := len([]rune(InstructionStamp())); n > 400 {
		t.Errorf("the stamp is %d characters against a %d-character delivery budget; it is prepended to "+
			"every surface, so it spends that budget before any instruction does", n, InstructionBudget)
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

// THE VERSION MUST RIDE IN RESULTS, because the TOOL cannot reach the sessions that
// need it.
//
// ken_version shipped in 3.1.0. A conversation begun before 3.1.0 does not have it in its
// tool list and has no handle to call it — Vlad found this by asking me to use it and
// watching ToolSearch return nothing. Parameters cross the freeze; whole tools do not.
//
// So the answer rides in results a frozen session ALREADY calls, and it has to be on all
// three surfaces: a comm-only session never calls station_me, a KB-only session calls
// neither, and each of them is equally unable to call the tool.
//
// Source-level, and deliberately: this asserts the field is WIRED on every surface, which
// a behavioural test on one of them would silently not cover for the other two — the same
// reason the registration test above is written this way.
func TestTheRunningVersionRidesInResultsOnEverySurface(t *testing.T) {
	surfaces := map[string]string{
		"station_me (stations)": filepath.Join("..", "stationserver", "stationserver.go"),
		"comm_poll (comm)":      filepath.Join("..", "commserver", "commserver.go"),
		"kb_search (knowledge)": filepath.Join("..", "mcpserver", "server.go"),
	}
	for name, path := range surfaces {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		// Matched on the ASSIGNMENT, not a mention: the identifier appears in comments
		// explaining why the field exists, and a test that cannot tell a use from an
		// explanation eventually forces the explanation out.
		if !regexp.MustCompile(`KenVersion:\s+version\.Version`).Match(body) {
			t.Errorf("%s does not report the running version in its RESULT.\n"+
				"A session on that surface whose conversation predates the ken_version tool cannot "+
				"call it and has no other way to learn what it is talking to.", name)
		}
	}
}
