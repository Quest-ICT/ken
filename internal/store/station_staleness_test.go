package store

import (
	"context"
	"errors"
	"strings"
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

// A BLIND OVERWRITE OF AN EXISTING PAGE IS REFUSED, and this is the case a replacement
// session actually hits.
//
// ken-prod-ops measured all four on a live 3.6.0 deployment: read returns the rev, a correct
// if_rev succeeds, a STALE if_rev is refused cleanly with no partial write — and NO if_rev at
// all overwrote SILENTLY. The mechanism was correct and complete when used, and nothing
// required using it.
//
// WHY IT IS SHARPER THAN "REMEMBER TO PASS IT": `mode` defaults to append, which is
// non-destructive, so this is not a trap you fall into by default — it is one you fall into by
// doing the RIGHT thing. A handoff page's own header says never to append (history grows with
// the square of the page), so a successor obeying that reaches for replace, and unless it ALSO
// knows to read first it destroys the page it was told to read. Two correct instructions
// composing into the loss. Neither of the two stations that maintain handoff pages had ever
// called station_note_read, so the path most important to a takeover was the least exercised.
func TestReplacingAnExistingPageWithoutIfRevIsRefused(t *testing.T) {
	st, ctx, station := staleHarness(t)
	lim := DefaultStationNoteLimits()

	// A first write to a NEW key needs no rev — creating a page must stay unaffected.
	if _, err := st.WriteStationNote(ctx, lim, station, "handoff", "H", "first", nil, "replace", 0, "tok", 1, false); err != nil {
		t.Fatalf("creating a new page required a rev: %v", err)
	}

	// THE DEFECT: replacing it blind used to succeed silently.
	_, err := st.WriteStationNote(ctx, lim, station, "handoff", "H", "clobbered", nil, "replace", 0, "tok", 1, false)
	if !errors.Is(err, ErrNoteRevRequired) {
		t.Fatalf("a blind mode=replace over an existing page returned %v, want ErrNoteRevRequired.\n"+
			"This is the write a replacement session makes after being told never to append.", err)
	}
	// The refusal must NAME THE CURRENT REV, because an error is the only channel that
	// reaches a session whose tool description predates this rule — descriptions pin at
	// conversation start and never refresh.
	if !strings.Contains(err.Error(), "rev 1") {
		t.Errorf("the refusal does not name the current rev, so a refused session cannot retry "+
			"without a second call: %v", err)
	}
	// AND NOTHING WAS WRITTEN.
	n, err := st.ReadStationNote(ctx, station, "handoff")
	if err != nil {
		t.Fatal(err)
	}
	if n.Body != "first" || n.Rev != 1 {
		t.Fatalf("the refused write still changed the page: rev %d, body %q", n.Rev, n.Body)
	}

	// CONTROLS — the three cases that must keep working, so the guard is proved to be
	// narrow rather than a blanket refusal.
	if _, err := st.WriteStationNote(ctx, lim, station, "handoff", "H", "proper", nil, "replace", 1, "tok", 1, false); err != nil {
		t.Fatalf("a correct if_rev was refused: %v", err)
	}
	if _, err := st.WriteStationNote(ctx, lim, station, "handoff", "H", "stale", nil, "replace", 1, "tok", 1, false); !errors.Is(err, ErrNoteRevConflict) {
		t.Fatalf("a STALE if_rev returned %v, want ErrNoteRevConflict — the two mistakes want "+
			"different remedies and must not be conflated", err)
	}
	if _, err := st.WriteStationNote(ctx, lim, station, "handoff", "H", "\nmore", nil, "append", 0, "tok", 1, false); err != nil {
		t.Fatalf("append without a rev was refused (%v) — append is non-destructive and must stay open", err)
	}
}
