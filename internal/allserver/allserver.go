// Package allserver serves ALL THREE Ken surfaces from ONE MCP endpoint, so a human adds one
// connector instead of three.
//
// *** WHY THIS EXISTS. *** Vlad, on the clean-VM acceptance run: "Under the, also absurd, current
// model, 3 connectors, one for each surface, while we could have only one for the 3 surfaces."
// He is right, and the reason for the split has already been deleted.
//
// `/mcp`, `/comm/mcp` and `/station/mcp` were separate because they accepted MUTUALLY EXCLUSIVE
// credential families — a knowledge-base token could not send messages and a comm token could not
// write knowledge, enforced by `store.CheckScopeMix`. That function was deleted in 3.31.0 on
// Vlad's ruling that no surface is optional, and since 3.25.0 a single OAuth grant has carried
// `ken:kb ken:comm ken:station` together. **So one grant already authorises all three surfaces,
// and the three-way URL split became vestigial** — its whole remaining cost landing on the user
// as three connectors, three consents, and three UUID prefixes in every tool list.
//
// *** WHAT IT DOES NOT DO: REPLACE THE THREE ENDPOINTS. *** They keep working, unchanged. Prod
// holds eight bound comm endpoints and other machines are configured against the specific URLs;
// breaking them to save a connector would be a bad trade, and this is additive precisely so
// nobody has to migrate on Ken's schedule.
//
// *** THE RULE THIS ENDPOINT ENFORCES, AND WHY IT IS THE HONEST ONE. ***
//
// A credential reaching here must carry EVERY capability. That falls out of the mechanism — each
// package reads its principal from its own private context key, so all three middlewares must run
// and each fails closed on a missing scope — but it is also the right rule rather than an
// accident. This endpoint offers everything; a credential that cannot use everything should use
// the specific endpoint it was minted for, where the refusal is precise instead of confusing.
//
// It also preserves the property IDENTITY-CONTROLS.md records as surviving: transport-level scope
// enforcement fails closed, so a surface a caller cannot use is UNREACHABLE rather than
// reachable-and-erroring. A collapse that admitted partial credentials and refused per tool would
// have quietly turned that into a reconnaissance surface — the tool list would name every station
// and comm tool to a knowledge-base-only grant.
//
// *** THE INSTRUCTION BUDGET IS WHY THE TEXT BELOW IS SHORT, AND IT WAS MEASURED. ***
//
// Delivered instructions on 3.35.1: /mcp 2045 characters, /comm/mcp 2046, against a client
// delivery budget of 2048. Concatenating three surfaces' blocks would send ~6100 characters into
// a 2048 window and silently destroy two thirds of the guidance — the exact truncation defect
// 3.26.0 fixed, re-created at triple scale.
//
// So this block orients and points. It can do that because the two things it points at already
// exist and already arrive intact: per-tool rules were moved into TOOL DESCRIPTIONS in the 3.26.0
// refit, and `ken_instructions` — Vlad's own suggestion — serves the full untruncated text of any
// surface on demand. The collapse is only affordable because that tool shipped first.
package allserver

import (
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Quest-ICT/ken/internal/commserver"
	"github.com/Quest-ICT/ken/internal/mcpserver"
	"github.com/Quest-ICT/ken/internal/stationserver"
	"github.com/Quest-ICT/ken/internal/version"
)

// sessionTimeout matches the per-surface handlers: an idle session is closed rather than left
// authorized for as long as a client holds it.
const sessionTimeout = 30 * 60 * 1e9 // 30 minutes, in nanoseconds

// Instructions is the whole of what a session reads on connect here.
//
// EVERY SENTENCE EARNS ITS CHARACTERS, because there are ~2048 of them and three surfaces to
// introduce. What is deliberately absent: per-tool rules (they are in the tool descriptions, which
// arrive intact), and per-surface detail (that is what ken_instructions is for).
//
// The middle paragraph is the one that must not be cut. `session_key` shipped in 3.35.0 and a
// session whose tool list predates it will conclude the parameter does not exist — ken-prod-ops
// watched exactly that happen and it nearly stopped the acceptance run. The result field
// how_to_keep_this_workspace says it too, but only after a call; this says it before one.
const Instructions = `Ken — ONE connection, THREE surfaces. Knowledge base (kb_*), inter-session messaging (comm_*), and a durable working identity (station_*). You have all three; none is optional.

CALL station_me FIRST, EVERY SESSION, and pass session_key — a stable id for THIS conversation (in Claude Code, the UUID in your transcript or scratchpad path). It is how you return to the SAME workspace after a client restart instead of minting a new one and stranding this one. SEND IT EVEN IF YOUR TOOL SCHEMA DOES NOT LIST IT: your schema was captured when this conversation began and never refreshes, while tool RESULTS are always current.

Then, IN YOUR FIRST MESSAGE, tell your human in words what station_me says is waiting on them. A briefing nobody relays is the original problem with extra steps.

THE RULES FOR EACH TOOL ARE IN THAT TOOL'S OWN DESCRIPTION, which arrives intact. NOW CALL ken_instructions FOR EACH SURFACE YOU WILL USE AND READ THEM BEFORE YOU START WORKING — this block is all you get here, and that is the only guidance that is never truncated and never stale.

SEARCH KEN BEFORE DEBUGGING ANYTHING NON-TRIVIAL (kb_search), record an outcome after you act on an entry (kb_record_outcome), and save durable lessons back (kb_save). Your writes are PROPOSALS; a human promotes them. You never curate.`

// Deps carries the three surfaces' dependency sets. Each is built exactly as it is for that
// surface's own endpoint — this package adapts nothing, so the unified endpoint cannot drift from
// the specific ones in behaviour.
type Deps struct {
	KB      mcpserver.Deps
	Comm    commserver.Deps
	CommH   *commserver.Handler
	Station stationserver.Deps
}

// NewHTTPHandler builds the unified endpoint: one MCP server carrying every tool, behind all three
// surfaces' authentication chained together.
//
// THE TOOLS ARE THE SAME DEFINITIONS, not copies. Each package's RegisterTools is called against
// this one server, so a tool's description, schema and handler are identical here and on its own
// endpoint. A second copy would drift, and a tool that says different things depending on which
// URL reached it is the defect class this project keeps paying for.
func NewHTTPHandler(d Deps) http.Handler {
	s := mcp.NewServer(&mcp.Implementation{
		Name:    "ken",
		Title:   "Ken — knowledge base, messaging and working identity",
		Version: version.Version,
	}, &mcp.ServerOptions{
		Instructions: version.InstructionStamp() + Instructions,
		KeepAlive:    30 * 1e9,
	})

	mcpserver.RegisterTools(s, d.KB)
	if d.CommH != nil {
		commserver.RegisterTools(s, d.Comm, d.CommH)
	}
	stationserver.RegisterTools(s, d.Station)

	inner := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return s },
		&mcp.StreamableHTTPOptions{SessionTimeout: sessionTimeout})

	// CHAINED, INNERMOST LAST. Each middleware authenticates the same bearer against its own
	// surface's requirement and injects its own principal, so by the time the request reaches the
	// server all three are present. A credential missing any capability is refused at the
	// transport by whichever link needs it — which is the fail-closed property, kept.
	h := stationserver.AuthMiddleware(d.Station, inner)
	if d.CommH != nil {
		h = commserver.AuthMiddleware(d.Comm, h)
	}
	return mcpserver.AuthMiddleware(d.KB, h)
}
