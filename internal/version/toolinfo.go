package version

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/Quest-ICT/ken/internal/tooldoc"
)

// Info is what `ken_version` returns.
//
// It was registered three times, once per surface, because a session could hold a credential for
// only one of /mcp, /comm/mcp or /station/mcp. There is one endpoint now and one registration; see
// version.RegisterMetaTools for why three registrations of one name on one server was worse than
// it sounds.
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
	// SURFACES IS DELETED, AND THE PROBLEM IT SOLVED IS DELETED WITH IT.
	//
	// It named every MCP endpoint this deployment served, because a session holding only /mcp had
	// no way to learn that COMM and stations existed — the knowledge-base instructions mentioned
	// neither, and the blocks that would have said so were on endpoints it could not reach. A
	// Claude Code session spent a conversation probing four paths and reading three 404s to find
	// /station/mcp, then correctly refused to assert Station did not exist, because it could not
	// tell "absent" from "undocumented".
	//
	// There is one endpoint now and it carries every tool, so no session can hold a partial view
	// to be told about. Keeping the field would mean answering that question forever with a list
	// of one — or worse, with the three-entry default below, which is what it held until this
	// change and would have named two URLs that 404.
	// Instructions is present only when the caller passed include_instructions. Omitted
	// otherwise: ken_version is called often and cheaply, and a couple of kilobytes on every
	// call would make sessions stop calling the one tool that tells them they are stale.
	Instructions *InstructionsInfo `json:"instructions,omitempty"`
}

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
	"Ken serves ONE machine surface, /mcp, and it carries every tool: kb_* the knowledge base, comm_* " +
	"messaging between AI sessions, station_* a durable working identity. If you hold the connector you " +
	"have all three; none is optional and there is no second endpoint to ask your human for."

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
	// A STRING-OR-BOOLEAN, and the type is the whole design of this field.
	//
	// ken-prod-ops ran the test from inside the exact population this argument exists for — a
	// session whose captured ken_version schema has no such property — and it failed:
	//
	//	on 3.23.0:  ken_version{include_instructions: true}
	//	  -> validating /properties/include_instructions: type: true has type "string", want "boolean"
	//
	// The argument CROSSED the freeze, which confirms the premise. Then schema validation
	// rejected it, because their client had no schema telling it the value was a boolean and so
	// serialized it as the string "true".
	//
	// **A client with no schema for a property cannot type that property correctly.** That is not
	// a client bug; it is the definition of the case. So a boolean-only contract is correct for
	// every caller EXCEPT the ones this feature is for, which makes it the wrong contract.
	//
	// Declared as `any` so the generated schema constrains nothing, and coerced below. The
	// description carries the intent for clients that DO have the schema; the leniency is scoped
	// to this one argument rather than adopted as a general posture.
	IncludeInstructions any `json:"include_instructions,omitempty" jsonschema:"true to return this surface's CURRENT connect-time instructions in full (the string \"true\" is accepted too, because a client whose schema predates this property cannot know it is a boolean). Use it when your captured text may be older than the running server, or when it looks truncated: the client cuts the instructions field, and a result is never cut"`
}

// Wants reports whether the caller asked for the instructions, accepting every shape a client
// without a schema plausibly sends.
//
// DELIBERATELY LENIENT IN ONE DIRECTION ONLY: anything that clearly means yes is yes, and
// everything else — including an unparseable value — is no. The failure mode of guessing wrong
// toward "yes" is a couple of extra kilobytes in one result; toward "no" it is a frozen session
// silently not getting the fix it asked for, which is the thing that already happened.
func (in InstructionsIn) Wants() bool {
	switch v := in.IncludeInstructions.(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "yes", "y", "1", "on":
			return true
		}
	case float64: // JSON numbers arrive as float64
		return v != 0
	}
	return false
}

// InstructionsRequest is ken_instructions' input: nothing, or the name of one tool.
type InstructionsRequest struct {
	// Tool asks for ONE tool's complete rules instead of the connect-time block.
	//
	// This is where per-tool detail lives now. It used to live in the tool DESCRIPTION, which was
	// an improvement on living in the connect instructions (the client truncates those) but not a
	// cure: a description is captured when the conversation begins and never refreshes, so a rule
	// written there is permanently as old as the session reading it. A result is computed per call.
	//
	// A PARAMETER RATHER THAN A TOOL PER SUBJECT, deliberately. Whole tools do not cross the
	// freeze — one added after a conversation began is absent from that conversation forever — but
	// arguments do, because the server validates what ARRIVES rather than the client's captured
	// schema. So a session whose tool list predates this field can still pass it.
	Tool string `json:"tool,omitempty" jsonschema:"optional; the name of ONE tool, to get its complete and CURRENT rules instead of the connect-time block. Omit it to get the connect-time instructions plus the list of tools you may ask about. Pass it even if your captured schema does not list this property: the server reads what you send"`
}

