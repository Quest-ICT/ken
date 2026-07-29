package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/Quest-ICT/ken/internal/store"
)

// `ken station …` — the HUMAN's path to stations (docs/STATIONS.md S3).
//
// Creating and naming a station is a withheld capability: no MCP tool does it, and the
// whole design rests on that. This subcommand and the console are the only ways, which
// is why it exists rather than being folded into `ken token add` — a station key is not
// just a token with an extra scope, it is a token BOUND to a durable identity a human
// named.
func runStation(args []string) {
	if len(args) == 0 {
		die("usage: ken station add|list|key|requests")
	}
	ctx := context.Background()

	switch args[0] {
	case "add":
		fs := flag.NewFlagSet("station add", flag.ExitOnError)
		name := fs.String("name", "", "station name, e.g. prod-ops (required; YOU choose it, not the agent)")
		purpose := fs.String("purpose", "", "what this station is for")
		actor := fs.String("actor", "", "human actor recorded as creator (default: the first human user)")
		_ = fs.Parse(args[1:])
		if *name == "" {
			die("--name is required — a station's name is human-chosen by design")
		}
		st := mustOpenStore(envOr("KEN_DB", "./data/ken.db"))
		defer st.Close()
		actorID := mustActor(ctx, st, *actor)
		s, err := st.CreateStation(ctx, 1, *name, *purpose, actorID)
		if errors.Is(err, store.ErrStationNameTaken) {
			die(fmt.Sprintf("a station named %q already exists in this space", *name))
		}
		must(err)
		fmt.Printf("station %s created (id %s)\n", s.Name, s.StationID)
		fmt.Println("mint a key for a machine with:")
		fmt.Printf("  ken station key --station %s --label laptop\n", s.Name)

	case "list":
		st := mustOpenStore(envOr("KEN_DB", "./data/ken.db"))
		defer st.Close()
		ss, err := st.ListStations(ctx, 1)
		must(err)
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tSTATE\tPUBLISHED\tID\tPURPOSE")
		for _, s := range ss {
			pub := "no"
			if s.Published {
				pub = "yes"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", s.Name, s.State, pub, s.StationID, s.Purpose)
		}
		_ = w.Flush()

	case "key":
		fs := flag.NewFlagSet("station key", flag.ExitOnError)
		name := fs.String("station", "", "station name (required)")
		label := fs.String("label", "", "which machine this key is for, e.g. laptop (recommended)")
		actor := fs.String("actor", "", "actor to mint under — MUST match this machine's comm token actor")
		locker := fs.Bool("locker", false, "also grant the station-locker scope")
		_ = fs.Parse(args[1:])
		if *name == "" {
			die("--station is required")
		}
		st := mustOpenStore(envOr("KEN_DB", "./data/ken.db"))
		defer st.Close()
		s, err := st.StationByName(ctx, 1, *name)
		if errors.Is(err, store.ErrNotFound) {
			die(fmt.Sprintf("no station named %q — create it first: ken station add --name %s", *name, *name))
		}
		must(err)
		actorID := mustActor(ctx, st, *actor)
		scopes := []string{"station"}
		if *locker {
			scopes = append(scopes, "station-locker")
		}
		key, err := st.IssueStationKey(ctx, actorID, s.StationID, *label, scopes)
		must(err)
		fmt.Printf("station key for %s (%s):\n\n  %s\n\n", s.Name, *label, key)
		fmt.Println("Shown ONCE. Put it in the project's MCP config, never in a prompt or a tool argument:")
		fmt.Printf("  claude mcp add --transport http ken-station https://<ken-host>/station/mcp \\\n")
		fmt.Printf("      --header \"Authorization: Bearer %s\"\n\n", key)
		fmt.Println("Mint a SEPARATE key per machine: revocation is per key, which is what makes it targeted.")
		if *actor == "" {
			fmt.Println("\nNOTE: minted under the default actor. If this machine also has a COMM token, mint")
			fmt.Println("both under the SAME --actor, or the hearsay marking silently fails open.")
		}

	case "requests":
		st := mustOpenStore(envOr("KEN_DB", "./data/ken.db"))
		defer st.Close()
		rs, err := st.PendingStationRequests(ctx, 1)
		must(err)
		if len(rs) == 0 {
			fmt.Println("no pending requests")
			return
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tKIND\tNAME HINT\tPURPOSE / REASON\tASKED")
		for _, r := range rs {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.RequestID, r.Kind, r.NameHint, r.Purpose+r.Reason, r.CreatedAt)
		}
		_ = w.Flush()
		fmt.Println("\nApprove by creating the station with the name YOU choose:")
		fmt.Println("  ken station add --name <your-name> --purpose \"…\"")

	default:
		die("usage: ken station add|list|key|requests")
	}
}

// mustActor resolves a human actor, defaulting to the first human user so the common
// case needs no flag.
func mustActor(ctx context.Context, st *store.Store, name string) int64 {
	if name != "" {
		id, err := st.FindOrCreateActor(ctx, "human", name)
		must(err)
		return id
	}
	id, err := st.FirstHumanActor(ctx)
	if err != nil {
		die("no human user exists yet — create one first: ken user add --name you")
	}
	return id
}
