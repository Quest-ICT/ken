package comm

import (
	"context"
	"testing"
)

// fixtureActor is the actor owner() mints every test endpoint under — one actor per
// machine, which is the shape the marker keys on and the reason it is estate-wide.
const fixtureActor = 7

// A BROADCAST AND A DIRECT MESSAGE MUST NOT LOOK THE SAME TO A CURATOR.
//
// Before rooms, every message had one recipient, so "this actor received something"
// meant somebody addressed it. One send to a nine-station room now marks nine actors,
// so the marker fires far more often while meaning far less — and ken-prod-ops had
// already measured the badge as nearly always on before rooms existed, naming the
// consequence exactly: a curator learns to ignore it.
func TestDirectedAndBroadcastTrafficAreDistinguishable(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()

	// Two endpoints under ONE actor, which is the ordinary shape: owner() hands out
	// actor 7 to everything, exactly as one machine's tokens do on a real deployment.
	alpha := stationEndpoint(t, st, "tok-a", "st-alpha")
	beta := stationEndpoint(t, st, "tok-b", "st-beta")
	if err := st.ReplaceRoomMirror(ctx, map[string][]string{
		"ops": {"s:st-alpha", "s:st-beta", "s:st-gamma"},
	}, 1); err != nil {
		t.Fatal(err)
	}

	// A room message, polled by beta so it registers as received.
	if _, err := st.SendToRoom(ctx, alpha, "ops", "anyone seen the logs", SendOpts{}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Poll(ctx, beta, 10); err != nil {
		t.Fatal(err)
	}

	srcs, err := st.ReceivedFrom(ctx, fixtureActor, 3600)
	if err != nil {
		t.Fatal(err)
	}
	if len(srcs) == 0 {
		t.Fatal("a room message registered as no traffic at all — the marker is blind to rooms")
	}
	if !srcs[0].Broadcast {
		t.Fatalf("a room message with three members is reported as directed: %+v.\n"+
			"One send marks every member, so treating it as a direct message makes the badge fire "+
			"constantly and mean nothing.", srcs[0])
	}

	// Now a DIRECTED message on a channel, which must outrank the broadcast.
	a2, b2, chID := pair(t, st)
	if _, err := st.Send(ctx, a2, chID, "just for you", SendOpts{}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Poll(ctx, b2, 10); err != nil {
		t.Fatal(err)
	}
	srcs, err = st.ReceivedFrom(ctx, fixtureActor, 3600)
	if err != nil {
		t.Fatal(err)
	}
	if len(srcs) < 2 {
		t.Fatalf("expected both the broadcast and the directed message, got %d", len(srcs))
	}
	if srcs[0].Broadcast {
		t.Fatalf("the strongest source is reported as a broadcast while a DIRECTED message exists.\n" +
			"A curator reading the badge gets the weakest reason first, which is the reason they will discount.")
	}

	// CONTROL: the boolean still answers what it always answered, so nothing that reads
	// the old signal changed meaning underneath it.
	got, err := st.ReceivedSince(ctx, fixtureActor, 3600)
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Error("ReceivedSince stopped reporting traffic it used to report")
	}
}

// A quiet actor is unmarked, and an empty window means "no signal" rather than "clean".
func TestAnActorWithNoRecentTrafficHasNoSources(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	// A channel exists and nobody sends anything on it. The endpoints are real, the
	// actor is real, and there is simply no traffic — which must read as no signal.
	pair(t, st)

	srcs, err := st.ReceivedFrom(ctx, fixtureActor, 3600)
	if err != nil {
		t.Fatal(err)
	}
	if len(srcs) != 0 {
		t.Fatalf("an actor that received nothing has %d sources", len(srcs))
	}
	// A zero window is "the feature is off", and must report nothing rather than
	// everything — a disabled marker that marked every entry would be worse than none.
	srcs, err = st.ReceivedFrom(ctx, fixtureActor, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(srcs) != 0 {
		t.Fatalf("a zero window returned %d sources", len(srcs))
	}
}
