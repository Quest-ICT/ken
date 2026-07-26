// Package passwd hashes and verifies passwords with Argon2id, producing and
// consuming standard PHC strings ($argon2id$v=19$m=..,t=..,p=..$salt$hash). The
// cost parameters travel with each hash, so changing profiles never breaks
// verification of existing hashes.
package passwd

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Profile selects Argon2id cost parameters (salt 16, hash 32, t=2, p=1 in both;
// only the memory cost differs). Standard is the OWASP minimum for Argon2id; Ken
// defaults to High because a curator account is the only thing standing between an
// attacker and the whole knowledge base, and logins are rare enough that the extra
// memory cost is never on a hot path.
type Profile struct {
	MemoryKiB uint32
	Time      uint32
	Threads   uint8
}

var (
	// Standard is the OWASP minimum for Argon2id (19 MiB).
	Standard = Profile{MemoryKiB: 19 * 1024, Time: 2, Threads: 1}
	// High (32 MiB) is Ken's default for the human login.
	High = Profile{MemoryKiB: 32 * 1024, Time: 2, Threads: 1}
)

const (
	saltLen = 16
	keyLen  = 32
)

var b64 = base64.RawStdEncoding

// Hash hashes password with the given profile and returns a PHC string.
func Hash(password string, p Profile) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, p.Time, p.MemoryKiB, p.Threads, keyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.MemoryKiB, p.Time, p.Threads,
		b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// Verify reports whether password matches the PHC-encoded hash. It reads the
// cost parameters from the hash itself, so hashes made with any profile verify.
func Verify(password, phc string) (bool, error) {
	parts := strings.Split(phc, "$") // ["", "argon2id", "v=19", "m=..,t=..,p=..", salt, hash]
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errors.New("invalid argon2id hash")
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errors.New("unsupported argon2 version")
	}
	var mem, t uint32
	var par uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &mem, &t, &par); err != nil {
		return false, errors.New("invalid argon2id parameters")
	}
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return false, fmt.Errorf("decode salt: %w", err)
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil {
		return false, fmt.Errorf("decode hash: %w", err)
	}
	got := argon2.IDKey([]byte(password), salt, t, mem, par, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
