package store

import (
	"context"
	"strings"
	"testing"
)

func taskFixture(t *testing.T) (*Store, context.Context, StationTaskLimits, string, int64) {
	t.Helper()
	st, ctx, actorID := stationFixture(t)
	s, err := st.CreateStation(ctx, "prod-ops", "", actorID)
	if err != nil {
		t.Fatal(err)
	}
	return st, ctx, DefaultStationTaskLimits(), s.StationID, actorID
}

func add(t *testing.T, st *Store, ctx context.Context, lim StationTaskLimits, sid, text, blocked string, actorID int64) *StationTask {
	t.Helper()
	task, _, err := st.AddStationTask(ctx, lim, StationTask{StationID: sid, Text: text, BlockedOn: blocked}, "tok", actorID, false)
	if err != nil {
		t.Fatalf("add %q: %v", text, err)
	}
	return task
}

// blocked_on is required and its values are constrained: the human's only cross-station
// view must never be fed an unstated default (§11.3).
func TestBlockedOnIsRequiredAndConstrained(t *testing.T) {
	st, ctx, lim, sid, actorID := taskFixture(t)
	if _, _, err := st.AddStationTask(ctx, lim, StationTask{StationID: sid, Text: "x"}, "tok", actorID, false); err == nil {
		t.Fatal("an empty blocked_on must be refused")
	}
	if _, _, err := st.AddStationTask(ctx, lim, StationTask{StationID: sid, Text: "x", BlockedOn: "someone"}, "tok", actorID, false); err == nil {
		t.Fatal("an unknown blocked_on must be refused")
	}
	// The error must teach the vocabulary, since two sessions have to classify alike.
	_, _, err := st.AddStationTask(ctx, lim, StationTask{StationID: sid, Text: "x", BlockedOn: "nope"}, "tok", actorID, false)
	if !strings.Contains(err.Error(), "self = I can act now") {
		t.Fatalf("the refusal should define the enum, got: %v", err)
	}
}

// THE ORDERING CONTRACT (§11.5). The head has fixed slots precisely so the two monotonic
// classes cannot starve the aging clause — the failure this whole feature exists to fix.
func TestBriefingHeadDoesNotStarveAging(t *testing.T) {
	st, ctx, lim, sid, actorID := taskFixture(t)

	// A pile of human-blocked work — the class that by definition never clears.
	for i := 0; i < 10; i++ {
		add(t, st, ctx, lim, sid, "waiting on the owner "+string(rune('a'+i)), "human", actorID)
	}
	// One old self-blocked item: the thing that decays today.
	old := add(t, st, ctx, lim, sid, "the aging item nobody mentions", "self", actorID)

	b, err := st.BriefStationTasks(ctx, lim, sid)
	if err != nil {
		t.Fatal(err)
	}
	if b.OpenTotal != 11 {
		t.Fatalf("open total = %d, want 11", b.OpenTotal)
	}
	found := false
	for _, h := range b.Head {
		if h.TaskID == old.TaskID {
			found = true
		}
	}
	if !found {
		var got []string
		for _, h := range b.Head {
			got = append(got, h.BlockedOn)
		}
		t.Fatalf("the aging item was starved out of the head by human-blocked work: head = %v", got)
	}
	if len(b.Head) > 7 {
		t.Fatalf("head should be at most 2+2+3 rows, got %d", len(b.Head))
	}
}

