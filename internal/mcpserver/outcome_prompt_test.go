package mcpserver

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/Quest-ICT/ken/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// *** THE PROMPT ARRIVES AT THE MOMENT THE OUTCOME IS OWED, NOT AT CONNECT. ***
//
// Ken's connect-time text has said "close the loop EVERY time … do not skip it" for its whole
// life, and the live deployment measures 250 uses against 37 outcomes — 14.8%, with only 22 of 108
// entries carrying any outcome at all. Every session skips it, including the ones that wrote the
// sentence.
//
// FINISHING.md's diagnosis is the one this acts on: it is "something the instructions request that
// nothing prompts for AT THE MOMENT IT MATTERS." A rule delivered once at connect competes with
// the whole conversation; a rule in the RESULT arrives at the occasion. kb_get is that occasion
// exactly — `use_count` is bumped only by Store.Get, whose sole caller is this tool, so a "use" is
// precisely "an agent fetched the full entry to apply it".
//
// Asserted on the value the AGENT receives, not on a const, because that is the lesson from the
// instruction refit: a test that reads a definition cannot see what delivery does to it.
func TestKbGetPromptsForTheOutcomeItIsAboutToBeOwed(t *testing.T) {
	out := getOut{}
	if out.OutcomeNote != "" || len(out.OutcomeOwed) != 0 {
		t.Fatal("an empty result carries a prompt; a session that fetched nothing owes nothing")
	}

	got := buildGetOut(t, []string{"dns-ttl-tradeoffs", "sqlite-wal-copy"})

	// EVERY slug is named. A generic "record an outcome" produces ONE report for a batch of
	// several, which is the failure mode the tracker predicted: "one kb_get may carry several
	// slugs and bumps each, while a session is likely to record at most one outcome for the batch."
	for _, slug := range []string{"dns-ttl-tradeoffs", "sqlite-wal-copy"} {
		found := false
		for _, s := range got.OutcomeOwed {
			if s == slug {
				found = true
			}
		}
		if !found {
			t.Errorf("outcome_owed does not name %q — a session reporting once for the batch leaves the "+
				"other entry unproven forever", slug)
		}
	}
	if n := strconv.Itoa(len(got.OutcomeOwed)); !strings.Contains(got.OutcomeNote, n) {
		t.Errorf("the note does not state the count (%s), so a session cannot tell it owes more than one", n)
	}
	for _, want := range []string{"kb_record_outcome", "helped", "didnt-apply", "was-wrong"} {
		if !strings.Contains(got.OutcomeNote, want) {
			t.Errorf("the note never mentions %q — a prompt that does not name the call is a nag", want)
		}
	}
	// AND THE FALLBACK, because the tool is the thing most likely to be missing: some clients
	// filter what they show, and a session that cannot see kb_record_outcome silently does not
	// close the loop and reports nothing wrong.
	for _, want := range []string{"not in your tool list", "tell your human in words", "naming the slug"} {
		if !strings.Contains(got.OutcomeNote, want) {
			t.Errorf("the note is missing %q — a session whose client hides kb_record_outcome needs the "+
				"condition, the fallback, AND what to say; drop any one and it skips the step and tells nobody", want)
		}
	}
}

// buildGetOut assembles what the handler assembles, THROUGH THE SAME FUNCTION — so this test
// cannot pass against a sentence only it knows.
func buildGetOut(t *testing.T, slugs []string) getOut {
	t.Helper()
	return getOut{OutcomeOwed: slugs, OutcomeNote: outcomeNote(len(slugs))}
}

// *** THE BATCH CASE, OVER THE REAL TRANSPORT — which is the only place it can be proven. ***
//
// The tracker predicted the failure precisely: "one kb_get may carry several slugs and bumps each,
// while a session is likely to record at most one outcome for the batch." So the prompt must name
// EVERY slug, and only a multi-entry fetch can show that it does.
//
// Written because mutation caught the gap: collecting only entries[0] into outcome_owed survived
// both the unit test above (which builds its own list) and the existing end-to-end test (which
// fetches a single slug). Two tests, neither able to see it. A single-item fixture cannot
// distinguish "all of them" from "the first one".
func TestKbGetNamesEverySlugInABatch(t *testing.T) {
	t.Setenv("KEN_DEV_TOKEN", "dev-secret")
	srv, st := testServer(t)
	ctx := context.Background()

	if _, err := st.Save(ctx, store.SaveInput{
		Slug: "second-entry-for-the-batch", Kind: "reference", Category: "test",
		Content:    store.Content{Title: "Second entry", Summary: "s", Problem: "p", Solution: "sol"},
		Confidence: 0.5, AuthorKind: "ai",
	}); err != nil {
		t.Fatalf("seed a second entry: %v", err)
	}

	cli := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	sess, err := cli.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: srv.URL, HTTPClient: clientWithToken("dev-secret"), DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	want := []string{"docker-copy-manifests-before-source", "second-entry-for-the-batch"}
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name: "kb_get", Arguments: map[string]any{"slugs": want}})
	if err != nil {
		t.Fatal(err)
	}
	var out getOut
	decodeResult(t, res, &out)

	if len(out.Entries) != len(want) {
		t.Fatalf("fixture: asked for %d slugs, got %d entries back — the batch case is not being exercised",
			len(want), len(out.Entries))
	}
	for _, slug := range want {
		found := false
		for _, s := range out.OutcomeOwed {
			if s == slug {
				found = true
			}
		}
		if !found {
			t.Errorf("kb_get handed over %q and did not name it in outcome_owed (%v). use_count is bumped "+
				"per slug, so an outcome is owed per slug; naming a subset is how a batch of five becomes "+
				"one report and four entries that stay unproven forever", slug, out.OutcomeOwed)
		}
	}
}
