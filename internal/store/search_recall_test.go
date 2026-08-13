package store

import (
	"context"
	"strings"
	"testing"
)

// AN ENTRY MUST BE FINDABLE BY ITS OWN TITLE.
//
// ken-prod-ops measured the opposite on the live knowledge base: querying one distinctive
// word returned the target at rank 1, and a query built from the entry's OWN TITLE WORDS
// did not return it in the top six. Adding words that all appear in the target pushed it
// out.
//
// What it cost is the reason this is a defect and not a ranking preference. They searched
// twice for an entry, got nothing, and told Vlad it "never landed" — writing "the proposal
// was lost" into a task. It had been curated and indexed the whole time. **An empty search
// result is indistinguishable from a missing entry**, which is the failure class this
// project keeps cataloguing, sitting in the retrieval path everything else depends on.
//
// Ken's own instructions tell sessions to query with "natural language + the exact
// symptoms/error text" — precisely the long, specific style that fails.
func TestAnEntryIsFindableByItsOwnTitle(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	target := "Time — you have no clock, so never state a duration you did not measure"
	if _, err := st.Save(ctx, SaveInput{
		Kind: "feedback", AuthorKind: "ai",
		Content: Content{
			Title:   target,
			Summary: "Durations are generated in the register of a measurement and drift upward; calibration cannot fix it.",
			Problem: "A session reports how long something took without reading a clock.",
		},
	}); err != nil {
		t.Fatal(err)
	}
	// Decoys that share the COMMON words but not the specific claim — the shape of a real
	// knowledge base, where "time", "measure" and "state" are everywhere.
	for _, d := range []struct{ title, summary string }{
		{"Measure the deployed tag, never the working tree", "State what you verified rather than what you remember."},
		{"State machine for message delivery", "Time-based transitions measured against the configured TTL."},
		{"Never state a fact you did not check", "Measure first; the duration of a check is short."},
	} {
		if _, err := st.Save(ctx, SaveInput{
			Kind: "feedback", AuthorKind: "ai",
			Content: Content{Title: d.title, Summary: d.summary},
		}); err != nil {
			t.Fatal(err)
		}
	}

	find := func(q string) int {
		t.Helper()
		res, err := st.Search(ctx, q, SearchOpts{K: 10, Scope: "all"})
		if err != nil {
			t.Fatal(err)
		}
		for i, r := range res {
			if strings.HasPrefix(r.Title, "Time — you have no clock") {
				return i + 1
			}
		}
		return 0
	}

	// CONTROL: one distinctive word finds it. If this fails the corpus or the index is
	// wrong and everything below would be measuring the wrong thing.
	if got := find("clock"); got == 0 {
		t.Fatal("the target is not findable by a single distinctive word — the index is not built, " +
			"so this test proves nothing about ranking")
	}

	// THE PROPERTY: its own title finds it, and finds it well.
	rank := find(target)
	if rank == 0 {
		t.Fatalf("THE ENTRY IS NOT FINDABLE BY ITS OWN TITLE.\n"+
			"Query: %q\nEvery word of that query appears in the entry. A session that searches this way — "+
			"which is exactly what Ken's own instructions tell it to do — gets an empty result, and an empty "+
			"result is indistinguishable from a missing entry.", target)
	}
	if rank > 3 {
		t.Errorf("the entry ranks %d for its own title, behind decoys that share only common words", rank)
	}
}

