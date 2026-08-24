package commserver

import (
	"errors"
	"fmt"
	"testing"

	"github.com/Quest-ICT/ken/internal/comm"
	"github.com/Quest-ICT/ken/internal/store"
)

// EVERY REFUSAL WHOSE TEXT WAS WRITTEN FOR A CALLER MUST REACH THAT CALLER.
//
// `commError` flattens by sentinel so refusals stay uniform, and anything its switch does not
// name becomes the literal string "internal error". That is correct as a default and wrong for
// an error whose author wrote a paragraph explaining what to do next: the paragraph is
// discarded and the session is told nothing. A 2026-08-19 sweep found THIRTEEN of these
// (docs/PARKING-LOT.md A0) — including `ErrRoomEmpty`, whose own text ends "Ask your human to
// check the /stations console" and which no session had ever seen.
//
// THIS FILE USED TO BE A PROBE THAT ONLY LOGGED. It enumerated the sentinels, printed whether
// each survived, and asserted nothing — so it could not fail, and the class it was written to
// investigate went on recurring underneath it. It crossed the mapper, which is the hard part
// and the thing A0 says every store-level test structurally cannot do; it just never made a
// claim. It does now.
//
// THE FLATTEN LIST IS THE POINT, NOT AN EXEMPTION. Some refusals SHOULD collapse — a probing
// caller must not learn from them. Each one is named here with the reason, and the test fails
// in BOTH directions: a listed error whose text starts reaching callers must leave the list.
// Without that half the list becomes a place to put inconvenient failures.
func TestEveryCallerFacingRefusalSurvivesTheMapper(t *testing.T) {
	// *** WHAT THIS GATE CAN AND CANNOT GUARD, stated so nobody trusts it further than it goes. ***
	//
	// It enumerates NAMED SENTINELS and passes the real exported value through the real mapper.
	// It CANNOT guard an error built inline at a raise site — `errors.New("sha256 must be 64 hex
	// digits")` has no handle to enumerate, and an earlier draft of this test listed those by
	// re-typing their text. That version asserted on a COPY of the error rather than the one the
	// code returns: it went green the moment the copy was wrapped, while the raise site could
	// have stayed bare. A test of a reconstructed value is not a test of the value that is read.
	//
	// Those raise sites (three OfferFile validations, two attachment-state checks, one scope
	// parse — docs/PARKING-LOT.md A0) were wrapped with comm.CallerSafe in the same change, and
	// they are NOT covered here. Closing that needs a test driving the real call through the MCP
	// surface, which is a bigger fixture than this file is.
	//
	// Refusals that MUST arrive intact — each carries an instruction the caller can act on.
	intact := []struct {
		name string
		err  error
	}{
		{"comm.ErrNotFound", comm.ErrNotFound},
		{"comm.ErrDenied", comm.ErrDenied},
		{"comm.ErrBackpressure", comm.ErrBackpressure},
		{"comm.ErrTooLarge", comm.ErrTooLarge},
		{"comm.ErrChannelClosed", comm.ErrChannelClosed},
		{"comm.ErrFilesDisabled", comm.ErrFilesDisabled},
		{"comm.ErrQuota", comm.ErrQuota},
		{"comm.ErrBadName", comm.ErrBadName},
		{"comm.ErrShortWrite", comm.ErrShortWrite},
		{"comm.ErrNotAStation", comm.ErrNotAStation},
		{"comm.ErrNotLinked", comm.ErrNotLinked},
		{"comm.ErrSelfSend", comm.ErrSelfSend},
		{"comm.ErrUnknownStation", comm.ErrUnknownStation},
		{"comm.ErrRoomEmpty", comm.ErrRoomEmpty},
		{"comm.ErrNoAudience", comm.ErrNoAudience},
		{"store.ErrStationKeyRevoked", store.ErrStationKeyRevoked},
		{"store.ErrStationArchived", store.ErrStationArchived},
		{"CallerSafe ChannelFor room-as-channel", comm.CallerSafe(fmt.Errorf("%w: %q is a ROOM, not a channel", comm.ErrNotFound, "r1"))},
	}
	for _, c := range intact {
		got := commError(c.err)
		if got == nil {
			t.Errorf("%s: commError returned nil", c.name)
			continue
		}
		// THE PROPERTY IS NOT "BYTE-IDENTICAL". `commError`'s switch deliberately SUBSTITUTES
		// a richer, purpose-written sentence for several sentinels — ErrBackpressure's raise
		// site says "channel backpressure: too many unacknowledged messages" and the caller
		// correctly receives "...stop sending and wait for the peer to catch up; do NOT retry
		// in a loop". That is the mapper doing its job, not discarding anything.
		//
		// What must never happen is the OPAQUE DEFAULT: the caller told "internal error" about
		// a refusal it could have acted on. That is the whole of defect class A0.
		if got.Error() == "internal error" {
			t.Errorf("%s reaches the caller as \"internal error\".\n  the author wrote: %q\n"+
				"  Name it in commError's switch, or wrap it with comm.CallerSafe.", c.name, c.err.Error())
		}
		// A CallerSafe error is the case where the author's own words are the contract — the
		// seam exists precisely so the text passes through untouched. Substituting there would
		// be a silent rewrite of a sentence somebody wrote for a session to read.
		if isCallerSafe(c.err) && got.Error() != c.err.Error() {
			t.Errorf("%s is CallerSafe but its text was rewritten.\n  wrote: %q\n  caller gets: %q",
				c.name, c.err.Error(), got.Error())
		}
	}

	// Refusals that MUST flatten, with the reason. A probing caller learns nothing from these.
	flatten := []struct {
		name, why string
		err       error
	}{
		{"wrapped-not-safe (deliver to X)", "an internal delivery failure names a party the caller may not know exists",
			fmt.Errorf("deliver to %s: %w", "s:a", comm.ErrBackpressure)},
		{"unknown sentinel", "the default branch: anything unnamed is deliberately opaque",
			errors.New("some new internal failure nobody has classified")},
	}
	for _, c := range flatten {
		got := commError(c.err)
		if got != nil && got.Error() == c.err.Error() {
			t.Errorf("%s now reaches the caller intact — it is on the flatten list because %s.\n"+
				"Either remove it from the list, or undo the change that made it caller-safe.", c.name, c.why)
		}
	}

	// POSITIVE CONTROL ON THE INSTRUMENT. If commError ever stopped flattening at all, every
	// assertion above would pass and this test would certify the opposite of its purpose.
	if got := commError(errors.New("zzz-unclassified-zzz")); got == nil || got.Error() == "zzz-unclassified-zzz" {
		t.Fatalf("commError no longer flattens an unknown error (%v) — this test cannot detect the class it guards", got)
	}
	if len(intact) < 14 {
		t.Fatalf("only %d caller-facing sentinels enumerated — the list has shrunk and this test is guarding less than it claims", len(intact))
	}
	_ = store.ErrStationArchived
}

// isCallerSafe mirrors what the mapper itself checks — the CallerSafeText interface, declared
// as a method precisely so a package outside comm can recognise the marker without importing a
// concrete type. Asserted through the same seam the production code uses, not a parallel one.
func isCallerSafe(err error) bool {
	var cs interface{ CallerSafeText() string }
	return errors.As(err, &cs)
}
