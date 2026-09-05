package store

import (
	"context"
	"strings"
	"testing"
)

// *** THE THREE GUARANTEES 6.0.0 REPLACED THE CURATION GATE WITH. ***
//
// Each of these was either impossible before (retire), or was the gate's opposite (a write being
// live), or is a hazard the gate used to hide (the denormalized columns drifting). They are in one
// file because they stand or fall together: remove the gate without all three and the release is a
// net loss, not a simplification.

// A REVISION MUST REFRESH THE DENORMALIZED COLUMNS, AND THIS IS THE EASIEST THING IN THE CHANGE TO
// GET WRONG.
//
// get.go reads title/summary/tags/triggers from `entry` and the BODY from the version. They agreed
// before 6.0.0 only because Promote was their sole writer and ProposeEnhancement never touched
// them. Move the head without the refresh and the first revision that edits a title serves the OLD
// title beside the NEW body — in kb_get, in kb_search and on /browse — permanently, with nothing
// failing anywhere.
//
// VERIFY BY DELETION: drop the four title/summary/tags/triggers lines from the UPDATE in
// ProposeEnhancement and this goes red on the title.
func TestARevisionRefreshesTheColumnsTheReadersActuallyUse(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	sr, err := st.Save(ctx, SaveInput{Kind: "reference", AuthorKind: "ai",
		Content: Content{Title: "Old title", Summary: "old summary", Solution: "old solution"}})
	if err != nil {
		t.Fatal(err)
	}
	newTitle, newSummary := "New title", "new summary"
	if _, err := st.ProposeEnhancement(ctx, ProposeInput{
		Slug: sr.Slug, ChangeNote: "retitle", AuthorKind: "ai",
		Patch: Patch{Title: &newTitle, Summary: &newSummary},
	}); err != nil {
		t.Fatal(err)
	}

	e, err := st.GetEntry(ctx, sr.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if e.Title != newTitle {
		t.Errorf("entry.title is %q after a revision that set %q — the denormalized columns did "+
			"not move with the head, so every reader gets the old title beside the new body",
			e.Title, newTitle)
	}
	if e.Summary != newSummary {
		t.Errorf("entry.summary is %q, want %q", e.Summary, newSummary)
	}
	// And search — which ranks on the entry row — must find the new words, not the old.
	if res, _ := st.Search(ctx, "New title", SearchOpts{}); len(res) == 0 {
		t.Error("the revised title is not searchable")
	}
}

// A RETIRED ENTRY MUST STILL ANSWER, LOUDLY. It stops being DISCOVERABLE; it does not vanish.
//
// The asymmetry is deliberate and it is the opposite of the gate's mistake. A session holding a
// slug from an older conversation, or following a link, must be TOLD the knowledge was retired.
// Answering "not found" would let it conclude the entry never existed and write it again — which
// is how a deliberate retirement turns into a loop.
//
// VERIFY BY DELETION: make SetLifecycle('archived') also hide the entry from GetEntry and the
// second half goes red.
func TestARetiredEntryLeavesSearchButStillAnswers(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	sr, err := st.Save(ctx, SaveInput{Kind: "reference", AuthorKind: "ai",
		Content: Content{Title: "Retire me", Summary: "quokka baseline for retirement"}})
	if err != nil {
		t.Fatal(err)
	}
	if res, _ := st.Search(ctx, "quokka baseline", SearchOpts{}); len(res) == 0 {
		t.Fatal("the entry is not findable before retirement, so this test proves nothing")
	}

	const why = "superseded by the vendor's own documentation"
	if err := st.SetLifecycle(ctx, sr.Slug, "archived", why, 0, "ai"); err != nil {
		t.Fatal(err)
	}

	// Gone from discovery.
	if res, _ := st.Search(ctx, "quokka baseline", SearchOpts{}); len(res) != 0 {
		t.Error("a retired entry is still returned by the default search")
	}
	// But it answers, and the reason survives.
	e, err := st.GetEntry(ctx, sr.Slug)
	if err != nil {
		t.Fatalf("a retired entry must still answer, not 404: %v", err)
	}
	if e.Lifecycle != "archived" {
		t.Errorf("lifecycle is %q, want archived", e.Lifecycle)
	}
	if got, _ := st.RetiredReason(ctx, sr.Slug); !strings.Contains(got, "vendor") {
		t.Errorf("the retirement reason did not survive: %q", got)
	}

	// AND IT COMES BACK. Retiring is reversible or it is a delete wearing a softer word.
	if err := st.SetLifecycle(ctx, sr.Slug, "active", "", 0, "human"); err != nil {
		t.Fatal(err)
	}
	if res, _ := st.Search(ctx, "quokka baseline", SearchOpts{}); len(res) == 0 {
		t.Error("a restored entry did not come back into the search")
	}
}

// RETIRING WITHOUT A REASON IS REFUSED, IN THE STORE.
//
// Not in the handler and not in the form: the console, the CLI and kb_retract all reach this, and
// a rule enforced at one caller is a rule the next caller skips. An entry that disappears from
// every search with no recorded explanation is the quietest way this system can lose knowledge.
func TestRetiringWithoutAReasonIsRefused(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	sr, _ := st.Save(ctx, SaveInput{Kind: "reference", AuthorKind: "ai",
		Content: Content{Title: "T", Summary: "s"}})

	if err := st.SetLifecycle(ctx, sr.Slug, "archived", "   ", 0, "ai"); err == nil {
		t.Fatal("a retirement with a blank reason was accepted")
	}
	if e, _ := st.GetEntry(ctx, sr.Slug); e.Lifecycle == "archived" {
		t.Fatal("the entry was retired anyway by the call that was refused")
	}
}

// RETIRING MUST NOT BE A ONE-WAY DOOR IN THE CONSOLE.
//
// browse hid archived entries with an unconditional "lifecycle != 'archived'" while also offering
// an optional "lifecycle = ?" filter — so asking for archived produced two contradictory clauses
// and an always-empty page. That was harmless while nothing could archive anything. 6.0.0 gives
// 'archived' its first writer, and with the contradiction left in place a retired entry would leave
// search AND browse with no console surface able to list it again, so Restore could never be
// reached. The store would still hold it; the human would have no way in.
//
// VERIFY BY DELETION: put the unconditional "lifecycle != 'archived'" back and the second half
// goes red.
func TestBrowseCanListRetiredEntriesSoTheyCanBeRestored(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	live, _ := st.Save(ctx, SaveInput{Kind: "reference", AuthorKind: "ai",
		Content: Content{Title: "Still live", Summary: "s"}})
	gone, _ := st.Save(ctx, SaveInput{Kind: "reference", AuthorKind: "ai",
		Content: Content{Title: "Retired one", Summary: "s"}})
	if err := st.SetLifecycle(ctx, gone.Slug, "archived", "obsolete", 0, "human"); err != nil {
		t.Fatal(err)
	}

	has := func(rows []BrowseRow, slug string) bool {
		for _, r := range rows {
			if r.Slug == slug {
				return true
			}
		}
		return false
	}

	def, _, err := st.ListEntries(ctx, BrowseFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if !has(def, live.Slug) {
		t.Error("the live entry is missing from the default browse")
	}
	if has(def, gone.Slug) {
		t.Error("a retired entry appears in the default browse")
	}

	arc, _, err := st.ListEntries(ctx, BrowseFilter{Lifecycle: "archived"})
	if err != nil {
		t.Fatal(err)
	}
	if !has(arc, gone.Slug) {
		t.Fatal("browse?lifecycle=archived does not list the retired entry — retiring is a one-way " +
			"door and nothing in the console can reach it to restore it")
	}
	if has(arc, live.Slug) {
		t.Error("the archived filter also returned a live entry")
	}
}

// AN AGENT MUST NOT BE ABLE TO ERASE A REFUTATION WITH A COSMETIC EDIT.
//
// A was-wrong outcome blocks an entry's top maturity tier. refuted_since compared the report's
// timestamp against the HEAD VERSION's, so any new version — a tags-only edit, a one-word touch —
// carried a newer timestamp, put every prior report "before" the head, and cleared the refutation.
// That is the entry's author erasing the only evidence against it, and with the curation gate gone
// there is no human in front of the write to notice.
//
// A REAL rewrite still answers a report, and should: the content the report was about is gone.
// The line is prose-or-code changed, which is the same line ProposeEnhancement uses for staleness.
//
// VERIFY BY DELETION: drop the staleness arm from refuted_since in search.go and this goes red.
func TestACosmeticRevisionCannotEraseAWasWrongReport(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	sr, err := st.Save(ctx, SaveInput{Kind: "reference", AuthorKind: "ai",
		Content: Content{Title: "Wombat pooling", Summary: "wombat connection pooling baseline",
			Solution: "raise the idle cap"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.RecordOutcome(ctx, sr.Slug, "was-wrong", 0, "ai", "sess-1", "the idle cap was not the cause"); err != nil {
		t.Fatal(err)
	}

	refuted := func(what string) bool {
		t.Helper()
		res, err := st.Search(ctx, "wombat connection pooling", SearchOpts{})
		if err != nil || len(res) == 0 {
			t.Fatalf("%s: entry not found: %v", what, err)
		}
		return res[0].Maturity == "refuted" || res[0].Staleness == "stale"
	}
	if !refuted("baseline") {
		t.Fatal("a was-wrong report did not register at all, so this test proves nothing")
	}

	// A TAGS-ONLY revision must NOT clear it.
	if _, err := st.ProposeEnhancement(ctx, ProposeInput{
		Slug: sr.Slug, ChangeNote: "add a tag", AuthorKind: "ai",
		Patch: Patch{Tags: &[]string{"database"}},
	}); err != nil {
		t.Fatal(err)
	}
	if !refuted("after a tags-only revision") {
		t.Fatal("a tags-only revision erased a standing was-wrong — an agent can clear the only " +
			"evidence against its own entry without changing anything a reader can see")
	}

	// A REAL rewrite does clear it, which is correct: the content reported wrong is gone.
	fixed := "return the connection in a defer"
	if _, err := st.ProposeEnhancement(ctx, ProposeInput{
		Slug: sr.Slug, ChangeNote: "the report was right", AuthorKind: "ai",
		Patch: Patch{Solution: &fixed},
	}); err != nil {
		t.Fatal(err)
	}
	if refuted("after a real rewrite") {
		t.Error("a genuine rewrite did not answer the report — one was-wrong would be permanently fatal")
	}
}
