package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func noteFixture(t *testing.T) (*Store, context.Context, StationNoteLimits, string, int64) {
	t.Helper()
	st, ctx, actorID := stationFixture(t)
	s, err := st.CreateStation(ctx, 1, "prod-ops", "", actorID)
	if err != nil {
		t.Fatal(err)
	}
	return st, ctx, DefaultStationNoteLimits(), s.StationID, actorID
}

// append vs replace, and revisions retained — the undo buffer S12 carves out.
func TestNoteAppendReplaceAndRevisions(t *testing.T) {
	st, ctx, lim, sid, actorID := noteFixture(t)

	n, err := st.WriteStationNote(ctx, lim, sid, "handoff", "Handoff", "first line", nil, "replace", 0, "tok", actorID, false)
	if err != nil {
		t.Fatal(err)
	}
	if n.Rev != 1 {
		t.Fatalf("rev = %d, want 1", n.Rev)
	}
	n, err = st.WriteStationNote(ctx, lim, sid, "handoff", "Handoff", "second line", nil, "append", 0, "tok", actorID, false)
	if err != nil {
		t.Fatal(err)
	}
	if n.Rev != 2 || !strings.Contains(n.Body, "first line") || !strings.Contains(n.Body, "second line") {
		t.Fatalf("append failed: rev=%d body=%q", n.Rev, n.Body)
	}
	// The outgoing body is kept as history.
	var revs int
	st.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM station_note_revision WHERE station_id=? AND key='handoff'`, sid).Scan(&revs)
	if revs != 1 {
		t.Fatalf("want 1 retained revision, got %d", revs)
	}

	n, err = st.WriteStationNote(ctx, lim, sid, "handoff", "Handoff", "only this", nil, "replace", 0, "tok", actorID, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(n.Body, "first line") {
		t.Fatal("replace should not keep the old body")
	}
}

// Two sessions may staff one station, so a blind write must not silently destroy the
// other's page: if_rev is an optimistic-concurrency precondition (§3).
func TestNoteIfRevPreventsSilentOverwrite(t *testing.T) {
	st, ctx, lim, sid, actorID := noteFixture(t)
	if _, err := st.WriteStationNote(ctx, lim, sid, "plan", "", "v1", nil, "replace", 0, "a", actorID, false); err != nil {
		t.Fatal(err)
	}
	// Session B advances it.
	if _, err := st.WriteStationNote(ctx, lim, sid, "plan", "", "v2", nil, "replace", 1, "b", actorID, false); err != nil {
		t.Fatal(err)
	}
	// Session A still thinks it is at rev 1.
	_, err := st.WriteStationNote(ctx, lim, sid, "plan", "", "v1-edited", nil, "replace", 1, "a", actorID, false)
	if !errors.Is(err, ErrNoteRevConflict) {
		t.Fatalf("a stale if_rev must be refused, got %v", err)
	}
	got, _ := st.ReadStationNote(ctx, sid, "plan")
	if got.Body != "v2" {
		t.Fatalf("the losing write clobbered the page: %q", got.Body)
	}
}

// Caps REFUSE rather than evict, and the refusal says what to do instead (S12).
func TestNoteCapsRefuseAndExplain(t *testing.T) {
	st, ctx, _, sid, actorID := noteFixture(t)
	tiny := StationNoteLimits{MaxPageBytes: 32, MaxRevisionBytes: 64, MaxNotebookBytes: 128}

	_, err := st.WriteStationNote(ctx, tiny, sid, "big", "", strings.Repeat("x", 100), nil, "replace", 0, "tok", actorID, false)
	if !errors.Is(err, ErrNotebookCapReached) {
		t.Fatalf("an oversized page must be refused, got %v", err)
	}
	if !strings.Contains(err.Error(), "not a note") {
		t.Fatalf("the refusal should say what to do instead, got: %v", err)
	}
	// Nothing was written — refusing is not a partial write.
	if _, err := st.ReadStationNote(ctx, sid, "big"); !errors.Is(err, ErrNotFound) {
		t.Fatal("a refused write must leave no page behind")
	}
}

// Revision history is pruned oldest-first — the one deletion this design makes without
// asking, and it must never touch the head.
func TestRevisionHistoryPrunesOldestFirstAndKeepsHead(t *testing.T) {
	st, ctx, _, sid, actorID := noteFixture(t)
	lim := StationNoteLimits{MaxPageBytes: 1 << 10, MaxRevisionBytes: 40, MaxNotebookBytes: 1 << 20}
	for i := 0; i < 8; i++ {
		if _, err := st.WriteStationNote(ctx, lim, sid, "p", "", strings.Repeat("abcdefghij", 2), nil, "replace", 0, "tok", actorID, false); err != nil {
			t.Fatal(err)
		}
	}
	var bytes int
	st.R.QueryRowContext(ctx, `SELECT COALESCE(SUM(LENGTH(body)),0) FROM station_note_revision WHERE station_id=? AND key='p'`, sid).Scan(&bytes)
	if bytes > lim.MaxRevisionBytes {
		t.Fatalf("history is %d bytes, over the %d cap — pruning did not run", bytes, lim.MaxRevisionBytes)
	}
	head, err := st.ReadStationNote(ctx, sid, "p")
	if err != nil || head.Rev != 8 {
		t.Fatalf("the head must survive pruning: %+v %v", head, err)
	}
}

// Promotion writes a PENDING row for the human, never a curated one (S10).
func TestPromoteWritesPendingNotKnowledge(t *testing.T) {
	st, ctx, lim, sid, actorID := noteFixture(t)
	if _, err := st.WriteStationNote(ctx, lim, sid, "lesson", "A lesson", "body", nil, "replace", 0, "tok", actorID, true); err != nil {
		t.Fatal(err)
	}
	id, err := st.PromoteStationNote(ctx, sid, "lesson")
	if err != nil || id == "" {
		t.Fatalf("promote: %q %v", id, err)
	}
	var state string
	var hearsay bool
	if err := st.R.QueryRowContext(ctx, `SELECT state, COALESCE(hearsay_at_write,0) FROM station_promotion WHERE promotion_id=?`, id).
		Scan(&state, &hearsay); err != nil {
		t.Fatal(err)
	}
	if state != "pending" {
		t.Fatalf("promotion state = %q, want pending", state)
	}
	if !hearsay {
		t.Fatal("the write-time hearsay marking must travel with the promotion — a marking the model retypes is forgeable")
	}
	// No entry was created: /station cannot write knowledge.
	var entries int
	st.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM entry`).Scan(&entries)
	if entries != 0 {
		t.Fatalf("promotion created %d entries — /station must not write curated rows", entries)
	}
}

