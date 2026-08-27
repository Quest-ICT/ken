package comm

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// *** A REVOKED, UNBOUND SEAT IS A BLACK HOLE, AND THE SEND USED TO SUCCEED INTO IT. ***
//
// ken-prod-ops reported this class and then RETRACTED it, because the incident it inferred from
// turned out to be ordinary poll latency. It retracted the mechanism along with the incident, and
// only the incident was wrong. Measured before the fix:
//
//	send SUCCEEDED  recipients=1  state=queued
//	channel state after peer revocation: "open"   (and comm_channels showed open=true)
//	delivery filed under party "e:2" for endpoint rowid 2
//	AuthenticateEndpoint(that endpoint) -> denied, permanently: nothing anywhere clears revoked_at
//
// So a permanently undeliverable send rendered identically to a healthy one, and the only
// correction was an `expired` notice at the undelivered TTL — 30 days on shipped defaults.
func TestSendingToARevokedUnboundPeerIsRefused(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, DefaultLimits())
	a, b, chID := pair(t, st)

	// CONTROL FIRST: the channel carries mail normally. Without this the refusal below could be
	// any breakage at all rather than the one under test.
	if _, err := st.Send(ctx, a, chID, "before", SendOpts{}); err != nil {
		t.Fatalf("the channel did not work before revocation: %v", err)
	}

	if err := st.RevokeEndpoint(ctx, b.EndpointID); err != nil {
		t.Fatal(err)
	}
	_, err := st.Send(ctx, a, chID, "into the void", SendOpts{})
	if err == nil {
		t.Fatal("the send SUCCEEDED into a seat nobody can ever hold — the sender gets a green " +
			"light and the mail is filed under a rowid that can never authenticate again")
	}
	if !errors.Is(err, ErrChannelClosed) {
		t.Errorf("refusal is %v, want it to wrap ErrChannelClosed — every caller already handles that", err)
	}
	// IT MUST REACH THE CALLER. This project has shipped a correct refusal that arrived as the
	// literal "not found" because it was not CallerSafe; the string in the binary is not the test.
	if !strings.Contains(err.Error(), "revoked") || !strings.Contains(err.Error(), "to_station") {
		t.Errorf("the refusal does not tell the sender what happened or what to do instead: %q", err)
	}
}

// *** THE HALF THAT MUST NOT BREAK: A REVOKED **BOUND** PEER STILL WORKS. ***
//
// Its mail is filed under `s:<station>`, and a successor endpoint on that station collects it —
// that is the successor inheritance the station model exists for. Gating on revocation ALONE would
// destroy it, which is why the gate tests revoked AND unbound. This test is the reason the fix is
// six lines instead of one.
func TestSendingToARevokedBoundPeerStillWorksAndASuccessorReadsIt(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, DefaultLimits())

	a, _, err := st.RegisterEndpoint(ctx, owner("tok-a"), "dev", "")
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := st.RegisterEndpoint(ctx, owner("tok-b"), "prod", "")
	if err != nil {
		t.Fatal(err)
	}
	a = bindEndpoint(t, st, a, "stn_dev", "kens_a")
	b = bindEndpoint(t, st, b, "stn_prod", "kens_b")

	ch, err := st.OpenLinkedChannel(ctx, a, b, 42, "dev <-> prod")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RevokeEndpoint(ctx, b.EndpointID); err != nil {
		t.Fatal(err)
	}

	if _, err := st.Send(ctx, a, ch.ChannelID, "successor should get this", SendOpts{}); err != nil {
		t.Fatalf("a revoked BOUND peer refused the send: %v — this breaks successor inheritance, "+
			"where a human retires one session's endpoint and the next session on that post picks "+
			"up its mail", err)
	}

	// THE PROOF IS THE COLLECTION, not the acceptance: mail addressed to a party nobody can hold
	// would also have been "accepted".
	successor, _, err := st.RegisterEndpoint(ctx, owner("tok-b2"), "prod-successor", "")
	if err != nil {
		t.Fatal(err)
	}
	successor = bindEndpoint(t, st, successor, "stn_prod", "kens_b2")
	got, err := st.Poll(ctx, successor, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Body != "successor should get this" {
		t.Fatalf("the successor collected %d messages, want the one sent after its predecessor was "+
			"revoked", len(got))
	}
}

