package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// oauthFixture creates a human curator, a connector actor, a registered client,
// and returns them for the OAuth store tests.
func oauthFixture(t *testing.T, st *Store, ctx context.Context) (clientID string, connActor, human int64) {
	t.Helper()
	var err error
	if human, err = st.FindOrCreateActor(ctx, "human", "curator"); err != nil {
		t.Fatal(err)
	}
	if connActor, err = st.FindOrCreateActor(ctx, "ai", "Claude"); err != nil {
		t.Fatal(err)
	}
	if clientID, err = st.RegisterOAuthClient(ctx, "Claude", []string{"https://claude.ai/api/mcp/auth_callback"}); err != nil {
		t.Fatal(err)
	}
	return clientID, connActor, human
}

func newCode(t *testing.T, st *Store, ctx context.Context, clientID string, conn, human int64, ttl time.Duration) string {
	t.Helper()
	code, err := st.CreateOAuthGrantAndCode(ctx, NewAuthCode{
		ClientID: clientID, ConnectorActorID: conn, HumanActorID: human,
		RedirectURI:   "https://claude.ai/api/mcp/auth_callback",
		CodeChallenge: "challenge-abc", CodeChallengeMethod: "S256",
		Scope: "read write offline_access", Resource: "https://host/mcp",
	}, ttl)
	if err != nil {
		t.Fatal(err)
	}
	return code
}

