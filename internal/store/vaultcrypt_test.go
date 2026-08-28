package store

import (
	"context"
	"errors"
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

// *** A SECRET CROSSES TO ANOTHER STATION WITHOUT EVER BEING PLAINTEXT ANYWHERE BUT MEMORY. ***
//
// The feature exists because every other route was wrong: a message body is stored and retained,
// a relayed file is written to disk, and asking the human to copy it by hand is the credential
// tax the requirement exists to remove. So the test that matters is not "did it arrive" — it is
// "did it arrive ENCRYPTED, and is the sender's copy still there".
func TestASecretSentToAnotherStationLandsEncrypted(t *testing.T) {
	st, ctx, from, actor, lim := vaultFixture(t)
	to, err := st.CreateStation(ctx, "peer-ops", "the other machine", actor)
	if err != nil {
		t.Fatal(err)
	}
	const secret = "sk-cross-machine-DO-NOT-LEAK-11d4"
	if _, _, err := st.PutStationVaultSecret(ctx, lim, from, "deploy-key", secret, "", "tok", actor); err != nil {
		t.Fatal(err)
	}
	linkStations(t, st, ctx, from, to.StationID, actor)

	got, _, err := st.SendStationVaultSecret(ctx, lim, from, to.StationID, "deploy-key", "", "tok", actor)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if got.SHA256 == "" {
		t.Error("the receipt carries no digest, so neither side can confirm they hold the same secret")
	}

	// IN THE RECIPIENT'S VAULT, AS CIPHERTEXT. Reading the raw column, because a round trip
	// would pass even if the transfer wrote plaintext.
	var stored string
	if err := st.R.QueryRowContext(ctx,
		`SELECT secret FROM station_vault WHERE station_id=? AND name=?`, to.StationID, "deploy-key").Scan(&stored); err != nil {
		t.Fatalf("nothing arrived in the recipient's vault: %v", err)
	}
	if strings.Contains(stored, secret) {
		t.Fatal("the transferred secret is PLAINTEXT in the recipient's vault")
	}
	if !strings.HasPrefix(stored, vaultSealPrefix) {
		t.Errorf("the recipient's copy is not marked encrypted: %q", stored)
	}

	// AND IT IS THE RIGHT SECRET — otherwise the assertion above passes against garbage.
	back, err := st.GetStationVaultSecret(ctx, lim, to.StationID, "deploy-key", "station", "tok2", actor)
	if err != nil {
		t.Fatal(err)
	}
	if back.Secret != secret {
		t.Errorf("recipient read %q, want the original secret", back.Secret)
	}

	// THE SENDER KEEPS THEIRS. A transfer that emptied the source would make a mistyped station
	// id destructive, and every vault write is supposed to be reversible.
	mine, err := st.GetStationVaultSecret(ctx, lim, from, "deploy-key", "station", "tok", actor)
	if err != nil {
		t.Fatalf("the sender lost their own copy: %v", err)
	}
	if mine.Secret != secret {
		t.Error("the sender's copy changed during a transfer")
	}

	// THE TRANSFER IS AUDITED AS A TRANSFER, not as an ordinary read — "who saw this secret"
	// has to stay answerable, and a transfer is a materially different event.
	var vias string
	if err := st.R.QueryRowContext(ctx,
		`SELECT COALESCE(GROUP_CONCAT(via),'') FROM station_vault_read WHERE station_id=? AND name=?`,
		from, "deploy-key").Scan(&vias); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(vias, "transfer") {
		t.Errorf("the sender's read log is %q — the transfer is not distinguishable from a normal read", vias)
	}
}

// UNLINKED STATIONS CANNOT PASS SECRETS. The link is a human saying these two posts may talk;
// without it a station id typed by a session would be enough to push a credential anywhere.
func TestASecretCannotBeSentToAnUnlinkedStation(t *testing.T) {
	st, ctx, from, actor, lim := vaultFixture(t)
	to, err := st.CreateStation(ctx, "stranger", "not linked", actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.PutStationVaultSecret(ctx, lim, from, "k", "s3cret-value", "", "tok", actor); err != nil {
		t.Fatal(err)
	}

	// CONTROL FIRST: the send fails for the RIGHT reason. Without this the test would pass
	// against a station that does not exist, or a secret that was never stored.
	_, _, err = st.SendStationVaultSecret(ctx, lim, from, to.StationID, "k", "", "tok", actor)
	if !errors.Is(err, ErrStationsNotLinked) {
		t.Fatalf("want ErrStationsNotLinked, got %v", err)
	}
	linkStations(t, st, ctx, from, to.StationID, actor)
	if _, _, err := st.SendStationVaultSecret(ctx, lim, from, to.StationID, "k", "", "tok", actor); err != nil {
		t.Fatalf("after linking, the same send must succeed — otherwise the refusal above proved nothing: %v", err)
	}
}

// A TRANSFER TO YOURSELF IS REFUSED rather than quietly bumping a revision for nothing.
func TestASecretCannotBeSentToTheSendersOwnVault(t *testing.T) {
	st, ctx, from, actor, lim := vaultFixture(t)
	if _, _, err := st.PutStationVaultSecret(ctx, lim, from, "k", "v", "", "tok", actor); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.SendStationVaultSecret(ctx, lim, from, from, "k", "", "tok", actor); !errors.Is(err, ErrCannotSendToSelf) {
		t.Errorf("want ErrCannotSendToSelf, got %v", err)
	}
}

// linkStations approves a link the way the console does, so the tests above gate on the real
// predicate rather than on a fixture that fakes it.
func linkStations(t *testing.T, st *Store, ctx context.Context, a, b string, actor int64) {
	t.Helper()
	if _, err := st.EnsureStationLink(ctx, a, b, actor); err != nil {
		t.Fatal(err)
	}
}

// *** A TRANSFER IS AUDITED BUT NOT COUNTED AS A RETRIEVAL. ***
//
// read_count renders in the console as "how often this credential was retrieved". A send is a
// different event — that is why it has its own `via` and why migration 0022 exists — so counting
// it there too would state one act twice, once under a label meaning something else.
//
// ken-prod-ops found the original behaviour in the first live transfer ever performed: m600 never
// called station_vault_get and its sender copy still read 1.
func TestASendIsLoggedButDoesNotCountAsARetrieval(t *testing.T) {
	st, ctx, from, actor, lim := vaultFixture(t)
	to, err := st.CreateStation(ctx, "counter-peer", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.PutStationVaultSecret(ctx, lim, from, "k", "v-for-counting", "", "tok", actor); err != nil {
		t.Fatal(err)
	}
	linkStations(t, st, ctx, from, to.StationID, actor)

	readCount := func() int {
		t.Helper()
		var n int
		if err := st.R.QueryRowContext(ctx,
			`SELECT read_count FROM station_vault WHERE station_id=? AND name=?`, from, "k").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := readCount(); n != 0 {
		t.Fatalf("a freshly stored secret reads %d; the fixture is wrong", n)
	}

	if _, _, err := st.SendStationVaultSecret(ctx, lim, from, to.StationID, "k", "", "tok", actor); err != nil {
		t.Fatal(err)
	}
	if n := readCount(); n != 0 {
		t.Errorf("read_count is %d after a SEND — the console renders that number as retrievals, "+
			"and an operator auditing 'this key was read N times' would be counting transfers", n)
	}

	// BUT IT IS STILL AUDITED. Without this the test would pass against a transfer that vanished
	// from the trail entirely, which is far worse than over-counting.
	var n int
	if err := st.R.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM station_vault_read WHERE station_id=? AND name=? AND via='transfer'`,
		from, "k").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("the transfer left %d audit rows, want 1 — not counting it must not mean not recording it", n)
	}

	// CONTROL: a REAL retrieval still counts, so the change is scoped to transfers.
	if _, err := st.GetStationVaultSecret(ctx, lim, from, "k", "station", "tok", actor); err != nil {
		t.Fatal(err)
	}
	if n := readCount(); n != 1 {
		t.Errorf("read_count is %d after a genuine station read; the fix has disabled counting "+
			"altogether rather than excluding transfers", n)
	}
}
