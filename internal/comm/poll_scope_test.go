package comm

import (
	"context"
	"errors"
	"testing"
)

// THE GRAMMAR OF THE FILTER, AT THE LAYER THAT ENFORCES IT.
//
// PollScoped refuses a scope naming no namespace rather than filtering to nothing, because
// otherwise "no such scope" and "nothing waiting" are the same empty list and the caller
// cannot tell which it got. Both arms are here on purpose: every refusal, and the accepted
// forms that prove the refusals are not simply "PollScoped always errors".
func TestPollScopedRefusesAScopeThatNamesNoNamespace(t *testing.T) {
	ctx := context.Background()
	st := newStore(t, DefaultLimits())
	a, b, channelID := pair(t, st)
	if _, err := st.Send(ctx, a, channelID, "one", SendOpts{}); err != nil {
		t.Fatal(err)
	}

	for _, bad := range []string{"ops", "ch:", "r:", "b:", "p:", "p:only-one", "p:|b", "p:a|", "x:1", " ch:1"} {
		if _, err := st.PollScoped(ctx, b, 10, bad); !errors.Is(err, ErrBadScope) {
			t.Errorf("PollScoped(scope=%q) returned %v, want ErrBadScope — an unparseable filter that "+
				"returns an empty list is indistinguishable from an empty inbox", bad, err)
		}
	}

	// ACCEPTED, AND ACTUALLY NARROWING.
	got, err := st.PollScoped(ctx, b, 10, "ch:"+channelID)
	if err != nil {
		t.Fatalf("a well-formed channel scope was refused: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("the accepted scope returned %d messages, want 1 — the refusals above prove nothing "+
			"if nothing is ever accepted", len(got))
	}

	// WELL-FORMED BUT UNKNOWN IS AN EMPTY RESULT, NOT A REFUSAL: erroring here would turn the
	// filter into an existence oracle for ids the caller may not hold.
	got, err = st.PollScoped(ctx, b, 10, "r:no-such-room")
	if err != nil || len(got) != 0 {
		t.Fatalf("PollScoped(r:no-such-room) = %d messages, %v — want an ordinary empty result", len(got), err)
	}

	// AND AN EMPTY SCOPE IS NOT A FILTER: every existing caller keeps the shipped behaviour,
	// which is what lets Poll delegate here without touching 77 call sites.
	got, err = st.PollScoped(ctx, b, 10, "")
	if err != nil || len(got) != 1 {
		t.Fatalf("PollScoped with no scope returned %d messages, %v — want the unfiltered behaviour", len(got), err)
	}
}
