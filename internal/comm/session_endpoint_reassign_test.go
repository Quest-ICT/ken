package comm

import (
	"context"
	"errors"
	"testing"
)

// *** RECOVERING A WORKSPACE WITHOUT ITS MAILBOX IS HALF A RECOVERY. ***
//
// A session that takes over an abandoned workspace inherits the station's bound endpoints — and
// could not open them. A claimed one is driven by the DEAD conversation's key; an unclaimed one by
// a secret shown once to a session that is gone. The only existing answer was rotate, which hands
// the session a secret to write to disk, and a claude.ai chat has no disk.
//
// The assertion is the recovery as the session experiences it: after the human reassigns, the key
// AUTHENTICATES the mailbox. A test that only checked the column would pass while the auth path
// still refused, which is the entire failure.
func TestReassigningAnEndpointLetsANewConversationDriveIt(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	owner := Owner{TokenID: "tok", ActorID: 7}

	abandoned, _, err := st.ClaimEndpointForSession(ctx, owner, "conv-dead", "old laptop", "")
	if err != nil {
		t.Fatal(err)
	}

	// CONTROL FIRST: the new conversation cannot reach it before the human acts. Without this the
	// assertion below would pass against an implementation with no ownership rule at all.
	if _, err := st.AuthenticateEndpointBySessionKey(ctx, "conv-new"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a mailbox answered an unclaimed key (%v) — nothing here is being enforced", err)
	}

	res, err := st.ReassignEndpointToSession(ctx, abandoned.EndpointID, "conv-new")
	if err != nil {
		t.Fatal(err)
	}
	if res.Endpoint.EndpointID != abandoned.EndpointID {
		t.Fatalf("reassign returned %q, want the endpoint asked for", res.Endpoint.EndpointID)
	}

	got, err := st.AuthenticateEndpointBySessionKey(ctx, "conv-new")
	if err != nil {
		t.Fatalf("the recovered mailbox does not authenticate by its new key: %v", err)
	}
	if got.EndpointID != abandoned.EndpointID {
		t.Errorf("the key drives %q, want the recovered mailbox %q", got.EndpointID, abandoned.EndpointID)
	}
	// And the dead conversation's key no longer opens it — two conversations on one mailbox would
	// have them polling and acking each other's mail, which is what the secret existed to prevent.
	if _, err := st.AuthenticateEndpointBySessionKey(ctx, "conv-dead"); !errors.Is(err, ErrNotFound) {
		t.Errorf("the old conversation key still drives the mailbox: %v", err)
	}
}

// THE QUEUED MAIL IS THE POINT. Reassignment that dropped the inbox would be a rotate with extra
// steps — what makes it a recovery is that the message the peer sent yesterday is still there.
func TestAReassignedEndpointKeepsItsQueuedMail(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	owner := Owner{TokenID: "tok", ActorID: 7}

	a, _, err := st.ClaimEndpointForSession(ctx, owner, "conv-sender", "sender", "")
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := st.ClaimEndpointForSession(ctx, owner, "conv-dead", "receiver", "")
	if err != nil {
		t.Fatal(err)
	}
	code, err := st.MintPairingCode(ctx, 42, "sender<->receiver")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.JoinChannel(ctx, a, code); err != nil {
		t.Fatal(err)
	}
	ch, err := st.JoinChannel(ctx, b, code)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Send(ctx, a, ch.ChannelID, "still here after the takeover", SendOpts{}); err != nil {
		t.Fatal(err)
	}

	if _, err := st.ReassignEndpointToSession(ctx, b.EndpointID, "conv-took-over"); err != nil {
		t.Fatal(err)
	}
	ep, err := st.AuthenticateEndpointBySessionKey(ctx, "conv-took-over")
	if err != nil {
		t.Fatal(err)
	}
	msgs, err := st.Poll(ctx, ep, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Body != "still here after the takeover" {
		t.Fatalf("the recovered mailbox delivered %d messages, want the one waiting in it — a "+
			"recovery that loses the queue is a rotation with extra steps", len(msgs))
	}
}

// THE KEY IS TAKEN FROM WHOEVER HOLDS IT AND THE DISPLACEMENT IS REPORTED — the same ruling as the
// station form, because it is the same main path: a chat session asked for its key has usually
// already claimed a fresh empty mailbox under it.
func TestReassigningTakesTheKeyAndNamesWhatItTookItFrom(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	owner := Owner{TokenID: "tok", ActorID: 7}

	abandoned, _, err := st.ClaimEndpointForSession(ctx, owner, "conv-dead", "old laptop", "")
	if err != nil {
		t.Fatal(err)
	}
	fresh, _, err := st.ClaimEndpointForSession(ctx, owner, "conv-new", "chat", "")
	if err != nil {
		t.Fatal(err)
	}

	res, err := st.ReassignEndpointToSession(ctx, abandoned.EndpointID, "conv-new")
	if err != nil {
		t.Fatalf("the main recovery path was refused: %v", err)
	}
	if res.TakenFromID != fresh.EndpointID {
		t.Errorf("the result says the key came from %q, want %q — an unreported displacement is a "+
			"silent steal", res.TakenFromID, fresh.EndpointID)
	}
	got, err := st.AuthenticateEndpointBySessionKey(ctx, "conv-new")
	if err != nil {
		t.Fatal(err)
	}
	if got.EndpointID != abandoned.EndpointID {
		t.Errorf("the key still drives %q, want the mailbox it was moved to", got.EndpointID)
	}
}

// AN EMPTY KEY RELEASES, so a mailbox pointed at the wrong conversation is not stuck to it — and
// the released mailbox must stop answering that key, or the release is cosmetic.
func TestReassigningAnEndpointWithAnEmptyKeyReleasesIt(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	ep, _, err := st.ClaimEndpointForSession(ctx, Owner{TokenID: "tok", ActorID: 7}, "conv-holder", "held", "")
	if err != nil {
		t.Fatal(err)
	}
	res, err := st.ReassignEndpointToSession(ctx, ep.EndpointID, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Endpoint.SessionKey != "" {
		t.Errorf("the mailbox still answers to %q after release", res.Endpoint.SessionKey)
	}
	if _, err := st.AuthenticateEndpointBySessionKey(ctx, "conv-holder"); !errors.Is(err, ErrNotFound) {
		t.Errorf("the released key still drives the mailbox: %v", err)
	}
}

// A REVOKED MAILBOX STAYS REVOKED. Revocation is how a human shuts a session out; re-staffing one
// from this form would make it advisory, and the operator would have no way to explain a revoked
// endpoint reading mail.
func TestReassigningARevokedEndpointIsRefused(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	ep, _, err := st.ClaimEndpointForSession(ctx, Owner{TokenID: "tok", ActorID: 7}, "conv-dead", "old", "")
	if err != nil {
		t.Fatal(err)
	}
	// CONTROL: reassignable BEFORE revocation, so the refusal below is about the revocation.
	if _, err := st.ReassignEndpointToSession(ctx, ep.EndpointID, "conv-x"); err != nil {
		t.Fatalf("a live endpoint refused a reassign: %v", err)
	}
	if err := st.RevokeEndpoint(ctx, ep.EndpointID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReassignEndpointToSession(ctx, ep.EndpointID, "conv-y"); err == nil {
		t.Error("a REVOKED mailbox accepted a reassign — revocation would be advisory")
	}
}
