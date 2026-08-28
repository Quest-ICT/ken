package audit

import (
	"os"
	"strings"
	"testing"
)

// TestEveryServerWiresTheInstructionRefetch fails when a server can serve instructions but cannot
// hand them back.
//
// *** THE HAZARD INVERTED, AND THE OLD TEST WOULD NOT HAVE NOTICED. ***
//
// It used to read: three packages each registered ken_version and ken_instructions, the pair had to
// be kept in step BY HAND, and a package that forgot one failed invisibly — a session on that
// endpoint simply never learned the door existed. So this scanned all three sources for the
// registration.
//
// Then the three endpoints became one. mcp.AddTool "adds a Tool to the server, OR REPLACES ONE WITH
// THE SAME NAME", so three registrations against one server left whichever ran last, and
// ken_instructions returned a third of the guidance while looking entirely complete. The old test
// was green throughout: it checked that each package registered the tool, which all three did, and
// that was exactly the defect.
//
// One registration site now (version.RegisterMetaTools) removes the drift this test was written
// for, and creates the opposite one: a server constructor that forgets to CALL it. That failure is
// just as quiet — the tool is simply absent, and absence is what a session cannot distinguish from
// "this deployment does not have it". So the gate moved to the call sites.
//
// SOURCE-SCANNED ON PURPOSE. A behavioural test would need each package's full dependency set built
// twice over; what is actually at risk is one line in one constructor, and the cheap instrument
// that reads that line is worth more than the expensive one nobody runs.
func TestEveryServerWiresTheInstructionRefetch(t *testing.T) {
	// Every file that constructs an MCP server. The unified one is what production mounts; the
	// other three build the per-surface servers the tests drive.
	servers := map[string]string{
		"unified /mcp": "../allserver/allserver.go",
		"knowledge":    "../mcpserver/server.go",
		"comm":         "../commserver/commserver.go",
		"station":      "../stationserver/stationserver.go",
	}
	for name, path := range servers {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		src := string(b)
		// POSITIVE CONTROL. If this file stopped building a server, every assertion below would be
		// vacuous and the test would report health while checking a file that does nothing.
		if !strings.Contains(src, "mcp.NewServer(") {
			t.Errorf("%s (%s) no longer constructs an MCP server; this gate is reading the wrong file", name, path)
			continue
		}
		if !strings.Contains(src, "version.RegisterMetaTools(s,") {
			t.Errorf("%s does not wire version.RegisterMetaTools, so it serves neither ken_version nor "+
				"ken_instructions. A session there cannot ask what is running or re-read its own "+
				"guidance, and — the part that makes it invisible — has no way to tell that from a "+
				"deployment where those tools were never built.", name)
		}
		// AND NOBODY REGISTERS THEM BY HAND AGAIN. A second registration under either name would be
		// silently accepted by the SDK and would win or lose depending on ordering, which is the
		// exact defect the single site exists to end.
		for _, forbidden := range []string{`Name:        "ken_version"`, `Name:        "ken_instructions"`} {
			if strings.Contains(src, forbidden) {
				t.Errorf("%s registers %s directly; it must go through version.RegisterMetaTools, or "+
					"two registrations of one name will resolve by whichever ran last", name, forbidden)
			}
		}
	}
}
