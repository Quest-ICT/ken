package comm

import (
	"context"
	"errors"
	"testing"
)

// stationEndpoint registers an endpoint and binds it to a station, which is what a room
// member IS — rooms hold posts, not connections.
func stationEndpoint(t *testing.T, st *Store, token, stationID string) *Endpoint {
	t.Helper()
	ctx := context.Background()
	ep, secret, err := st.RegisterEndpoint(ctx, owner(token), stationID, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.BindEndpointToStation(ctx, ep.EndpointID, stationID, "kens_k"); err != nil {
		t.Fatal(err)
	}
	bound, err := st.AuthenticateEndpoint(ctx, ep.EndpointID, secret)
	if err != nil {
		t.Fatal(err)
	}
	return bound
}

// roomFixture builds a room in the MIRROR directly. ken.db owns rooms and this package
// cannot reach it, which is the whole reason the mirror exists — so a comm-level test
// populates the projection, exactly as the boot-time rebuild does.
func roomFixture(t *testing.T, st *Store, roomID string, parties ...string) {
	t.Helper()
	rooms := map[string][]string{roomID: parties}
	if err := st.ReplaceRoomMirror(context.Background(), rooms, 1); err != nil {
		t.Fatal(err)
	}
}

// ONE BODY, N DELIVERIES — the shape of the whole feature.
//
// A fan-out that copied the body per recipient would multiply every size bound by the
// audience, so this asserts the storage shape and not merely that everyone got mail.
func TestARoomMessageIsStoredOnceAndDeliveredToEveryMember(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	sender := stationEndpoint(t, st, "tok-a", "st-alpha")
	roomFixture(t, st, "room1", "s:st-alpha", "s:st-beta", "s:st-gamma")

	m, err := st.SendToRoom(ctx, sender, "room1", "morning, all", SendOpts{})
	if err != nil {
		t.Fatal(err)
	}

	var messages, deliveries, bodies int
	if err := st.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM message`).Scan(&messages); err != nil {
		t.Fatal(err)
	}
	if err := st.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM delivery`).Scan(&deliveries); err != nil {
		t.Fatal(err)
	}
	if err := st.R.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM message WHERE body IS NOT NULL`).Scan(&bodies); err != nil {
		t.Fatal(err)
	}
	if messages != 1 || bodies != 1 {
		t.Fatalf("%d message rows and %d bodies for one broadcast — the body is being copied per recipient, "+
			"which multiplies every quota by the audience", messages, bodies)
	}
	if deliveries != 2 {
		t.Fatalf("%d delivery rows, want 2 — one per member EXCLUDING the sender", deliveries)
	}
	if m.Seq != 1 {
		t.Errorf("scope_seq = %d, want 1", m.Seq)
	}

	// The sender is not among its own recipients. Without this a broadcast would put
	// its author's own message back in their inbox on the next poll.
	var selfRows int
	if err := st.R.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM delivery WHERE party_key='s:st-alpha'`).Scan(&selfRows); err != nil {
		t.Fatal(err)
	}
	if selfRows != 0 {
		t.Error("the sender was delivered its own broadcast")
	}
}

