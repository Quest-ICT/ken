package store

import (
	"context"
	"errors"
	"testing"
)

func stationFixture(t *testing.T) (*Store, context.Context, int64) {
	t.Helper()
	st := newStore(t)
	ctx := context.Background()
	actorID, err := st.FindOrCreateActor(ctx, "human", "curator")
	if err != nil {
		t.Fatal(err)
	}
	return st, ctx, actorID
}

// TestStationKeyAuthAndRevocation IS DELETED WITH STATION KEYS. A `kens_` credential no longer exists: /mcp requires an OAuth
// grant carrying every capability, and a session declares WHICH station with session_key rather
// than by presenting a credential that names one.

// TestStationLessKeyResolvesWithoutStation IS DELETED WITH STATION KEYS. A `kens_` credential no longer exists: /mcp requires an OAuth
// grant carrying every capability, and a session declares WHICH station with session_key rather
// than by presenting a credential that names one.

// TestStationKeyRequiresStationScope IS DELETED WITH STATION KEYS. A `kens_` credential no longer exists: /mcp requires an OAuth
// grant carrying every capability, and a session declares WHICH station with session_key rather
// than by presenting a credential that names one.

// Names are unique and display-only. A collision is refused with a NAMED error, which is
// load-bearing rather than cosmetic: CreateStationAutoNamed retries on ErrStationNameTaken
// to decorate an auto-chosen name, so a session onboarding into a folder whose name is
// already taken still gets a workspace instead of an error.
//
// RENAMED from TestStationNameUniquePerSpace when space_id was removed (§9.1). The
// uniqueness is real and survives; only the "per instance" half was a claim about a second
// space that never existed. Its cross-space clause went with the column.
func TestStationNameIsUniqueAndCollisionIsNamed(t *testing.T) {
	st, ctx, actorID := stationFixture(t)
	if _, err := st.CreateStation(ctx, "promo", "", actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateStation(ctx, "promo", "", actorID); !errors.Is(err, ErrStationNameTaken) {
		t.Fatalf("duplicate name should be ErrStationNameTaken, got %v", err)
	}
}

// Archiving is REVERSIBLE and must not destroy anything: links go dormant rather than
// revoked, so unarchiving restores them (S3, §10).
func TestArchiveIsReversibleAndLinksGoDormant(t *testing.T) {
	st, ctx, actorID := stationFixture(t)
	a, _ := st.CreateStation(ctx, "alpha", "", actorID)
	b, _ := st.CreateStation(ctx, "beta", "", actorID)

	lo, hi := a.StationID, b.StationID
	if lo > hi {
		lo, hi = hi, lo
	}
	if _, err := st.W.ExecContext(ctx, `
INSERT INTO station_link(link_id, station_a, station_b, approved_by_actor_id)
VALUES('lnk1',?,?,?)`, lo, hi, actorID); err != nil {
		t.Fatal(err)
	}

	linkState := func() string {
		var s string
		_ = st.R.QueryRowContext(ctx, `SELECT state FROM station_link WHERE link_id='lnk1'`).Scan(&s)
		return s
	}

	if err := st.ArchiveStation(ctx, a.StationID, true); err != nil {
		t.Fatal(err)
	}
	if got := linkState(); got != "dormant" {
		t.Fatalf("archiving should make the link dormant, got %q", got)
	}
	got, err := st.StationByID(ctx, a.StationID)
	if err != nil || got.State != "archived" {
		t.Fatalf("station state = %v (%v), want archived", got, err)
	}

	if err := st.ArchiveStation(ctx, a.StationID, false); err != nil {
		t.Fatal(err)
	}
	if got := linkState(); got != "active" {
		t.Fatalf("unarchiving should restore the link, got %q", got)
	}
}

// The self-description is the one field an agent writes, and it lands in columns whose
// NAMES carry the untrustworthiness (S8) — not beside a sibling flag that a harness
// would flatten away.
func TestSelfDescriptionIsStoredInClaimNamedColumns(t *testing.T) {
	st, ctx, actorID := stationFixture(t)
	s1, _ := st.CreateStation(ctx, "public-dev", "", actorID)
	if err := st.SetStationSelfDescription(ctx, s1.StationID, "I maintain the public repo", []string{"go", "release"}); err != nil {
		t.Fatal(err)
	}
	got, err := st.StationByID(ctx, s1.StationID)
	if err != nil {
		t.Fatal(err)
	}
	if got.SelfDescribedAbout != "I maintain the public repo" || len(got.SelfDescribedTags) != 2 {
		t.Fatalf("self-description round trip failed: %+v", got)
	}
	// The human-typed name is untouched by the agent-writable path.
	if got.Name != "public-dev" {
		t.Fatalf("name changed by a self-description write: %q", got.Name)
	}
	var cols int
	if err := st.R.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('station') WHERE name IN ('self_described_about','self_described_tags')`).Scan(&cols); err != nil {
		t.Fatal(err)
	}
	if cols != 2 {
		t.Fatal("claim columns must be named self_described_* so the marking survives flattening")
	}
}

// *** AN UNPUBLISHED, UNLINKED STATION IS REACHABLE NOW — THE PRECONDITION IS GONE. ***
//
// This test used to assert the opposite, and it was right to: station_link_request resolved names
// through a narrowed lookup so that a correct guess could not "put an agent-authored ask for an
// unpublished post in front of its human". ken-prod-ops had found the gate untested and said
// plainly they had not watched it fail — "the code says it cannot resolve; I did not watch it
// fail" — and this was that watching.
//
// Both the tool and the narrowed resolver are gone. There is no request to file, so a guessed name
// buys nothing, and the directory lists the estate to the one human who owns all of it.
//
// WHAT THE OLD TEST WAS REALLY MEASURING was a COST: IDENTITY.md §6 promised "two stations talking
// → one approval, once per pair" and did not mention that the target must first be published, so
// an acceptance test counting approvals against §6 would score a correct system as one over
// budget. That cost is now zero, which is the fact worth pinning — a station nobody published and
// nobody linked is discoverable, and discoverable is what §6's count depends on.
func TestAnUnpublishedUnlinkedStationIsStillDiscoverable(t *testing.T) {
	st, ctx, actorID := stationFixture(t)
	seeker, err := st.CreateStation(ctx, "seeker", "", actorID)
	if err != nil {
		t.Fatal(err)
	}
	target, err := st.CreateStation(ctx, "quiet-post", "", actorID)
	if err != nil {
		t.Fatal(err)
	}

	// DEFAULT IS UNPUBLISHED. Asserted rather than assumed — without it this test would pass on a
	// system where every new station is published, which would prove nothing about the filter.
	var published int
	if err := st.R.QueryRowContext(ctx,
		`SELECT published FROM station WHERE station_id=?`, target.StationID).Scan(&published); err != nil {
		t.Fatal(err)
	}
	if published != 0 {
		t.Fatalf("a new station is published=%d; this test assumes 0, or it is not testing an unpublished station at all", published)
	}

	list, err := st.ListStationsVisibleTo(ctx, seeker.StationID)
	if err != nil {
		t.Fatal(err)
	}
	var found *DirectoryEntry
	for i := range list {
		if list[i].Name == "quiet-post" {
			found = &list[i]
		}
	}
	if found == nil {
		t.Fatal("an unpublished, unlinked station is absent from the directory — a session cannot " +
			"address a peer it was never told about, and removing the approval gate would have " +
			"moved the wall rather than removed it")
	}
	// AND IT IS HONEST ABOUT THE TWO FACTS IT DID NOT CHANGE. Listing a station is not claiming a
	// relationship with it; if these came back true the directory would be reporting a permission
	// nobody granted.
	if found.Published {
		t.Error("the unpublished station reports published=true")
	}
	if found.Linked {
		t.Error("a station with no link reports linked=true — discovery and permission are being conflated")
	}
}
