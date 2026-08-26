package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// *** THE ONLY TEST THAT PROVES ENCRYPTION HAPPENED: READ THE RAW COLUMN. ***
//
// A write-then-read round trip passes identically whether the value was encrypted or stored in
// plaintext, because both paths return what went in. That is the defect class this project keeps
// paying for — a check that cannot fail for the reason it exists — so this reaches past the
// accessor and asserts on the bytes in the database.
func TestAVaultSecretIsCiphertextInTheDatabase(t *testing.T) {
	st, ctx, station, actor, lim := vaultFixture(t)
	const secret = "sk-live-THIS-MUST-NOT-APPEAR-9f2c"

	if _, _, err := st.PutStationVaultSecret(ctx, lim, station, "api", secret, "n", "tok", actor); err != nil {
		t.Fatal(err)
	}

	var stored string
	if err := st.R.QueryRowContext(ctx,
		`SELECT secret FROM station_vault WHERE station_id=? AND name=?`, station, "api").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, secret) {
		t.Fatal("the plaintext secret is in the database — the value was stored unencrypted")
	}
	if !strings.HasPrefix(stored, vaultSealPrefix) {
		t.Errorf("stored value has no %q prefix, so nothing marks it as encrypted: %q", vaultSealPrefix, stored)
	}

	// CONTROL: the fixture really did store something, so the assertion above is not passing
	// against an empty column.
	if stored == "" {
		t.Fatal("nothing was stored at all; the check above would pass against an empty vault")
	}

	// And it still reads back — encryption that loses the secret is not a feature.
	got, err := st.GetStationVaultSecret(ctx, lim, station, "api", "station", "tok", actor)
	if err != nil {
		t.Fatal(err)
	}
	if got.Secret != secret {
		t.Errorf("round trip returned %q, want the original secret", got.Secret)
	}
}

// HISTORY IS THE HALF THAT IS EASY TO FORGET. Every update copies the outgoing value into
// station_vault_history, and a vault that encrypts the live row while leaving every superseded
// value in plaintext protects nothing — the backup carries the history table too.
func TestVaultHistoryIsCiphertextToo(t *testing.T) {
	st, ctx, station, actor, lim := vaultFixture(t)
	const first = "first-secret-8a13"
	const second = "second-secret-b77e"

	for _, v := range []string{first, second} {
		if _, _, err := st.PutStationVaultSecret(ctx, lim, station, "api", v, "n", "tok", actor); err != nil {
			t.Fatal(err)
		}
	}

	var hist string
	if err := st.R.QueryRowContext(ctx,
		`SELECT secret FROM station_vault_history WHERE station_id=? AND name=? ORDER BY rev DESC LIMIT 1`,
		station, "api").Scan(&hist); err != nil {
		t.Fatal(err)
	}
	if hist == "" {
		t.Fatal("no history row was written, so this test asserts nothing")
	}
	if strings.Contains(hist, first) {
		t.Error("the superseded secret sits in station_vault_history in plaintext — encrypting only " +
			"the live row leaves every previous value readable in the same backup")
	}
	if !strings.HasPrefix(hist, vaultSealPrefix) {
		t.Errorf("history value is not marked as encrypted: %q", hist)
	}
}

// THE KEY MUST BE OUTSIDE THE DATABASE, because that location is the entire security argument.
// If it ever moves into ken.db, this scheme becomes the theatre migration 0016 warned about.
func TestTheVaultKeyLivesOutsideTheDatabaseAndIsNotWorldReadable(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	info, err := os.Stat(filepath.Join(dir, vaultKeyFile))
	if err != nil {
		t.Fatalf("Open did not create %s beside the database: %v", vaultKeyFile, err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("%s is mode %o, want 600 — a key any local account can read protects nobody", vaultKeyFile, perm)
	}
	if info.Size() != 32 {
		t.Errorf("%s is %d bytes, want 32 (AES-256)", vaultKeyFile, info.Size())
	}
}

// A KEY THAT DOES NOT MATCH MUST FAIL LOUDLY. GCM authenticates, so a wrong key is detectable —
// and the alternative, returning an empty string or garbage, is how a caller ends up storing
// nonsense over a real secret during a restore.
func TestAWrongKeyIsAnErrorRatherThanGarbage(t *testing.T) {
	st := &Store{vaultKey: make([]byte, 32)}
	sealed, err := st.sealVaultSecret("the-real-secret")
	if err != nil {
		t.Fatal(err)
	}

	other := &Store{vaultKey: append(make([]byte, 31), 0xFF)}
	got, err := other.openVaultSecret(sealed)
	if err == nil {
		t.Fatalf("a wrong key decrypted to %q instead of failing; a silent wrong answer here "+
			"overwrites a real secret on the next restore", got)
	}
	if !strings.Contains(err.Error(), vaultKeyFile) {
		t.Errorf("the error does not name %s, so an operator is not told where to look: %v", vaultKeyFile, err)
	}
}

// A STORE WITH NO KEY REFUSES TO WRITE. The tempting fallback is to store plaintext and carry on;
// that is the silent-failure shape, and it is worse than an error because the operator believes
// the value is sealed.
func TestAStoreWithNoKeyRefusesToStoreASecret(t *testing.T) {
	st := &Store{}
	if _, err := st.sealVaultSecret("secret"); err == nil {
		t.Fatal("a Store with no key sealed a secret; it must refuse rather than store plaintext")
	}
}

// PRE-ENCRYPTION ROWS STILL READ. This is what lets an existing deployment upgrade with no
// migration, and the honest cost — those rows stay plaintext until rewritten — is recorded in
// openVaultSecret rather than left for someone to discover.
func TestAPlaintextRowWrittenBeforeEncryptionStillReads(t *testing.T) {
	st := &Store{vaultKey: make([]byte, 32)}
	got, err := st.openVaultSecret("legacy-plaintext-value")
	if err != nil {
		t.Fatalf("a pre-encryption value failed to read: %v", err)
	}
	if got != "legacy-plaintext-value" {
		t.Errorf("got %q, want the legacy value unchanged", got)
	}
}
