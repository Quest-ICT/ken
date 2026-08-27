package comm

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// *** A CONVERSATION CLAIMS AN ENDPOINT AND DRIVES IT WITH NO SECRET. ***
//
// This is what lets a claude.ai CHAT session use comm at all. comm_register's instruction was
// "WRITE THEM TO A FILE ON DISK NOW", and a chat session has no disk — it could register once and
// then lose the ability to poll forever, because the secret is shown once and nothing it controls
// survives a compaction.
func TestAConversationClaimsAnEndpointAndNeedsNoSecret(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	owner := Owner{TokenID: "tok-chat", ActorID: 7}

	ep, created, err := st.ClaimEndpointForSession(ctx, owner, "conv-abc", "chat", "")
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("the first claim did not create an endpoint")
	}
	if ep.SessionKey != "conv-abc" {
		t.Errorf("the endpoint records session_key %q, want the declared key", ep.SessionKey)
	}

	// THE POINT: it authenticates with the key and NOTHING ELSE.
	got, err := st.AuthenticateEndpointBySessionKey(ctx, "conv-abc")
	if err != nil {
		t.Fatalf("a claimed endpoint could not authenticate by its key: %v", err)
	}
	if got.EndpointID != ep.EndpointID {
		t.Errorf("authenticated as %q, want %q", got.EndpointID, ep.EndpointID)
	}
}

// IDEMPOTENT PER CONVERSATION — the restart case, which is the whole reason the key is durable.
// A second claim must return the SAME endpoint, not a second one, or a chat session accumulates
// mailboxes it cannot read and its mail lands in whichever one it no longer holds.
func TestClaimingTwiceReturnsTheSameEndpoint(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	owner := Owner{TokenID: "tok-chat", ActorID: 7}

	first, created, err := st.ClaimEndpointForSession(ctx, owner, "conv-restart", "chat", "")
	if err != nil || !created {
		t.Fatalf("first claim: created=%v err=%v", created, err)
	}
	second, created2, err := st.ClaimEndpointForSession(ctx, owner, "conv-restart", "chat", "")
	if err != nil {
		t.Fatal(err)
	}
	if created2 {
		t.Error("the second claim CREATED another endpoint; a restarted conversation would lose " +
			"its channels and its queued mail")
	}
	if second.EndpointID != first.EndpointID {
		t.Errorf("came back as %q, want the same endpoint %q", second.EndpointID, first.EndpointID)
	}

	// CONTROL: a DIFFERENT conversation gets a DIFFERENT endpoint. Without this the test would
	// pass against an implementation that returned one endpoint to every caller.
	other, _, err := st.ClaimEndpointForSession(ctx, owner, "conv-other", "chat", "")
	if err != nil {
		t.Fatal(err)
	}
	if other.EndpointID == first.EndpointID {
		t.Error("two conversations share one endpoint — they would poll and ack each other's mail, " +
			"which is precisely what the secret was invented to prevent")
	}
}

// *** THE KEY DOES NOT CROSS TOKENS. ***
//
// The conversation key says WHICH conversation; the bearer says WHOSE estate. A key presented
// under a different token must be refused, or a leaked key becomes replayable from any account —
// which would make this strictly worse than the secret it replaces.
func TestAClaimedEndpointIsNotReachableFromAnotherToken(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	mine := Owner{TokenID: "tok-mine", ActorID: 1}
	theirs := Owner{TokenID: "tok-theirs", ActorID: 2}

	if _, _, err := st.ClaimEndpointForSession(ctx, mine, "conv-x", "mine", ""); err != nil {
		t.Fatal(err)
	}

	// CONTROL FIRST: the rightful owner can re-claim it, so the refusal below is about the TOKEN
	// and not about the claim being broken.
	if _, _, err := st.ClaimEndpointForSession(ctx, mine, "conv-x", "mine", ""); err != nil {
		t.Fatalf("the rightful owner could not re-claim: %v", err)
	}
	if _, _, err := st.ClaimEndpointForSession(ctx, theirs, "conv-x", "theirs", ""); !errors.Is(err, ErrDenied) {
		t.Errorf("another token claimed this conversation's endpoint (got %v) — a leaked key would "+
			"be replayable from any account", err)
	}
}

// A CLAIMED ENDPOINT CANNOT ALSO BE DRIVEN BY A GUESSED SECRET. The claim path discards the secret
// it generates rather than returning it, so the row holds a hash of a value nobody has. This
// asserts the consequence rather than the implementation: no secret opens it.
func TestAClaimedEndpointHasNoUsableSecret(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	ep, _, err := st.ClaimEndpointForSession(ctx, Owner{TokenID: "t", ActorID: 1}, "conv-nosecret", "x", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, guess := range []string{"", "secret", strings.Repeat("a", 40)} {
		if _, err := st.AuthenticateEndpoint(ctx, ep.EndpointID, guess); err == nil {
			t.Fatalf("a claimed endpoint authenticated with secret %q", guess)
		}
	}
}

// SECRETS KEEP WORKING. Every endpoint that exists predates this and is driven by one; production
// holds eight. Breaking them to remove an instruction would be the wrong trade, and this is the
// assertion that says so.
func TestAnUnclaimedEndpointStillAuthenticatesWithItsSecret(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	ep, secret, err := st.RegisterEndpoint(ctx, Owner{TokenID: "t", ActorID: 1}, "legacy", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AuthenticateEndpoint(ctx, ep.EndpointID, secret); err != nil {
		t.Fatalf("an endpoint registered the old way stopped working: %v", err)
	}
	// And it is NOT reachable by a key, because it never claimed one.
	if _, err := st.AuthenticateEndpointBySessionKey(ctx, "anything"); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unclaimed endpoint answered a session-key lookup: %v", err)
	}
}
