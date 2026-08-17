package comm

import (
	"context"
	"testing"
)

// A NOTICE IS A QUERY OVER ROWS THAT ALREADY EXIST, and it must say the same things the
// written notice used to say — without writing anything.
//
// The written form made a failure signal into a second thing that could fail, and it did:
// the notice path scanned a column that is NULL for room mail and took the whole sweep
// down with it in 3.0.0 and 3.0.1. Sweep expires, retains, purges, cleans files and
// removes idle endpoints; all of it stopped because a notice could not be written.
func TestASenderLearnsTheirMessageExpiredUnread(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	a, _, chID := pair(t, st)

	m, err := st.Send(ctx, a, chID, "nobody will read this", SendOpts{IdempotencyKey: "the-subject-line"})
	if err != nil {
		t.Fatal(err)
	}
	senderParty := endpointPartyKey(a.ID)

	// Nothing has happened yet: a notice before the fact would be a false alarm.
	n, err := st.NoticesFor(ctx, senderParty, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(n) != 0 {
		t.Fatalf("%d notices for a live message", len(n))
	}

	age(t, st, m.MessageID, "-31 days")
	if _, _, err := st.Sweep(ctx); err != nil {
		t.Fatal(err)
	}

	n, err = st.NoticesFor(ctx, senderParty, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(n) != 1 {
		t.Fatalf("%d notices after the message expired unread, want 1 — the sender is not told, "+
			"and silence is what they would otherwise read as delivery", len(n))
	}
	if n[0].Reason != "expired" || n[0].MessageID != m.MessageID {
		t.Fatalf("notice = %+v, want an expiry for %s", n[0], m.MessageID)
	}
	// The key is echoed because it may be the only surviving description: retention
	// blanks the body and keeps the metadata, so a notice naming an opaque id and
	// nothing else describes something the sender can no longer look up.
	if n[0].IdempotencyKey != "the-subject-line" {
		t.Errorf("the notice does not carry the key, so it names an id and no subject: %+v", n[0])
	}
	if len(n[0].Recipients) != 1 {
		t.Errorf("the notice names %d recipients — for a room message that is most of the information", len(n[0].Recipients))
	}

	// AND SWEEP WROTE NOTHING. This is the property the whole slice exists for.
	var written int
	if err := st.R.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM message WHERE kind='status'`).Scan(&written); err != nil {
		t.Fatal(err)
	}
	if written != 0 {
		t.Fatalf("the sweep wrote %d status message(s). A pass whose job is deleting must not insert: "+
			"it can hit backpressure, it can fail, and it rolls back its deletions when it does.", written)
	}
}

// A message somebody READ is not an expiry notice, and a message somebody ACKED is not
// either. Without this the query would report every settled message as a failure.
func TestNoNoticeForAMessageThatWasActuallyRead(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	a, b, chID := pair(t, st)

	m, err := st.Send(ctx, a, chID, "this one lands", SendOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Poll(ctx, b, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Ack(ctx, b, m.MessageID); err != nil {
		t.Fatal(err)
	}
	age(t, st, m.MessageID, "-31 days")
	if _, _, err := st.Sweep(ctx); err != nil {
		t.Fatal(err)
	}

	n, err := st.NoticesFor(ctx, endpointPartyKey(a.ID), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(n) != 0 {
		t.Fatalf("%d notices for a message that was read and acked: %+v.\n"+
			"A stream that reports successes as failures is one a sender learns to ignore.", len(n), n)
	}
}

// THE WATERMARK MUST NOT SKIP. It advances to the timestamp the caller was SHOWN, never
// to "now" — a notice that becomes true between the query and the mark would otherwise be
// silently dropped, which is invisible until someone loses a message they were told about.
func TestTheWatermarkClearsWhatWasSeenAndNothingElse(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	a, _, chID := pair(t, st)
	senderParty := endpointPartyKey(a.ID)

	first, err := st.Send(ctx, a, chID, "one", SendOpts{})
	if err != nil {
		t.Fatal(err)
	}
	age(t, st, first.MessageID, "-31 days")
	if _, _, err := st.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	n, err := st.NoticesFor(ctx, senderParty, 10)
	if err != nil || len(n) != 1 {
		t.Fatalf("setup: %d notices, err %v", len(n), err)
	}

	// Seen through the first one.
	if err := st.MarkNoticesSeen(ctx, senderParty, n[0].At); err != nil {
		t.Fatal(err)
	}
	after, err := st.NoticesFor(ctx, senderParty, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 0 {
		t.Fatalf("%d notices after marking them seen — the stream repeats forever", len(after))
	}

	// A SECOND failure, later, must still arrive. This is the half a "mark everything
	// read" implementation would silently break.
	second, err := st.Send(ctx, a, chID, "two", SendOpts{})
	if err != nil {
		t.Fatal(err)
	}
	age(t, st, second.MessageID, "-31 days")
	if _, _, err := st.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	after, err = st.NoticesFor(ctx, senderParty, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 || after[0].MessageID != second.MessageID {
		t.Fatalf("after a new failure the sender sees %+v, want exactly the second message.\n"+
			"A watermark that swallows later events is worse than no watermark.", after)
	}
}

// A ROOM message that expires unread produces a notice too — the case whose absence took
// the sweep down, now asserted from the sender's side rather than the sweep's.
func TestARoomMessageThatExpiresUnreadNotifiesItsSender(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	alpha := stationEndpoint(t, st, "tok-a", "st-alpha")
	roomFixture(t, st, "ops", "s:st-alpha", "s:st-beta", "s:st-gamma")

	m, err := st.SendToRoom(ctx, alpha, "ops", "anyone?", SendOpts{})
	if err != nil {
		t.Fatal(err)
	}
	age(t, st, m.MessageID, "-31 days")
	if _, _, err := st.Sweep(ctx); err != nil {
		t.Fatalf("the sweep failed on an expired room message: %v", err)
	}

	n, err := st.NoticesFor(ctx, stationParty("st-alpha"), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(n) != 1 || n[0].Scope != "r:ops" {
		t.Fatalf("notices for an expired room message = %+v, want one on r:ops", n)
	}
	// And it names WHO did not read it. With two recipients, "nobody engaged" and "one
	// station is quiet" are different facts and the sender can act on only one of them.
	if len(n[0].Recipients) != 2 {
		t.Errorf("the notice names %d of 2 recipients: %+v", len(n[0].Recipients), n[0].Recipients)
	}
}

// THE SHOWING CALL MUST NOT CLEAR. A notice is confirmed by the caller's NEXT poll, so
// a fault between this query and the caller holding the result cannot swallow it.
func TestAPollDoesNotClearTheNoticesItIsShowing(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	a, _, chID := pair(t, st)
	party := endpointPartyKey(a.ID)

	m, err := st.Send(ctx, a, chID, "nobody home", SendOpts{})
	if err != nil {
		t.Fatal(err)
	}
	age(t, st, m.MessageID, "-31 days")
	if _, _, err := st.Sweep(ctx); err != nil {
		t.Fatal(err)
	}

	// A FIRST POLL FIRST, so a watermark row EXISTS for the assertion below.
	//
	// Without this the test proves nothing: on a party's very first poll there is no
	// row to update, so a mutant that clears on the showing call is inert precisely
	// where the test looks. Mutation testing caught that — the test passed against an
	// implementation that cleared immediately, which is the defect it is named for.
	if _, err := st.NoticesForPoll(ctx, party, 25); err != nil {
		t.Fatal(err)
	}
	if _, err := st.NoticesForPoll(ctx, party, 25); err != nil {
		t.Fatal(err)
	}
	// NOW the interesting one: a second failure, against an existing watermark row.
	m2, err := st.Send(ctx, a, chID, "nobody home either", SendOpts{})
	if err != nil {
		t.Fatal(err)
	}
	age(t, st, m2.MessageID, "-31 days")
	if _, _, err := st.Sweep(ctx); err != nil {
		t.Fatal(err)
	}

	shown, err := st.NoticesForPoll(ctx, party, 25)
	if err != nil {
		t.Fatal(err)
	}
	if len(shown) != 1 || shown[0].MessageID != m2.MessageID {
		t.Fatalf("poll carried %+v, want the second failure", shown)
	}
	// NOTHING IS CONFIRMED YET: an unfiltered read still sees it, which is what makes
	// the result recoverable if this call's caller never got it.
	still, err := st.NoticesFor(ctx, party, 25)
	if err != nil {
		t.Fatal(err)
	}
	if len(still) != 1 {
		t.Fatalf("the showing call already advanced the confirmed watermark (%d left) — "+
			"a poll that dies after the read loses the notice outright", len(still))
	}

	// The NEXT poll confirms it and it stops.
	after, err := st.NoticesForPoll(ctx, party, 25)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 0 {
		t.Fatalf("the notice repeats on the next poll (%d) — a stream that never clears "+
			"is one a sender learns to skip, which is the failure it exists to prevent", len(after))
	}
}

// AND CONFIRMATION MUST NOT SKIP PAST SOMETHING NEW. The bug this shape invites: poll 1
// shows A, a second failure B happens, poll 2 confirms "everything up to now" and B is
// gone before anyone saw it. Invisible until a sender loses a message they were never told
// about — so it is asserted rather than reasoned about.
func TestConfirmingTheLastPollDoesNotSwallowAFailureThatArrivedSince(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	a, _, chID := pair(t, st)
	party := endpointPartyKey(a.ID)

	first, err := st.Send(ctx, a, chID, "one", SendOpts{})
	if err != nil {
		t.Fatal(err)
	}
	age(t, st, first.MessageID, "-31 days")
	if _, _, err := st.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if n, err := st.NoticesForPoll(ctx, party, 25); err != nil || len(n) != 1 {
		t.Fatalf("setup: %d notices, err %v", len(n), err)
	}

	// B fails BETWEEN the two polls.
	second, err := st.Send(ctx, a, chID, "two", SendOpts{})
	if err != nil {
		t.Fatal(err)
	}
	age(t, st, second.MessageID, "-31 days")
	if _, _, err := st.Sweep(ctx); err != nil {
		t.Fatal(err)
	}

	got, err := st.NoticesForPoll(ctx, party, 25)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].MessageID != second.MessageID {
		t.Fatalf("the second poll carried %+v, want exactly the message that failed since.\n"+
			"Confirming with 'now' instead of 'what was shown' drops anything in the gap.", got)
	}
}

// PARTIAL SILENCE IN A ROOM MUST STILL BE REPORTED — and it was not.
//
// Found by mutation testing: removing 'acked' from the exclusion changed no test, which
// meant no test covered the only case where it matters. It matters a lot. A room message
// that one station reads and another never touches is settled for the reader and dead for
// the writer, and the old predicate — report only when NO delivery is queued, delivered
// OR acked — treated one reader as though everybody had read it.
//
// The consequence is the exact failure this whole slice exists to remove, in the case
// rooms make ordinary: the sender sees nothing, and nothing is what delivery looks like.
// "Nobody engaged" and "two of three are quiet" are different facts, and only one of them
// was reachable.
func TestARoomMessageOneStationReadAndAnotherIgnoredStillTellsTheSender(t *testing.T) {
	st := newStore(t, DefaultLimits())
	ctx := context.Background()
	alpha := stationEndpoint(t, st, "tok-a", "st-alpha")
	beta := stationEndpoint(t, st, "tok-b", "st-beta")
	roomFixture(t, st, "ops", "s:st-alpha", "s:st-beta", "s:st-gamma")

	m, err := st.SendToRoom(ctx, alpha, "ops", "two of you are listening", SendOpts{})
	if err != nil {
		t.Fatal(err)
	}
	// beta reads AND acks. gamma never comes.
	if _, err := st.Poll(ctx, beta, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Ack(ctx, beta, m.MessageID); err != nil {
		t.Fatal(err)
	}
	// AGED BY TWO DAYS, NOT THIRTY, and the difference is a real property rather than a
	// fixture detail. beta's poll armed the 24 h DELIVERED clock, so -31 days puts this
	// message's settle moment a month back — past MetadataTTLSeconds (7 d), and the
	// metadata purge deletes the row in the same sweep that expires it. A DERIVED notice
	// cannot outlive the rows it is derived from, so the notice window is bounded by
	// metadata retention. That is documented on NoticesFor; here it decides the fixture.
	age(t, st, m.MessageID, "-2 days")
	if _, _, err := st.Sweep(ctx); err != nil {
		t.Fatal(err)
	}

	n, err := st.NoticesFor(ctx, stationParty("st-alpha"), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(n) != 1 {
		t.Fatalf("%d notices when one of two recipients never read it — the sender is told nothing, "+
			"and one station acking hides the other's silence completely", len(n))
	}
	// AND IT NAMES ONLY THE ONE THAT WENT QUIET. Naming beta too would be a false
	// accusation about a station that did its part.
	if len(n[0].Recipients) != 1 || n[0].Recipients[0] != stationParty("st-gamma") {
		t.Fatalf("the notice names %v, want only st-gamma — beta read it", n[0].Recipients)
	}
}

// A DERIVED QUERY MUST NOT RUN BACKWARDS ACROSS ITS OWN INPUT'S INTRODUCTION.
//
// `delivery.replied_by` has existed as a column since 0001 and was written by nothing until
// migration 0009. So on an upgraded deployment every earlier requires_response message has
// it NULL permanently, and the derived notice read that as "nobody replied" rather than
// "this predates the column being written" — reporting a peer as owing answers it had given
// within minutes. ken-prod-ops received four such notices and measured 136 rows permanently
// eligible; they could only disbelieve it because they held the other side of the thread.
func TestReplyOverdueIgnoresDeliveriesOlderThanTheMechanism(t *testing.T) {
	ctx := context.Background()
	l := DefaultLimits()
	l.ReplyDeadlineSeconds = -1 // the deadline is already past at delivery
	st := newStore(t, l)
	a, b, channelID := pair(t, st)

	sent, err := st.Send(ctx, a, channelID, "please do X", SendOpts{RequiresResponse: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Poll(ctx, b, 10); err != nil { // arms the deadline at delivery
		t.Fatal(err)
	}

	// CONTROL: a delivery from AFTER the mechanism existed still produces the notice.
	// Without this arm, a predicate that excluded everything would read as a pass — which
	// is exactly the failure mode of the bug being fixed, in the other direction.
	got, err := st.NoticesFor(ctx, endpointPartyKey(a.ID), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Reason != "reply_overdue" {
		t.Fatalf("control: a genuinely unanswered request produced %d notices (%+v) — the "+
			"lower bound is excluding live traffic", len(got), got)
	}

	// Now age the delivery to before this deployment gained the mechanism, which is what
	// every pre-0009 row looks like. Nothing else about it changes.
	if _, err := st.W.Exec(`
UPDATE delivery SET first_delivered_at = strftime('%Y-%m-%dT%H:%M:%fZ','now','-30 day')
WHERE message_row = (SELECT id FROM message WHERE message_id=?)`, sent.MessageID); err != nil {
		t.Fatal(err)
	}

	got, err = st.NoticesFor(ctx, endpointPartyKey(a.ID), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("a delivery predating the reply-linking mechanism produced %d notice(s): %+v\n"+
			"replied_by could NEVER have been set for it, so NULL cannot mean 'nobody "+
			"replied' — and the notice manufactures work for whoever receives it.", len(got), got)
	}

	// The boundary is read from the deployment's own migration record, not compiled in.
	var appliedAt string
	if err := st.R.QueryRow(`SELECT applied_at FROM schema_migration WHERE version = 9`).Scan(&appliedAt); err != nil {
		t.Fatalf("the boundary this rule depends on is not recorded: %v", err)
	}
	if appliedAt == "" {
		t.Error("schema_migration.applied_at for version 9 is empty — the lower bound would " +
			"silently admit everything")
	}
}
