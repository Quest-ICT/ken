package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"golang.org/x/term"

	"github.com/Quest-ICT/ken/internal/passwd"
)

func runUser(args []string) {
	if len(args) == 0 {
		die("usage: ken user add|list")
	}
	st := mustOpenStore(envOr("KEN_DB", "./data/ken.db"))
	defer st.Close()
	ctx := context.Background()

	switch args[0] {
	case "add":
		fs := flag.NewFlagSet("user add", flag.ExitOnError)
		name := fs.String("name", "", "user login name")
		password := fs.String("password", "", "password (insecure on the CLI; prefer the prompt or KEN_PASSWORD)")
		_ = fs.Parse(args[1:])
		if *name == "" {
			die("--name is required")
		}
		pw := *password
		if pw == "" {
			pw = os.Getenv("KEN_PASSWORD")
		}
		if pw == "" {
			pw = promptPassword("Password for " + *name + ": ")
		}
		if len(pw) < 8 {
			die("password must be at least 8 characters")
		}
		hash, err := passwd.Hash(pw, passwd.High)
		must(err)
		id, err := st.CreateHumanUser(ctx, *name, hash)
		must(err)
		fmt.Printf("created user %q (actor #%d)\n", *name, id)

	case "list":
		users, err := st.ListHumanUsers(ctx)
		must(err)
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ACTOR_ID\tNAME\tCREATED")
		for _, u := range users {
			fmt.Fprintf(w, "%d\t%s\t%s\n", u.ActorID, u.Name, u.CreatedAt)
		}
		_ = w.Flush()

	default:
		die("unknown user subcommand: " + args[0])
	}
}

// promptPassword reads a password from the terminal without echo.
func promptPassword(prompt string) string {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		die("no password provided and stdin is not a terminal; use --password or KEN_PASSWORD")
	}
	fmt.Fprint(os.Stderr, prompt)
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	must(err)
	return strings.TrimSpace(string(b))
}
