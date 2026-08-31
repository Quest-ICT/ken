package store

import (
	"context"
	"testing"
	"time"
)

// TestAnAbandonedGrantIsRevokedAndALiveFlowIsNot pins both halves of the predicate.
//
// A grant is created at /authorize, not at token exchange, so a retry or a closed consent screen
// leaves one with no tokens and revoked_at IS NULL forever. ken-prod-ops measured two grants for
// one client 2.3 seconds apart on the live deployment — one with an access and a refresh token, one
// with none, both showing as connected applications and distinguishable only by an Active-tokens
// count of 0. Vlad's rule is that two active authorizations is a fault.
//
// THE SECOND ARM IS THE ONE THAT MATTERS. A sweep that revoked every tokenless grant would kill the
// flow that is still in progress — the human on the consent screen right now — and it would pass a
// test that only checked the abandoned one was cleaned up. So the live-code arm is not a courtesy
// case: it is the difference between a cleanup and an outage.
func TestAnAbandonedGrantIsRevokedAndALiveFlowIsNot(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	clientID, actorID, humanID := oauthFixture(t, s, ctx)

	// (1) ABANDONED: a grant whose code has already expired and which issued no token.
	dead, err := s.CreateOAuthGrantAndCode(ctx, NewAuthCode{
		ClientID: clientID, ConnectorActorID: actorID, HumanActorID: humanID,
		RedirectURI: "https://example.invalid/cb", CodeChallenge: "x", CodeChallengeMethod: "S256",
		Scope: "ken:kb",
	}, -1*time.Second) // already expired
	if err != nil {
		t.Fatalf("create abandoned grant: %v", err)
	}
	_ = dead

	// (2) MID-FLOW: a grant whose code is still live. The human may be finishing consent now.
	live, err := s.CreateOAuthGrantAndCode(ctx, NewAuthCode{
		ClientID: clientID, ConnectorActorID: actorID, HumanActorID: humanID,
		RedirectURI: "https://example.invalid/cb", CodeChallenge: "y", CodeChallengeMethod: "S256",
		Scope: "ken:kb",
	}, 10*time.Minute) // still live
	if err != nil {
		t.Fatalf("create live grant: %v", err)
	}
	_ = live

	n, err := s.RevokeAbandonedGrants(ctx)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 1 {
		t.Errorf("swept %d grant(s), want exactly 1 — the abandoned one and not the mid-flow one", n)
	}

	var liveActive, deadActive int
	if err := s.R.QueryRow(`SELECT COUNT(*) FROM oauth_grant WHERE revoked_at IS NULL`).Scan(&liveActive); err != nil {
		t.Fatal(err)
	}
	if liveActive != 1 {
		t.Errorf("%d grants left active, want 1: a sweep that takes the mid-flow grant breaks the "+
			"consent the human is completing right now", liveActive)
	}
	if err := s.R.QueryRow(`SELECT COUNT(*) FROM oauth_grant WHERE revoked_at IS NOT NULL`).Scan(&deadActive); err != nil {
		t.Fatal(err)
	}
	if deadActive != 1 {
		t.Errorf("%d grants revoked, want 1", deadActive)
	}
}
