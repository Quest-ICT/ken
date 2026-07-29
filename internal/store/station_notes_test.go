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
