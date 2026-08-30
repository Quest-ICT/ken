package store

import (
	"context"
	"testing"
)

func linkFixture(t *testing.T) (*Store, context.Context, int64, string, string, string) {
	t.Helper()
	st, ctx, actorID := stationFixture(t)
	a, err := st.CreateStation(ctx, "alpha", "", actorID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateStation(ctx, "beta", "", actorID)
	if err != nil {
		t.Fatal(err)
	}
	created, err := st.EnsureStationLink(ctx, a.StationID, b.StationID, actorID)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("the first contact did not create a link")
	}
	links, err := st.ListStationLinks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 {
		t.Fatalf("%d links after first contact, want 1", len(links))
	}
	return st, ctx, actorID, a.StationID, b.StationID, links[0].LinkID
}

// *** A LINK IS BORN ACTIVE, ON FIRST CONTACT, WITH NO HUMAN. ***
//
// Vlad removed both human gates on comm: it is available immediately to any session holding the
// connector, exactly like the station surface. His reasoning is Ken's own design doc — one human,
// one account, "there is no other tenant to protect against" — so the approval guarded a threat
// model the design denies.
func TestAFirstContactCreatesAnActiveLink(t *testing.T) {
	st, ctx, actorID, a, b, _ := linkFixture(t)

	links, _ := st.ListStationLinks(ctx)
	if links[0].State != "active" {
		t.Errorf("a new link is %q, want active — nothing approves it", links[0].State)
	}
	// IDEMPOTENT: a second message must not file a second link.
	created, err := st.EnsureStationLink(ctx, a, b, actorID)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Error("a second contact created a SECOND link between the same pair")
	}
	// AND THE PAIR IS UNORDERED. Reversing the arguments is the same relationship, or a
	// conversation would get a fresh link every time the other side spoke first.
	if created, err := st.EnsureStationLink(ctx, b, a, actorID); err != nil {
		t.Fatal(err)
	} else if created {
		t.Error("the reversed pair created a second link — the relationship is not unordered")
	}
}

// *** THE HAZARD ken-prod-ops FLAGGED, ASSERTED RATHER THAN ARGUED. ***
//
// It warned that `dormant` and `suspended` are two INDEPENDENT reasons to be not-active and that
// one column cannot hold both: archive a suspended link, unarchive it, and the unarchive silently
// resumes a relationship a human turned off. That is a real failure mode and it is worth a test
// even though this implementation avoids it — the avoidance is a property of ONE STATEMENT
// (ArchiveStation's `WHERE state=?` guard), and a future edit could drop the guard without anyone
// noticing what it was load-bearing for.
func TestSuspendSurvivesAnArchiveAndUnarchive(t *testing.T) {
	st, ctx, _, a, _, linkID := linkFixture(t)

	if err := st.SetStationLinkSuspended(ctx, linkID, true); err != nil {
		t.Fatal(err)
	}
	if got := linkState(t, st, ctx, linkID); got != "suspended" {
		t.Fatalf("state after suspend = %q, want suspended", got)
	}

	// The station is archived and then brought back. Neither step may touch a suspension.
	if err := st.ArchiveStation(ctx, a, true); err != nil {
		t.Fatal(err)
	}
	if got := linkState(t, st, ctx, linkID); got != "suspended" {
		t.Fatalf("archiving a SUSPENDED link made it %q — the archive path overwrote a human's "+
			"decision", got)
	}
	if err := st.ArchiveStation(ctx, a, false); err != nil {
		t.Fatal(err)
	}
	if got := linkState(t, st, ctx, linkID); got != "suspended" {
		t.Fatalf("unarchiving RESUMED a suspended link (state %q). A human turned this "+
			"relationship off and an unrelated action turned it back on, silently — which is "+
			"exactly what prod warned one state column would do", got)
	}
	// And the human can still resume it deliberately.
	if err := st.SetStationLinkSuspended(ctx, linkID, false); err != nil {
		t.Fatal(err)
	}
	if got := linkState(t, st, ctx, linkID); got != "active" {
		t.Fatalf("resume left the link %q, want active", got)
	}
}

// THE OTHER DIRECTION, which is the case the guard is really for: an ACTIVE link goes dormant when
// its station is archived, and comes back when it is unarchived. Without this the test above could
// pass against an archive path that had simply stopped touching links at all.
func TestArchivingMakesAnActiveLinkDormantAndUnarchivingRestoresIt(t *testing.T) {
	st, ctx, _, a, _, linkID := linkFixture(t)

	if err := st.ArchiveStation(ctx, a, true); err != nil {
		t.Fatal(err)
	}
	if got := linkState(t, st, ctx, linkID); got != "dormant" {
		t.Fatalf("state after archiving = %q, want dormant", got)
	}
	if err := st.ArchiveStation(ctx, a, false); err != nil {
		t.Fatal(err)
	}
	if got := linkState(t, st, ctx, linkID); got != "active" {
		t.Fatalf("state after unarchiving = %q, want active", got)
	}
}

// A SUSPENDED LINK IS NOT RESURRECTED BY THE NEXT MESSAGE. Without this, Suspend would be undone
// by the first thing it exists to stop — an auto-approving gate that overwrites the human's own
// off-switch is worse than no switch, because it looks like one.
func TestAutoLinkingDoesNotResurrectASuspendedLink(t *testing.T) {
	st, ctx, actorID, a, b, linkID := linkFixture(t)

	if err := st.SetStationLinkSuspended(ctx, linkID, true); err != nil {
		t.Fatal(err)
	}
	if created, err := st.EnsureStationLink(ctx, a, b, actorID); err != nil {
		t.Fatal(err)
	} else if created {
		t.Fatal("a second contact created a new link alongside the suspended one")
	}
	if got := linkState(t, st, ctx, linkID); got != "suspended" {
		t.Fatalf("the next message reactivated a suspended link (state %q)", got)
	}
}

