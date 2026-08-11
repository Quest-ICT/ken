package web

import (
	"net/url"
	"strings"
	"testing"

	"github.com/Quest-ICT/ken/internal/store"
)

const vaultSecret = "sk-live-DO-NOT-RENDER-9f3c"

// The console is the operator's surface for the vault, and it has to do three things a
// listing alone cannot: show what is held WITHOUT showing values, hand one over when the
// human asks and record that it did, and undo a session's mistake.
//
// This drives the HTTP surface rather than the store, for the same reason the promotion
// console test does: the store was never the missing half. A vault whose values are
// perfectly stored and unreachable by the only person allowed to read them is not a
// feature.
func TestTheConsoleShowsTheVaultWithoutShowingSecrets(t *testing.T) {
	st, ctx, cli, base, actor := stationsHarness(t)

	station, err := st.CreateStation(ctx, spaceForSession, "prod-ops", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.PutStationVaultSecret(ctx, store.DefaultStationVaultLimits(), station.StationID,
		"deploy-key", vaultSecret, "the rsync key for the backup host", "tok", actor); err != nil {
		t.Fatal(err)
	}

	page := get(t, cli, base+"/stations")

	// The identifying metadata is there — a human has to be able to tell WHICH secret
	// this is without being shown one.
	for _, want := range []string{"deploy-key", "the rsync key for the backup host"} {
		if !strings.Contains(page, want) {
			t.Fatalf("the vault does not reach the console: %q is absent", want)
		}
	}
	// And the value is NOT.
	if strings.Contains(page, vaultSecret) {
		t.Fatal("THE STATIONS PAGE RENDERS THE SECRET. It is a long page an operator leaves open, and the live-refresh poller reloads it — a value here sits on screen long after anyone is looking at it.")
	}
	// The reveal control exists, or the value is stored where nobody can reach it.
	if !strings.Contains(page, "/vault/reveal") {
		t.Fatal("no way to reveal a secret — the vault is write-only from the operator's side")
	}
}

// Revealing hands over the value AND lands in the trail, marked as coming from the
// console. Both halves are asserted because either alone is the defect: a reveal that
// does not log corrupts the audit trail's meaning, and a log with no reveal is a store
// the owner cannot read.
func TestRevealingASecretReturnsItAndIsRecorded(t *testing.T) {
	st, ctx, cli, base, actor := stationsHarness(t)

	station, err := st.CreateStation(ctx, spaceForSession, "prod-ops", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.PutStationVaultSecret(ctx, store.DefaultStationVaultLimits(), station.StationID,
		"deploy-key", vaultSecret, "", "tok", actor); err != nil {
		t.Fatal(err)
	}

	csrf := extract(t, cli, base+"/stations", `name="csrf" value="([^"]+)"`)
	body := postForm(t, cli, base+"/stations/"+station.StationID+"/vault/reveal",
		url.Values{"csrf": {csrf}, "name": {"deploy-key"}})

	if !strings.Contains(body, vaultSecret) {
		t.Fatal("the reveal did not return the value — the owner of the instance cannot read their own vault")
	}

	reads, total, err := st.StationVaultReads(ctx, station.StationID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(reads) != 1 {
		t.Fatalf("the console read is not in the trail (retained %d, total %d) — a secret was handed out unrecorded", len(reads), total)
	}
	if reads[0].Via != "console" {
		t.Errorf("the trail records the reveal as %q, want console — a human's read and a session's must be distinguishable", reads[0].Via)
	}

	// CONTROL: the ordinary page still does not carry the value. Without this the test
	// above could pass on a page that renders every secret all the time.
	if strings.Contains(get(t, cli, base+"/stations"), vaultSecret) {
		t.Fatal("after one reveal the secret is rendered into the page on every load")
	}
}

// A reveal is a POST with CSRF. A GET would put the value in browser history and one
// prefetch away from firing with no human deciding to — and it would land in the audit
// trail as a deliberate read, which is worse than not logging at all.
func TestRevealRefusesWithoutTheCSRFToken(t *testing.T) {
	st, ctx, cli, base, actor := stationsHarness(t)

	station, err := st.CreateStation(ctx, spaceForSession, "prod-ops", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.PutStationVaultSecret(ctx, store.DefaultStationVaultLimits(), station.StationID,
		"k", vaultSecret, "", "tok", actor); err != nil {
		t.Fatal(err)
	}

	body := postForm(t, cli, base+"/stations/"+station.StationID+"/vault/reveal",
		url.Values{"name": {"k"}}) // no csrf
	if strings.Contains(body, vaultSecret) {
		t.Fatal("a reveal without a CSRF token returned the secret")
	}
	if _, total, _ := st.StationVaultReads(ctx, station.StationID, 10); total != 0 {
		t.Fatalf("a refused reveal was logged as a read (total %d) — the trail would overstate exposure", total)
	}
}

// The console can undo what a session did. This is the operator half of "every vault
// write is reversible", and it is deliberately not offered as a station tool: a session
// that has just destroyed something by mistake is not the party to decide what goes back.
func TestTheConsoleCanRestoreWhatASessionOverwrote(t *testing.T) {
	st, ctx, cli, base, actor := stationsHarness(t)
	lim := store.DefaultStationVaultLimits()

	station, err := st.CreateStation(ctx, spaceForSession, "prod-ops", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.PutStationVaultSecret(ctx, lim, station.StationID, "k", "the-right-value", "", "tok", actor); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.PutStationVaultSecret(ctx, lim, station.StationID, "k", "the-wrong-value", "", "tok", actor); err != nil {
		t.Fatal(err)
	}

	csrf := extract(t, cli, base+"/stations", `name="csrf" value="([^"]+)"`)
	postForm(t, cli, base+"/stations/"+station.StationID+"/vault/restore",
		url.Values{"csrf": {csrf}, "name": {"k"}})

	got, err := st.GetStationVaultSecret(ctx, lim, station.StationID, "k", "console", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	if got.Secret != "the-right-value" {
		t.Fatalf("after restoring, the value is %q — the console cannot undo a bad overwrite, which is the whole reason history is kept", got.Secret)
	}
}

// A deleted secret stays VISIBLE as a tombstone. An operator deciding whether a
// credential is safely gone has to be able to see that it is only soft-deleted — and
// that they can put it back.
func TestADeletedSecretIsShownAsRecoverableRatherThanVanishing(t *testing.T) {
	st, ctx, cli, base, actor := stationsHarness(t)
	lim := store.DefaultStationVaultLimits()

	station, err := st.CreateStation(ctx, spaceForSession, "prod-ops", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.PutStationVaultSecret(ctx, lim, station.StationID, "retired-key", vaultSecret, "", "tok", actor); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteStationVaultSecret(ctx, lim, station.StationID, "retired-key", "tok", actor); err != nil {
		t.Fatal(err)
	}

	page := get(t, cli, base+"/stations")
	if !strings.Contains(page, "retired-key") {
		t.Fatal("a deleted secret disappears from the console — an operator would believe it was destroyed when it is still in the database and in every backup")
	}
	if !strings.Contains(page, "/vault/restore") {
		t.Fatal("the tombstone offers no way back, so the delete is reversible only in principle")
	}
	if strings.Contains(page, vaultSecret) {
		t.Fatal("the tombstone renders its value")
	}
}
