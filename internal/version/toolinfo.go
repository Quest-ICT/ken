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
