package store

import (
	"context"
	"errors"
	"strings"
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

// A station key is a `kens_` credential that resolves to its station. The properties
// that matter: the prefix is distinct from ken_/kenc_, the secret is never stored, and
// retired/revoked keys are refused INDISTINGUISHABLY from unknown ones (S5, S6, §5).
func TestStationKeyAuthAndRevocation(t *testing.T) {
	st, ctx, actorID := stationFixture(t)
	station, err := st.CreateStation(ctx, "prod-ops", "production operations", actorID)
	if err != nil {
		t.Fatal(err)
	}

	key, err := st.IssueStationKey(ctx, actorID, station.StationID, "laptop", []string{"station"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(key, "kens_") {
		t.Fatalf("station key must carry the kens_ prefix, got %.8s…", key)
	}
	// The prefix must not be mistakable for an API token's.
	if strings.HasPrefix(key, "ken_") {
		t.Fatal("kens_ must not also match the ken_ prefix")
	}
	// The raw secret is never persisted.
	secret := key[strings.LastIndex(key, "_")+1:]
	var n int
	if err := st.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM api_token WHERE secret_sha256=?`, secret).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("the secret itself is stored — only its SHA-256 may be")
	}

	p, err := st.AuthenticateStationKey(ctx, key)
	if err != nil {
		t.Fatalf("valid key should authenticate: %v", err)
	}
	if p.StationID != station.StationID || p.ActorID != actorID {
		t.Fatalf("principal = %+v, want station %s actor %d", p, station.StationID, actorID)
	}

	// A tampered secret is refused.
	//
	// *** THE TAMPER IS ASSERTED TO BE A TAMPER, AND THAT IS NOT PEDANTRY — IT COST A RELEASE. ***
	//
	// This was `key[:len(key)-1]+"x"`. Secrets are base62, so ROUGHLY ONE RUN IN 62 the secret
	// already ended in 'x', the "tampered" key was byte-identical to the real one, authentication
	// correctly succeeded, and the test reported that Ken accepts a forged station key. It fired
	// for the first time on the v3.26.0 release build — green locally, red in CI, on a security
	// assertion — and read exactly like a regression in the code I had just changed.
	//
	// The bug was never the flakiness. It was that the test could not tell "the secret was
	// tampered with and accepted" from "the secret was not tampered with at all", which is this
	// project's own defect class inside its own suite.
	tampered := key[:len(key)-1] + "x"
	if tampered == key {
		tampered = key[:len(key)-1] + "y"
	}
	if tampered == key {
		t.Fatal("the tampered key is identical to the real one, so the assertion below would be " +
			"testing that a VALID key authenticates — which is the opposite of its name")
	}
	if _, err := st.AuthenticateStationKey(ctx, tampered); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tampered secret should be ErrNotFound, got %v", err)
	}
	// An unknown key is refused the SAME way — unprobeable (§5).
	if _, err := st.AuthenticateStationKey(ctx, "kens_aaaaaaaaaaaa_bbbb"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown key should be ErrNotFound, got %v", err)
	}

	// Retiring stops it binding, and is indistinguishable from unknown.
	if err := st.RetireStationKey(ctx, p.TokenID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AuthenticateStationKey(ctx, key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("retired key must not authenticate, got %v", err)
	}
}

// A key with no station may exist: it is how a session with no station asks for one,
// and it must be able to do nothing else (S3).
func TestStationLessKeyResolvesWithoutStation(t *testing.T) {
	st, ctx, actorID := stationFixture(t)
	key, err := st.IssueStationKey(ctx, actorID, "", "bootstrap", []string{"station"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := st.AuthenticateStationKey(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if p.StationID != "" {
		t.Fatalf("station-less key resolved to station %q", p.StationID)
	}
}

// A credential without the `station` scope is not a station key, whatever its prefix —
// the scope is what grants, not the shape of the string.
func TestStationKeyRequiresStationScope(t *testing.T) {
	st, ctx, actorID := stationFixture(t)
	key, err := st.IssueStationKey(ctx, actorID, "", "wrong-scopes", []string{"read"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AuthenticateStationKey(ctx, key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a key without the station scope must not authenticate, got %v", err)
	}
}

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

// *** AN UNPUBLISHED, UNLINKED STATION IS NOT REACHABLE BY NAME — THE PRECONDITION §6 OMITS. ***
//
// ken-prod-ops found this while planning the acceptance test and said plainly that they had NOT
// watched it fail: "the code says it cannot resolve; I did not watch it fail." This is that
// watching, because a gate nobody has seen refuse is a gate nobody has tested.
//
// IT IS DELIBERATE, NOT A DEFECT. station_link_request's own comment: a correct guess would "put
// an agent-authored ask for an unpublished post in front of its human, which is exactly the
// unsolicited approach publication exists to prevent." So publication is the control that stops a
// session cold-calling a human it found by guessing a name.
//
// WHAT IT COSTS, AND WHY IT IS WORTH A TEST: IDENTITY.md §6 promises "two workspaces talking →
// one, once per pair" and does not mention that the target must first be published. Publication is
// once per STATION rather than once per pair — amortised, not repeated — but it is a real console
// action, and an acceptance test counting approvals against §6 alone would score a correct system
// as one approval over budget.
func TestAnUnpublishedUnlinkedStationCannotBeFoundByName(t *testing.T) {
	st, ctx, actorID := stationFixture(t)
	seeker, err := st.CreateStation(ctx, "seeker", "", actorID)
	if err != nil {
		t.Fatal(err)
	}
	target, err := st.CreateStation(ctx, "quiet-post", "", actorID)
	if err != nil {
		t.Fatal(err)
	}

	// DEFAULT IS UNPUBLISHED. Asserted rather than assumed — the whole finding rests on it.
	var published int
	if err := st.R.QueryRowContext(ctx,
		`SELECT published FROM station WHERE station_id=?`, target.StationID).Scan(&published); err != nil {
		t.Fatal(err)
	}
	if published != 0 {
		t.Fatalf("a new station is published=%d; this test and the finding behind it assume 0", published)
	}

	if _, err := st.StationByNameVisibleTo(ctx, seeker.StationID, "quiet-post"); err == nil {
		t.Fatal("an unpublished, unlinked station resolved by name — a session could file a link " +
			"request against a human who never advertised that post")
	}

	// CONTROL 1: publishing makes it resolvable. Without this the refusal above could be caused
	// by a typo, a missing fixture, or any other error, and would pass for the wrong reason.
	if err := st.SetStationPublished(ctx, target.StationID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := st.StationByNameVisibleTo(ctx, seeker.StationID, "quiet-post"); err != nil {
		t.Fatalf("after publishing, the same lookup must succeed — otherwise the refusal proved "+
			"nothing about publication: %v", err)
	}

	// CONTROL 2: an ACTIVE LINK is the other half of the predicate, so an unpublished station
	// already linked to the seeker stays reachable. This is what makes publication a ONE-TIME
	// cost rather than a permanent requirement.
	if err := st.SetStationPublished(ctx, target.StationID, false); err != nil {
		t.Fatal(err)
	}
	linkStations(t, st, ctx, seeker.StationID, target.StationID, actorID)
	if _, err := st.StationByNameVisibleTo(ctx, seeker.StationID, "quiet-post"); err != nil {
		t.Errorf("an unpublished station the seeker is LINKED to must stay visible, or a pair "+
			"would need re-publishing forever: %v", err)
	}
}
