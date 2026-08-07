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
