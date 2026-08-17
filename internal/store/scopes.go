package store

import "fmt"

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

// CheckScopeMix enforces that a token is DEDICATED to one surface family.
//
// This is what makes the design's claim true rather than aspirational — "a knowledge-base token
// cannot send messages and a comm token cannot write knowledge". Without it an operator could
// quietly widen their everyday agent token, and since API tokens have no expiry (only
// revocation), every already-copied instance of that token would gain the new capability
// retroactively.
//
// THREE families, not two. An earlier shape bucketed everything that was not comm as
// knowledge-base, so the moment `station` became a valid scope it would have been treated as a
// KB scope: `read,write-draft,propose,station` would have minted silently while `comm,station`
// was refused — exactly backwards, since a session legitimately staffs a post and talks from it,
// while a token that can both read working notes and write knowledge is the mixing this function
// exists to prevent.
//
// The one permitted pair is station+comm (docs/STATIONS.md §6).
func CheckScopeMix(scopes []string) error {
	var comm, station, kb []string
	for _, s := range scopes {
		switch {
		case CommScopes[s]:
			comm = append(comm, s)
		case StationScopes[s]:
			station = append(station, s)
		default:
			kb = append(kb, s)
		}
	}
	if len(kb) > 0 && len(comm) > 0 {
		return fmt.Errorf("a comm token must be dedicated: %v cannot be combined with %v — mint two tokens and register Ken twice", comm, kb)
	}
	if len(kb) > 0 && len(station) > 0 {
		return fmt.Errorf("a station token must be dedicated: %v cannot be combined with %v — mint two tokens and register Ken twice", station, kb)
	}
	return nil
}
