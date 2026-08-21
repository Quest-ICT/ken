package store

import (
	"context"
	"testing"
)

// THE COUNT AND THE LIST MUST AGREE, or the page says "showing 200 of N" with an N that
// counts something else — a completeness claim that is wrong, which is worse than no claim.
func TestCrossStationCountMatchesTheListItCaps(t *testing.T) {
	st, ctx, actor := stationFixture(t)
	a, err := st.CreateStation(ctx, 1, "alpha", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateStation(ctx, 1, "beta", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	lim := DefaultStationTaskLimits()
	add := func(station, text, blocked string) {
		t.Helper()
		if _, _, err := st.AddStationTask(ctx, lim,
			StationTask{StationID: station, Text: text, BlockedOn: blocked}, "tok", actor, false); err != nil {
			t.Fatal(err)
		}
	}
	add(a.StationID, "alpha waits on human", "human")
	add(a.StationID, "alpha waits on self", "self")
	add(b.StationID, "beta waits on human", "human")
	add(b.StationID, "beta waits on peer", "peer")

	for _, f := range []struct {
		filter string
		want   int
	}{{"human", 2}, {"self", 1}, {"peer", 1}, {"", 4}} {
		list, err := st.CrossStationHumanTasks(ctx, 1, f.filter, 1000)
		if err != nil {
			t.Fatal(err)
		}
		n, err := CountOrFail(t, st, ctx, f.filter)
		if err != nil {
			t.Fatal(err)
		}
		if n != f.want || len(list) != f.want {
			t.Fatalf("filter %q: count=%d list=%d, want %d for both", f.filter, n, len(list), f.want)
		}
	}

	// AND THE COUNT MUST EXCEED THE LIST WHEN THE CAP BITES — that is the case the
	// "showing X of Y" line exists for, and a count that silently obeyed the same limit
	// would report X of X and say nothing at all.
	capped, err := st.CrossStationHumanTasks(ctx, 1, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	total, err := st.CountCrossStationTasks(ctx, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(capped) != 2 || total != 4 {
		t.Fatalf("capped list %d, total %d — want 2 and 4, or the cap is invisible", len(capped), total)
	}
}

func CountOrFail(t *testing.T, st *Store, ctx context.Context, filter string) (int, error) {
	t.Helper()
	return st.CountCrossStationTasks(ctx, 1, filter)
}

// HumanBlockedElsewhere EXCLUDES THE CALLER'S OWN STATION. If it did not, every session
// would double-count its own pile and tell its human a number larger than the truth — and
// the briefing already reports the local count separately, right beside it.
func TestHumanBlockedElsewhereExcludesTheCaller(t *testing.T) {
	st, ctx, actor := stationFixture(t)
	mine, err := st.CreateStation(ctx, 1, "mine", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.CreateStation(ctx, 1, "other", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	third, err := st.CreateStation(ctx, 1, "third", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	lim := DefaultStationTaskLimits()
	add := func(station, text, blocked string) {
		t.Helper()
		if _, _, err := st.AddStationTask(ctx, lim,
			StationTask{StationID: station, Text: text, BlockedOn: blocked}, "tok", actor, false); err != nil {
			t.Fatal(err)
		}
	}
	add(mine.StationID, "mine A", "human")
	add(mine.StationID, "mine B", "human")
	add(other.StationID, "other A", "human")
	add(third.StationID, "third A", "human")
	add(third.StationID, "third B", "human")
	add(third.StationID, "third self", "self") // must not be counted: not blocked on the human

	tasks, stations, err := st.HumanBlockedElsewhere(ctx, 1, mine.StationID)
	if err != nil {
		t.Fatal(err)
	}
	if tasks != 3 || stations != 2 {
		t.Fatalf("got %d tasks across %d stations, want 3 across 2 (mine's own 2 excluded, 'self' excluded)", tasks, stations)
	}

	// CONTROL: from a station with nothing of its own, the answer still counts the others.
	// Without this the test passes on a query that returns a constant.
	tasks, stations, err = st.HumanBlockedElsewhere(ctx, 1, other.StationID)
	if err != nil {
		t.Fatal(err)
	}
	if tasks != 4 || stations != 2 {
		t.Fatalf("from 'other': got %d across %d, want 4 across 2", tasks, stations)
	}
}

// *** THE PROPERTY THAT COULD CORRUPT ANOTHER STATION'S STATE. ***
//
// This caller does not staff the stations it is counting and cannot relay their contents,
// so if the count stamped last_briefed_at it would record a briefing that never happened —
// and suppress those items for the session that could actually give one. A silent write on
// a read path is invisible until someone notices a task stopped being raised.
func TestHumanBlockedElsewhereStampsNothing(t *testing.T) {
	st, ctx, actor := stationFixture(t)
	mine, err := st.CreateStation(ctx, 1, "mine", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.CreateStation(ctx, 1, "other", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.AddStationTask(ctx, DefaultStationTaskLimits(),
		StationTask{StationID: other.StationID, Text: "untouched", BlockedOn: "human"}, "tok", actor, false); err != nil {
		t.Fatal(err)
	}

	snapshot := func() (briefed string, count int) {
		t.Helper()
		if err := st.R.QueryRowContext(ctx,
			`SELECT COALESCE(last_briefed_at,''), briefed_count FROM station_task WHERE station_id=?`,
			other.StationID).Scan(&briefed, &count); err != nil {
			t.Fatal(err)
		}
		return
	}
	beforeAt, beforeN := snapshot()

	for i := 0; i < 3; i++ {
		if _, _, err := st.HumanBlockedElsewhere(ctx, 1, mine.StationID); err != nil {
			t.Fatal(err)
		}
	}
	afterAt, afterN := snapshot()
	if afterAt != beforeAt || afterN != beforeN {
		t.Fatalf("counting stamped another station's task: last_briefed_at %q->%q, briefed_count %d->%d",
			beforeAt, afterAt, beforeN, afterN)
	}

	// POSITIVE CONTROL ON THE INSTRUMENT. Without it this test passes against a snapshot
	// function that reads the wrong row, or a fixture where nothing can ever change.
	if _, err := st.BriefStationTasks(ctx, DefaultStationTaskLimits(), other.StationID); err != nil {
		t.Fatal(err)
	}
	ctrlAt, ctrlN := snapshot()
	if ctrlAt == beforeAt && ctrlN == beforeN {
		t.Fatalf("the control did not move the stamp either — this test cannot detect a write (at %q, n %d)", ctrlAt, ctrlN)
	}
}