// Handoff staleness is measured in station ACTIVITY, not wall-clock time: an idle
// station is never stale (§4).
func TestHandoffStalenessCountsActivityNotTime(t *testing.T) {
	st, ctx, lim, sid, actorID := noteFixture(t)
	tl := DefaultStationTaskLimits()

	if _, _, err := st.HandoffStaleness(ctx, sid); err != nil {
		t.Fatal(err)
	}
	_, n, _ := st.HandoffStaleness(ctx, sid)
	if n != -1 {
		t.Fatalf("no handoff at all should report -1, got %d", n)
	}

	if _, err := st.WriteStationNote(ctx, lim, sid, HandoffKey, "", "state of play", nil, "replace", 0, "tok", actorID, false); err != nil {
		t.Fatal(err)
	}
	if _, n, _ := st.HandoffStaleness(ctx, sid); n != 0 {
		t.Fatalf("a freshly written handoff should be 0 activities stale, got %d", n)
	}
	// Do things.
	for i := 0; i < 3; i++ {
		if _, _, err := st.AddStationTask(ctx, tl, StationTask{StationID: sid, Text: "work", BlockedOn: "self"}, "tok", actorID, false); err != nil {
			t.Fatal(err)
		}
	}
	if _, n, _ := st.HandoffStaleness(ctx, sid); n < 3 {
		t.Fatalf("activity since the handoff should count the tasks, got %d", n)
	}
}

