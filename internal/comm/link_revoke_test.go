package comm

import (
	"context"
	"testing"
)

// Revoking a relationship must end its LIVE TRAFFIC, not merely its permission.
//
// A channel opened while a link held carries its own state, so withdrawing the link
// alone leaves the conversation working — a revocation that revokes nothing
// observable. This is the operator brake a durable roster needs before it can replace
// a pairing code: a membership list nobody can take away is not a stronger gate than
// a bearer code, it is a weaker one that lasts longer.
//
// The control that makes this test worth running is the THIRD station: a targeted
// revoke that also severed unrelated traffic would pass every assertion about the
// pair and be catastrophic in use.
func TestRevokeChannelsBetweenStationsEndsOnlyThatPair(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()

	mk := func(tok, label, station, key string) *Endpoint {
		t.Helper()
		ep, _, err := st.RegisterEndpoint(ctx, owner(tok), label, "")
		if err != nil {
			t.Fatal(err)
		}
		return bindEndpoint(t, st, ep, station, key)
	}
	dev := mk("tok-dev", "dev", "stn_dev", "kens_dev")
	prod := mk("tok-prod", "prod", "stn_prod", "kens_prod")
	infra := mk("tok-infra", "infra", "stn_infra", "kens_infra")

	devProd, err := st.OpenLinkedChannel(ctx, dev, prod, 1, "dev <-> prod")
	if err != nil {
		t.Fatal(err)
	}
	devInfra, err := st.OpenLinkedChannel(ctx, dev, infra, 1, "dev <-> infra")
	if err != nil {
		t.Fatal(err)
	}

	// The count is shown to a human BEFORE the click, so it has to be right, and it
	// has to be right in both column orders — which seat a station took is an
	// accident of who opened the channel.
	for _, o := range []struct{ a, b string }{{"stn_dev", "stn_prod"}, {"stn_prod", "stn_dev"}} {
		n, err := st.CountOpenChannelsBetweenStations(ctx, o.a, o.b)
		if err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("CountOpenChannelsBetweenStations(%s,%s) = %d, want 1 — the blast radius shown to the operator is wrong in this column order", o.a, o.b, n)
		}
	}

	closed, err := st.RevokeChannelsBetweenStations(ctx, "stn_dev", "stn_prod")
	if err != nil {
		t.Fatal(err)
	}
	if closed != 1 {
		t.Fatalf("revoke closed %d channel(s), want 1", closed)
	}

	// The pair's traffic is genuinely dead, not merely flagged.
	if _, err := st.Send(ctx, dev, devProd.ChannelID, "after revoke", SendOpts{}); err == nil {
		t.Fatal("a send SUCCEEDED on a revoked channel — the revoke flipped a column without ending the conversation")
	}

	// CONTROL: the unrelated relationship is untouched and still carries traffic.
	n, err := st.CountOpenChannelsBetweenStations(ctx, "stn_dev", "stn_infra")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("dev<->infra shows %d open channel(s), want 1 — revoking one relationship severed another", n)
	}
	if _, err := st.Send(ctx, dev, devInfra.ChannelID, "still fine", SendOpts{}); err != nil {
		t.Fatalf("the unrelated channel stopped accepting traffic after a revoke aimed at a different pair: %v", err)
	}

	// Idempotent: an operator who clicks twice, or a retry after a lost response,
	// must not see a failure for work already done.
	again, err := st.RevokeChannelsBetweenStations(ctx, "stn_dev", "stn_prod")
	if err != nil {
		t.Fatalf("a second revoke errored instead of being a no-op: %v", err)
	}
	if again != 0 {
		t.Fatalf("a second revoke reported %d closed, want 0", again)
	}
}

