package comm

import (
	"context"
	"errors"
	"testing"
)

// linkFixture populates the station-link mirror directly, exactly as the boot-time
// rebuild and the console refresh do. ken.db owns links and this package cannot reach
// it, which is the reason the mirror exists at all.
func linkFixture(t *testing.T, st *Store, pairs ...[2]string) {
	t.Helper()
	if err := st.ReplaceLinkMirror(context.Background(), pairs, 1); err != nil {
		t.Fatal(err)
	}
}

// THE POINT OF P2: a link is enough. No pairing code, no channel row, no second session
// online at the moment of sending.
func TestAStationCanWriteToALinkedPeerWithNoChannel(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	sender := stationEndpoint(t, st, "tok-a", "st-alpha")
	linkFixture(t, st, [2]string{"st-alpha", "st-beta"})

	m, err := st.SendToStation(ctx, sender, "st-beta", "the migration finished", SendOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if m.Scope != "p:st-alpha|st-beta" {
		t.Errorf("scope = %q, want the sorted pair scope", m.Scope)
	}
	if m.Recipients != 1 {
		t.Errorf("recipients = %d, want 1", m.Recipients)
	}

	// NOT A CHANNEL. If a channel row appeared, the conversation would once again
	// depend on both sides being staffed at the moment of creation — the exact
	// dependency this item removes.
	var channels int
	if err := st.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM channel`).Scan(&channels); err != nil {
		t.Fatal(err)
	}
	if channels != 0 {
		t.Errorf("%d channel rows — a pair send must not create one", channels)
	}

	// Addressed to the PARTY with no endpoint attached, so a successor session
	// staffing st-beta inherits the conversation without anything being re-pointed.
	var party string
	var epRow any
	if err := st.R.QueryRowContext(ctx,
		`SELECT party_key, recipient_endpoint FROM delivery`).Scan(&party, &epRow); err != nil {
		t.Fatal(err)
	}
	if party != "s:st-beta" {
		t.Errorf("delivery party = %q, want s:st-beta", party)
	}
	if epRow != nil {
		t.Errorf("recipient_endpoint = %v, want NULL — a pair message is addressed to a post, "+
			"and which connection reads it is decided at poll time", epRow)
	}
}

// The peer receives it, and the poll says how to answer.
func TestALinkedPeerPollsThePairMessageAndLearnsTheReplyAddress(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	alpha := stationEndpoint(t, st, "tok-a", "st-alpha")
	beta := stationEndpoint(t, st, "tok-b", "st-beta")
	linkFixture(t, st, [2]string{"st-alpha", "st-beta"})

	if _, err := st.SendToStation(ctx, alpha, "st-beta", "ping", SendOpts{}); err != nil {
		t.Fatal(err)
	}
	msgs, err := st.Poll(ctx, beta, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("beta polled %d messages, want 1", len(msgs))
	}
	if msgs[0].SenderStationID != "st-alpha" {
		t.Errorf("sender station = %q, want st-alpha — without it the recipient cannot address a reply",
			msgs[0].SenderStationID)
	}

	// And the reply lands in the SAME scope with the next sequence number, which is
	// what makes one ascending stream cover the exchange rather than two interleaved
	// ones — the defect that made a cumulative ack settle the other direction's mail.
	reply, err := st.SendToStation(ctx, beta, "st-alpha", "pong", SendOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if reply.Scope != msgs[0].Scope {
		t.Errorf("reply scope %q != original %q — the pair must be one place from both sides",
			reply.Scope, msgs[0].Scope)
	}
	if reply.Seq != 2 {
		t.Errorf("reply seq = %d, want 2 — one counter per pair, spanning both senders", reply.Seq)
	}
}

// AUTHORISATION IS THE LINK. This is the test that must fail if the check is removed.
func TestAnUnlinkedStationIsRefused(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	sender := stationEndpoint(t, st, "tok-a", "st-alpha")
	// st-beta exists in a link with someone else, so it is a KNOWN station that this
	// sender is simply not joined to — the case that must say "not linked", not
	// "unknown station".
	linkFixture(t, st, [2]string{"st-beta", "st-gamma"})

	_, err := st.SendToStation(ctx, sender, "st-beta", "hello", SendOpts{})
	if !errors.Is(err, ErrNotLinked) {
		t.Fatalf("err = %v, want ErrNotLinked", err)
	}
	var n int
	if err := st.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM message`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d message rows written by a refused send", n)
	}
}

// A station id nobody has ever been linked to is a TYPO, and gets a different sentence.
// The distinction matters because the two remedies are different: one is a re-read of
// comm_directory, the other is a human approval that cannot be retried into existence.
func TestAnUnknownStationIsNamedAsUnknown(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	sender := stationEndpoint(t, st, "tok-a", "st-alpha")
	linkFixture(t, st, [2]string{"st-alpha", "st-beta"})

	_, err := st.SendToStation(ctx, sender, "st-typo", "hello", SendOpts{})
	if !errors.Is(err, ErrUnknownStation) {
		t.Fatalf("err = %v, want ErrUnknownStation", err)
	}
}

