package store

import (
	"context"
	"testing"
)

// THE BRIEFING IS A SAMPLE AND SAID NOTHING ABOUT IT.
//
// The head holds at most seven items. `not_shown` reported how many were not shown THIS
// TIME, which reads as a queue awaiting its turn — harmless. It is not: with more open
// tasks than head slots the same handful surfaces every time and the rest are never shown,
// never counted, never aged, and invisible to the session AND the human.
//
// Measured across this estate on 2026-08-14: ~45 tasks blocked on one human, the large
// majority never surfaced to him once. One station held 42 open with 37 never briefed.
func TestBriefingSaysHowManyTasksHaveNeverBeenShown(t *testing.T) {
	st, ctx, station := staleHarness(t)

	// More tasks than the head can hold.
	for i := 0; i < 12; i++ {
		if _, _, err := st.AddStationTask(ctx, taskLim,
			StationTask{StationID: station, Text: "task", BlockedOn: "self"}, "tok", 1, false); err != nil {
			t.Fatal(err)
		}
	}
	lim := StationTaskLimits{BriefStampThrottleSec: 0}

	first, err := st.BriefStationTasks(ctx, lim, station)
	if err != nil {
		t.Fatal(err)
	}
	if first.NeverBriefed != 12 {
		t.Fatalf("before any briefing, never_briefed = %d, want 12", first.NeverBriefed)
	}

	second, err := st.BriefStationTasks(ctx, lim, station)
	if err != nil {
		t.Fatal(err)
	}
	// The head stamped some; the rest have STILL never been shown, and that is the number
	// nothing reported.
	if second.NeverBriefed == 0 {
		t.Fatal("never_briefed is 0 after one briefing of a 12-task list the head cannot hold — " +
			"the figure is not measuring what has never been surfaced")
	}
	if second.NeverBriefed >= 12 {
		t.Fatalf("never_briefed = %d after a briefing stamped the head — nothing is being counted as briefed", second.NeverBriefed)
	}
	// CONTROL: the shown ones really did leave the never-briefed population, so the number
	// above is a genuine partition rather than a constant.
	if second.NeverBriefed != 12-len(second.Head) {
		t.Fatalf("never_briefed = %d with a head of %d, want %d", second.NeverBriefed, len(second.Head), 12-len(second.Head))
	}
}

// AND THE HUMAN-BLOCKED PILE IS AGED, because a bare count carries no urgency and no way
// to tell a fresh request from one that has sat for a month.
func TestBriefingAgesTheHumanBlockedPile(t *testing.T) {
	st, ctx, station := staleHarness(t)

	created, _, err := st.AddStationTask(ctx, taskLim,
		StationTask{StationID: station, Text: "waiting on a decision", BlockedOn: "human"}, "tok", 1, false)
	if err != nil {
		t.Fatal(err)
	}
	id := created.TaskID
	if _, err := st.W.ExecContext(ctx,
		`UPDATE station_task SET created_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','-40 days') WHERE task_id=?`, id); err != nil {
		t.Fatal(err)
	}
	// A YOUNG one alongside it, so the figure is proved to be the MAX rather than whatever
	// it happened to read first.
	if _, _, err := st.AddStationTask(ctx, taskLim,
		StationTask{StationID: station, Text: "asked this morning", BlockedOn: "human"}, "tok", 1, false); err != nil {
		t.Fatal(err)
	}

	b, err := st.BriefStationTasks(ctx, StationTaskLimits{BriefStampThrottleSec: 0}, station)
	if err != nil {
		t.Fatal(err)
	}
	if b.OldestBlockedDays < 39 || b.OldestBlockedDays > 41 {
		t.Fatalf("oldest blocked-on-human age = %d days, want ~40", b.OldestBlockedDays)
	}
}

// THE STALE-RISK POPULATION: blocked on the human AND not briefed in over a week.
//
// blocked_on is set once at creation and NOTHING EVER REVISITS IT, so a task whose
// condition has been satisfied is indistinguishable from one still waiting — and both are
// counted in "waiting on you". Two of this station's own were found done-but-open on
// 2026-08-14, one five releases out of date; production reported the same about itself the
// same day, having twice told its human he owed something he had already finished.
func TestBriefingFlagsHumanBlockedItemsNobodyHasRevisited(t *testing.T) {
	st, ctx, station := staleHarness(t)

	oldT, _, err := st.AddStationTask(ctx, taskLim,
		StationTask{StationID: station, Text: "probably already done", BlockedOn: "human"}, "tok", 1, false)
	if err != nil {
		t.Fatal(err)
	}
	old := oldT.TaskID
	if _, err := st.W.ExecContext(ctx, `
UPDATE station_task SET last_briefed_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','-30 days')
 WHERE task_id=?`, old); err != nil {
		t.Fatal(err)
	}
	// CONTROLS: one briefed today (not stale), and one stale but NOT blocked on the human.
	freshT, _, err := st.AddStationTask(ctx, taskLim,
		StationTask{StationID: station, Text: "raised today", BlockedOn: "human"}, "tok", 1, false)
	if err != nil {
		t.Fatal(err)
	}
	fresh := freshT.TaskID
	if _, err := st.W.ExecContext(ctx, `
UPDATE station_task SET last_briefed_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE task_id=?`, fresh); err != nil {
		t.Fatal(err)
	}
	mineT, _, err := st.AddStationTask(ctx, taskLim,
		StationTask{StationID: station, Text: "my own, long untouched", BlockedOn: "self"}, "tok", 1, false)
	if err != nil {
		t.Fatal(err)
	}
	mine := mineT.TaskID
	if _, err := st.W.ExecContext(ctx, `
UPDATE station_task SET last_briefed_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','-30 days') WHERE task_id=?`, mine); err != nil {
		t.Fatal(err)
	}

	b, err := st.BriefStationTasks(ctx, StationTaskLimits{BriefStampThrottleSec: 999999}, station)
	if err != nil {
		t.Fatal(err)
	}
	if b.StaleRisk != 1 {
		t.Fatalf("stale-risk count = %d, want exactly 1.\n"+
			"It must exclude the human-blocked item briefed today AND the stale item that is "+
			"blocked on ME — widening it to either makes the number stop meaning anything.", b.StaleRisk)
	}
}

// taskLim is permissive: these tests are about the briefing figures, not about caps.
var taskLim = StationTaskLimits{MaxOpen: 500, MaxTextBytes: 4096, MaxDetailBytes: 65536, BriefStampThrottleSec: 0}

func staleHarness(t *testing.T) (*Store, context.Context, string) {
	t.Helper()
	st := newStore(t)
	ctx := context.Background()
	actor, err := st.FindOrCreateActor(ctx, "human", "admin")
	if err != nil {
		t.Fatal(err)
	}
	s, err := st.CreateStation(ctx, 1, "staleness", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	return st, ctx, s.StationID
}
