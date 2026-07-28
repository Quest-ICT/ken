package main

import (
	"context"
	"flag"
	"fmt"
	"syscall"

	"github.com/Quest-ICT/ken/internal/store"
)

func runBackup(args []string) {
	if len(args) == 0 {
		die("usage: ken backup snapshot|verify")
	}
	// Everything this subcommand writes is a full copy of the knowledge base, or a
	// sidecar of one: the snapshot itself, and the rollback journal `verify` creates
	// beside the file it opens. Narrow the umask for the whole subcommand so those are
	// 0600 FROM CREATION, not merely chmod'd once the bytes are already on disk — a
	// multi-gigabyte VACUUM INTO would otherwise sit world-readable for its whole
	// duration. The shell wrappers set this too, but an operator following the runbook
	// by hand gets no wrapper, and the docs point them at /tmp (mode 1777).
	syscall.Umask(0o077)
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
