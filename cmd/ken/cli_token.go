package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
)

var validScopes = map[string]bool{
	"read": true, "write-draft": true, "propose": true, "curate": true,
	// Inter-session communication (docs/COMM.md). `comm-file` is reserved and
	// required by nothing yet — declared now because splitting a shipped `comm`
	// into two scopes later would be a MAJOR, while merging two is free.
	"comm": true, "comm-file": true,
}

// commScopes are the scopes that belong to the COMM endpoint.
var commScopes = map[string]bool{"comm": true, "comm-file": true}

// checkScopeMix enforces that a COMM token is a DEDICATED token: a token may hold
// comm scopes or knowledge-base scopes, never both.
//
// This is what makes the design's claim true rather than aspirational — "a
// knowledge-base token cannot send messages and a comm token cannot write
// knowledge". Without it an operator could quietly widen their everyday agent
// token, and since API tokens have no expiry (only revocation), every already-
// copied instance of that token would gain the new capability retroactively.
func checkScopeMix(scopes []string) error {
	var comm, kb []string
	for _, s := range scopes {
		if commScopes[s] {
			comm = append(comm, s)
		} else {
			kb = append(kb, s)
		}
	}
	if len(comm) > 0 && len(kb) > 0 {
		return fmt.Errorf("a comm token must be dedicated: %v cannot be combined with %v — mint two tokens and register Ken twice", comm, kb)
	}
	return nil
}

func runToken(args []string) {
	if len(args) == 0 {
		die("usage: ken token add|list|revoke")
	}
	st := mustOpenStore(envOr("KEN_DB", "./data/ken.db"))
	defer st.Close()
	ctx := context.Background()

	switch args[0] {
	case "add":
		fs := flag.NewFlagSet("token add", flag.ExitOnError)
		actor := fs.String("actor", "", "actor display name (created if absent)")
		kind := fs.String("kind", "ai", "actor kind: ai|human")
		scopesCSV := fs.String("scopes", "read,write-draft,propose", "comma-separated: read,write-draft,propose,curate | comm (dedicated; see docs/COMM.md)")
		label := fs.String("label", "", "human-readable token label")
		_ = fs.Parse(args[1:])
		if *actor == "" {
			die("--actor is required")
		}
		if *kind != "ai" && *kind != "human" {
			die("--kind must be ai or human")
		}
		scopes := splitCSV(*scopesCSV)
		if len(scopes) == 0 {
			die("--scopes must list at least one scope")
		}
		for _, s := range scopes {
			if !validScopes[s] {
				die("unknown scope: " + s)
			}
		}
		if err := checkScopeMix(scopes); err != nil {
			die(err.Error())
		}
		actorID, err := st.FindOrCreateActor(ctx, *kind, *actor)
		must(err)
		tok, err := st.IssueToken(ctx, actorID, scopes, *label)
		must(err)
		fmt.Printf("Token for actor %q (scopes: %s)\n\n    %s\n\nStore it now — the secret is shown only once.\n",
			*actor, strings.Join(scopes, ","), tok)

	case "list":
		rows, err := st.ListTokens(ctx)
		must(err)
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "TOKEN_ID\tACTOR\tKIND\tSCOPES\tLABEL\tLAST_USED\tREVOKED")
		for _, r := range rows {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				r.TokenID, r.ActorName, r.Kind, r.Scopes, dash(r.Label), dash(r.LastUsedAt), dash(r.RevokedAt))
		}
		_ = w.Flush()

	case "revoke":
		if len(args) < 2 {
			die("usage: ken token revoke <token_id>")
		}
		must(st.RevokeToken(ctx, args[1]))
		fmt.Println("revoked", args[1])

	default:
		die("unknown token subcommand: " + args[0])
	}
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
