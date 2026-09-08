package tooldoc

import "github.com/modelcontextprotocol/go-sdk/mcp"

// MCP tool annotations — what a CLIENT is told about a tool before it decides whether to ask its
// human for permission.
//
// *** WHY THIS FILE EXISTS: THE SDK'S DEFAULTS DESCRIBE KEN AS SOMETHING IT IS NOT. ***
//
// ToolAnnotations defaults, from the SDK's own doc comments: DestructiveHint TRUE, OpenWorldHint
// TRUE, ReadOnlyHint false. So a tool that sets nothing is advertised as a destructive tool that
// reaches an open world of external entities. Ken set nothing, on all 42 tools. Every one of them
// — kb_search, comm_directory, ken_version — described itself to every client as destructive and
// open-world.
//
// ken-prod measured the consequence on 2026-09-08: a claude.ai web session had kb_save,
// station_task_add and comm_directory all refused with "No approval received". They proved it was
// client-side rather than Ken's — ken_mcp_tool_calls_total showed those three never reached a
// handler, the string exists nowhere in Ken or the SDK, and Ken has no per-call approval concept.
// Anthropic's connector guidance is explicit that read-only tools skip per-call confirmation only
// when the connector annotates them.
//
// KEN IS A CLOSED WORLD. It is a self-hosted knowledge base and message bus; no tool reaches an
// external entity. OpenWorldHint is false on every single one, and that is a fact about the
// product rather than a per-tool judgement.
//
// *** THE TABLE IS EXHAUSTIVE AND THE TEST ENFORCES THAT. *** A tool added later without an entry
// here does not quietly inherit "destructive, open-world" — TestEveryRegisteredToolIsAnnotated
// fails. That is the whole point: the defect was not a wrong annotation, it was an absent one, and
// absence is what defaults badly.
type kind int

const (
	// readOnly: observes and changes nothing. Safe to run without asking, which is what lets a
	// session search before it writes rather than prompting its human to allow a search.
	readOnly kind = iota
	// additive: writes, but only ever ADDS. Ken's version history is append-only and its lifecycle
	// changes are reversible, so nothing here can remove knowledge.
	additive
	// destructive: can remove something, or send something that cannot be recalled.
	destructive
)

var toolKinds = map[string]kind{
	// --- knowledge base ---
	"kb_search":              readOnly,
	"kb_get":                 readOnly,
	"kb_diff":                readOnly,
	"kb_recent_context":      readOnly,
	"kb_save":                additive, // creates an entry; removes nothing
	"kb_propose_enhancement": additive, // appends a version; the displaced one is retained
	"kb_record_outcome":      additive,
	"kb_flag_stale":          additive,
	"kb_retract":             destructive, // withdraws an entry from every search
	"ken_version":            readOnly,
	"ken_instructions":       readOnly,

	// --- comm ---
	"comm_directory":    readOnly,
	"comm_channels":     readOnly,
	"comm_poll":         additive, // marks mail delivered and arms its clocks
	"comm_send":         additive, // A MESSAGE CANNOT BE UNSENT, but it destroys nothing
	"comm_ack":          additive,
	"comm_file_offer":   additive,
	"comm_file_grant":   additive,
	"comm_open_channel": additive,

	// --- station ---
	"station_me":            additive, // may mint a station on first call
	"station_directory":     readOnly,
	"station_request":       additive,
	"station_room_request":  additive,
	"station_note_read":     readOnly,
	"station_note_list":     readOnly,
	"station_note_write":    additive, // every revision is retained
	"station_note_promote":  additive,
	"station_task_list":     readOnly,
	"station_task_add":      additive,
	"station_task_close":    additive,
	"station_task_defer":    additive,
	"station_task_reopen":   additive,
	"station_task_drop":     destructive,
	"station_locker_list":   readOnly,
	"station_locker_get":    readOnly,
	"station_locker_put":    additive,
	"station_locker_delete": destructive,
	"station_vault_list":    readOnly,
	"station_vault_get":     readOnly, // reads a secret; logged, but changes nothing
	"station_vault_put":     additive, // overwrite retains the previous value
	"station_vault_delete":  destructive,
	"station_vault_send":    additive,
}

// Annotate fills in a tool's annotations from the table.
//
// Unknown names are left ALONE rather than guessed at: a silent default is what produced this
// defect, and the test below is what catches the omission.
func Annotate(t *mcp.Tool) {
	k, ok := toolKinds[t.Name]
	if !ok {
		return
	}
	no, yes := false, true
	a := &mcp.ToolAnnotations{OpenWorldHint: &no}
	switch k {
	case readOnly:
		a.ReadOnlyHint = true
		a.IdempotentHint = true
	case additive:
		a.DestructiveHint = &no
	case destructive:
		a.DestructiveHint = &yes
	}
	t.Annotations = a
}

// IsAnnotated reports whether a tool name is classified, for the exhaustiveness test.
func IsAnnotated(name string) bool { _, ok := toolKinds[name]; return ok }
