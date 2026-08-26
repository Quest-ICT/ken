package store

import (
	"errors"
	"testing"
)

// AN OPERATION WHOSE NO-OP IS INDISTINGUISHABLE FROM SUCCESS IS A DEFECT, and these two
// were it: both returned nil for a station id that names nothing, and the console flashed
// "published" / "archived" over an UPDATE that matched zero rows.
//
// RenameStation, eight lines away in the same file, has done this correctly since
// 2026-08-21. One instance was fixed and its neighbours were not looked at — which is why
// this test covers all three together, so the next person who fixes one sees the other two.
func TestStationWritesRefuseAnUnknownStation(t *testing.T) {
	st, ctx, actor := stationFixture(t)

	for _, c := range []struct {
		name string
		call func(string) error
	}{
		{"SetStationPublished(true)", func(id string) error { return st.SetStationPublished(ctx, id, true) }},
		{"SetStationPublished(false)", func(id string) error { return st.SetStationPublished(ctx, id, false) }},
		{"ArchiveStation(true)", func(id string) error { return st.ArchiveStation(ctx, id, true) }},
		{"ArchiveStation(false)", func(id string) error { return st.ArchiveStation(ctx, id, false) }},
		{"RenameStation", func(id string) error { return st.RenameStation(ctx, id, "whatever") }},
	} {
		if err := c.call("no-such-station"); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s on an unknown id: got %v, want ErrNotFound", c.name, err)
		}
	}

	// POSITIVE CONTROL ON THE INSTRUMENT. Without it this test passes against a function
	// that refuses EVERYTHING, which would be a worse defect than the one it replaces.
	s, err := st.CreateStation(ctx, "real-post", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetStationPublished(ctx, s.StationID, true); err != nil {
		t.Fatalf("publishing a real station: %v", err)
	}
	if err := st.ArchiveStation(ctx, s.StationID, true); err != nil {
		t.Fatalf("archiving a real station: %v", err)
	}
	got, err := st.StationByID(ctx, s.StationID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Published || got.State != "archived" {
		t.Fatalf("the control did not take effect: published=%v state=%q", got.Published, got.State)
	}
}

// *** AND THE NO-OP MUST NOT TELL THE DEPLOYMENT THAT MEMBERSHIP CHANGED. ***
//
// ArchiveStation advances the roster epoch on purpose — archiving changes who a room
// delivers to, and a membership change nothing can detect is one nobody is told about. But
// it advanced it for an id that names nothing too, so a mistyped archive broadcast a change
// it had not made, and every mirror consumer in the deployment believed its roster was
// stale. A no-op that reports success is bad; one that propagates is worse.
func TestArchivingAnUnknownStationDoesNotMoveTheRosterEpoch(t *testing.T) {
	st, ctx, actor := stationFixture(t)
	s, err := st.CreateStation(ctx, "real-post", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	before, err := st.RosterEpoch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ArchiveStation(ctx, "no-such-station", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("archiving an unknown station: got %v, want ErrNotFound", err)
	}
	after, err := st.RosterEpoch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("a no-op archive moved the roster epoch %d -> %d", before, after)
	}

	// CONTROL: a REAL archive must still move it, or this test would pass against a build
	// that had simply stopped bumping the epoch at all — which is the actual bug the bump
	// exists to prevent.
	if err := st.ArchiveStation(ctx, s.StationID, true); err != nil {
		t.Fatal(err)
	}
	moved, err := st.RosterEpoch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if moved == before {
		t.Fatalf("a real archive did NOT move the epoch (%d) — the control is dead and this test proves nothing", moved)
	}
}
