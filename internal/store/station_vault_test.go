package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// vaultFixture is a station with a vault and nothing in it.
func vaultFixture(t *testing.T) (*Store, context.Context, string, int64, StationVaultLimits) {
	t.Helper()
	st, ctx, actorID := stationFixture(t)
	station, err := st.CreateStation(ctx, "prod-ops", "production operations", actorID)
	if err != nil {
		t.Fatal(err)
	}
	return st, ctx, station.StationID, actorID, DefaultStationVaultLimits()
}

// A LISTING MUST NEVER CARRY A VALUE, and the control is that the value exists.
//
// This is the property the whole feature rests on: a vault whose listing leaks is a
// locker with extra steps. Asserting only "the listing has no secret" would also pass
// against a vault that stored nothing at all, so the same secret is then read back
// through the one path allowed to return it.
func TestAVaultListingCarriesNoSecretButTheValueIsThere(t *testing.T) {
	st, ctx, station, actor, lim := vaultFixture(t)
	const secret = "sk-live-DO-NOT-LEAK-4a91"

	if _, _, err := st.PutStationVaultSecret(ctx, lim, station, "stripe", secret, "billing key", "tok", actor); err != nil {
		t.Fatal(err)
	}

	list, err := st.ListStationVault(ctx, station)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("listing has %d entries, want 1", len(list))
	}
	if list[0].Secret != "" {
		t.Fatalf("THE LISTING CARRIES THE SECRET (%q) — every surface that lists a vault would spill it", list[0].Secret)
	}
	// The metadata that lets a human identify a secret without seeing one.
	if list[0].Note != "billing key" || list[0].SizeBytes != len(secret) || list[0].SHA256 == "" {
		t.Fatalf("listing cannot identify the entry without its value: %+v", list[0])
	}

	// CONTROL: the value is genuinely stored, so the assertion above is about the
	// listing rather than about an empty vault.
	got, err := st.GetStationVaultSecret(ctx, lim, station, "stripe", "station", "tok", actor)
	if err != nil {
		t.Fatal(err)
	}
	if got.Secret != secret {
		t.Fatalf("the vault stored %q, want %q", got.Secret, secret)
	}
}

// EVERY WRITE IS REVERSIBLE. Vlad's condition on secrets living in Ken at all was that
// storing them "does not modify them or at least it is reversible", and the locker's
// delete is destructive today. Overwrite and delete are both tested, because they are
// two different ways to lose a value and only one of them looks like losing it.
func TestOverwritingAndDeletingAreBothRecoverable(t *testing.T) {
	st, ctx, station, actor, lim := vaultFixture(t)

	if _, _, err := st.PutStationVaultSecret(ctx, lim, station, "db", "original-password", "", "tok", actor); err != nil {
		t.Fatal(err)
	}
	// The mistake: a session overwrites the wrong name.
	if _, _, err := st.PutStationVaultSecret(ctx, lim, station, "db", "oops-wrong-value", "", "tok", actor); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.RestoreStationVaultSecret(ctx, lim, station, "db", 0, "tok", actor); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetStationVaultSecret(ctx, lim, station, "db", "console", "tok", actor)
	if err != nil {
		t.Fatal(err)
	}
	if got.Secret != "original-password" {
		t.Fatalf("after restore the value is %q, want the original — an overwrite was irreversible", got.Secret)
	}

	// The other mistake: a session deletes it.
	if err := st.DeleteStationVaultSecret(ctx, lim, station, "db", "tok", actor); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetStationVaultSecret(ctx, lim, station, "db", "station", "tok", actor); !errors.Is(err, ErrVaultDeleted) {
		t.Fatalf("reading a tombstone returned %v, want ErrVaultDeleted — a deleted secret must not be readable, and must not look like one that never existed either", err)
	}
	if _, _, err := st.RestoreStationVaultSecret(ctx, lim, station, "db", 0, "tok", actor); err != nil {
		t.Fatal(err)
	}
	got, err = st.GetStationVaultSecret(ctx, lim, station, "db", "console", "tok", actor)
	if err != nil {
		t.Fatal(err)
	}
	if got.Secret != "original-password" {
		t.Fatalf("after restoring a deleted entry the value is %q — the delete was destructive", got.Secret)
	}
}

