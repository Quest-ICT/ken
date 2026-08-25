package version

import (
	"fmt"
	"runtime"
)

// Info is what `ken_version` returns on every surface.
//
// One shape, one meaning, three registrations: a session may hold a credential for only
// one of /mcp, /comm/mcp or /station/mcp, and "what am I talking to" is the same question
// on all of them. Registering it three times with one answer beats three near-identical
// tools whose wording drifts.
type Info struct {
	// Version is what is running RIGHT NOW, computed per call. This is the number to
	// compare against the one in your connect-time instructions.
	Version string `json:"version"`
	// InstructionsMayBeStale is always true, and says so rather than pretending to
	// know. The server cannot see which text a client captured — that is the whole
	// shape of the problem — so this field exists to make the comparison the session's
	// to perform, with the instruction attached rather than implied.
	InstructionsMayBeStale bool `json:"instructions_may_be_stale"`
	// HowToCheck is the one-line procedure. Present in the RESULT rather than only in
	// the tool description, because a description is captured at conversation start and
	// a result is not — a self-describing answer is the only kind guaranteed to arrive
	// intact at a long-running session.
	HowToCheck string `json:"how_to_check"`
	// ReleaseBuild is false for a source build, where Version is a compiled-in
	// placeholder rather than a published artifact. Reported because "my session says
	// 3.1.0" means something different when nobody published a 3.1.0.
	ReleaseBuild bool `json:"release_build"`
	// Platform and SourceURL let a session answer an operator's question without
	// guessing. SourceURL honours the AGPL-3.0 §13 obligation of the RUNNING instance,
	// so a fork reports its own.
	Platform  string `json:"platform"`
	SourceURL string `json:"source_url"`
	// Surfaces names every MCP endpoint this deployment serves.
	//
	// WHY IT IS IN A RESULT. A session holding only /mcp had no way to learn that COMM and
	// stations exist: the knowledge-base instructions never mentioned either, and the two
	// blocks that would have said so are on endpoints it cannot reach. A Claude Code session
	// spent a conversation probing four paths and reading three 404s to find /station/mcp,
	// then correctly refused to assert Station did not exist because it could not tell
	// "absent" from "undocumented".
	//
	// Instructions cannot fix that for a RUNNING session — they pin at connect. A result can.
	// This is the same reasoning as HowToCheck one level out: the answer a session needs about
	// the shape of the server has to arrive through the one channel that is never stale and
	// never truncated.
	Surfaces []string `json:"surfaces"`
	// Instructions is present only when the caller passed include_instructions. Omitted
	// otherwise: ken_version is called often and cheaply, and a couple of kilobytes on every
	// call would make sessions stop calling the one tool that tells them they are stale.
	Instructions *InstructionsInfo `json:"instructions,omitempty"`
}

// Surfaces is set once at startup by the process that decides which endpoints to serve.
// Defaulted rather than left nil so a source build, a test, or any embedding that forgets to
// set it still describes the shipped shape instead of reporting that Ken has no surfaces —
// which a session would read as "this deployment has none", the worse of the two errors.
var Surfaces = []string{"/mcp", "/comm/mcp", "/station/mcp"}

// Current builds the answer.
func Current() Info {
	return Info{
		Version:                Version,
		InstructionsMayBeStale: true,
		HowToCheck: fmt.Sprintf(
			"Your connect-time instructions state the version that wrote them. If it is not %s, "+
				"that text and every tool description you hold are older than this server — they pin "+
				"when a CONVERSATION begins and never refresh, not on reconnect and not on upgrade. "+
				"Tool results like this one are always current.", Version),
		ReleaseBuild: IsReleaseBuild(),
		Platform:     runtime.GOOS + "/" + runtime.GOARCH,
		SourceURL:    SourceURL(),
		Surfaces:     append([]string(nil), Surfaces...),
	}
}

