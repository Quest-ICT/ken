package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
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
		die("usage: ken station add|list|rename|requests")
	}
	ctx := context.Background()

	switch args[0] {
	case "key":
		// NAMED EXPLICITLY rather than falling into the usage default, so a script that called it
		// gets a message about a RETIREMENT instead of one that reads like a typo. The two are
		// indistinguishable otherwise, which is the whole failure mode this release keeps finding.
		die("`ken station key` was retired in 4.0.0: there are no station keys. A session claims its " +
			"station by passing session_key to station_me, and authenticates with the OAuth grant. " +
			"See docs/UPGRADING.md")
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
		actorID := mustHumanActor(ctx, st, *actor)
		s, err := st.CreateStation(ctx, *name, *purpose, actorID)
		if errors.Is(err, store.ErrStationNameTaken) {
			die(fmt.Sprintf("a station named %q already exists in this space", *name))
		}
		must(err)
		fmt.Printf("station %s created (id %s)\n", s.Name, s.StationID)
		// NO NEXT STEP TO PRINT, AND THAT IS THE FEATURE. This used to say "mint a key for a
		// machine with: ken station key …" — a subcommand retired in 4.0.0, so the line handed the
		// operator a command that dies with a usage string advertising the verb it just rejected.
		// A session claims a station by stating its own conversation id on its first station_me
		// call; there is nothing to mint, deliver or protect.
		fmt.Println("a session claims it by passing session_key to station_me — nothing to mint")

	// `rename` is the FALLBACK, not the surface. The console owns this (a name is the one
	// thing about a station that is purely the human's, and it belongs where they can see
	// what they are renaming) — this exists for a headless box, and because RenameStation's
	// own comment has always promised both paths while providing neither.
	case "rename":
		fs := flag.NewFlagSet("station rename", flag.ExitOnError)
		from := fs.String("station", "", "current station name (required)")
		to := fs.String("to", "", "new name (required)")
		_ = fs.Parse(args[1:])
		if *from == "" || *to == "" {
			die("--station and --to are both required")
		}
		st := mustOpenStore(envOr("KEN_DB", "./data/ken.db"))
		defer st.Close()
		s, err := st.StationByName(ctx, *from)
		if errors.Is(err, store.ErrNotFound) {
			die(fmt.Sprintf("no station named %q", *from))
		}
		must(err)
		err = st.RenameStation(ctx, s.StationID, *to)
		if errors.Is(err, store.ErrStationNameTaken) {
			die(fmt.Sprintf("a station named %q already exists in this space", *to))
		}
		must(err)
		// The id is printed because it, not the name, is what every config and link holds —
		// so this line also shows that renaming moved nothing anything else depends on.
		fmt.Printf("station %s renamed to %s (id %s, unchanged)\n", *from, *to, s.StationID)

	case "list":
		st := mustOpenStore(envOr("KEN_DB", "./data/ken.db"))
		defer st.Close()
		ss, err := st.ListStations(ctx)
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

	// `ken station key` IS DELETED. It minted a `kens_` bearer per machine and printed a
	// `claude mcp add ... /station/mcp` line to paste into a config — two things that no longer
	// exist: station keys are retired, and there is no /station/mcp. A session claims its station
	// in-band with session_key and needs no credential of its own.

	case "requests":
		st := mustOpenStore(envOr("KEN_DB", "./data/ken.db"))
		defer st.Close()
		rs, err := st.PendingStationRequests(ctx)
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
		// DO NOT tell the operator to approve with `ken station add`. CreateStation never
		// touches station_request, so the row is left pending forever while a station
		// exists — the exact split state ApproveStationRequest's transaction exists to
		// prevent. The stranded row then renders a live Approve form: clicking it later
		// creates a SECOND station or fails on the name, and the only other exit is Deny,
		// which records a refusal for a request that was granted.
		fmt.Println("\nApprove or deny from the /stations console — that is the only path that")
		fmt.Println("resolves the request and creates the station in one transaction.")
		fmt.Println("(`ken station add` creates an UNRELATED station and leaves the request pending.)")

	default:
		die("usage: ken station add|list|rename|requests")
	}
}

// mustActor resolves a human actor, defaulting to the first human user so the common
// case needs no flag.
// mustHumanActor resolves the HUMAN recorded as a station's creator. Creation is a
// human act by design (S3), so hardcoding the kind is correct here — unlike key
// minting, where it was the defect.
func mustHumanActor(ctx context.Context, st *store.Store, name string) int64 {
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

// mustStationActor resolves which actor a station key belongs to.
//
// It belongs to the SESSION, not to the curator who mints it — and this used to get
// that wrong in a way nothing surfaced. The kind was hardcoded to "human" while COMM
// tokens default to "ai", and (kind, display_name) is unique, so the station key and
// the comm token on one machine were different actors. The hearsay window joins on the
// actor, so it could never match: the marking was permanently false, silently, on any
// deployment that followed the documented setup.
//
// So: no --actor now means "work out which one, and say so" rather than "use the first
// human". An actor is never CREATED here — a typo would otherwise mint a key that
// authenticates perfectly and marks nothing.
func mustStationActor(ctx context.Context, st *store.Store, name, kind string) int64 {
	if kind != "ai" && kind != "human" {
		die("--kind must be ai or human")
	}
	if name != "" {
		id, err := st.FindActor(ctx, kind, name)
		if errors.Is(err, store.ErrNotFound) {
			die(fmt.Sprintf("no %s actor named %q exists. Existing actors:\n%s",
				kind, name, actorTable(ctx, st)))
		}
		must(err)
		return id
	}

	cands, err := st.ActorsWithCommStatus(ctx)
	must(err)
	var withComm []store.ActorCandidate
	for _, c := range cands {
		if c.HasComm {
			withComm = append(withComm, c)
		}
	}
	switch len(withComm) {
	case 1:
		c := withComm[0]
		fmt.Printf("Minting under actor %q (%s) — it holds this deployment's comm token, so the\n"+
			"hearsay marking will work. Override with --actor/--kind.\n\n", c.Name, c.Kind)
		return c.ID
	case 0:
		// No comm token anywhere: stations work with COMM off (S2), so this is a
		// legitimate deployment rather than an error. Any actor will do, and the
		// marking is simply absent — "no signal", never "known clean".
		id, ferr := st.FirstHumanActor(ctx)
		if ferr != nil {
			die("no actors exist yet — create a user first: ken user add --name you")
		}
		fmt.Print("No comm token found, so nothing to match: the hearsay marking will be absent\n" +
			"rather than wrong. If you enable COMM later, re-mint this key under that token's actor.\n\n")
		return id
	default:
		die(fmt.Sprintf("several actors hold comm tokens, so I will not guess which machine this key is for.\n"+
			"Pass --actor NAME (and --kind if not ai):\n%s", actorTable(ctx, st)))
		return 0
	}
}

// actorTable renders the candidates so an operator can pick without a second command.
func actorTable(ctx context.Context, st *store.Store) string {
	cands, err := st.ActorsWithCommStatus(ctx)
	if err != nil {
		return "  (could not list actors: " + err.Error() + ")"
	}
	var b strings.Builder
	for _, c := range cands {
		mark := "  "
		if c.HasComm {
			mark = "* "
		}
		fmt.Fprintf(&b, "%s%-8s %-24s %s\n", mark, c.Kind, c.Name, c.CommTags)
	}
	b.WriteString("  (* holds a comm token — that is the one to match)")
	return b.String()
}
