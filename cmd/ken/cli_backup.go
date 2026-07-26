package main

import (
	"context"
	"flag"
	"fmt"

	"github.com/Quest-ICT/ken/internal/store"
)

func runBackup(args []string) {
	if len(args) == 0 {
		die("usage: ken backup snapshot|verify")
	}
	ctx := context.Background()

	switch args[0] {
	case "snapshot":
		fs := flag.NewFlagSet("backup snapshot", flag.ExitOnError)
		out := fs.String("out", "", "output snapshot path (required)")
		_ = fs.Parse(args[1:])
		if *out == "" {
			die("--out is required")
		}
		st := mustOpenStore(envOr("KEN_DB", "./data/ken.db"))
		defer st.Close()
		must(st.Snapshot(ctx, *out))
		n, err := store.VerifySnapshot(ctx, *out)
		must(err)
		fmt.Printf("snapshot: %s (%d entries, integrity ok)\n", *out, n)

	case "verify":
		if len(args) < 2 {
			die("usage: ken backup verify <file>")
		}
		n, err := store.VerifySnapshot(ctx, args[1])
		must(err)
		fmt.Printf("%s: integrity ok, %d entries\n", args[1], n)

	default:
		die("unknown backup subcommand: " + args[0])
	}
}
