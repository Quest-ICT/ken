package audit

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/Quest-ICT/ken/internal/tooldoc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// *** EVERY REGISTERED TOOL MUST BE CLASSIFIED, BECAUSE THE DEFAULT IS THE DEFECT. ***
//
// An mcp.Tool that sets no Annotations is advertised with DestructiveHint TRUE and OpenWorldHint
// TRUE — the SDK's own documented defaults. Ken shipped all 42 tools that way, so kb_search,
// comm_directory and ken_version each described themselves to every client as a destructive tool
// reaching an open world of external entities.
//
// ken-prod measured what that costs on 2026-09-08: a claude.ai session had three calls refused
// with "No approval received", and proved from ken_mcp_tool_calls_total that they never reached a
// handler. Anthropic's connector guidance gives read-only tools a pass from per-call confirmation
// only when the connector annotates them.
//
// THIS TEST IS THE INSTRUMENT, NOT THE TABLE. A wrong annotation is a judgement call somebody can
// argue with; an ABSENT one is invisible and defaults to the worst reading. So a tool added later
// without an entry in tooldoc's table fails here rather than quietly inheriting "destructive".
func TestEveryRegisteredToolIsAnnotated(t *testing.T) {
	// Requires the mcp.Tool shape — Name followed by Description — so the server's own
	// mcp.Implementation{Name: "ken", Title: …} is not mistaken for a tool. Without this the gate
	// reports a phantom and the real list is one entry longer than it should be.
	name := regexp.MustCompile(`Name:\s*"([a-z_0-9]+)",?\s*(?://[^\n]*\n\s*)*Description:`)
	seen := map[string]bool{}

	dirs, err := os.ReadDir("..")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range dirs {
		if !d.IsDir() || !strings.HasSuffix(d.Name(), "server") {
			continue
		}
		files, _ := os.ReadDir("../" + d.Name())
		for _, f := range files {
			if !strings.HasSuffix(f.Name(), ".go") || strings.HasSuffix(f.Name(), "_test.go") {
				continue
			}
			b, err := os.ReadFile("../" + d.Name() + "/" + f.Name())
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range name.FindAllStringSubmatch(string(b), -1) {
				seen[m[1]] = true
			}
		}
	}

	// PROVE THE SCANNER FOUND SOMETHING before trusting what it did not find. An empty sweep
	// satisfies every assertion below.
	if len(seen) < 30 {
		t.Fatalf("the scanner found only %d tool names; it is broken, not the annotations", len(seen))
	}

	var missing []string
	for n := range seen {
		if !tooldoc.IsAnnotated(n) {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		t.Errorf("%d tool(s) have no annotation and will be advertised as DESTRUCTIVE and "+
			"OPEN-WORLD, which is the SDK's default: %v\n"+
			"Add each to toolKinds in internal/tooldoc/annotations.go. A read-only tool that is "+
			"not marked read-only makes a client ask its human for permission to search.",
			len(missing), missing)
	}
}

// AND THE ANNOTATION MUST REACH THE WIRE, not merely exist in a table. The table is only useful if
// Annotate() is actually called before the tool is registered — three separate addTool wrappers
// have to do it, and a fourth server added later would silently skip it.
func TestAnnotationsAreAppliedNotJustTabulated(t *testing.T) {
	for _, tc := range []struct {
		name     string
		readOnly bool
	}{
		{"kb_search", true}, {"comm_directory", true}, {"ken_version", true},
		{"kb_save", false}, {"kb_retract", false},
	} {
		tool := &mcp.Tool{Name: tc.name, Description: "x"}
		tooldoc.Annotate(tool)
		if tool.Annotations == nil {
			t.Errorf("%s got no annotations at all", tc.name)
			continue
		}
		if tool.Annotations.ReadOnlyHint != tc.readOnly {
			t.Errorf("%s ReadOnlyHint = %v, want %v", tc.name, tool.Annotations.ReadOnlyHint, tc.readOnly)
		}
		// Ken reaches no external entity, on any tool. The SDK defaults this to TRUE.
		if tool.Annotations.OpenWorldHint == nil || *tool.Annotations.OpenWorldHint {
			t.Errorf("%s is advertised as open-world; Ken is a closed world on every tool", tc.name)
		}
	}
}