// MORE MATCHING WORDS MUST NOT RANK AN ENTRY LOWER.
//
// The specific pathology prod measured: a longer query, every word of which appears in the
// target, scoring it WORSE than a shorter one. That is monotonicity, and it is the
// property a session relies on when it follows the instruction to include exact symptom
// text.
func TestAddingWordsThatMatchDoesNotPushAnEntryDown(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	if _, err := st.Save(ctx, SaveInput{
		Kind: "reference", AuthorKind: "ai",
		Content: Content{
			Title:   "Sequence collision after adopting a station",
			Summary: "Binding moved the counter namespace and the replacement session restarted at one.",
			Problem: "comm_send returns an internal error on a channel that worked yesterday.",
		},
	}); err != nil {
		t.Fatal(err)
	}
	for i, d := range []string{
		"Station adoption keeps existing channels",
		"Sequence numbering follows the station",
		"Collision handling in the pairing code table",
	} {
		if _, err := st.Save(ctx, SaveInput{
			Kind: "reference", AuthorKind: "ai",
			Content: Content{Title: d, Summary: strings.Repeat("station sequence ", i+1)},
		}); err != nil {
			t.Fatal(err)
		}
	}

	rankOf := func(q string) int {
		t.Helper()
		res, err := st.Search(ctx, q, SearchOpts{K: 10, Scope: "all"})
		if err != nil {
			t.Fatal(err)
		}
		for i, r := range res {
			if strings.HasPrefix(r.Title, "Sequence collision") {
				return i + 1
			}
		}
		return 99
	}

	short := rankOf("sequence collision")
	long := rankOf("sequence collision after adopting a station internal error on a channel that worked yesterday")
	if long > short {
		t.Errorf("adding words that ALL appear in the entry moved it from rank %d to rank %d.\n"+
			"A session told to include the exact error text is being punished for following the instruction.",
			short, long)
	}
}

// AN EMPTY RESULT MUST BE DISTINGUISHABLE FROM AN ABSENT ENTRY.
//
// This is the defect prod's report actually exposed, and it is not a ranking preference:
// they searched twice, got nothing, and wrote "the proposal was lost" into a task about
// an entry that was curated and indexed the whole time. Nothing in the result could have
// told them. A search that matched forty and showed ten, and a search that matched
// nothing, returned the same shape.
//
// The fix is not better ranking — that trades one silent failure for another. It is
// making the ranking's effect visible.
func TestASearchSaysWhatItMatchedNotOnlyWhatItReturned(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	for i := 0; i < 8; i++ {
		if _, err := st.Save(ctx, SaveInput{
			Kind: "reference", AuthorKind: "ai",
			Content: Content{
				Title:   "Retention and the snapshot chain, note " + string(rune('a'+i)),
				Summary: "Retention governs how long a snapshot survives.",
			},
		}); err != nil {
			t.Fatal(err)
		}
	}

	// MANY MATCHED, FEW SHOWN. The page is a slice and must say so, or a session reads
	// two results as "there are two".
	res, err := st.Search(ctx, "retention snapshot", SearchOpts{K: 2, Scope: "all"})
	if err != nil {
		t.Fatal(err)
	}
	diag, err := st.Diagnose(ctx, "retention snapshot", SearchOpts{K: 2, Scope: "all"}, len(res))
	if err != nil {
		t.Fatal(err)
	}
	if diag.Matched <= len(res) {
		t.Fatalf("matched=%d with %d results — a page cut from eight matching entries reports no "+
			"more than it showed, so a session cannot tell selection from scarcity", diag.Matched, len(res))
	}
	if !diag.Truncated {
		t.Error("the page is a slice and does not say so")
	}

	// NOTHING MATCHED. Zero must be reported as zero rather than omitted, because that
	// is the value the whole field exists for.
	diag, err = st.Diagnose(ctx, "zygomorphic pterodactyl", SearchOpts{Scope: "all"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if diag.Matched != 0 {
		t.Errorf("matched=%d for words that appear nowhere", diag.Matched)
	}

	// AND IT NAMES THE DEAD WORDS, which is the actionable half: "pterodactyl matched
	// nothing" is a next query; "no results" is a conclusion.
	if len(diag.DeadTerms) == 0 {
		t.Fatal("no dead terms reported for a query of two words that appear nowhere — a session " +
			"is left to guess which of its words was the problem")
	}

	// CONTROL: a word that DOES appear is not reported dead. Without this the field
	// could satisfy the assertion above by naming every term always.
	diag, err = st.Diagnose(ctx, "retention pterodactyl", SearchOpts{Scope: "all"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, dead := range diag.DeadTerms {
		if dead == "retention" {
			t.Fatal("a word present in eight entries is reported as matching nothing — the signal " +
				"names every term regardless, which makes it noise")
		}
	}
	if len(diag.DeadTerms) != 1 || diag.DeadTerms[0] != "pterodactyl" {
		t.Fatalf("dead terms = %v, want exactly [pterodactyl]", diag.DeadTerms)
	}
}
