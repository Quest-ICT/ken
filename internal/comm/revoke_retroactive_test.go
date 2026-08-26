package comm

import (
	"context"
	"database/sql"
	"testing"
)

// Revoking a channel must stop mail that is ALREADY QUEUED on it, not merely refuse new
// sends. That property is held by THREE hand-mirrored SQL predicates — Poll's, the
// waiting_for_you probe's, and the one behind every pending counter — and until this file
// the only test asserted that a SEND fails afterwards, which none of the three touch.
//
// Three copies of a clause is three chances to edit two of them. The counter predicate is
// the one that hides: a console reporting mail a poll will not hand over is a drift a human
// notices long before a test does, because nothing fails.
//
// EVERY ASSERTION HERE IS TWO-SIDED. The revoked arm expects zero, and zero is also what a
// test observes when the setup never queued anything, when the endpoint is wrong, or when a
// predicate went from "wrong" to "matches nothing" — so a one-sided test of a revocation
// passes just as loudly while asserting nothing at all. The control arm runs the identical
// assertions against a store where the revocation did NOT happen, and demands one.

// queuedFor calls the unexported waiting_for_you probe directly.
//
// The exported route to it is the WaitingForYou field on a Send result, which cannot reach
// the revoked arm: sending on a revoked channel fails before the probe runs. Calling it
// in-package is what lets the same assertion stand on both sides of the control.
func queuedFor(t *testing.T, st *Store, ep *Endpoint) int {
	t.Helper()
	ctx := context.Background()
	var n int
	err := st.tx(ctx, func(tx *sql.Tx) error {
		var err error
		n, err = queuedForEndpoint(ctx, tx, ep)
		return err
	})
	if err != nil {
		t.Fatalf("queuedForEndpoint: %v", err)
	}
	return n
}

func TestRevokingAChannelRetroactivelyStopsMailAlreadyQueued(t *testing.T) {
	for _, arm := range []struct {
		name   string
		revoke bool
		want   int
	}{
		{"control: channel left open", false, 1},
		{"channel revoked after the message was queued", true, 0},
	} {
		t.Run(arm.name, func(t *testing.T) {
			ctx := context.Background()
			st := newStore(t, DefaultLimits())
			a, b, channelID := pair(t, st)

			if _, err := st.Send(ctx, a, channelID, "queued before any revocation", SendOpts{}); err != nil {
				t.Fatalf("send: %v", err)
			}
			if arm.revoke {
				if err := st.RevokeChannel(ctx, channelID); err != nil {
					t.Fatalf("revoke: %v", err)
				}
			}

			// pending.go's shared predicate, through both of its callers.
			total, err := st.PendingTotalFor(ctx, b)
			if err != nil {
				t.Fatalf("PendingTotalFor: %v", err)
			}
			if total != arm.want {
				t.Errorf("PendingTotalFor = %d, want %d", total, arm.want)
			}
			perChannel, err := st.PendingForEndpoint(ctx, b)
			if err != nil {
				t.Fatalf("PendingForEndpoint: %v", err)
			}
			if got := perChannel[channelID]; got != arm.want {
				t.Errorf("PendingForEndpoint[%s] = %d, want %d", channelID, got, arm.want)
			}

			// message.go's waiting_for_you probe, which is a fourth copy of the clause
			// and not covered by the counters above.
			if got := queuedFor(t, st, b); got != arm.want {
				t.Errorf("queuedForEndpoint = %d, want %d", got, arm.want)
			}

			// Poll LAST: it is the only reader here that mutates delivery state, so
			// running it earlier would drain the rows the counters are meant to see.
			got, err := st.Poll(ctx, b, 10)
			if err != nil {
				t.Fatalf("poll: %v", err)
			}
			if len(got) != arm.want {
				t.Errorf("Poll returned %d messages, want %d", len(got), arm.want)
			}
		})
	}
}

// The same three predicates carry a second clause that is easy to lose: room and broadcast
// mail have channel_id NULL, and an INNER JOIN to channel drops every such row. That is not
// hypothetical — it is the exact mistake that made the console blind to room traffic, and
// pending.go's own comment records it.
//
// Revoking a channel is the situation where the two clauses meet: the join must exclude the
// revoked channel's mail while still passing rows that have no channel at all. A test that
// only revokes a channel between two endpoints in no room cannot tell a correct LEFT JOIN
// from an INNER one, because both return zero.
func TestRevokingAChannelLeavesRoomMailUntouched(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, DefaultLimits())

	sender := stationEndpoint(t, st, "tok-a", "st-alpha")
	recipient := stationEndpoint(t, st, "tok-b", "st-beta")
	roomFixture(t, st, "room1", "s:st-alpha", "s:st-beta")

	// A channel between the SAME two stations, so the revocation below is genuinely
	// adjacent to the room mail rather than in an unrelated corner of the database.
	code, err := st.MintPairingCode(ctx, 42, "alpha<->beta")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if _, err := st.JoinChannel(ctx, sender, code); err != nil {
		t.Fatalf("join sender: %v", err)
	}
	ch, err := st.JoinChannel(ctx, recipient, code)
	if err != nil {
		t.Fatalf("join recipient: %v", err)
	}
	if !ch.Open() {
		t.Fatalf("channel not open: state=%q", ch.State)
	}

	if _, err := st.Send(ctx, sender, ch.ChannelID, "on the channel", SendOpts{}); err != nil {
		t.Fatalf("send on channel: %v", err)
	}
	if _, err := st.SendToRoom(ctx, sender, "room1", "in the room", SendOpts{}); err != nil {
		t.Fatalf("send to room: %v", err)
	}

	// Control: both are waiting before anything is revoked. Without this, the assertion
	// below cannot tell "the room message survived" from "the room message never existed".
	if got := mustPendingTotal(t, st, recipient); got != 2 {
		t.Fatalf("before revocation: PendingTotalFor = %d, want 2 (one channel, one room)", got)
	}

	if err := st.RevokeChannel(ctx, ch.ChannelID); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	// Exactly one survives, and it must be the ROOM one.
	if got := mustPendingTotal(t, st, recipient); got != 1 {
		t.Errorf("after revocation: PendingTotalFor = %d, want 1 (the room message)", got)
	}
	if got := queuedFor(t, st, recipient); got != 1 {
		t.Errorf("after revocation: queuedForEndpoint = %d, want 1 (the room message)", got)
	}
	if got := mustPendingTotal(t, st, recipient); got == 0 {
		t.Error("room mail vanished with the channel — the join to channel is excluding " +
			"rows whose channel_id is NULL, which is the console-blind-to-rooms defect")
	}

	got, err := st.Poll(ctx, recipient, 10)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Poll returned %d messages, want 1", len(got))
	}
	if got[0].Body != "in the room" {
		t.Errorf("Poll returned %q — the surviving message must be the room one, not the "+
			"revoked channel's", got[0].Body)
	}
}

func mustPendingTotal(t *testing.T, st *Store, ep *Endpoint) int {
	t.Helper()
	n, err := st.PendingTotalFor(context.Background(), ep)
	if err != nil {
		t.Fatalf("PendingTotalFor: %v", err)
	}
	return n
}