// THE CAP IS IN BYTES AND MUST COUNT BYTES.
//
// SQLite's LENGTH() on TEXT returns CHARACTERS. Every bound in this file is a BACKUP
// decision (S12) — the number an operator reasons about is how much disk a station can
// make the nightly snapshot carry — so counting characters under-reports every
// non-ASCII page and lets a notebook exceed the size its setting promised.
//
// Benign on the corpus ken-prod-ops measured: 934,305 characters against 943,072 bytes
// estate-wide. Not benign in a Spanish or French notebook, which is most of them here.
func TestNotebookBoundsCountBytesNotCharacters(t *testing.T) {
	st, ctx, actorID := stationFixture(t)
	station, err := st.CreateStation(ctx, 1, "prod-ops", "", actorID)
	if err != nil {
		t.Fatal(err)
	}
	lim := DefaultStationNoteLimits()

	// Every rune here is 2 bytes in UTF-8 and 1 character. A cap that counts characters
	// sees half of what is really stored.
	body := strings.Repeat("é", 100)
	if _, err := st.WriteStationNote(ctx, lim, station.StationID, "acentos", "t", body,
		nil, "replace", 0, "tok", actorID, false); err != nil {
		t.Fatal(err)
	}

	notes, err := st.ListStationNotes(ctx, station.StationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 {
		t.Fatalf("%d notes, want 1", len(notes))
	}
	if notes[0].Bytes != len(body) {
		t.Fatalf("the notebook reports %d bytes for a page that occupies %d.\n"+
			"Every bound here is a backup decision, and one that under-counts by half on accented text "+
			"lets a station carry twice the disk its setting promised.", notes[0].Bytes, len(body))
	}
}

// A SESSION MUST BE ABLE TO SEE WHAT PRUNING TOOK.
//
// The history bound deletes oldest-first and says nothing — no log line, no field in the
// write result, nothing in the page listing. A live station was measured at head rev 26
// holding only revisions 18 and up: seventeen gone, including its original context, and
// it could not have found out.
//
// This is the cheapest surface that tells it, and it is a tool RESULT rather than a tool
// description or an instruction — measured to be the only channel that reaches a
// conversation already in progress.
func TestTheNoteListingRevealsRevisionsAlreadyPruned(t *testing.T) {
	st, ctx, actorID := stationFixture(t)
	station, err := st.CreateStation(ctx, 1, "prod-ops", "", actorID)
	if err != nil {
		t.Fatal(err)
	}
	lim := DefaultStationNoteLimits()
	lim.MaxRevisionBytes = 300 // small enough to cross in a handful of writes

	for i := 0; i < 12; i++ {
		if _, err := st.WriteStationNote(ctx, lim, station.StationID, "handoff", "t",
			strings.Repeat("x", 100), nil, "replace", 0, "tok", actorID, false); err != nil {
			t.Fatal(err)
		}
	}

	notes, err := st.ListStationNotes(ctx, station.StationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 {
		t.Fatalf("%d notes, want 1", len(notes))
	}
	n := notes[0]
	if n.Rev != 12 {
		t.Fatalf("head rev = %d, want 12", n.Rev)
	}
	// TWO-SIDED, because one side alone is satisfied by a field that always reports the
	// head revision — which is exactly what a mutation returning n.rev did, and my first
	// version of this check let it through.
	if n.OldestRev >= n.Rev {
		t.Fatalf("oldest retained revision (%d) is not BELOW the head (%d) — the field is reporting "+
			"the head rather than the lowest revision actually held, so it would say 'nothing lost' forever",
			n.OldestRev, n.Rev)
	}
	if n.OldestRev <= 1 {
		t.Fatalf("oldest retained revision is %d after twelve writes past a 300-byte bound — "+
			"nothing was pruned, so this test is not exercising the loss it exists to report", n.OldestRev)
	}
	if n.HistoryBytes == 0 {
		t.Error("history size reports zero while revisions are retained")
	}

	// CONTROL: a page whose history has NOT been pruned reports no loss. Without this,
	// the assertion above would also pass on a field that always reported a gap.
	if _, err := st.WriteStationNote(ctx, lim, station.StationID, "fresh", "t", "short",
		nil, "replace", 0, "tok", actorID, false); err != nil {
		t.Fatal(err)
	}
	notes, err = st.ListStationNotes(ctx, station.StationID)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range notes {
		if p.Key == "fresh" && p.Rev != p.OldestRev {
			t.Fatalf("a page written once reports revisions lost (head %d, oldest %d) — "+
				"the signal fires on pages that lost nothing, which is how a curator learns to ignore it",
				p.Rev, p.OldestRev)
		}
	}
}

// THE NUMBER MUST BE HOW MANY ARE GONE, NOT HOW MANY REMAIN.
//
// Shipped in 3.0.0 as `head_rev - oldest_retained`, which is the count of SURVIVING
// history rows. ken-prod-ops measured it on a live station within twenty minutes of the
// release: head 6, revisions 1..5 present, nothing ever pruned, and the field returned 5.
//
// The inversion is worse than an off-by-N because it points at the wrong station. A
// healthy page with a long history reports a big number; the one page in their estate
// that genuinely lost seventeen revisions would report 8, LESS than the healthy ones
// around it. The field was added so that station would finally see its own damage.
func TestRevisionsLostCountsWhatIsMissingNotWhatSurvived(t *testing.T) {
	st, ctx, actorID := stationFixture(t)
	station, err := st.CreateStation(ctx, 1, "prod-ops", "", actorID)
	if err != nil {
		t.Fatal(err)
	}
	lim := DefaultStationNoteLimits()

	// A page with plenty of history and NOTHING pruned: the bound is generous, so every
	// revision from 1 up is still held.
	for i := 0; i < 6; i++ {
		if _, err := st.WriteStationNote(ctx, lim, station.StationID, "healthy", "t",
			strings.Repeat("a", 50), nil, "replace", 0, "tok", actorID, false); err != nil {
			t.Fatal(err)
		}
	}
	// A page that HAS been pruned: a tight bound, many writes.
	tight := lim
	tight.MaxRevisionBytes = 300
	for i := 0; i < 12; i++ {
		if _, err := st.WriteStationNote(ctx, tight, station.StationID, "damaged", "t",
			strings.Repeat("b", 100), nil, "replace", 0, "tok", actorID, false); err != nil {
			t.Fatal(err)
		}
	}
	// A page written exactly once: no history at all.
	if _, err := st.WriteStationNote(ctx, lim, station.StationID, "fresh", "t", "once",
		nil, "replace", 0, "tok", actorID, false); err != nil {
		t.Fatal(err)
	}

	notes, err := st.ListStationNotes(ctx, station.StationID)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]StationNote{}
	for _, n := range notes {
		by[n.Key] = n
	}

	if got := by["healthy"].RevisionsLost; got != 0 {
		t.Errorf("a page that never pruned reports %d revisions lost, want 0.\n"+
			"Every healthy station is told it lost history, which is how the signal becomes noise.", got)
	}
	if got := by["fresh"].RevisionsLost; got != 0 {
		t.Errorf("a page written once reports %d revisions lost, want 0", got)
	}

	dmg := by["damaged"]
	if dmg.RevisionsLost == 0 {
		t.Fatalf("a page pruned past a 300-byte bound reports nothing lost (head %d) — "+
			"the one page that needs the signal does not get it", dmg.Rev)
	}
	// The exact relationship, not merely "non-zero": revisions are numbered 1..head-1 in
	// history, so what is missing is everything below the oldest still held.
	if want := dmg.OldestRev - 1; dmg.RevisionsLost != want {
		t.Errorf("revisions lost = %d, want %d (oldest retained %d, head %d)",
			dmg.RevisionsLost, want, dmg.OldestRev, dmg.Rev)
	}
	// AND THE ORDERING, which is the property that actually failed in production: the
	// damaged page must report MORE loss than the healthy one. The shipped arithmetic
	// inverted exactly this.
	if dmg.RevisionsLost <= by["healthy"].RevisionsLost {
		t.Fatalf("the pruned page reports %d lost and the intact page reports %d — "+
			"a curator comparing them is pointed at the wrong station",
			dmg.RevisionsLost, by["healthy"].RevisionsLost)
	}
}