// THE STAMPING SEMANTICS (§11.4). A list call is a pure query. If it stamped, a model
// checking its own list would silently demote items nobody was told about — and the
// aging order this feature rests on would be scrambled by its own reader.
func TestListNeverStampsAndBriefingStampsOnce(t *testing.T) {
	st, ctx, lim, sid, actorID := taskFixture(t)
	task := add(t, st, ctx, lim, sid, "an item", "self", actorID)

	for i := 0; i < 3; i++ {
		if _, _, err := st.ListStationTasks(ctx, lim, sid, "open", "", 0); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := st.StationTaskByID(ctx, task.TaskID)
	if got.BriefedCount != 0 || got.LastBriefedAt != "" {
		t.Fatalf("a list call stamped: briefed_count=%d last_briefed_at=%q", got.BriefedCount, got.LastBriefedAt)
	}

	if _, err := st.BriefStationTasks(ctx, lim, sid); err != nil {
		t.Fatal(err)
	}
	got, _ = st.StationTaskByID(ctx, task.TaskID)
	if got.BriefedCount != 1 {
		t.Fatalf("briefing should stamp once, briefed_count=%d", got.BriefedCount)
	}

	// A second briefing inside the throttle window must NOT re-stamp — otherwise
	// `station_me` called repeatedly shows a perfect surfacing history for an item the
	// human was told about once.
	if _, err := st.BriefStationTasks(ctx, lim, sid); err != nil {
		t.Fatal(err)
	}
	got, _ = st.StationTaskByID(ctx, task.TaskID)
	if got.BriefedCount != 1 {
		t.Fatalf("a re-brief inside the throttle window re-stamped: briefed_count=%d", got.BriefedCount)
	}
}

// A session may not drop what the human owes. Without this the "briefed repeatedly,
// unchanged" nag would aim the one destructive verb at the pile the feature protects.
func TestDropRefusesHumanBlockedWithoutTheHuman(t *testing.T) {
	st, ctx, lim, sid, actorID := taskFixture(t)
	mine := add(t, st, ctx, lim, sid, "my own follow-up", "self", actorID)
	theirs := add(t, st, ctx, lim, sid, "the owner must decide", "human", actorID)

	if _, err := st.DropStationTasks(ctx, sid, []string{theirs.TaskID}, "stale", false, actorID); err == nil {
		t.Fatal("a session must not drop a human-blocked task")
	}
	if n, err := st.DropStationTasks(ctx, sid, []string{mine.TaskID}, "not needed", false, actorID); err != nil || n != 1 {
		t.Fatalf("a self-blocked task should drop: n=%d err=%v", n, err)
	}
	// With the human's decision it is allowed.
	if n, err := st.DropStationTasks(ctx, sid, []string{theirs.TaskID}, "owner said no", true, actorID); err != nil || n != 1 {
		t.Fatalf("the human's own decision should drop it: n=%d err=%v", n, err)
	}
}

// Closing is the cheapest verb and takes a batch; deferring costs a date AND a reason.
func TestCloseIsBatchAndDeferCostsMore(t *testing.T) {
	st, ctx, lim, sid, actorID := taskFixture(t)
	a := add(t, st, ctx, lim, sid, "alpha", "self", actorID)
	b := add(t, st, ctx, lim, sid, "beta", "self", actorID)

	n, err := st.CloseStationTasks(ctx, sid, []string{a.TaskID, b.TaskID}, "shipped in 1.4.1", "", actorID)
	if err != nil || n != 2 {
		t.Fatalf("batch close: n=%d err=%v", n, err)
	}
	if _, err := st.CloseStationTasks(ctx, sid, []string{a.TaskID}, "", "", actorID); err == nil {
		t.Fatal("closing without a resolution must be refused — the record is the point")
	}

	c := add(t, st, ctx, lim, sid, "gamma", "self", actorID)
	if err := st.DeferStationTask(ctx, sid, c.TaskID, "2026-12-01T00:00:00.000Z", ""); err == nil {
		t.Fatal("deferring without a reason must be refused")
	}
	if err := st.DeferStationTask(ctx, sid, c.TaskID, "2026-12-01T00:00:00.000Z", "waiting on upstream"); err != nil {
		t.Fatal(err)
	}
	got, _ := st.StationTaskByID(ctx, c.TaskID)
	if got.DeferCount != 1 {
		t.Fatalf("defer should leave a counted trace, got %d", got.DeferCount)
	}
}

// Deferral suppresses from the HEAD, never from membership: the items that have gone
// quiet are exactly the ones the duplicate check and the counts must still see (§11.5).
func TestDeferralIsSurfacingNotMembership(t *testing.T) {
	st, ctx, lim, sid, actorID := taskFixture(t)
	d := add(t, st, ctx, lim, sid, "deferred thing", "self", actorID)
	if err := st.DeferStationTask(ctx, sid, d.TaskID, "2099-01-01T00:00:00.000Z", "later"); err != nil {
		t.Fatal(err)
	}
	// Still in the open set and the counts.
	ts, total, err := st.ListStationTasks(ctx, lim, sid, "open", "", 0)
	if err != nil || total != 1 || len(ts) != 1 {
		t.Fatalf("a deferred task must stay in the list: total=%d len=%d err=%v", total, len(ts), err)
	}
	b, _ := st.BriefStationTasks(ctx, lim, sid)
	if b.OpenTotal != 1 {
		t.Fatalf("a deferred task must stay in the counts, got %d", b.OpenTotal)
	}
	// But it is not in the head.
	for _, h := range b.Head {
		if h.TaskID == d.TaskID {
			t.Fatal("a not-yet-due deferred task must not occupy the briefing head")
		}
	}
	// And the near-match check still sees it — the duplicate defense must not lose it.
	_, near, err := st.AddStationTask(ctx, lim, StationTask{StationID: sid, Text: "deferred thing again", BlockedOn: "self"}, "tok", actorID, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(near) == 0 {
		t.Fatal("the near-match check must still see deferred tasks")
	}
}

// The human's whole-pile view is ordered by the SAME contract, not by recency — sinking
// old items on the one surface built to stop that would defeat the feature (§11.8).
func TestCrossStationViewOrdersByTheContract(t *testing.T) {
	st, ctx, lim, sid, actorID := taskFixture(t)
	other, _ := st.CreateStation(ctx, "promo", "", actorID)

	oldOne := add(t, st, ctx, lim, sid, "old owner decision", "human", actorID)
	add(t, st, ctx, lim, other.StationID, "new owner decision", "human", actorID)
	// Brief only the first station, so the old item gets a last_briefed_at and the new
	// one keeps NULL — under aging-first, the never-briefed one must come first.
	if _, err := st.BriefStationTasks(ctx, lim, sid); err != nil {
		t.Fatal(err)
	}

	rows, err := st.CrossStationHumanTasks(ctx, "human", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("want both stations' human-blocked tasks, got %d", len(rows))
	}
	if rows[0].TaskID == oldOne.TaskID {
		t.Fatal("a briefed item outranked a never-briefed one: the view is ordered by recency, not by the aging contract")
	}
	if rows[0].StationName == "" {
		t.Fatal("the cross-station view must carry the station name")
	}
}
