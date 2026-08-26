package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/Quest-ICT/ken/internal/store"
)

// The scope vocabulary and the one-family rule now live in internal/store, so that the
// console's mint path enforces the same rule this one does. See internal/store/scopes.go.
var validScopes = store.ValidScopes

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
		// *** THIS REFUSAL WAS REMOVED IN 3.36.0 BECAUSE ITS REASON HAD BEEN FALSE SINCE 3.28.0. ***
		//
		// It said: "station scopes are not mintable here: /station/mcp requires a kens_ key BOUND
		// to a station, and this command issues an unbound ken_ token." That was true when it was
		// written. 3.27.0 taught /station/mcp to accept a plain `ken_` token carrying the station
		// scope, and it was proven on the wire from a Windows machine the next day — a session
		// with no key called station_me and got a workspace.
		//
		// THE CONSOLE'S IDENTICAL REFUSAL WAS FIXED IN 3.28.0 AND THIS ONE WAS NOT, which is the
		// same half-fix the console itself had suffered: ken-prod-ops found `consoleCommScopes`
		// still excluding station scopes with a comment citing THIS command as its authority, and
		// the fix went to the console alone. The justification and the code parted company in two
		// places and only one was repaired.
		//
		// Found by the unified endpoint's own first test, which could not mint a credential
		// carrying all three families — the exact credential /all/mcp exists to serve.
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
		fmt.Fprintln(w, "TOKEN_ID\tACTOR\tKIND\tSTATION\tSCOPES\tLABEL\tLAST_USED\tREVOKED")
		for _, r := range rows {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				r.TokenID, r.ActorName, r.Kind, dash(r.Station), r.Scopes, dash(r.Label),
				dash(r.LastUsedAt), dash(r.RevokedAt))
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
