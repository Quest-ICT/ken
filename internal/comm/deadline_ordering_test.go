package comm

import (
	"strings"
	"testing"
)

// A REPLY DEADLINE MUST NOT OUTLIVE THE MESSAGE IT IS ABOUT.
//
// Longer, and the body is destroyed before the deadline arrives, so reply_overdue asks a
// sender to chase an answer to text nobody can read any more. ken-prod-ops found it live on
// 2026-08-20 while tracing a notice I could not explain: comm_reply_deadline_sec 604800
// against comm_message_ttl_sec 259200, so EVERY unanswered requires_response message there
// produced a notice about a body that had expired four days earlier.
//
// The shipped defaults have it right — 3600 inside 86400 — which is exactly why nothing
// caught it. A check that only ever runs against sound values is not a check.
func TestReplyDeadlineOutlivingTheMessageIsReported(t *testing.T) {
	// THE SHIPPED DEFAULTS MUST BE SILENT. This is the control: without it, a function
	// that returned a warning unconditionally would pass every assertion below.
	if got := CheckDeadlineOrdering(DefaultLimits()); got != "" {
		t.Fatalf("the shipped defaults produce a warning (%q) — either the defaults are wrong or "+
			"the check fires on everything, and both make the positive case below meaningless", got)
	}

	// The live production configuration that produced the notice.
	prod := DefaultLimits()
	prod.MessageTTLSeconds = 259200    // 3 days
	prod.ReplyDeadlineSeconds = 604800 // 7 days
	got := CheckDeadlineOrdering(prod)
	if got == "" {
		t.Fatal("a 7-day reply deadline inside a 3-day message TTL was reported as sound; " +
			"that is the configuration that generates notices about destroyed bodies")
	}
	for _, want := range []string{"604800", "259200", "345600"} {
		if !strings.Contains(got, want) {
			t.Errorf("the warning does not carry %s, so an operator cannot see which value to "+
				"move or by how much: %q", want, got)
		}
	}

	// EQUAL IS SOUND, not a warning: the deadline arrives exactly as the body dies. The
	// boundary is where an off-by-one would hide.
	eq := DefaultLimits()
	eq.MessageTTLSeconds, eq.ReplyDeadlineSeconds = 3600, 3600
	if got := CheckDeadlineOrdering(eq); got != "" {
		t.Errorf("equal values warned: %q", got)
	}

	// Zero means "unset / feature off" on both, and must not warn — an operator who has
	// configured neither has not made a mistake.
	for _, l := range []Limits{
		{MessageTTLSeconds: 86400, ReplyDeadlineSeconds: 0},
		{MessageTTLSeconds: 0, ReplyDeadlineSeconds: 604800},
	} {
		if got := CheckDeadlineOrdering(l); got != "" {
			t.Errorf("an unset value warned (%+v): %q", l, got)
		}
	}
}