// A pair that never spoke is not an error, and neither is an unbound endpoint with no
// station id. Both are the common case on a fresh deployment, and a revoke that
// errored on them would make the console's confirm dialog fail for every link whose
// stations have not yet opened a channel.
func TestRevokeChannelsBetweenStationsToleratesNothingToDo(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()

	for _, c := range []struct{ name, a, b string }{
		{"never spoke", "stn_x", "stn_y"},
		{"empty a", "", "stn_y"},
		{"empty b", "stn_x", ""},
	} {
		n, err := st.CountOpenChannelsBetweenStations(ctx, c.a, c.b)
		if err != nil || n != 0 {
			t.Fatalf("count(%s): n=%d err=%v, want 0/nil", c.name, n, err)
		}
		closed, err := st.RevokeChannelsBetweenStations(ctx, c.a, c.b)
		if err != nil || closed != 0 {
			t.Fatalf("revoke(%s): closed=%d err=%v, want 0/nil", c.name, closed, err)
		}
	}
}

// AN AGENT MUST NOT BE ABLE TO HIDE ITS CHANNEL FROM THE OPERATOR'S REVOKE.
//
// The pair used to be derived by JOINing endpoint at query time, so it read the
// endpoint's CURRENT binding rather than the one that authorised the channel. A
// single comm_unbind — which comm_unbind's own tool description RECOMMENDS, and
// which needs no voucher and no human — made the channel match nothing: the console
// showed 0 live channels, the sweep closed 0, and the flash said "No channels were
// open" while both sides kept talking under a withdrawn permission.
//
// This is the instructed path, not an attack, which is what made it serious.
func TestUnbindingCannotHideAChannelFromRevocation(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()

	mk := func(tok, label, station, key string) *Endpoint {
		t.Helper()
		ep, _, err := st.RegisterEndpoint(ctx, owner(tok), label, "")
		if err != nil {
			t.Fatal(err)
		}
		return bindEndpoint(t, st, ep, station, key)
	}
	dev := mk("tok-dev", "dev", "stn_dev", "kens_dev")
	prod := mk("tok-prod", "prod", "stn_prod", "kens_prod")

	ch, err := st.OpenLinkedChannel(ctx, dev, prod, 1, "dev <-> prod")
	if err != nil {
		t.Fatal(err)
	}
	// CONTROL: visible before the unbind, so a later zero cannot be blamed on setup.
	if n, _ := st.CountOpenChannelsBetweenStations(ctx, "stn_dev", "stn_prod"); n != 1 {
		t.Fatalf("setup: blast radius %d before unbind, want 1", n)
	}

	// The evasion, in one agent tool call.
	if err := st.UnbindEndpointFromStation(ctx, dev.EndpointID); err != nil {
		t.Fatal(err)
	}

	n, err := st.CountOpenChannelsBetweenStations(ctx, "stn_dev", "stn_prod")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("after comm_unbind the console shows %d live channels, want 1 — an agent hid its conversation from the operator's brake", n)
	}
	closed, err := st.RevokeChannelsBetweenStations(ctx, "stn_dev", "stn_prod")
	if err != nil {
		t.Fatal(err)
	}
	if closed != 1 {
		t.Fatalf("revoke closed %d, want 1 — the permission ended and the conversation did not", closed)
	}
	if _, err := st.Send(ctx, prod, ch.ChannelID, "still talking?", SendOpts{}); err == nil {
		t.Fatal("the channel still carries traffic after its link was revoked")
	}
}