// REVOCATION IS THE OPERATION THAT MUST WORK. A link withdrawn from the mirror stops
// authorising immediately — this is the whole reason the check reads the mirror inside
// the writing transaction rather than trusting anything cached.
func TestRevokingTheLinkStopsTheNextSend(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	sender := stationEndpoint(t, st, "tok-a", "st-alpha")
	linkFixture(t, st, [2]string{"st-alpha", "st-beta"})
	if _, err := st.SendToStation(ctx, sender, "st-beta", "before", SendOpts{}); err != nil {
		t.Fatal(err)
	}
	// The console's refresh after a revoke: the pair is simply no longer in the set.
	linkFixture(t, st)

	if _, err := st.SendToStation(ctx, sender, "st-beta", "after", SendOpts{}); !errors.Is(err, ErrUnknownStation) &&
		!errors.Is(err, ErrNotLinked) {
		t.Fatalf("err = %v, want a refusal after revocation", err)
	}
}

// TestAnUnboundEndpointCannotAddressAStation IS DELETED. A mailbox cannot exist without a station,
// so the sender it guarded against cannot be constructed.

// A station addressing itself would receive its own message back as peer mail.
func TestAStationCannotAddressItself(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	sender := stationEndpoint(t, st, "tok-a", "st-alpha")
	linkFixture(t, st, [2]string{"st-alpha", "st-beta"})

	if _, err := st.SendToStation(ctx, sender, "st-alpha", "hi", SendOpts{}); !errors.Is(err, ErrSelfSend) {
		t.Fatalf("err = %v, want ErrSelfSend", err)
	}
}

// BOTH DIRECTIONS NAME ONE PLACE. Written as its own test because the failure it guards
// is silent: two scopes for one relationship gives each side its own sequence, its own
// backpressure budget, and a conversation where neither can cumulatively ack the other.
func TestThePairScopeIsTheSameFromBothSides(t *testing.T) {
	if pairScope("st-zulu", "st-alpha") != pairScope("st-alpha", "st-zulu") {
		t.Fatal("pairScope is not order-independent")
	}
	if got := pairScope("st-zulu", "st-alpha"); got != "p:st-alpha|st-zulu" {
		t.Errorf("pairScope = %q, want the sorted form", got)
	}
	a, b, ok := pairStationsOfScope("p:st-alpha|st-zulu")
	if !ok || a != "st-alpha" || b != "st-zulu" {
		t.Errorf("pairStationsOfScope = %q,%q,%v", a, b, ok)
	}
	// A malformed scope must not resolve to a blank station: "s:" is a party no
	// endpoint can hold, so a delivery addressed to it disappears without an error.
	for _, bad := range []string{"p:", "p:only", "p:|b", "p:a|", "r:room1", "ch:c1"} {
		if _, _, ok := pairStationsOfScope(bad); ok {
			t.Errorf("pairStationsOfScope(%q) accepted a malformed scope", bad)
		}
	}
}

// comm_channels must name exactly the peers the send path would accept. A listing that
// offered a peer the send then refused would be worse than no listing at all.
func TestPairsForListsTheLinkedPeersAndTheirPendingMail(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	alpha := stationEndpoint(t, st, "tok-a", "st-alpha")
	beta := stationEndpoint(t, st, "tok-b", "st-beta")
	linkFixture(t, st, [2]string{"st-alpha", "st-beta"}, [2]string{"st-alpha", "st-gamma"})

	if _, err := st.SendToStation(ctx, beta, "st-alpha", "one", SendOpts{}); err != nil {
		t.Fatal(err)
	}
	pairs, err := st.PairsFor(ctx, alpha)
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 2 {
		t.Fatalf("%d pairs, want 2 (beta and gamma)", len(pairs))
	}
	byID := map[string]PairConversation{}
	for _, p := range pairs {
		byID[p.StationID] = p
	}
	if byID["st-beta"].Pending != 1 {
		t.Errorf("pending from beta = %d, want 1", byID["st-beta"].Pending)
	}
	// GAMMA IS LISTED WITH NO MAIL. The list answers "who may I write to", which is the
	// question a session actually has; hiding an empty conversation would hide the
	// permission itself.
	if _, ok := byID["st-gamma"]; !ok {
		t.Error("a linked peer with no mail is missing from the listing")
	}
	if byID["st-gamma"].Pending != 0 {
		t.Errorf("pending from gamma = %d, want 0", byID["st-gamma"].Pending)
	}
}

// An unbound endpoint asking what it can reach gets an honest empty list, not an error:
// comm_channels is a read, and failing it would cost the caller the counts it came for.
func TestPairsForIsEmptyRatherThanFailingForAnUnboundEndpoint(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	ep, err := st.MailboxFor(ctx, "tok-x", owner("tok-x"))
	if err != nil {
		t.Fatal(err)
	}
	bound, err := st.MailboxFor(ctx, ep.StationID, owner("tok"))
	if err != nil {
		t.Fatal(err)
	}
	pairs, err := st.PairsFor(ctx, bound)
	if err != nil {
		t.Fatalf("PairsFor returned an error for an unbound endpoint: %v", err)
	}
	if len(pairs) != 0 {
		t.Errorf("%d pairs for an unbound endpoint", len(pairs))
	}
}
