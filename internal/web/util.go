package web

import (
	"crypto/rand"
	"encoding/hex"
	"net/url"

	"github.com/Quest-ICT/ken/internal/passwd"
)

// dummyHash lets the login path run a real Argon2id verify even for unknown
// users, keeping timing uniform (no user-enumeration oracle).
var dummyHash, _ = passwd.Hash("timing-uniform-placeholder", passwd.High)

func randToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func urlq(s string) string { return url.QueryEscape(s) }
