package mcpserver

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"
)

// dedupTTL bounds how long a search authorizes a subsequent kb_save.
const dedupTTL = 10 * time.Minute

// issueDedupToken mints an HMAC-signed, time-boxed token. kb_search is the only
// place tokens are minted; kb_save requires one, so search-before-save cannot be
// skipped. The token proves a recent search happened; it is not bound to the
// query text (a save is not tied to one query).
func issueDedupToken(secret []byte) string {
	exp := strconv.FormatInt(time.Now().Add(dedupTTL).Unix(), 10)
	return "dct_v1." + exp + "." + sign(secret, exp)
}

// verifyDedupToken checks the token's signature and expiry.
func verifyDedupToken(secret []byte, token string) error {
	parts := strings.SplitN(token, ".", 3)
	if len(parts) != 3 || parts[0] != "dct_v1" {
		return errors.New("malformed dedup_check_token — call kb_search first")
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return errors.New("malformed dedup_check_token")
	}
	if time.Now().Unix() > exp {
		return errors.New("dedup_check_token expired — run kb_search again before saving")
	}
	if !hmac.Equal([]byte(sign(secret, parts[1])), []byte(parts[2])) {
		return errors.New("invalid dedup_check_token")
	}
	return nil
}

func sign(secret []byte, msg string) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(msg))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}
