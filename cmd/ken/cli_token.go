package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
)

var validScopes = map[string]bool{"read": true, "write-draft": true, "propose": true, "curate": true}

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
		scopesCSV := fs.String("scopes", "read,write-draft,propose", "comma-separated: read,write-draft,propose,curate")
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