// A DELETED NAME AND AN ABSENT NAME MUST BE DISTINGUISHABLE, because the recovery action
// differs: one is Restore, the other is Put. Collapsing them is how an operator concludes
// a secret is gone when it is one click from coming back.
func TestATombstoneIsNotConfusedWithANameThatNeverExisted(t *testing.T) {
	st, ctx, station, actor, lim := vaultFixture(t)

	if _, _, err := st.PutStationVaultSecret(ctx, lim, station, "gone", "v", "", "tok", actor); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteStationVaultSecret(ctx, lim, station, "gone", "tok", actor); err != nil {
		t.Fatal(err)
	}

	_, deleted := st.GetStationVaultSecret(ctx, lim, station, "gone", "station", "tok", actor)
	_, absent := st.GetStationVaultSecret(ctx, lim, station, "never-existed", "station", "tok", actor)
	if errors.Is(deleted, ErrNotFound) || !errors.Is(deleted, ErrVaultDeleted) {
		t.Fatalf("a tombstone reports %v, want ErrVaultDeleted", deleted)
	}
	if !errors.Is(absent, ErrNotFound) {
		t.Fatalf("an unknown name reports %v, want ErrNotFound", absent)
	}

	// And the tombstone stays VISIBLE in the listing — a human deciding whether a secret
	// is safely gone has to be able to see that it is only soft-deleted.
	list, err := st.ListStationVault(ctx, station)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].DeletedAt == "" {
		t.Fatal("the tombstone is invisible in the listing — an operator would believe the secret was destroyed")
	}
}

