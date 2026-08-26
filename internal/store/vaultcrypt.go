package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Vault encryption at rest.
//
// DECIDED BY VLAD 2026-08-26 (IDENTITY.md §11, option A): vault values are encrypted in `ken.db`
// under a key held by the SERVER, stored OUTSIDE the database and excluded from backups.
// Explicitly NOT end-to-end — see the §11 note for why that option was rejected rather than
// deferred.
//
// *** WHAT THIS PROTECTS, AND IT IS EXACTLY ONE THING: COPIES OF THE DATABASE THAT LEAVE THE
// HOST. *** `ken.db` is backed up nightly and those snapshots travel off-box. Before this, every
// vault secret travelled with them in plaintext. Now the snapshot carries ciphertext and the key
// is not in it.
//
// IT DOES NOT PROTECT AGAINST ROOT ON THE HOST. Anyone who can read `ken.db` can almost certainly
// read `vault.key` beside it, and the running process holds the key in memory regardless. Saying
// so is a CONDITION of shipping this, not a disclaimer: migration 0016 argued — correctly — that
// encryption whose key travels with the ciphertext is theatre, and that "theatre in a security
// store is worse than an honest absence because it invites the operator to relax a control that
// is not there." The only reason this is not that is the key's LOCATION, so the moment anyone
// puts `vault.key` inside a backup, this becomes the thing 0016 warned about.
//
// *** THE RESIDUAL WEAKNESS, STATED BECAUSE IT IS NOT OBVIOUS. *** `station_vault.sha256` and
// `size_bytes` are still computed over the PLAINTEXT, deliberately: the digest is what the
// console shows to identify a secret and what an operator compares against an external copy, and
// an HMAC would silently stop being "the sha256 of my secret". The consequence is that a
// LOW-ENTROPY secret remains guessable from its digest even though its value is encrypted — the
// digest travels in the same backup. The vault is for high-entropy credentials (API keys, tokens,
// private keys); a short passphrase stored here is protected by this scheme far less than it
// appears.
const (
	// vaultKeyFile sits next to ken.db, alongside the existing dedup.key. The nightly snapshot
	// (scripts/ken-snapshot.sh) copies ONLY ken.db, so this file is outside the backup by
	// construction rather than by an exclusion rule someone has to remember.
	vaultKeyFile = "vault.key"

	// vaultSealPrefix makes a stored value self-describing, so a plaintext row written before
	// this shipped is distinguishable from ciphertext WITHOUT a schema column or a migration.
	// The version number is here so a future scheme can be introduced without guessing.
	vaultSealPrefix = "kv1:"
)

// ErrVaultKeyMissing is returned when a vault write is attempted on a Store that holds no key.
//
// FAIL CLOSED, LOUDLY. The tempting alternative — fall back to storing plaintext — is the defect
// class this project keeps paying for: the write succeeds, the operator believes the value is
// encrypted, and nothing anywhere says otherwise. A refused write is recoverable; a silent
// plaintext write in a store the operator thinks is sealed is not.
var ErrVaultKeyMissing = errors.New("the vault encryption key is not loaded, so this value cannot be stored — refusing rather than writing it in plaintext")

// loadOrCreateVaultKey returns the 32-byte AES-256 key for the vault, creating and persisting one
// on first use.
//
// UNLIKE dedup.key, LOSING THIS IS UNRECOVERABLE. The dedup secret only invalidates in-flight save
// tokens; this key is the only way back to every secret in the vault. So a failure to PERSIST a
// freshly generated key is a hard error here, where the dedup path logs a warning and continues —
// continuing would hand out a key that works until the next restart and then silently orphans
// everything written with it.
func loadOrCreateVaultKey(dbPath string) ([]byte, error) {
	path := filepath.Join(filepath.Dir(dbPath), vaultKeyFile)
	switch b, err := os.ReadFile(path); {
	case err == nil && len(b) == 32:
		return b, nil
	case err == nil:
		// A short or padded file is not a key. Refusing beats deriving something from it:
		// a wrong key decrypts nothing and looks exactly like corruption.
		return nil, fmt.Errorf("%s holds %d bytes, not a 32-byte key — refusing to guess; "+
			"restore the real key or move this file aside to start a NEW vault (existing secrets "+
			"become unreadable)", path, len(b))
	case !os.IsNotExist(err):
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate vault key: %w", err)
	}
	// O_EXCL so two processes racing on first start cannot each write a key and leave one of
	// them holding a key that no longer opens what it wrote.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			// Lost the race — read the winner's key rather than overwrite it.
			if b, rerr := os.ReadFile(path); rerr == nil && len(b) == 32 {
				return b, nil
			}
		}
		return nil, fmt.Errorf("persist vault key to %s: %w — refusing to run with a key that "+
			"would not survive a restart", path, err)
	}
	if _, err := f.Write(key); err != nil {
		f.Close()
		return nil, fmt.Errorf("write vault key: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("close vault key: %w", err)
	}
	return key, nil
}

// sealVaultSecret encrypts a plaintext secret for storage. AES-256-GCM with a fresh random nonce
// per write, so the same secret stored twice produces different ciphertext and the database
// reveals nothing by comparison.
func (s *Store) sealVaultSecret(plain string) (string, error) {
	if len(s.vaultKey) != 32 {
		return "", ErrVaultKeyMissing
	}
	block, err := aes.NewCipher(s.vaultKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := gcm.Seal(nonce, nonce, []byte(plain), nil)
	return vaultSealPrefix + base64.StdEncoding.EncodeToString(ct), nil
}

// openVaultSecret decrypts a stored value.
//
// A value with no prefix is returned AS-IS, because it was written before this shipped. That is
// deliberate and it is the reason no migration was needed: an existing deployment keeps working,
// and each secret becomes ciphertext the next time it is written. The honest consequence is that
// old values stay plaintext until rewritten — `ken vault reseal` is the intended sweep, and until
// it runs an operator should assume nothing about rows older than this release.
func (s *Store) openVaultSecret(stored string) (string, error) {
	if !strings.HasPrefix(stored, vaultSealPrefix) {
		return stored, nil
	}
	if len(s.vaultKey) != 32 {
		return "", ErrVaultKeyMissing
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(stored, vaultSealPrefix))
	if err != nil {
		return "", fmt.Errorf("vault value is not decodable: %w", err)
	}
	block, err := aes.NewCipher(s.vaultKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("vault value is too short to contain a nonce")
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
	if err != nil {
		// GCM authenticates, so this is either the wrong key or a modified row. Both are
		// worth saying out loud rather than returning an empty string a caller might store.
		return "", fmt.Errorf("vault value did not decrypt — the key in %s does not match the one "+
			"that wrote this secret, or the row was altered: %w", vaultKeyFile, err)
	}
	return string(plain), nil
}
