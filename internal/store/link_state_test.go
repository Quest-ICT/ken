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