// ToolDescription is the shared tool text, so three registrations cannot drift.
const ToolDescription = "What version of Ken you are actually talking to, computed fresh on every call. " +
	"Call this when anything you were told does not match what you observe, before reporting a bug, " +
	"and when your human asks what is deployed. " +
	"WHY IT EXISTS: your instructions and every tool description you hold were captured when this " +
	"CONVERSATION began and never refresh — not when the server upgrades, not when you reconnect. " +
	"A long conversation is routinely reading an older manual and cannot tell. The instructions state " +
	"which version wrote them; this tells you which version is running. Comparing the two is the check. " +
	"WHAT THE FREEZE BLOCKS AND WHAT IT DOES NOT — the distinction is worth reading twice. " +
	"PARAMETERS TRAVEL: if your human, a peer or the docs tell you a tool has gained an argument, PASS IT. " +
	"The server validates what ARRIVES, not your captured copy of the schema, so a call that refuses your " +
	"old way may accept the new one. WHOLE TOOLS DO NOT travel: a tool added after this conversation began " +
	"is not in your list and you have no handle to call it, however much you know about it — which is why " +
	"the running version also rides inside results you already call. Nothing is broken: reading an older " +
	"manual is the ordinary condition of a long conversation, and reconnecting does not help. " +
	"The result also names every MCP surface this deployment serves — /mcp (kb_*, the knowledge base), " +
	"/comm/mcp (comm_*, messaging between AI sessions), /station/mcp (station_*, a durable working " +
	"identity) — each a SEPARATE MCP server entry your human configures. Holding one tells you nothing " +
	"about the others, so if you need a surface you do not have, ask your human for it by name."

// --- re-fetching the instructions, which is the one thing the freeze cannot block ---

// InstructionsIn is ken_version's optional argument, and the reason it lives THERE rather than
// only on a dedicated tool.
//
// WHOLE TOOLS DO NOT TRAVEL: a tool added after a conversation began is absent from that
// conversation's list forever, so `ken_instructions` cannot reach the sessions that need it most.
// PARAMETERS DO travel — the server validates what ARRIVES, not the client's captured copy of the
// schema. ken-prod-ops proved it by passing `to_room` to a comm_send schema that has no such
// property and watching it work.
//
// So a session whose tool list froze before `ken_instructions` existed can still ask for the
// current text, through a tool it already holds, by passing an argument its schema does not
// mention. That is not a trick; it is the documented shape of the freeze, used deliberately.
//
// FOUND THE HARD WAY, 2026-08-25: an MCP registration on two separate machines was serving
// pre-3.22.0 instructions and pre-3.22.0 tool descriptions against a fully patched 3.22.0 server,
// in conversations that began AFTER the upgrade — while its ken_version RESULT came back
// completely current. No Ken release can reach that captured text. A result can.
type InstructionsIn struct {
	IncludeInstructions bool `json:"include_instructions,omitempty" jsonschema:"return this surface's CURRENT connect-time instructions in full. Use it when your captured text may be older than the running server, or when it looks truncated: the client cuts the instructions field, and a result is never cut"`
}

// InstructionsInfo is what ken_instructions returns, and what ken_version returns alongside the
// version when asked.
type InstructionsInfo struct {
	// Surface is which endpoint this text belongs to, so a session holding several can tell
	// them apart without guessing from the content.
	Surface string `json:"surface"`
	// Version wrote the text below. Compare it with the version your connect-time copy names.
	Version string `json:"version"`
	// Instructions is the CURRENT text, in full and never truncated.
	//
	// The connect-time copy of this is cut at InstructionBudget characters by the client. That
	// is why the blocks are written to fit, and why this exists anyway: fitting protects a
	// session that connects today, and only a RESULT reaches one that connected before.
	Instructions string `json:"instructions"`
	// Surfaces names every endpoint this deployment serves, repeated here so a session that
	// asked only for instructions still learns what else it could be given.
	Surfaces []string `json:"surfaces"`
	// Note says what to do with a difference, because a session that finds one and is told
	// nothing will reconnect — which does not help.
	Note string `json:"note"`
}

// InstructionsFor builds the answer for one surface.
func InstructionsFor(surface, text string) InstructionsInfo {
	return InstructionsInfo{
		Surface:      surface,
		Version:      Version,
		Instructions: text,
		Surfaces:     append([]string(nil), Surfaces...),
		Note: "This is the CURRENT text, in full. Your connect-time copy pins when the conversation " +
			"begins, never refreshes, and is truncated by the client — so if it differs from this, this " +
			"is the one to follow. Reconnecting does not update it; calling this does.",
	}
}

// InstructionsToolDescription is shared by all three registrations, like ToolDescription, so the
// wording cannot drift between surfaces.
const InstructionsToolDescription = "Re-fetch this surface's connect-time instructions, CURRENT and in full. " +
	"Call it when your instructions look truncated or cut off mid-sentence, when ken_version reports a version " +
	"different from the one your instructions name, when you are unsure whether a rule you remember still applies, " +
	"or at the start of a long session that may outlive its own manual. " +
	"WHY IT EXISTS: the connect-time instructions field is captured by your client when the CONVERSATION begins and " +
	"never refreshes — and the client also TRUNCATES it, so what you were given may be both old and incomplete. " +
	"A tool result is neither: it is computed per call and is not cut. " +
	"The answer also names every MCP surface this deployment serves, so a session holding one endpoint can learn " +
	"what the others do and ask its human for them by name."
