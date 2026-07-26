package main

import (
	"context"
	"fmt"

	"github.com/Quest-ICT/ken/internal/embed"
)

func runEmbed(args []string) {
	if len(args) == 0 {
		die("usage: ken embed backfill|status")
	}
	emb, err := embed.FromEnv()
	must(err)
	st := mustOpenStore(envOr("KEN_DB", "./data/ken.db"))
	defer st.Close()
	ctx := context.Background()

	switch args[0] {
	case "status":
		embedded, total, err := st.EmbeddingStats(ctx)
		must(err)
		prov := "none (set KEN_EMBED_PROVIDER)"
		if emb != nil {
			prov = fmt.Sprintf("%s (dim %d)", emb.ID(), emb.Dimension())
		}
		fmt.Printf("provider: %s\nembedded: %d / %d versions\n", prov, embedded, total)

	case "backfill":
		if emb == nil {
			die("no embedding provider configured (set KEN_EMBED_PROVIDER=http|hash and KEN_EMBED_* vars)")
		}
		targets, err := st.VersionsNeedingEmbedding(ctx, emb.ID(), 0)
		must(err)
		if len(targets) == 0 {
			fmt.Println("nothing to embed — all versions already have vectors for", emb.ID())
			return
		}
		const batch = 64
		done := 0
		for i := 0; i < len(targets); i += batch {
			end := i + batch
			if end > len(targets) {
				end = len(targets)
			}
			chunk := targets[i:end]
			texts := make([]string, len(chunk))
			for j, t := range chunk {
				texts[j] = t.Text
			}
			vecs, err := emb.Embed(ctx, texts)
			must(err)
			for j, t := range chunk {
				must(st.UpsertEmbedding(ctx, t.VersionID, emb.ID(), vecs[j]))
				done++
			}
			fmt.Printf("embedded %d/%d\r", done, len(targets))
		}
		fmt.Printf("\ndone: embedded %d versions with %s\n", done, emb.ID())

	default:
		die("unknown embed subcommand: " + args[0])
	}
}
