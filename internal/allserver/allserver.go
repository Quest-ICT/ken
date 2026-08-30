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
	"sync/atomic"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Quest-ICT/ken/internal/commserver"
	"github.com/Quest-ICT/ken/internal/mcpserver"
	"github.com/Quest-ICT/ken/internal/stationserver"
	"github.com/Quest-ICT/ken/internal/store"
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
// how_to_keep_this_station says it too, but only after a call; this says it before one.
const Instructions = `Ken — ONE connection, THREE surfaces. Knowledge base (kb_*), inter-session messaging (comm_*), and a durable working identity (station_*). You have all three; none is optional.

CALL station_me FIRST, EVERY SESSION, and pass session_key — a stable id for THIS conversation. In Claude Code it is the UUID in your transcript or scratchpad path. IF YOU HAVE NO SUCH ID (a claude.ai chat cannot see its own conversation id): INVENT one random string, use it for the whole conversation, and STATE IT IN YOUR REPLY to your human — the transcript is what survives a reload, so a key you only put in a tool call is a key you lose. SEND IT EVEN IF YOUR TOOL SCHEMA DOES NOT LIST IT: your schema was captured when this conversation began and never refreshes, while tool RESULTS are always current.

Then, IN YOUR FIRST MESSAGE, tell your human in words what station_me says is waiting on them. A briefing nobody relays is the original problem with extra steps.

EACH TOOL'S LIST ENTRY IS ONE SENTENCE; its real rules are in ken_instructions{tool:"<name>"}. CALL THAT BEFORE YOU FIRST USE A TOOL HERE. This block and every description froze when the conversation began and never refresh — a tool result is computed now, so it is the only guidance that is never stale and never cut. With no argument it lists every tool you can ask about.

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
// Handler is the /mcp endpoint. It holds its MCP server in an atomic pointer so a settings edit can
// swap in a rebuilt one without dropping the endpoint, exactly as mcpserver.Handler always has.
//
// *** IT DID NOT, AND EVERY "LIVE" SETTING SILENTLY STOPPED BEING LIVE. ***
//
// When the three endpoints collapsed into this one, mcpserver.Handler and stationserver.Handler
// stopped being mounted — but main.go kept wiring live.OnChange to them. So an operator lowering a
// station locker cap, or declaring a curation language, edited a handler no request could reach:
// measured, a 4 KiB station_locker_put succeeded against a handler whose cap had just been set to
// 1 KiB. The settings page went on reporting those fields as live, and the two tests guarding the
// behaviour were green against handlers nothing served.
//
// The curation language is the worst of them, because it is the one rule tooldoc.MustArrive exists
// for: a session cannot pull an answer to a question it does not know to ask, so a stale curation
// sentence produces proposals the curator cannot read, with no error anywhere.
type Handler struct {
	http.Handler
	d   Deps
	ptr atomic.Pointer[mcp.Server]
}

// SetCurationLangs rebuilds the served tool set so kb_save and kb_propose_enhancement carry the
// current curation-language rule. Cheap and rare — it fires only on a settings edit — and existing
// connections keep working, picking the new text up on their next initialize.
func (h *Handler) SetCurationLangs(langs []string) {
	d := h.d
	d.KB.CurationLangs = langs
	h.d = d
	h.ptr.Store(buildServer(d))
}

// SetStationLimits swaps the station caps every station_* tool reads.
//
// It rebuilds rather than mutating in place because stationserver.Deps carries its limits as a
// closure installed by its own constructor, and this package registers tools from the Deps value
// directly. Rebuilding is the honest way to make the new value reach the handlers.
func (h *Handler) SetStationLimits(task store.StationTaskLimits, note store.StationNoteLimits,
	locker store.StationLockerLimits, vault store.StationVaultLimits) {
	d := h.d
	d.Station.TaskLimits, d.Station.NoteLimits = task, note
	d.Station.LockerLimits, d.Station.VaultLimits = locker, vault
	h.d = d
	h.ptr.Store(buildServer(d))
}

func NewHTTPHandler(d Deps) *Handler {
	h := &Handler{d: d}
	h.ptr.Store(buildServer(d))
	inner := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return h.ptr.Load() },
		&mcp.StreamableHTTPOptions{SessionTimeout: sessionTimeout})

	// CHAINED, INNERMOST LAST. Each middleware authenticates the same bearer against its own
	// surface's requirement and injects its own principal, so by the time the request reaches the
	// server all three are present. A credential missing any capability is refused at the
	// transport by whichever link needs it — which is the fail-closed property, kept.
	ch := stationserver.AuthMiddleware(d.Station, inner)
	if d.CommH != nil {
		ch = commserver.AuthMiddleware(d.Comm, ch)
	}
	h.Handler = mcpserver.AuthMiddleware(d.KB, ch)
	return h
}

func buildServer(d Deps) *mcp.Server {
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
	// ONCE, AFTER THE THREE. Each package used to register its own ken_version and
	// ken_instructions, which on this single server meant three tools fighting over two names —
	// and mcp.AddTool replaces silently, so the pair that survived answered for one surface and
	// looked complete. The closure hands back the block a session actually receives here, so what
	// ken_instructions returns cannot drift from what was delivered at connect.
	version.RegisterMetaTools(s, func() string { return Instructions })
	return s
}
