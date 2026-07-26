package store

import (
	"context"
	"testing"
)

func TestV1Tools(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	sr, err := st.Save(ctx, SaveInput{Kind: "project",
		Content: Content{Title: "T", Summary: "s", Solution: "old solution"}, AuthorKind: "ai"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Promote(ctx, PromoteInput{Slug: sr.Slug, VersionID: sr.VersionID, ActorKind: "human"}); err != nil {
		t.Fatal(err)
	}
	newSol := "new solution"
	p2, err := st.ProposeEnhancement(ctx, ProposeInput{Slug: sr.Slug, ChangeNote: "c", Patch: Patch{Solution: &newSol}, AuthorKind: "ai"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Promote(ctx, PromoteInput{Slug: sr.Slug, VersionID: p2.VersionID, ActorKind: "human"}); err != nil {
		t.Fatal(err)
	}

	// kb_diff: rev 1 vs rev 2 — solution changed, title didn't.
	d, err := st.VersionDiff(ctx, sr.Slug, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	changed := map[string]bool{}
	for _, f := range d.Fields {
		changed[f.Field] = f.Changed
	}
	if !changed["solution"] || changed["title"] {
		t.Fatalf("unexpected diff flags: %+v", d.Fields)
	}

	// kb_record_outcome: was-wrong flags the entry stale.
	if st1, err := st.RecordOutcome(ctx, sr.Slug, "was-wrong", 0, "ai", "", "obsolete"); err != nil || st1 != "stale" {
		t.Fatalf("was-wrong should flag stale: %s %v", st1, err)
	}
	if e, _ := st.GetEntry(ctx, sr.Slug); e.Staleness != "stale" {
		t.Fatalf("entry should be stale, got %s", e.Staleness)
	}
	// helped is recorded without changing lifecycle/staleness.
	if _, err := st.RecordOutcome(ctx, sr.Slug, "helped", 0, "ai", "", ""); err != nil {
		t.Fatal(err)
	}

	// kb_recent_context includes the entry.
	rc, err := st.RecentContext(ctx, 30, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range rc {
		if r.Slug == sr.Slug {
			found = true
		}
	}
	if !found {
		t.Fatalf("recent context should include %q: %+v", sr.Slug, rc)
	}
}
