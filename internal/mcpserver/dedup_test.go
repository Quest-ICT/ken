package mcpserver

import (
	"context"
	"strings"
	"testing"
)

// A dedup token must be useless to any principal other than the one that
// searched. Before this binding the token was signed over its expiry alone, which
// made it a transferable bearer capability: handing the string to another session
// would let that session save without ever searching, reducing the structural
// search-before-save gate to a convention.
func TestDedupTokenIsBoundToTheCallingPrincipal(t *testing.T) {
	secret := []byte("test-secret")

	tok := issueDedupToken(secret, "tokenA")

	if err := verifyDedupToken(secret, tok, "tokenA"); err != nil {
		t.Fatalf("the issuing principal must be able to use its own token: %v", err)
	}
	if err := verifyDedupToken(secret, tok, "tokenB"); err == nil {
		t.Fatal("a token issued to tokenA was accepted for tokenB — it is transferable")
	}
	if err := verifyDedupToken(secret, tok, ""); err == nil {
		t.Fatal("a token issued to tokenA was accepted for an empty subject")
	}
}

// The dev-token principal has an empty TokenID, so its tokens bind to the empty
// subject and must still work end to end.
func TestDedupTokenRoundTripsForTheEmptySubject(t *testing.T) {
	secret := []byte("test-secret")
	tok := issueDedupToken(secret, "")
	if err := verifyDedupToken(secret, tok, ""); err != nil {
		t.Fatalf("empty subject must round-trip: %v", err)
	}
	if err := verifyDedupToken(secret, tok, "tokenA"); err == nil {
		t.Fatal("an empty-subject token was accepted for a named principal")
	}
}

func TestDedupTokenRejectsTamperingAndWrongSecret(t *testing.T) {
	secret := []byte("test-secret")
	tok := issueDedupToken(secret, "tokenA")

	if err := verifyDedupToken([]byte("other-secret"), tok, "tokenA"); err == nil {
		t.Fatal("a token verified under a different server secret")
	}
	if err := verifyDedupToken(secret, tok+"x", "tokenA"); err == nil {
		t.Fatal("a tampered signature was accepted")
	}
	for _, bad := range []string{"", "garbage", "dct_v1.notanumber.sig", "dct_v2.123.sig"} {
		if err := verifyDedupToken(secret, bad, "tokenA"); err == nil {
			t.Fatalf("malformed token %q was accepted", bad)
		}
	}
}

// An expired token is rejected with the message that tells the agent what to do,
// and expiry is checked before the signature so the guidance is specific.
func TestDedupTokenExpiryMessage(t *testing.T) {
	secret := []byte("test-secret")
	// exp far in the past; the signature is irrelevant because expiry is checked first.
	err := verifyDedupToken(secret, "dct_v1.1000000000.whatever", "tokenA")
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("want an expiry error, got %v", err)
	}
}

// dedupSubject reads the calling token, not the actor: several sessions can share
// one actor, and actors collapse by display name, so the token is the narrower
// handle and the only one that makes a token useless to a different caller.
func TestDedupSubjectUsesTheToken(t *testing.T) {
	ctx := withPrincipal(context.Background(), &principal{ActorID: 1, TokenID: "tok123"})
	if got := dedupSubject(ctx); got != "tok123" {
		t.Fatalf("dedupSubject = %q, want the token id", got)
	}
	if got := dedupSubject(context.Background()); got != "" {
		t.Fatalf("dedupSubject with no principal = %q, want empty", got)
	}

	// Two principals sharing an actor must not share a subject.
	a := withPrincipal(context.Background(), &principal{ActorID: 5, TokenID: "tokA"})
	b := withPrincipal(context.Background(), &principal{ActorID: 5, TokenID: "tokB"})
	if dedupSubject(a) == dedupSubject(b) {
		t.Fatal("two tokens under one actor produced the same dedup subject")
	}
}
