package store

// Token scopes, and the rule that keeps a token dedicated to ONE surface.
//
// THIS LIVED IN `package main` UNTIL 3.10.0, reachable only from `ken token add`. The console
// was safe from violating it not because it enforced it, but because its menu was too narrow to
// express a violation — it offered the three knowledge-base scopes and silently dropped anything
// else. The moment the console learned to mint a comm token, "safe by accident" stopped being
// good enough, so the rule moved here, where both mint paths reach it.
//
// A scope is a coarse capability, not a fine one, and the vocabulary is deliberately larger than
// what any tool checks today: `comm-file` and `station-locker` are RESERVED. Splitting a shipped
// scope into two later is a MAJOR change; merging two is free — so the cheap direction is to
// declare early and merge if it turns out one was enough.

// ValidScopes is every scope Ken will mint. Membership here does not mean any surface checks it.
var ValidScopes = map[string]bool{
	"read": true, "write-draft": true, "propose": true, "curate": true,
	"comm": true, "comm-file": true,
	"station": true, "station-locker": true,
}

// CommScopes belong to /comm/mcp and /comm/files.
var CommScopes = map[string]bool{"comm": true, "comm-file": true}

// StationScopes belong to /station/mcp.
var StationScopes = map[string]bool{"station": true, "station-locker": true}

// *** CheckScopeMix WAS DELETED HERE, 2026-08-26, DELIBERATELY. ***
//
// It enforced that a token was DEDICATED to one surface family: knowledge-base scopes could not
// be combined with comm or station, and only `{station, comm}` was permitted. It was a real,
// tested, enforced rule with a clear rationale, and it is gone for two reasons.
//
// FIRST, IT BUILT THE STATE VLAD FORBIDS. His standing requirement, stated more than once and
// most recently in exasperation: "no ken services (or surfaces) are optional. All sessions get
// everything (they can use)." This rule made that impossible on the token path — minting a
// station token REQUIRED unticking the knowledge base, so the m600 session onboarded on
// 2026-08-25 holds a station and a locker and cannot read the knowledge base at all. A rule that
// makes the product's core unreachable from its newest surface is not containment.
//
// SECOND, AND THIS IS WHAT SETTLED IT: THE PROPERTY IT CLAIMED WAS ALREADY FALSE. Its own comment
// justified it as "a knowledge-base token cannot send messages and a comm token cannot write
// knowledge". But it was never applied to OAuth grants — its only callers were `ken token add`
// and the console's token-create — and `GrantedCapabilities` hands a single OAuth bearer kb AND
// comm AND station. Verified on the wire 2026-08-26 against a 3.30.0 deployment: one grant, three
// surfaces, 200/200/200. So the same server already issued the credential this function refused
// to mint, and the rule constrained only the path a human uses by hand.
//
// WHAT WE GIVE UP, STATED RATHER THAN GLOSSED. The blast radius of a leaked token is now every
// surface rather than one family, and api_tokens have no expiry — only revocation — so an
// already-copied token gains nothing retroactively but is worth more when taken. That is a real
// cost. It is accepted because the alternative was an asymmetry with no defensible line: OAuth
// bearers already carry everything, and pretending otherwise for hand-minted tokens bought no
// containment while guaranteeing crippled sessions.
//
// `TestCheckScopeMix` (internal/store) and its cli_token_test counterpart were deleted in this
// same commit. Removing the rule and leaving the tests would have left them failing; removing the
// tests and leaving the rule would have left a live control nothing exercises. Both went together.
