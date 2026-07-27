package mcpserver

import (
	"context"
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

// issueDedupToken mints an HMAC-signed, time-boxed token BOUND TO THE CALLER.
// kb_search is the only place tokens are minted; kb_save requires one, so
// search-before-save cannot be skipped. The token proves a recent search happened;
// it is not bound to the query text (a save is not tied to one query).
//
// subject binds the token to the principal that searched. Without it the token is
// a transferable bearer capability: any holder could save, so one session handing
// another a 60-character string would reduce the structural search-before-save
// gate to a convention. That is inert while a token can only reach the session
// that minted it, and stops being inert the moment sessions can exchange strings.
// The binding is cheap, so it is done before that becomes possible rather than
// after.
//
// The wire shape is unchanged (an opaque "dct_v1.<exp>.<sig>" string): only the
// signed message gains the subject, so nothing outside this file needs to know.
// Tokens minted by an older build stop verifying at upgrade — cosmetic, given the
// 10-minute TTL.
func issueDedupToken(secret []byte, subject string) string {
	exp := strconv.FormatInt(time.Now().Add(dedupTTL).Unix(), 10)
	return "dct_v1." + exp + "." + sign(secret, exp+"|"+subject)
}

// verifyDedupToken checks the token's signature, expiry, and binding to subject.
func verifyDedupToken(secret []byte, token, subject string) error {
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
	// One check covers both tampering and a token issued to a different principal:
	// distinguishing them would tell a caller its stolen token was otherwise valid,
	// and the remedy is identical either way.
	if !hmac.Equal([]byte(sign(secret, parts[1]+"|"+subject)), []byte(parts[2])) {
		return errors.New("invalid dedup_check_token — run kb_search yourself before saving")
	}
	return nil
}

// dedupSubject is the identity a dedup token is bound to: the calling token, not
// the actor. The token is the narrower handle — several sessions can share one
// actor, and actors collapse by display name — so binding to it is what makes a
// token useless to a different caller.
//
// The dev-token principal has an empty TokenID and therefore one shared subject.
// That is acceptable: it is a single static credential, refused whenever TLS is on.
//
// The separator is a printable '|' rather than a control byte: it cannot appear in
// any TokenID we mint (base62, or "oauth-<grantID>"), and a NUL or tab here would
// be invisible in every log and diagnostic that ever printed it.
func dedupSubject(ctx context.Context) string {
	p := principalFrom(ctx)
	if p == nil {
		return ""
	}
	return p.TokenID
}

func sign(secret []byte, msg string) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(msg))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}