// Each recipient's state is its OWN. One member reading or acking must not settle the
// message for anybody else — the property that makes N recipients meaningful rather
// than decorative.
func TestOneMemberActingDoesNotSettleTheMessageForOthers(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	sender := stationEndpoint(t, st, "tok-a", "st-alpha")
	beta := stationEndpoint(t, st, "tok-b", "st-beta")
	roomFixture(t, st, "room1", "s:st-alpha", "s:st-beta", "s:st-gamma")

	m, err := st.SendToRoom(ctx, sender, "room1", "please look", SendOpts{RequiresResponse: true})
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.Poll(ctx, beta, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Body != "please look" {
		t.Fatalf("beta polled %+v, want the room message", got)
	}
	if err := st.Ack(ctx, beta, m.MessageID); err != nil {
		t.Fatal(err)
	}

	var betaState, gammaState string
	if err := st.R.QueryRowContext(ctx,
		`SELECT state FROM delivery WHERE party_key='s:st-beta'`).Scan(&betaState); err != nil {
		t.Fatal(err)
	}
	if err := st.R.QueryRowContext(ctx,
		`SELECT state FROM delivery WHERE party_key='s:st-gamma'`).Scan(&gammaState); err != nil {
		t.Fatal(err)
	}
	if betaState != "acked" {
		t.Errorf("the reader's own delivery is %q, want acked", betaState)
	}
	if gammaState != "queued" {
		t.Fatalf("a second member's delivery is %q after somebody ELSE acked — one recipient "+
			"settled the message for a station that has not seen it", gammaState)
	}

	// And the body survives, because somebody is still owed it. Blanking on the first
	// ack is the 97%-of-bodies defect rebuilt from a new cause.
	var body *string
	if err := st.R.QueryRowContext(ctx, `SELECT body FROM message WHERE message_id=?`, m.MessageID).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if body == nil {
		t.Fatal("the body was destroyed when the FIRST recipient acked — every member who had not read it yet lost the text")
	}
}

// Membership IS the authorization, and the two ways to fail it must be told apart: a
// station removed from a room needs to learn it was removed, not that the room is
// missing, or it goes looking for a typo.
func TestOnlyMembersMaySendAndTheRefusalsAreDistinct(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	outsider := stationEndpoint(t, st, "tok-x", "st-outsider")
	roomFixture(t, st, "room1", "s:st-alpha", "s:st-beta")

	_, err := st.SendToRoom(ctx, outsider, "room1", "let me in", SendOpts{})
	if !errors.Is(err, ErrNotInRoom) {
		t.Fatalf("a non-member sending to a populated room got %v, want ErrNotInRoom", err)
	}
	_, err = st.SendToRoom(ctx, outsider, "no-such-room", "hello?", SendOpts{})
	if !errors.Is(err, ErrRoomEmpty) {
		t.Fatalf("sending to an unknown room got %v, want ErrRoomEmpty", err)
	}

	// CONTROL: a member CAN send, so the refusals above are about membership rather
	// than about room sends being broken.
	alpha := stationEndpoint(t, st, "tok-a", "st-alpha")
	if _, err := st.SendToRoom(ctx, alpha, "room1", "hello", SendOpts{}); err != nil {
		t.Fatalf("a member could not send to its own room: %v", err)
	}
}

// A room with one member other than the sender is fine; a room with NONE is refused
// rather than silently delivered to nobody. A send that succeeds and reaches no one is
// the outcome hardest to notice: the sender gets a message_id and nothing says the
// audience was zero.
func TestSendingToARoomWithNoOtherMembersIsRefused(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	alone := stationEndpoint(t, st, "tok-a", "st-alpha")
	roomFixture(t, st, "empty", "s:st-alpha")

	if _, err := st.SendToRoom(ctx, alone, "empty", "anyone?", SendOpts{}); !errors.Is(err, ErrRoomEmpty) {
		t.Fatalf("sending into a room with no other members got %v, want ErrRoomEmpty", err)
	}
	var n int
	if err := st.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM message`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d message rows written for a refused send — the refusal is not atomic", n)
	}
}

// The mirror is a CACHE and must be replaceable wholesale without drifting. An
// incremental sync that missed a removal would leave a station able to send to a room a
// human took it out of — the failure that fails OPEN.
func TestReplacingTheMirrorRemovesMembershipsThatAreGone(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	beta := stationEndpoint(t, st, "tok-b", "st-beta")
	roomFixture(t, st, "room1", "s:st-alpha", "s:st-beta")

	if _, err := st.SendToRoom(ctx, beta, "room1", "still here", SendOpts{}); err != nil {
		t.Fatal(err)
	}
	// The human removes beta.
	if err := st.ReplaceRoomMirror(ctx, map[string][]string{"room1": {"s:st-alpha"}}, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SendToRoom(ctx, beta, "room1", "and now?", SendOpts{}); !errors.Is(err, ErrNotInRoom) {
		t.Fatalf("a removed station could still send: %v", err)
	}
	epoch, err := st.MirrorEpoch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if epoch != 2 {
		t.Errorf("mirror epoch = %d, want 2 — a projection that cannot say which generation it holds cannot be known to be stale", epoch)
	}
}
