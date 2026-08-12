package comm

import (
	"context"
	"testing"
)

// A session must be able to see what is waiting WITHOUT taking delivery.
//
// This is what makes "check before you send" followable. comm_poll is the only other
// way to find out and it hands you the messages: it stamps first_delivered_at, arms the
// expiry and reply-deadline clocks, and leaves you on the hook to handle what you were
// only trying to peek at. An instruction that costs a delivery every time you send gets
// skipped, and a skipped instruction is worse than none because it still looks like a
// control.
//
// The property under test is the ABSENCE of a side effect, which is the kind that
// silently stops holding — so the message state is read before and after and required
// to be identical.
func TestPendingCountDoesNotDeliverAnything(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	a, b, ch := pair(t, st)

	if _, err := st.Send(ctx, b, ch, "waiting for you", SendOpts{}); err != nil {
		t.Fatal(err)
	}

	var beforeState string
	var beforeDelivered any
	if err := st.R.QueryRow(
		`SELECT d.state, d.first_delivered_at FROM delivery d ORDER BY d.id DESC LIMIT 1`).Scan(&beforeState, &beforeDelivered); err != nil {
		t.Fatal(err)
	}
	if beforeState != "queued" {
		t.Fatalf("setup: message is %q, want queued", beforeState)
	}

	pending, err := st.PendingForEndpoint(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if pending[ch] != 1 {
		t.Fatalf("pending on %s = %d, want 1 — the count is the whole reason this exists", ch, pending[ch])
	}

	var afterState string
	var afterDelivered any
	if err := st.R.QueryRow(
		`SELECT d.state, d.first_delivered_at FROM delivery d ORDER BY d.id DESC LIMIT 1`).Scan(&afterState, &afterDelivered); err != nil {
		t.Fatal(err)
	}
	if afterState != beforeState {
		t.Errorf("counting changed the message state from %q to %q — the check took delivery, which is exactly what it exists to avoid", beforeState, afterState)
	}
	if afterDelivered != nil {
		t.Error("counting stamped first_delivered_at — the expiry and reply-deadline clocks are now running on a message nobody has read")
	}
}

// Counts QUEUED only. A delivered-but-unacked message has already been shown to this
// session, so counting it would tell them to go and read something they have — the same
// mistake waiting_for_you made when it fired on mail its recipient had already replied
// to.
func TestPendingIgnoresMailAlreadyShownToYou(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	a, b, ch := pair(t, st)

	if _, err := st.Send(ctx, b, ch, "one", SendOpts{}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Poll(ctx, a, 10); err != nil { // delivered, deliberately not acked
		t.Fatal(err)
	}

	pending, err := st.PendingForEndpoint(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if pending[ch] != 0 {
		t.Fatalf("pending = %d after polling — a message this session has already been handed is not waiting to be read", pending[ch])
	}

	// CONTROL: a genuinely new message still counts, so the zero above is about the
	// delivered state and not about the count being broken.
	if _, err := st.Send(ctx, b, ch, "two", SendOpts{}); err != nil {
		t.Fatal(err)
	}
	pending, err = st.PendingForEndpoint(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if pending[ch] != 1 {
		t.Fatalf("pending = %d after a fresh send — the count is not seeing new mail at all", pending[ch])
	}
}

// Your own outgoing mail is not waiting for you. Without this the count would tell a
// sender to go and read what they just wrote.
func TestPendingDoesNotCountYourOwnSends(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	a, _, ch := pair(t, st)

	if _, err := st.Send(ctx, a, ch, "mine", SendOpts{}); err != nil {
		t.Fatal(err)
	}
	pending, err := st.PendingForEndpoint(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if pending[ch] != 0 {
		t.Fatalf("pending = %d on a channel where the only message is my own — the sender is being told to read their own mail", pending[ch])
	}
}