// 'revoked' IS OUT OF THE VOCABULARY, and the CHECK is what makes that true rather than a
// convention. Vlad: "'revoke' concept is out of the table" and "I don't want historical shit."
func TestRevokedIsNotAStateALinkCanEnter(t *testing.T) {
	st, ctx, _, _, _, linkID := linkFixture(t)
	if _, err := st.W.ExecContext(ctx,
		`UPDATE station_link SET state='revoked' WHERE link_id=?`, linkID); err == nil {
		t.Fatal("a link accepted state 'revoked' — the CHECK still allows a terminal state")
	}
}

func linkState(t *testing.T, st *Store, ctx context.Context, linkID string) string {
	t.Helper()
	var s string
	if err := st.R.QueryRowContext(ctx,
		`SELECT state FROM station_link WHERE link_id=?`, linkID).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// LinkStateBetween is the refusal path's source of truth, so all four of its answers are asserted.
//
// It exists because comm.db's mirror holds ACTIVE links only: a suspended pair is absent from it
// entirely, which made "your human turned this off" indistinguishable from "no such station" at the
// one moment the difference decides what a session does next. Every branch below maps to a different
// sentence a session receives, so a wrong answer here is a wrong instruction there.
func TestLinkStateBetweenAnswersAllFourCases(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	actor, err := s.FindOrCreateActor(ctx, "human", "admin")
	if err != nil {
		t.Fatal(err)
	}
	mk := func(name string) *Station {
		t.Helper()
		st, err := s.CreateStation(ctx, name, "", actor)
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	me, peer, stranger := mk("me"), mk("peer"), mk("stranger")

	// 1. NO SUCH STATION — the only case that should ever tell a session to check its id.
	if state, exists, err := s.LinkStateBetween(ctx, me.StationID, "st-does-not-exist"); err != nil || exists || state != "" {
		t.Errorf("unknown station: state=%q exists=%v err=%v; want \"\", false, nil", state, exists, err)
	}
	// 2. EXISTS, NO LINK. Distinct from the above: the station is real and reachable, and the first
	//    message would create the link. A session told to check its id here would be misled.
	if state, exists, err := s.LinkStateBetween(ctx, me.StationID, stranger.StationID); err != nil || !exists || state != "" {
		t.Errorf("no link: state=%q exists=%v err=%v; want \"\", true, nil", state, exists, err)
	}
	// 3. ACTIVE.
	if _, err := s.EnsureStationLink(ctx, me.StationID, peer.StationID, actor); err != nil {
		t.Fatal(err)
	}
	if state, exists, err := s.LinkStateBetween(ctx, me.StationID, peer.StationID); err != nil || !exists || state != "active" {
		t.Errorf("active: state=%q exists=%v err=%v; want active, true, nil", state, exists, err)
	}
	// 4. SUSPENDED — the case the whole function was written for.
	links, err := s.ListStationLinks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetStationLinkSuspended(ctx, links[0].LinkID, true); err != nil {
		t.Fatal(err)
	}
	if state, exists, err := s.LinkStateBetween(ctx, me.StationID, peer.StationID); err != nil || !exists || state != "suspended" {
		t.Errorf("suspended: state=%q exists=%v err=%v; want suspended, true, nil", state, exists, err)
	}
	// AND IT IS ORDER-INDEPENDENT. The link is stored on an ordered pair; a caller asking from the
	// other side must get the same answer, or the refusal would depend on who sent.
	if state, _, _ := s.LinkStateBetween(ctx, peer.StationID, me.StationID); state != "suspended" {
		t.Errorf("reversed: state=%q, want suspended — the pair is unordered to callers", state)
	}
}

// FIRST CONTACT MUST NOT CREATE AN ACTIVE LINK TO AN ARCHIVED STATION.
//
// ArchiveStation's invariant is that archiving dormants a station's links. Auto-linking had no
// liveness guard, so the first message to an archived post created a fresh ACTIVE row behind the
// archive — the console badged a live relationship with a station the operator had just archived,
// and comm_open_channel (which reads AreStationsLinked, not the station-state-joined mirror) would
// open a channel to it. A control that a later write can undo without saying so is advisory.
//
// The send itself failed anyway, on the mirror, which is why this never looked like a broken send.
// It looked like a console disagreeing with itself.
func TestFirstContactDoesNotLinkToAnArchivedStation(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	actor, err := s.FindOrCreateActor(ctx, "human", "admin")
	if err != nil {
		t.Fatal(err)
	}
	live, err := s.CreateStation(ctx, "live", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	gone, err := s.CreateStation(ctx, "gone", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ArchiveStation(ctx, gone.StationID, true); err != nil {
		t.Fatal(err)
	}

	created, err := s.EnsureStationLink(ctx, live.StationID, gone.StationID, actor)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Error("first contact created a link to an ARCHIVED station")
	}
	links, err := s.ListStationLinks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 0 {
		t.Errorf("%d link(s) exist after contacting an archived station: %+v", len(links), links)
	}

	// CONTROL: unarchive and the same call succeeds. Without it, a guard that refused EVERY
	// auto-link would pass everything above and break the feature entirely.
	if err := s.ArchiveStation(ctx, gone.StationID, false); err != nil {
		t.Fatal(err)
	}
	if created, err := s.EnsureStationLink(ctx, live.StationID, gone.StationID, actor); err != nil || !created {
		t.Errorf("first contact with a LIVE station did not create a link (created=%v err=%v)", created, err)
	}
}