// TestOAuthHappyPathAndSingleUse: register → code → peek → exchange → the access
// token resolves to the connector actor; the code is single-use.
func TestOAuthHappyPathAndSingleUse(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	clientID, conn, human := oauthFixture(t, st, ctx)

	// The registered client round-trips.
	c, err := st.OAuthClientByID(ctx, clientID)
	if err != nil || len(c.RedirectURIs) != 1 || c.RedirectURIs[0] != "https://claude.ai/api/mcp/auth_callback" {
		t.Fatalf("client round-trip: %+v %v", c, err)
	}

	code := newCode(t, st, ctx, clientID, conn, human, time.Minute)

	cd, err := st.PeekOAuthCode(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	if cd.ClientID != clientID || cd.RedirectURI != "https://claude.ai/api/mcp/auth_callback" || cd.CodeChallenge != "challenge-abc" {
		t.Fatalf("peek mismatch: %+v", cd)
	}

	access, refresh, err := st.ExchangeOAuthCode(ctx, code, cd.GrantID, time.Hour, 90*24*time.Hour)
	if err != nil || access == "" || refresh == "" {
		t.Fatalf("exchange: %q %q %v", access, refresh, err)
	}

	// The access token authenticates as the connector actor under this grant.
	p, err := st.ValidateOAuthAccessToken(ctx, access)
	if err != nil {
		t.Fatalf("validate access: %v", err)
	}
	if p.ActorID != conn || p.GrantID != cd.GrantID {
		t.Fatalf("principal mismatch: %+v (want actor %d grant %d)", p, conn, cd.GrantID)
	}

	// Single-use: a second exchange of the same code fails and issues nothing.
	if _, _, err := st.ExchangeOAuthCode(ctx, code, cd.GrantID, time.Hour, 90*24*time.Hour); !errors.Is(err, ErrOAuthBadCode) {
		t.Fatalf("second exchange must be ErrOAuthBadCode, got %v", err)
	}
}

// TestOAuthRefreshRotationAndReuseKill: a refresh rotates; re-presenting a
// rotated refresh revokes the whole grant (theft signal).
func TestOAuthRefreshRotationAndReuseKill(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	clientID, conn, human := oauthFixture(t, st, ctx)
	code := newCode(t, st, ctx, clientID, conn, human, time.Minute)
	cd, _ := st.PeekOAuthCode(ctx, code)
	_, refresh, err := st.ExchangeOAuthCode(ctx, code, cd.GrantID, time.Hour, 90*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	// Rotate: new pair issued, and the new access works.
	access2, refresh2, rr, err := st.RotateOAuthRefresh(ctx, refresh, time.Hour, 90*24*time.Hour)
	if err != nil || rr == nil || rr.GrantID != cd.GrantID {
		t.Fatalf("rotate: %v %+v", err, rr)
	}
	if _, err := st.ValidateOAuthAccessToken(ctx, access2); err != nil {
		t.Fatalf("rotated access should be valid: %v", err)
	}

	// IMMEDIATE reuse of the old refresh = benign retry (grace window): rejected,
	// but the grant survives and the rotated access token still works.
	if _, _, _, err := st.RotateOAuthRefresh(ctx, refresh, time.Hour, 90*24*time.Hour); !errors.Is(err, ErrOAuthBadToken) {
		t.Fatalf("in-grace reuse should be a benign ErrOAuthBadToken, got %v", err)
	}
	if _, err := st.ValidateOAuthAccessToken(ctx, access2); err != nil {
		t.Fatalf("grant must survive an in-grace retry: %v", err)
	}

	// Backdate that spent refresh past the grace window → a genuine reuse/theft
	// replay now revokes the WHOLE grant.
	if _, err := st.W.ExecContext(ctx,
		`UPDATE oauth_token SET revoked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','-120 seconds') WHERE token_sha256=?`,
		sha256hex(refresh)); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := st.RotateOAuthRefresh(ctx, refresh, time.Hour, 90*24*time.Hour); !errors.Is(err, ErrOAuthReuseKill) {
		t.Fatalf("post-grace reused refresh must be ErrOAuthReuseKill, got %v", err)
	}
	// After the kill, the newly-rotated tokens are dead too (grant revoked).
	if _, err := st.ValidateOAuthAccessToken(ctx, access2); !errors.Is(err, ErrOAuthBadToken) {
		t.Fatalf("access under a reuse-killed grant must be invalid, got %v", err)
	}
	if _, _, _, err := st.RotateOAuthRefresh(ctx, refresh2, time.Hour, 90*24*time.Hour); !errors.Is(err, ErrOAuthBadToken) {
		t.Fatalf("refresh under a reuse-killed grant must be invalid, got %v", err)
	}
}

// TestOAuthExpiryAndRevocation: expired codes and access tokens are rejected, and
// revoking a grant instantly invalidates its tokens and hides it from the list.
func TestOAuthExpiryAndRevocation(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	clientID, conn, human := oauthFixture(t, st, ctx)

	// Expired authorization code (negative TTL) is not peekable/redeemable.
	expired := newCode(t, st, ctx, clientID, conn, human, -time.Minute)
	if _, err := st.PeekOAuthCode(ctx, expired); !errors.Is(err, ErrOAuthBadCode) {
		t.Fatalf("expired code peek must be ErrOAuthBadCode, got %v", err)
	}

	// Expired access token (negative access TTL) is rejected.
	code := newCode(t, st, ctx, clientID, conn, human, time.Minute)
	cd, _ := st.PeekOAuthCode(ctx, code)
	deadAccess, _, err := st.ExchangeOAuthCode(ctx, code, cd.GrantID, -time.Second, 90*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ValidateOAuthAccessToken(ctx, deadAccess); !errors.Is(err, ErrOAuthBadToken) {
		t.Fatalf("expired access must be ErrOAuthBadToken, got %v", err)
	}

	// A live grant → revoke → its access token dies and it drops from the list.
	code2 := newCode(t, st, ctx, clientID, conn, human, time.Minute)
	cd2, _ := st.PeekOAuthCode(ctx, code2)
	access, _, _ := st.ExchangeOAuthCode(ctx, code2, cd2.GrantID, time.Hour, 90*24*time.Hour)
	if _, err := st.ValidateOAuthAccessToken(ctx, access); err != nil {
		t.Fatalf("pre-revoke access should be valid: %v", err)
	}
	grants, _ := st.ListOAuthGrants(ctx)
	if len(grants) == 0 {
		t.Fatal("expected at least one live grant before revoke")
	}
	if err := st.RevokeOAuthGrant(ctx, cd2.GrantID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ValidateOAuthAccessToken(ctx, access); !errors.Is(err, ErrOAuthBadToken) {
		t.Fatalf("post-revoke access must be ErrOAuthBadToken, got %v", err)
	}

	// Unknown token → ErrOAuthBadToken (not a panic / not a different error).
	if _, err := st.ValidateOAuthAccessToken(ctx, "totally-unknown-token"); !errors.Is(err, ErrOAuthBadToken) {
		t.Fatalf("unknown token must be ErrOAuthBadToken, got %v", err)
	}
}
