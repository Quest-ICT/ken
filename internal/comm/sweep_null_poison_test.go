package comm

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// *** ONE PAIR MESSAGE PERMANENTLY DISABLES THE REVOKED-CHANNEL PURGE. ***
//
// The purge is `id NOT IN (SELECT channel_id FROM message)`. `message.channel_id` became
// NULLABLE in migration 0009 — a pair or room message belongs to no channel and writes NULL —
// and `NOT IN` over a set containing NULL is never true. So the first `to_station` send this
// deployment ever makes turns the purge into a permanent no-op, with no error and no log line.
//
// The rule is written verbatim twelve lines above the defect, in the endpoint purge:
// `WHERE recipient_endpoint IS NOT NULL` and `WHERE endpoint_b IS NOT NULL`. That arm got the
// guard when 0009 landed; this one did not.
//
// EXISTING SWEEP TESTS CANNOT CATCH THIS. Their fixtures leave `message` empty, and `NOT IN`
// over an EMPTY set is true — so the purge works perfectly in a test and never in production.
// That is why this test seeds a pair message first: the poison IS the fixture.
func TestOnePairMessageDoesNotDisableTheRevokedChannelPurge(t *testing.T) {
	ctx := context.Background()

	revokedAndSwept := func(t *testing.T, withPairMessage bool) int {
		t.Helper()
		lim := DefaultLimits()
		lim.MetadataTTLSeconds = 1
		st := newStore(t, lim)
		a := stationEndpoint(t, st, "tok-a", "st-alpha")
		b := stationEndpoint(t, st, "tok-b", "st-beta")

		// A channel that is revoked and aged well past the metadata TTL, referenced by nothing.
		code, err := st.MintPairingCode(ctx, 1, 1, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.JoinChannel(ctx, a, code); err != nil {
			t.Fatal(err)
		}
		ch, err := st.JoinChannel(ctx, b, code)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.RevokeChannel(ctx, ch.ChannelID); err != nil {
			t.Fatal(err)
		}
		if _, err := st.W.ExecContext(ctx,
			`UPDATE channel SET revoked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','-30 days')`); err != nil {
			t.Fatal(err)
		}

		if withPairMessage {
			// THE POISON: one station-addressed send, which writes channel_id = NULL.
			linkFixture(t, st, [2]string{"st-alpha", "st-beta"})
			if _, err := st.SendToStation(ctx, a, "st-beta", "hello", SendOpts{}); err != nil {
				t.Fatal(err)
			}
			var nulls int
			if err := st.R.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM message WHERE channel_id IS NULL`).Scan(&nulls); err != nil {
				t.Fatal(err)
			}
			if nulls == 0 {
				t.Fatal("fixture: the pair send did not produce a NULL channel_id, so this test proves nothing")
			}
		}

		if _, _, err := st.Sweep(ctx); err != nil {
			t.Fatal(err)
		}
		var left int
		if err := st.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM channel WHERE state='revoked'`).Scan(&left); err != nil {
			t.Fatal(err)
		}
		return left
	}

	// CONTROL, and it is the whole point: with no pair message the purge works, which is
	// exactly why every existing sweep test passes over this defect.
	if left := revokedAndSwept(t, false); left != 0 {
		t.Fatalf("control: the purge left %d revoked channel(s) with no pair message — the fixture is wrong, not the code", left)
	}
	if left := revokedAndSwept(t, true); left != 0 {
		t.Fatalf("one pair message left %d revoked channel(s) unpurged — NOT IN over a NULL-containing set is never true", left)
	}
}

// A ROOM ID OFFERED TO comm_file_offer MUST NOT BE TOLD TO USE `to_room`.
//
// ChannelFor's refusal was written for comm_send, where `to_room` exists and the advice is
// right. comm_file_offer has no such parameter, so following it returns
// `unexpected additional properties ["to_room"]` — and the session cannot tell "I called it
// wrong" from "the feature does not exist". Each error blames the other; that is the hunted
// class, on a live surface.
func TestOfferingAFileToARoomSaysFilesAreChannelOnly(t *testing.T) {
	ctx := context.Background()
	lim := DefaultLimits()
	lim.FilesEnabled = true
	st := newStore(t, lim)
	a := stationEndpoint(t, st, "tok-a", "st-alpha")
	roomFixture(t, st, "r-ops", "s:st-alpha", "s:st-beta")

	_, err := st.OfferFile(ctx, a, "r-ops", FileOffer{
		Name: "f.txt", SizeBytes: 5, SHA256: shaOf([]byte("hello")), Transfer: "upload"})
	if err == nil {
		t.Fatal("offering a file to a room succeeded — this test is asserting against the wrong state")
	}
	msg := err.Error()
	if strings.Contains(msg, "to_room") && !strings.Contains(msg, "no to_room") {
		t.Fatalf("the refusal points at a parameter comm_file_offer does not have: %q", msg)
	}
	if !strings.Contains(msg, "channel-only") {
		t.Fatalf("the refusal does not say files are channel-only today: %q", msg)
	}
	// AND IT MUST REACH THE CALLER. A paragraph flattened to "internal error" by the mapper
	// is the same silence in a different costume — see refusal_reachability_test.go.
	var cs interface{ CallerSafeText() string }
	if !errors.As(err, &cs) {
		t.Fatal("the refusal is not CallerSafe, so a session receives \"internal error\" instead of it")
	}
}
