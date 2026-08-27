package comm

import (
	"context"
	"testing"
)

func TestProbeRevokedPeerStillCountsAsRecipient(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, DefaultLimits())
	a, b, chID := pair(t, st)

	if err := st.RevokeEndpoint(ctx, b.EndpointID); err != nil {
		t.Fatal(err)
	}

	m, err := st.Send(ctx, a, chID, "into the void", SendOpts{})
	if err != nil {
		t.Fatalf("SEND REFUSED (good): %v", err)
	}
	t.Logf("send SUCCEEDED: message_id=%s recipients=%d state=%s", m.MessageID, m.Recipients, m.State)

	var state string
	if err := st.R.QueryRowContext(ctx, `SELECT state FROM channel WHERE channel_id=?`, chID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	t.Logf("channel state after peer revocation: %q", state)

	var pk string
	var recipEP any
	if err := st.R.QueryRowContext(ctx,
		`SELECT d.party_key, d.recipient_endpoint FROM delivery d JOIN message m ON m.id=d.message_row WHERE m.message_id=?`,
		m.MessageID).Scan(&pk, &recipEP); err != nil {
		t.Fatal(err)
	}
	t.Logf("delivery party_key=%q recipient_endpoint=%v (b rowid=%d)", pk, recipEP, b.ID)

	// Can the revoked endpoint ever come back?
	if _, err := st.AuthenticateEndpoint(ctx, b.EndpointID, "whatever"); err == nil {
		t.Fatal("revoked endpoint authenticated")
	} else {
		t.Logf("AuthenticateEndpoint(b) -> %v", err)
	}

	// Does comm_channels still list it as open for the sender?
	list, err := st.ListChannels(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range list {
		t.Logf("sender sees channel %s state=%q open=%v", c.ChannelID, c.State, c.Open())
	}

	// And is it still pending / undeliverable?
	pend, err := st.PendingForEndpoint(ctx, a)
	t.Logf("PendingForEndpoint(sender)=%v err=%v", pend, err)
}

// Same, but the revoked endpoint was BOUND to a station.
func TestProbeRevokedBoundPeer(t *testing.T) {
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
	m, err := st.Send(ctx, a, ch.ChannelID, "to a station with no live reader", SendOpts{})
	if err != nil {
		t.Fatalf("send refused: %v", err)
	}
	var pk string
	_ = st.R.QueryRowContext(ctx,
		`SELECT d.party_key FROM delivery d JOIN message m ON m.id=d.message_row WHERE m.message_id=?`,
		m.MessageID).Scan(&pk)
	var state string
	_ = st.R.QueryRowContext(ctx, `SELECT state FROM channel WHERE channel_id=?`, ch.ChannelID).Scan(&state)
	t.Logf("bound peer: recipients=%d party_key=%q channel state=%q", m.Recipients, pk, state)
}
