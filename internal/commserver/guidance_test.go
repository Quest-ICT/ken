package commserver

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Quest-ICT/ken/internal/comm"
)

// THE VALUE A CALLER READS IS ASSEMBLED HERE, not at the raise site — and until this
// test there was nothing at this layer.
//
// 3.3.0 shipped a fix for D2: passing a room id as channel_id returned an error naming
// to_room. The string was in the binary. A test at the raise site in internal/comm
// asserted it and passed. And a caller still got a bare "not found", byte-identical to
// the error that made ken-promo — the PROMOTION station — conclude rooms were
// receive-only and report that to its human.
//
// commError flattens by sentinel, so `%w`-wrapping ErrNotFound put the guidance behind
// the exact string it was written to replace. Two correct layers, one untested
// composition. Production found it by probing the running image; nothing in the suite
// could have, because no test crossed the boundary.
//
// So this one crosses it: the error comes from the REAL raise site and goes through the
// REAL mapper.
func TestTheRoomGuidanceSurvivesTheErrorMapper(t *testing.T) {
	cs, ep, roomID := roomFixture(t)

	_, _, err := cs.ChannelFor(context.Background(), ep, roomID)
	if err == nil {
		t.Fatal("a room id resolved as a channel; the fixture is wrong")
	}
	got := commError(err).Error()

	if !strings.Contains(got, "to_room") {
		t.Fatalf("the caller reads %q.\n"+
			"The guidance exists at the raise site and is discarded here, which is the 3.3.0 defect: "+
			"a fix that is present in the binary and unreachable from every caller.", got)
	}
	if !strings.Contains(got, "ROOM") {
		t.Errorf("the error does not say the id names a room: %q", got)
	}
}

// AND THE ORACLE STAYS CLOSED — both halves, because a marker that leaks everything
// would pass the test above while undoing the property the flattening exists for.
func TestFlatteningStillHidesWhatWasNotMarkedSafe(t *testing.T) {
	// A sentinel wrapped with a detail nobody cleared for release. This is the common
	// case and it must NOT reach the caller: the moment wrapped text is echoed by
	// default, every sentinel anyone ever annotates becomes an existence oracle.
	err := fmt.Errorf("%w: endpoint 4711 has no channel to station ken-prod-ops", comm.ErrNotFound)
	if got := commError(err).Error(); got != "not found" {
		t.Fatalf("an unmarked wrapped error reached the caller as %q, want the flat \"not found\".\n"+
			"The mapper has become a blanket echo, which is a worse defect than the one it fixed.", got)
	}

	// And a marked error keeps errors.Is intact, so nothing that branched on the
	// sentinel before stops branching on it now.
	marked := comm.CallerSafe(fmt.Errorf("%w: guidance", comm.ErrNotFound))
	if !errors.Is(marked, comm.ErrNotFound) {
		t.Fatal("CallerSafe broke the unwrap chain — every errors.Is on this path silently changed behaviour")
	}
}

// A NON-MEMBER IS TOLD NOTHING ABOUT THE ROOM. The security half, asserted through the
// mapper rather than at the raise site, for the same reason as above: the raise site
// being right is not evidence the caller sees it.
func TestANonMemberLearnsNothingAboutARoomThatExists(t *testing.T) {
	cs, _, roomID := roomFixture(t)

	// A second endpoint, in no room at all.
	outsider, err := cs.MailboxFor(context.Background(), "stn-outsider", comm.Owner{ActorID: 1})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = cs.ChannelFor(context.Background(), outsider, roomID)
	if err == nil {
		t.Fatal("the outsider resolved a room as a channel")
	}
	got := commError(err).Error()

	if strings.Contains(got, "is a ROOM") {
		t.Fatalf("a non-member is told %q — the error confirms the room exists, "+
			"which is the oracle comm_open_channel's uniform refusal exists to close, "+
			"reopened by a helpful message.", got)
	}
	// It may still mention rooms as a CONCEPT — true for every caller, informative to
	// none about who is in what — and that is what makes the generic branch useful.
	if !strings.Contains(got, "to_room") {
		t.Errorf("the generic branch lost its hint about the to_room parameter: %q", got)
	}
}

// roomFixture returns a comm store, an endpoint that is a MEMBER of the returned room,
// and the room id. Membership is written through ReplaceRoomMirror — the same projection
// the running server reads — rather than by reaching into the table directly.
func roomFixture(t *testing.T) (*comm.Store, *comm.Endpoint, string) {
	t.Helper()
	cs, err := comm.Open(filepath.Join(t.TempDir(), "comm.db"), comm.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	if err := cs.Migrate(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ep, err := cs.MailboxFor(ctx, "member", comm.Owner{ActorID: 1})
	if err != nil {
		t.Fatal(err)
	}
	const roomID = "0ePMqZlbE0IQBYx7"
	// An UNBOUND endpoint keys as e:<rowid>; that is the party the mirror must carry for
	// this endpoint to be a member, and using it here keeps the fixture independent of
	// station binding.
	if err := cs.ReplaceRoomMirror(ctx, map[string][]string{roomID: {fmt.Sprintf("e:%d", ep.ID)}}, 1); err != nil {
		t.Fatal(err)
	}
	return cs, ep, roomID
}