// And the mirror: revoking one relationship must not sever ANOTHER one's traffic.
//
// With the pair derived from live bindings, rebinding an endpoint moved its existing
// channels under a different pair — so infra's channel with prod became invisible to
// infra/prod's own revoke AND was killed by revoking the unrelated dev/prod link.
func TestRebindingDoesNotMoveAChannelUnderAnotherLink(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()

	mk := func(tok, label, station, key string) *Endpoint {
		t.Helper()
		ep, _, err := st.RegisterEndpoint(ctx, owner(tok), label, "")
		if err != nil {
			t.Fatal(err)
		}
		return bindEndpoint(t, st, ep, station, key)
	}
	infra := mk("tok-infra", "infra", "stn_infra", "kens_infra")
	prod := mk("tok-prod", "prod", "stn_prod", "kens_prod")

	ch, err := st.OpenLinkedChannel(ctx, infra, prod, 1, "infra <-> prod")
	if err != nil {
		t.Fatal(err)
	}
	// infra's session ends and its endpoint is re-bound to a different station.
	if err := st.UnbindEndpointFromStation(ctx, infra.EndpointID); err != nil {
		t.Fatal(err)
	}
	if err := st.BindEndpointToStation(ctx, infra.EndpointID, "stn_dev", "kens_dev"); err != nil {
		t.Fatal(err)
	}

	// The channel still belongs to the pair that AUTHORISED it.
	if n, _ := st.CountOpenChannelsBetweenStations(ctx, "stn_infra", "stn_prod"); n != 1 {
		t.Fatalf("infra<->prod sees %d of its own channels after a rebind, want 1", n)
	}
	// And revoking the unrelated dev/prod link must not touch it.
	closed, err := st.RevokeChannelsBetweenStations(ctx, "stn_dev", "stn_prod")
	if err != nil {
		t.Fatal(err)
	}
	if closed != 0 {
		t.Fatalf("revoking dev<->prod closed %d channel(s) belonging to infra<->prod", closed)
	}
	if _, err := st.Send(ctx, prod, ch.ChannelID, "unrelated traffic", SendOpts{}); err != nil {
		t.Fatalf("an unrelated link's revoke severed this channel: %v", err)
	}
}

// A STATION SUCCESSOR MUST BE ABLE TO ENUMERATE THE MAIL IT CAN ALREADY READ.
//
// Poll and ChannelFor were widened to station scope when stations shipped;
// ListChannels was not. So a replacement session could POLL a predecessor's channel
// and REPLY on it while comm_channels reported zero channels — able to act on a
// conversation it could not discover. Worst for the case stations exist for.
func TestListChannelsIsStationScopedLikePollAndChannelFor(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()

	mk := func(tok, label, station, key string) *Endpoint {
		t.Helper()
		ep, _, err := st.RegisterEndpoint(ctx, owner(tok), label, "")
		if err != nil {
			t.Fatal(err)
		}
		return bindEndpoint(t, st, ep, station, key)
	}
	first := mk("tok-a", "dev-v1", "stn_dev", "kens_a")
	peer := mk("tok-b", "prod", "stn_prod", "kens_b")

	ch, err := st.OpenLinkedChannel(ctx, first, peer, 1, "dev <-> prod")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Send(ctx, peer, ch.ChannelID, "for whoever is staffing dev", SendOpts{}); err != nil {
		t.Fatal(err)
	}

	// The predecessor's session ends; a replacement binds to the SAME station.
	successor := mk("tok-a", "dev-v2", "stn_dev", "kens_a")

	// It can read the mail — this is the station inbox working as designed.
	got, err := st.Poll(ctx, successor, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("the successor received %d messages, want 1 — the station inbox is broken and this test proves nothing", len(got))
	}

	// And it must be able to SEE the channel that mail arrived on.
	list, err := st.ListChannels(ctx, successor)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range list {
		if c.ChannelID == ch.ChannelID {
			found = true
		}
	}
	if !found {
		t.Fatalf("comm_channels listed %d channels and none was the one the successor just polled — it can act on a conversation it cannot enumerate", len(list))
	}

	// CONTROL: an unrelated station sees nothing, so the widening is not "list everything".
	stranger := mk("tok-c", "infra", "stn_infra", "kens_c")
	if l, err := st.ListChannels(ctx, stranger); err != nil || len(l) != 0 {
		t.Fatalf("an unrelated station lists %d channels (err=%v), want 0 — the predicate is too wide", len(l), err)
	}
}
