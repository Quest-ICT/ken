package mcpserver

import (
	"strings"
	"testing"
)

// Every session is told it has no clock — including sessions that only ever touch the
// knowledge base.
//
// This paragraph lives in baseInstructions rather than in the COMM or station blocks
// because the defect it addresses is not about messaging or about stations. A session
// writing "this was fixed a few weeks ago" into an entry commits a drifting number to
// the durable record, in the same confident voice as the measured figures beside it,
// and the human promoting it has no way to see which is which.
//
// The wording was chosen by trial rather than by taste: three drafts were each handed
// to fresh agents as their connect-time instruction, with tasks that invite an
// unmeasured time claim. Every draft that ran produced an agent that went and read a
// clock. Two failure modes the trials exposed are answered explicitly here — a claim
// with the number hidden ("recently", "long-standing"), and a real timestamp welded to
// an unverified assumption about the interval — because both slipped through drafts
// that only forbade unmeasured durations.
func TestEverySessionIsToldItHasNoClock(t *testing.T) {
	// The universal block. Not the COMM or station text: a KB-only session gets
	// neither, and is exactly as likely to write a drifting age into an entry.
	got := buildInstructions()

	for _, want := range []struct{ frag, why string }{
		{"You have no clock",
			"the mechanism, which is what makes the rule survive pressure — a session that only knows the rule drops it when a number is convenient"},
		{"date -u",
			"the remedy, which has to be visibly cheap or it will not be reached for"},
		{"the number hidden",
			"the non-numeric case. A trial agent following an earlier draft still wrote 'checked recently' — the same claim in prose clothing, and it passed a rule that only forbade durations"},
		{"does not license a claim about the span",
			"inference dressed as measurement. A trial agent produced 'uninterrupted since <a real timestamp>' by reading one endpoint and assuming the interval, which a rule about reading clocks does not catch"},
		{"neither can your curator",
			"the honest limit. Ken cannot distinguish a measured figure from a generated one, and saying so is what makes the reading the session's job rather than the server's"},
	} {
		if !strings.Contains(got, want.frag) {
			t.Errorf("the connect-time instructions no longer carry %q.\n  That fragment is load-bearing: %s", want.frag, want.why)
		}
	}
}

// The paragraph must not crowd out the block it lives in. It competes for attention
// with the search-first habit and the curation gate, and length itself causes
// skimming — a instruction nobody finishes protects nothing.
func TestTheClockParagraphStaysProportionate(t *testing.T) {
	whole := buildInstructions()
	i := strings.Index(whole, "You have no clock")
	if i < 0 {
		t.Fatal("the clock paragraph is absent, so its size cannot be judged")
	}
	// The paragraph now sits LAST in the block, so "ends at a blank line" became "ends at a
	// blank line OR at the end of the text". It was moved there deliberately when the block was
	// refitted under version.InstructionBudget: everything before it is procedure a session
	// needs in order, and this is the one rule that governs every step rather than any one of
	// them. Both terminators are accepted rather than pinning the position, because where it
	// sits is an editorial call and the size limit below is the property under test.
	para := whole[i:]
	if end := strings.Index(para, "\n\n"); end >= 0 {
		para = para[:end]
	}

	if len(para) > 800 {
		t.Errorf("the clock paragraph is %d characters, which is long enough to be skimmed past; the block it sits in is %d",
			len(para), len(whole))
	}
	if share := float64(len(para)) / float64(len(whole)); share > 0.35 {
		t.Errorf("the clock paragraph is %.0f%% of everything Ken says on connect — it has stopped being one rule among several", share*100)
	}
}
