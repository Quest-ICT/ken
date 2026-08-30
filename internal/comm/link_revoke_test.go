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
		ep, err := st.MailboxFor(ctx, label, owner(tok))
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

// TestUnbindingCannotHideAChannelFromRevocation IS DELETED, AND THE ATTACK WITH IT.
//
// It guarded a real evasion: a session could call comm_unbind — one agent tool call, no human —
// and its channel would stop counting toward the blast radius a human was about to act on. There
// is no comm_unbind and no unbound state, so the move does not exist to defend against. This is
// the shape ken-prod-ops predicted: several of the defects it reported live in machinery this
// change deletes rather than fixes.

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
		ep, err := st.MailboxFor(ctx, label, owner(tok))
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
	// infra's session ends and a DIFFERENT station's mailbox takes over the variable. An endpoint
	// cannot move between stations any more — it never could without comm_unbind, which is gone —
	// so the same scenario is expressed by naming the other station's mailbox directly.
	infra = mailbox(t, st, "stn_dev", "tok")

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
		ep, err := st.MailboxFor(ctx, label, owner(tok))
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

// A STATION MUST NOT BECOME ITS OWN PEER — AND THE SCHEMA NOW REFUSES THE STATE ENTIRELY.
//
// The original defect was in the deleted pairing-code join path: its re-join check compared endpoint
// ROWIDS, and the schema's CHECK (endpoint_b <> endpoint_a) catches only the same literal rowid, so
// a SECOND endpoint of a station that already held a seat matched neither guard, took the free seat,
// and the channel ended up with station_a = station_b. Every message that station sent came back as
// mail from a peer.
//
// This test then moved onto OpenLinkedChannel, building the two-mailboxes-for-one-station state by
// direct INSERT because MailboxFor is get-or-create and would not produce it. Migration 0021 makes
// that INSERT impossible: a partial UNIQUE index on endpoint(station_id) WHERE revoked_at IS NULL.
// The invariant every reader already assumed is now enforced where it cannot be forgotten.
//
// SO THE ASSERTION MOVED TO THE STRONGER GUARANTEE, and the code guard in OpenLinkedChannel stays
// as defence — it costs one comparison and it is the thing that would still hold if the index were
// ever dropped. Asserting the index rather than the guard is the honest choice: the index is what
// makes the state unreachable, and a test proving the guard works on a state nothing can construct
// proves less than one proving the state cannot be constructed.
func TestAStationCannotHaveTwoLiveMailboxes(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, DefaultLimits())

	first := stationEndpoint(t, st, "tok-1", "st-solo")

	// CONTROL: asking again returns the SAME row rather than a second one. This is the ordinary
	// path, and if it ever stopped holding, the refusal below would be hiding a broken get-or-create
	// rather than protecting an invariant.
	again := stationEndpoint(t, st, "tok-2", "st-solo")
	if again.ID != first.ID {
		t.Fatalf("MailboxFor returned a different mailbox for the same station (%d, %d)", first.ID, again.ID)
	}

	// AND THE SCHEMA REFUSES A SECOND ONE even when written past the constructor.
	_, err := st.W.ExecContext(ctx, `
INSERT INTO endpoint(endpoint_id, secret_sha256, token_id, actor_id, label, station_id, bound_at)
VALUES('ep-second-seat','x','tok-2',7,'st-solo','st-solo',strftime('%Y-%m-%dT%H:%M:%fZ','now'))`)
	if err == nil {
		t.Error("a second live mailbox was created for one station — two sessions could then take " +
			"both seats of a channel, giving station_a = station_b, and every attachment offered to " +
			"the first mailbox would strand when a reader resolved to the second")
	}

	// A DIFFERENT station is unaffected: the index is per station, not global.
	if other := stationEndpoint(t, st, "tok-3", "st-peer"); other.ID == first.ID {
		t.Error("two stations resolved to one mailbox")
	}
	if _, err := st.OpenLinkedChannel(ctx, first, stationEndpoint(t, st, "tok-3", "st-peer"), 42, "solo<->peer"); err != nil {
		t.Errorf("a real peer was refused a channel: %v", err)
	}
}

// AN OUTSTANDING REQUEST BELONGS TO THE STATION THAT MADE IT.
//
// PendingReplies filtered sender_endpoint, so the reading whose entire purpose is to
// outlive the session that made the request returned nothing to that session's successor.
func TestASuccessorSeesTheRequestsItsPredecessorIsStillOwed(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, DefaultLimits())

	asker := stationEndpoint(t, st, "tok-ask", "st-asker")
	peer := stationEndpoint(t, st, "tok-peer", "st-peer")
	ch := openChannel(t, st, asker, peer, "asker<->peer")
	if _, err := st.Send(ctx, asker, ch.ChannelID, "please do X", SendOpts{RequiresResponse: true}); err != nil {
		t.Fatal(err)
	}

	// CONTROL: the asking session itself sees it, so an empty list below is about the
	// successor and not about a request that was never outstanding.
	mine, err := st.PendingReplies(ctx, asker, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(mine) != 1 {
		t.Fatalf("setup: the asking session sees %d outstanding requests, want 1", len(mine))
	}

	successor := stationEndpoint(t, st, "tok-ask-2", "st-asker")
	got, err := st.PendingReplies(ctx, successor, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Errorf("a successor sees %d outstanding requests, want 1.\nIt inherits the inbox "+
			"the answer will arrive in, and is told it is waiting for nothing.", len(got))
	}
}

// TestAnUnboundPredecessorDoesNotCostItsStationTheChannel IS DELETED. It covered the case where a
// PREDECESSOR session unbinds and its station must keep reaching the channel — two mailboxes on one
// station, one of them leaving. A station has one mailbox and it never leaves.