// AN OFFLINE PEER IS NOT A DEAD ONE, and nothing here may confuse them. Unread-and-queued is the
// NORMAL state of this transport (documented median 11 min, p90 144 min); a check that fired on it
// would train senders to ignore the signal and put the real case back in the dark.
func TestSendingToAnIdlePeerIsNotRefused(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, DefaultLimits())
	a, b, chID := pair(t, st)

	// Not revoked, never polled, and deliberately not touched — the ordinary case.
	if _, err := st.Send(ctx, a, chID, "read this whenever", SendOpts{}); err != nil {
		t.Fatalf("a send to a peer that simply has not polled was refused: %v", err)
	}
	// And it is genuinely there for them.
	got, err := st.Poll(ctx, b, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("the idle peer collected %d messages, want 1", len(got))
	}
}

// *** THE REVOKED SEAT CANNOT SEND EITHER — BUT THE GATE FOR THAT IS AUTHENTICATION, NOT THIS. ***
//
// Written first as "a revoked endpoint cannot use its channel" against Send, and it FAILED: at the
// store layer a revoked *Endpoint value still sends. That is not a hole, and the distinction is
// worth pinning rather than papering over. Send takes an ALREADY-AUTHENTICATED endpoint; the
// refusal lives in AuthenticateEndpoint and its session-key counterpart, so no real caller can
// obtain the value this test would need. Adding a second revocation check inside ChannelFor would
// be two predicates that can disagree about the same fact — the failure mode this file's own gate
// comment argues against.
//
// So the assertion is on the layer that actually holds the line, both ways in.
func TestARevokedEndpointCannotAuthenticateByEitherPath(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, DefaultLimits())

	ep, secret, err := st.RegisterEndpoint(ctx, owner("tok-a"), "dev", "")
	if err != nil {
		t.Fatal(err)
	}
	// CONTROL: the credential works before revocation, so the denial below is about revocation
	// and not about a wrong secret.
	if _, err := st.AuthenticateEndpoint(ctx, ep.EndpointID, secret); err != nil {
		t.Fatalf("a live endpoint could not authenticate: %v", err)
	}
	claimed, _, err := st.ClaimEndpointForSession(ctx, Owner{TokenID: "tok-k", ActorID: 1}, "conv-k", "chat", "")
	if err != nil {
		t.Fatal(err)
	}

	if err := st.RevokeEndpoint(ctx, ep.EndpointID); err != nil {
		t.Fatal(err)
	}
	if err := st.RevokeEndpoint(ctx, claimed.EndpointID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AuthenticateEndpoint(ctx, ep.EndpointID, secret); !errors.Is(err, ErrDenied) {
		t.Errorf("a revoked endpoint authenticated with its secret: %v", err)
	}
	if _, err := st.AuthenticateEndpointBySessionKey(ctx, "conv-k"); err == nil {
		t.Error("a revoked endpoint authenticated by its conversation key — the secret-free path " +
			"must honour revocation exactly as the secret path does")
	}
}

// *** AN IDEMPOTENT REPLAY REPORTED recipients: 0 FOR A MESSAGE THAT HAS A DELIVERY ROW. ***
//
// The count is assigned after the insert; the replay branch returns before reaching it. A sender
// reading 0 as "it reached nobody" would resend under a new key — which is exactly what the
// idempotency key exists to prevent, so the bug attacked the feature it lived inside.
func TestAnIdempotentReplayReportsTheSameRecipientCount(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, DefaultLimits())
	a, _, chID := pair(t, st)

	first, err := st.Send(ctx, a, chID, "one", SendOpts{IdempotencyKey: "k1"})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := st.Send(ctx, a, chID, "one", SendOpts{IdempotencyKey: "k1"})
	if err != nil {
		t.Fatal(err)
	}
	if replay.MessageID != first.MessageID {
		t.Fatalf("the replay sent a SECOND message (%s vs %s)", replay.MessageID, first.MessageID)
	}
	if replay.Recipients != first.Recipients {
		t.Errorf("the replay reports recipients=%d against the original's %d — a sender reading 0 "+
			"as 'it reached nobody' resends under a new key, defeating the key",
			replay.Recipients, first.Recipients)
	}
}
