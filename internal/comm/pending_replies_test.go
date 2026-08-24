package comm

import (
	"context"
	"testing"
)

// *** ONE RECIPIENT REPLYING MUST NOT HIDE THE SILENT ONE. ***
//
// PendingReplies keyed on `m.answered_at IS NULL` — a message-level, any-recipient rollup —
// while the reply_overdue notice keys on `d.replied_by IS NULL` per delivery. So in a room of
// three, the moment one of two recipients answered, this returned ZERO while NoticesFor still
// named the other. Two surfaces answering the same question differently, and the one a sender
// would consult is the one that went quiet.
//
// It is the same failure notice.go:66 records mutation testing already catching in the
// expired arm — "one ack made two silences invisible" — reintroduced on the sender's side.
func TestPendingRepliesCountsPerRecipientNotPerMessage(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	sender := stationEndpoint(t, st, "tok-a", "st-alpha")
	one := stationEndpoint(t, st, "tok-b", "st-beta")
	two := stationEndpoint(t, st, "tok-c", "st-gamma")

	roomFixture(t, st, "r-ops", "s:st-alpha", "s:st-beta", "s:st-gamma")
	m, err := st.SendToRoom(ctx, sender, "r-ops", "please both confirm", SendOpts{RequiresResponse: true})
	if err != nil {
		t.Fatal(err)
	}

	// CONTROL: with nobody answering, the sender is owed one thing.
	if got, err := st.PendingReplies(ctx, sender, 0); err != nil || len(got) != 1 {
		t.Fatalf("before any reply: %d outstanding, err %v — want 1", len(got), err)
	}

	// ONE of the two answers.
	if _, err := st.Poll(ctx, one, 10); err != nil {
		t.Fatal(err)
	}
	// A room message belongs to no channel, so the reply goes back through the room.
	if _, err := st.SendToRoom(ctx, one, "r-ops", "confirmed", SendOpts{ReplyToMessageID: m.MessageID}); err != nil {
		t.Fatalf("reply from the first recipient: %v", err)
	}

	got, err := st.PendingReplies(ctx, sender, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("one of two recipients replied and the sender is now owed %d — the silent one vanished", len(got))
	}
	_ = two
}

// AND IT IS BOUNDED. It had no limit and then did one MessageByID per row. The obvious
// defence — MaxUnackedPerChannel, 64 — does not apply, because backpressure counts UN-ACKED
// and an ack is not a reply: a peer that polls and acks everything while answering nothing
// accumulates obligations under no ceiling at all. NoticesFor is capped at 25 for exactly
// this reason.
func TestPendingRepliesIsBounded(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	sender := stationEndpoint(t, st, "tok-a", "st-alpha")
	peer := stationEndpoint(t, st, "tok-b", "st-beta")
	linkFixture(t, st, [2]string{"st-alpha", "st-beta"})

	for i := 0; i < 30; i++ {
		if _, err := st.SendToStation(ctx, sender, "st-beta", "please confirm", SendOpts{RequiresResponse: true}); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	// The peer polls and ACKS everything without replying — which is what makes the
	// backpressure cap the wrong ceiling for this reading.
	msgs, err := st.Poll(ctx, peer, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		if _, err := st.Ack(ctx, peer, m.MessageID); err != nil {
			t.Fatal(err)
		}
	}

	got, err := st.PendingReplies(ctx, sender, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > 25 {
		t.Fatalf("returned %d outstanding requests with no limit asked for — the default bound is not applied", len(got))
	}
	// CONTROL: the bound is a CEILING, not a constant. A smaller explicit limit must be obeyed.
	small, err := st.PendingReplies(ctx, sender, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(small) != 5 {
		t.Fatalf("asked for 5 and got %d", len(small))
	}
}