// InstructionsInfo is what ken_instructions returns, and what ken_version returns alongside the
// version when asked.
type InstructionsInfo struct {
	// Version wrote the text below. Compare it with the version your connect-time copy names.
	Version string `json:"version"`
	// Instructions is the CURRENT text, in full and never truncated: the connect-time block, or
	// one tool's complete rules when `tool` was passed.
	//
	// The connect-time copy of this is cut at InstructionBudget characters by the client. That
	// is why the block is written to fit, and why this exists anyway: fitting protects a
	// session that connects today, and only a RESULT reaches one that connected before.
	Instructions string `json:"instructions"`
	// Tool echoes which tool was asked about, so an answer cannot be mistaken for the block.
	Tool string `json:"tool,omitempty"`
	// Tools names every tool whose full rules can be fetched here, returned when none was named.
	//
	// IT IS THE POINT OF THE NO-ARGUMENT CALL, not a courtesy. Tool descriptions were shortened to
	// one line plus a pointer, so this list is how a session learns what it can ask about — and it
	// arrives in a result, which means a session whose tool list is a release out of date still
	// gets the current one.
	Tools []string `json:"tools,omitempty"`
	// Note says what to do with a difference, because a session that finds one and is told
	// nothing will reconnect — which does not help.
	Note string `json:"note"`
}

// InstructionsFor builds the answer carrying the connect-time block.
//
// THE `surface` PARAMETER IS GONE, with the three endpoints it named. /comm/mcp and /station/mcp
// were deleted; there is one machine surface, /mcp, and it carries every tool. A field whose only
// job was telling apart endpoints that no longer exist is not harmless to keep — it would go on
// answering a question nobody can ask, in a session's context, forever.
func InstructionsFor(text string) InstructionsInfo {
	return InstructionsInfo{
		Version:      Version,
		Instructions: text,
		Tools:        tooldoc.Names(),
		Note: "This is the CURRENT text, in full. Your connect-time copy pins when the conversation " +
			"begins, never refreshes, and is truncated by the client — so if it differs from this, this " +
			"is the one to follow. Reconnecting does not update it; calling this does. " +
			"`tools` lists every tool whose COMPLETE rules you can fetch with ken_instructions{tool:\"…\"} — " +
			"each tool's list entry is one sentence, and the rest is there.",
	}
}

// forTool re-labels an answer as one tool's rules rather than the connect-time block.
//
// The tool list is dropped from it on purpose: a session that named a tool asked a specific
// question, and repeating forty-five names under the answer is the padding that trains sessions to
// skim results.
func (i InstructionsInfo) forTool(name string) InstructionsInfo {
	i.Tool = name
	i.Tools = nil
	i.Note = "These are " + name + "'s COMPLETE rules, current as of this call. Its entry in your tool " +
		"list is only the first sentence — that entry was captured when this conversation began and never " +
		"refreshes, while this was computed just now. Call ken_instructions with no argument for the " +
		"connect-time instructions and the list of every tool you can ask about."
	return i
}

// InstructionsToolDescription introduces the tool that now carries every per-tool rule.
const InstructionsToolDescription = "THE FULL RULES FOR ANY TOOL, and Ken's connect-time instructions, CURRENT and never truncated. " +
	"Pass tool:\"<name>\" for one tool's complete rules — every tool's list entry is ONE SENTENCE plus a pointer here, " +
	"and the rest of what it needs from you is behind this call. Call it with NO argument for the instructions in full " +
	"plus the list of every tool you may ask about. " +
	"WHY THE DETAIL LIVES HERE: a tool description and the instructions field are both captured by your client when the " +
	"CONVERSATION begins and never refresh — not on reconnect, not when the server upgrades — and the client TRUNCATES " +
	"the instructions on top of that. So anything written there is as old as your session and possibly cut in half. " +
	"A tool result is neither old nor cut: it is computed on this call. " +
	"Call it before using a tool you have not used in this conversation, when your instructions look cut off " +
	"mid-sentence, when ken_version reports a version different from the one your instructions name, and whenever you " +
	"are unsure whether a rule you remember still applies."
