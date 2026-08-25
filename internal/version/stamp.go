package version

import "fmt"

// The version stamp that makes instruction drift DETECTABLE by the session holding it.
//
// ken-prod-ops measured the problem on a live deployment: an MCP client captures a
// server's `instructions` — and its tool DESCRIPTIONS — when the CONVERSATION begins,
// and neither refreshes when the server upgrades. Their session held 1.7.0's text while
// the process serving it had been the 2.1.0 image for hours, and nothing anywhere said
// so. Only tool RESULTS are computed per call.
//
// They also, correctly, rejected asking a session to detect its own staleness: a session
// cannot check the thing it is made of, and their own case was the proof — they held old
// text, had no way to know, and found out because someone outside the conversation asked.
//
// SO THIS IS NOT SELF-INSPECTION. It is two statements from the same authority at two
// different times:
//
//	the INSTRUCTIONS say which version wrote them   — pinned when the conversation began
//	ken_version says which version is running NOW   — computed per call, always current
//
// A session compares two strings it was handed. That requires no introspection, no new
// protocol, and no reasoning about its own state — which is exactly why it works where
// self-detection cannot.

// InstructionStamp is prepended to every surface's connect-time instructions.
//
// PREPENDED, AND SHORT, BECAUSE FOR ITS ENTIRE LIFE IT WAS NEITHER. The MCP client truncates
// the instructions field at 2048 CHARACTERS. This stamp was 1053 characters APPENDED to blocks
// of 2754, 7042 and 4714 — so on all three surfaces it began past the cut, and **no session has
// ever received it.** Shipped in 3.1.0, measured 2026-08-25.
//
// The mechanism survived anyway, and only because it was built with two channels: `Info` carries
// `instructions_may_be_stale` and `how_to_check` in every ken_version RESULT, and results are not
// truncated. A session did report an instruction ambiguity rather than asserting an absence — the
// design worked through the half nobody knew was the only live one.
//
// So the rule this file now embodies: **under a cap, position IS delivery.** What must arrive goes
// first and stays small. The long explanation moved to ToolDescription (internal/version/toolinfo.go),
// which is read at the moment a session calls ken_version — the moment the discrepancy is in front
// of it — and which the client delivers intact.
func InstructionStamp() string {
	return fmt.Sprintf("KEN %s WROTE THIS TEXT. Call ken_version for what runs NOW; if it differs, this and "+
		"every tool description you hold are older — they pin when a conversation begins and never refresh, "+
		"not even on reconnect. Trust tool results: always current.\n\n", Version)
}

// InstructionBudget is the number of CHARACTERS an MCP client delivers of the instructions field.
//
// Not Ken's limit — Ken has no truncation code anywhere. Measured downstream three ways on
// 2026-08-25: two independently observed cut points, on two machines and two client builds, land
// exactly where character 2048 falls in the source (COMM after "...the other scopes are then
// hidden", STATION after "...most of your list has never been shown t"), and the third was
// arithmetic — computing that offset from the source predicted both fragments verbatim.
//
// Treated as a BUDGET rather than as a known constant. If the real cap is larger, staying under
// this costs a little brevity; if it is smaller or varies by client, everything load-bearing is
// still at the front. Enforced per surface by tests, on `instructions + InstructionStamp()` —
// the string that actually faces the cap, which is the distinction the first refit missed.
const InstructionBudget = 2048
