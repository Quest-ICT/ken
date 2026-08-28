package version

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Quest-ICT/ken/internal/tooldoc"
)

// RegisterMetaTools registers ken_version and ken_instructions — ONCE, for the whole server.
//
// *** THEY WERE REGISTERED THREE TIMES, AND THE SDK RESOLVES THAT SILENTLY. ***
//
// Each surface package registered its own pair, which was correct while /mcp, /comm/mcp and
// /station/mcp were three servers. They were collapsed into one endpoint, and mcp.AddTool
// "adds a Tool to the server, OR REPLACES ONE WITH THE SAME NAME" — so the last package to
// register won, and ken_instructions on the unified surface returned the STATION block and nothing
// else. A session asking for its instructions got a third of them, correctly formatted, with no
// indication that two thirds were missing. Exactly the class this project keeps paying for: a
// failure rendered identically to success.
//
// connectText returns the CURRENT connect-time block. A function rather than a string because
// mcpserver rebuilds its instructions live when the curation languages change, and a captured copy
// would go stale in the one tool whose entire purpose is not being stale.
func RegisterMetaTools(s *mcp.Server, connectText func() string) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "ken_version",
		Description: ToolDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in InstructionsIn) (*mcp.CallToolResult, Info, error) {
		out := Current()
		// THE ARGUMENT IS THE ESCAPE HATCH FOR SESSIONS THAT CANNOT SEE ken_instructions.
		// Whole tools do not travel across the freeze; parameters do, because the server validates
		// what ARRIVES rather than the client's captured schema. So a session frozen before
		// ken_instructions existed can still ask for the current text here.
		if in.Wants() {
			i := InstructionsFor(connectText())
			out.Instructions = &i
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "ken_instructions",
		Description: InstructionsToolDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in InstructionsRequest) (*mcp.CallToolResult, InstructionsInfo, error) {
		name := strings.TrimSpace(in.Tool)
		if name == "" {
			return nil, InstructionsFor(connectText()), nil
		}
		full, ok := tooldoc.Full(name)
		if !ok {
			// NAMES THE ALTERNATIVES RATHER THAN JUST REFUSING. A session that mistypes a tool
			// name, or asks about one this deployment does not serve, is one step from the right
			// call — and the list is the same list a no-argument call returns, so the recovery
			// never needs a second round trip.
			return nil, InstructionsInfo{}, fmt.Errorf(
				"no tool named %q is served here. Ask about one of: %s", name, strings.Join(tooldoc.Names(), ", "))
		}
		return nil, InstructionsFor(full).forTool(name), nil
	})
}
