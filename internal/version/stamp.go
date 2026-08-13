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

// InstructionStamp is appended to every surface's connect-time instructions.
//
// It states the version and tells the session what to do with the discrepancy, because a
// number with no instruction attached is a number a session will read past. The wording
// deliberately does not say "you are broken": stale text is the NORMAL condition of a
// long conversation, and the useful response is to re-read the tool descriptions, not to
// panic or reconnect — reconnecting does not help, which is the counter-intuitive part
// worth stating where it will be read.
func InstructionStamp() string {
	return fmt.Sprintf(`
THESE INSTRUCTIONS WERE WRITTEN BY KEN %s. Call ken_version to see what is running NOW.
If the two differ, this text and every tool description you hold are from the older one —
they were captured when this conversation began and do NOT refresh, not on reconnect and
not on a server upgrade. Nothing is broken; you are simply reading an older manual. Trust
the tool RESULTS, which are always current, and ask your human what changed.`, Version)
}
