package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Quest-ICT/ken/internal/importer"
	"github.com/Quest-ICT/ken/internal/store"
)

func runImport(args []string) {
	fs := flag.NewFlagSet("import", flag.ExitOnError)
	dir := fs.String("dir", "", "directory of flat memory .md files (required)")
	dryRun := fs.Bool("dry-run", false, "parse and report without writing")
	_ = fs.Parse(args)
	if *dir == "" {
		die("--dir is required")
	}

	ents, err := os.ReadDir(*dir)
	must(err)
	ctx := context.Background()

	var st *store.Store
	if !*dryRun {
		st = mustOpenStore(envOr("KEN_DB", "./data/ken.db"))
		defer st.Close()
	}

	var imported, skipped, failed int
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if !strings.HasSuffix(n, ".md") || strings.EqualFold(n, "MEMORY.md") {
			continue // MEMORY.md is the index, not an entry
		}
		m, err := importer.ParseFile(filepath.Join(*dir, n))
		if err != nil {
			fmt.Fprintf(os.Stderr, "skip %s: %v\n", n, err)
			failed++
			continue
		}
		if *dryRun {
			fmt.Printf("would import: %-42s kind=%-9s links=%d\n", m.Slug, m.Kind, len(m.Links))
			continue
		}

		links := make([]store.LinkInput, 0, len(m.Links))
		for _, l := range m.Links {
			links = append(links, store.LinkInput{ToSlug: l.ToSlug, LinkType: l.Type})
		}
		created, err := st.ImportEntry(ctx, store.ImportInput{
			Slug: m.Slug, Kind: m.Kind,
			Content: store.Content{
				Title: m.Title, Summary: m.Summary,
				Problem: m.Problem, Rationale: m.Rationale, Solution: m.Solution,
			},
			ChangeNote: "imported from " + m.Source,
			Links:      links,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "error %s: %v\n", n, err)
			failed++
			continue
		}
		if created {
			imported++
			fmt.Printf("imported %s\n", m.Slug)
		} else {
			skipped++
			fmt.Printf("skipped (exists) %s\n", m.Slug)
		}
	}
	fmt.Printf("\ndone: %d imported, %d skipped, %d failed\n", imported, skipped, failed)
}