// EVERY READ IS LOGGED, AND THE TOTAL SURVIVES THE BOUND.
//
// The audit table is capped, so after enough reads the trail no longer holds them all.
// The count on the entry is what keeps the console honest: "the last N of M" rather than
// N presented as the whole story. This is the notebook's silent-pruning defect refused in
// advance — there, a page lost its first seventeen revisions and no surface said so.
func TestReadsAreLoggedAndTheTrueTotalSurvivesPruning(t *testing.T) {
	st, ctx, station, actor, _ := vaultFixture(t)
	lim := DefaultStationVaultLimits()
	lim.MaxReadLog = 5 // a bound small enough to cross in a test

	if _, _, err := st.PutStationVaultSecret(ctx, lim, station, "k", "v", "", "tok", actor); err != nil {
		t.Fatal(err)
	}
	const reads = 12
	for i := 0; i < reads; i++ {
		if _, err := st.GetStationVaultSecret(ctx, lim, station, "k", "station", "tok", actor); err != nil {
			t.Fatal(err)
		}
	}

	trail, total, err := st.StationVaultReads(ctx, station, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(trail) > lim.MaxReadLog {
		t.Fatalf("the read log holds %d rows, over its %d bound — it grows with usage forever", len(trail), lim.MaxReadLog)
	}
	if total != reads {
		t.Fatalf("the true read total is reported as %d after %d reads.\n"+
			"Pruning the trail must not shrink the count, or the console cannot tell a rarely-read secret from a heavily-read one whose trail was dropped.", total, reads)
	}
	if len(trail) == 0 || trail[0].Via != "station" {
		t.Fatal("the retained trail does not record how the value was reached")
	}
}

// A READ CANNOT HAPPEN WITHOUT ITS AUDIT ROW. The two are one transaction, so this
// asserts the pairing rather than the presence of a log: a vault whose reads can slip
// past the trail cannot answer the only question worth asking after a leak.
func TestNoValueIsHandedOutWithoutBeingRecorded(t *testing.T) {
	st, ctx, station, actor, lim := vaultFixture(t)
	if _, _, err := st.PutStationVaultSecret(ctx, lim, station, "k", "v", "", "tok", actor); err != nil {
		t.Fatal(err)
	}

	// Baseline: storing is not reading. A Put must NOT register as a read, or the trail
	// says a secret was seen when it was only written.
	if _, total, err := st.StationVaultReads(ctx, station, 10); err != nil || total != 0 {
		t.Fatalf("before any read the total is %d (err %v), want 0 — writing a secret is not reading one", total, err)
	}

	for i := 1; i <= 3; i++ {
		if _, err := st.GetStationVaultSecret(ctx, lim, station, "k", "station", "tok", actor); err != nil {
			t.Fatal(err)
		}
		_, total, err := st.StationVaultReads(ctx, station, 10)
		if err != nil {
			t.Fatal(err)
		}
		if total != i {
			t.Fatalf("after %d reads the trail counts %d — a value was handed out unrecorded", i, total)
		}
	}

	// A REFUSED read is not a read. Auditing an attempt that returned nothing would make
	// the trail overstate exposure, which is the same disease as understating it.
	_, _ = st.GetStationVaultSecret(ctx, lim, station, "no-such-name", "station", "tok", actor)
	if _, total, _ := st.StationVaultReads(ctx, station, 10); total != 3 {
		t.Fatalf("a failed lookup counted as a read (total %d, want 3)", total)
	}
}

// HISTORY IS BOUNDED AND SAYS SO. The count comes back to the caller so a surface can
// state it; a bound nobody is told about is indistinguishable from no bound until the
// day someone needs the revision that is gone.
func TestHistoryPruningReportsWhatItDropped(t *testing.T) {
	st, ctx, station, actor, _ := vaultFixture(t)
	lim := DefaultStationVaultLimits()
	lim.MaxHistoryPerName = 3

	var lastDropped int
	for i := 0; i < 8; i++ {
		_, dropped, err := st.PutStationVaultSecret(ctx, lim, station, "k", strings.Repeat("v", i+1), "", "tok", actor)
		if err != nil {
			t.Fatal(err)
		}
		lastDropped = dropped
	}
	if lastDropped == 0 {
		t.Fatal("after eight writes past a three-revision bound, the store reported dropping nothing — the caller has no way to tell anyone their oldest values are gone")
	}

	hist, err := st.StationVaultHistoryFor(ctx, station, "k")
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != lim.MaxHistoryPerName {
		t.Fatalf("history holds %d revisions, want the %d-revision bound", len(hist), lim.MaxHistoryPerName)
	}
	// History describes what is recoverable; it must not be a second way to read values.
	for _, h := range hist {
		if h.SHA256 == "" || h.Reason == "" {
			t.Fatalf("a history row cannot be reasoned about: %+v", h)
		}
	}
}

// The caps REFUSE rather than evicting, and each refusal names what to do instead.
func TestVaultCapsRefuseAndExplain(t *testing.T) {
	st, ctx, station, actor, _ := vaultFixture(t)
	lim := DefaultStationVaultLimits()
	lim.MaxSecretBytes = 32
	lim.MaxEntries = 2

	_, _, err := st.PutStationVaultSecret(ctx, lim, station, "big", strings.Repeat("x", 33), "", "tok", actor)
	if !errors.Is(err, ErrVaultCapReached) {
		t.Fatalf("an oversized secret returned %v, want ErrVaultCapReached", err)
	}
	if !strings.Contains(err.Error(), "locker") {
		t.Errorf("the refusal does not point at where payloads belong: %v", err)
	}

	for _, n := range []string{"a", "b"} {
		if _, _, err := st.PutStationVaultSecret(ctx, lim, station, n, "v", "", "tok", actor); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := st.PutStationVaultSecret(ctx, lim, station, "c", "v", "", "tok", actor); !errors.Is(err, ErrVaultCapReached) {
		t.Fatalf("the entry cap did not refuse: %v", err)
	}
	// CONTROL: the cap counts LIVE entries, so deleting one makes room. Without this a
	// vault would fill permanently with tombstones and refuse forever.
	if err := st.DeleteStationVaultSecret(ctx, lim, station, "a", "tok", actor); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.PutStationVaultSecret(ctx, lim, station, "c", "v", "", "tok", actor); err != nil {
		t.Fatalf("after deleting an entry the cap still refuses (%v) — tombstones count against the living", err)
	}
}

// A vault name is a flat label, never a path — the same rule the locker enforces, tested
// here rather than assumed shared, because they are separate code paths.
func TestVaultNamesAreNotPaths(t *testing.T) {
	st, ctx, station, actor, lim := vaultFixture(t)
	for _, bad := range []string{"../etc/passwd", "a/b", `a\b`, "."} {
		if _, _, err := st.PutStationVaultSecret(ctx, lim, station, bad, "v", "", "tok", actor); err == nil {
			t.Errorf("the vault accepted %q as a name", bad)
		}
	}
}
