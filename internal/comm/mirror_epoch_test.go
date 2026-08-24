package comm

import (
	"context"
	"testing"
)

// *** A PARTIAL MIRROR REBUILD MUST NOT READ AS FRESH. ***
//
// `mirror_state` is one row and both projections used to stamp its `roster_epoch`
// themselves — while both rebuild paths deliberately run the halves INDEPENDENTLY and
// log-and-continue, so one failing cannot take the other down. So whichever half survived
// recorded the new generation for both, and `MirrorEpoch` reported fresh over stale data:
// the one check it exists to answer, answered wrongly in exactly the case that matters.
//
// Measured before the fix: rebuild both at 5, then only the link half at 6, and MirrorEpoch
// returned 6 while room_member_mirror still held epoch-5 rows.
func TestAHalfRebuiltMirrorDoesNotClaimToBeCurrent(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()

	rebuildBoth := func(epoch int64, rooms map[string][]string, links [][2]string) {
		t.Helper()
		if err := st.ReplaceRoomMirror(ctx, rooms, epoch); err != nil {
			t.Fatal(err)
		}
		if err := st.ReplaceLinkMirror(ctx, links, epoch); err != nil {
			t.Fatal(err)
		}
		if err := st.StampMirrorEpoch(ctx, epoch); err != nil {
			t.Fatal(err)
		}
	}

	rebuildBoth(5, map[string][]string{"r1": {"s:a", "s:b"}}, [][2]string{{"a", "b"}})
	if e, err := st.MirrorEpoch(ctx); err != nil || e != 5 {
		t.Fatalf("after a complete rebuild: epoch %d, err %v — want 5", e, err)
	}

	// NOW ONLY THE LINK HALF REBUILDS. The room read failed and was logged; nothing stamps.
	if err := st.ReplaceLinkMirror(ctx, [][2]string{{"a", "b"}, {"a", "c"}}, 6); err != nil {
		t.Fatal(err)
	}
	e, err := st.MirrorEpoch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if e != 5 {
		t.Fatalf("a half-rebuilt mirror reports epoch %d — it claims to be current at generation 6 "+
			"while room_member_mirror still holds generation-5 rows", e)
	}

	// AND THE SAME IN THE OTHER DIRECTION: the ROOM half alone. Both arms are needed and I
	// learned that the expensive way — my first mutant re-added the stamp to the room half
	// while this test's partial rebuild only called the link half, so a build with the defect
	// restored in one projection passed clean. A test that covers one direction of a symmetric
	// failure certifies the half nobody broke.
	if err := st.ReplaceRoomMirror(ctx, map[string][]string{"r1": {"s:a"}}, 7); err != nil {
		t.Fatal(err)
	}
	if e, err := st.MirrorEpoch(ctx); err != nil || e != 5 {
		t.Fatalf("after a ROOM-only rebuild: epoch %d, err %v — want 5, still behind", e, err)
	}

	// AND THE COMPLETE REBUILD THAT FOLLOWS DOES ADVANCE IT. Without this the test passes
	// against a build that simply stopped stamping, which would leave every mirror looking
	// permanently stale — the opposite failure and just as useless.
	rebuildBoth(6, map[string][]string{"r1": {"s:a"}}, [][2]string{{"a", "b"}})
	if e, err := st.MirrorEpoch(ctx); err != nil || e != 6 {
		t.Fatalf("after the next complete rebuild: epoch %d, err %v — want 6", e, err)
	}
}
