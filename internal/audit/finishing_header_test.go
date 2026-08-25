package audit

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestFinishingHeaderIsNotStale fails when docs/FINISHING.md's status header disagrees with the
// CHANGELOG or with its own checkboxes.
//
// WHY A TEST AND NOT A RULE. FINISHING.md already carries the rule — "Every item AND the status
// header are updated in the SAME COMMIT as the work. This file can never be stale, because letting
// it go stale is the failure it exists to prevent." It has gone stale anyway, twice recorded in
// the file itself:
//
//   - four releases stale once, and every one of those four release commits EDITED the file
//     without touching the header, because the discipline attaches to CHECKBOXES — which sit next
//     to the work — and not to a paragraph that sits somewhere else;
//   - then stale again within 24 hours of being fixed, which is what produced the file's own
//     conclusion: "A rule is not a mechanism."
//
// The file states the remedy for a third recurrence: "the answer is to derive the line rather than
// write it." It recurred across 3.13.0 through 3.21.0 — nine releases, every one of which touched
// this file. This is that derivation, as a gate rather than a generator: the header stays
// hand-written, because the prose around the number is worth writing, and the NUMBERS in it are
// checked against the two sources that cannot drift.
//
// The same shape as every other check in this repository: a claim that cannot fail silently.
func TestFinishingHeaderIsNotStale(t *testing.T) {
	fin, err := os.ReadFile("../../docs/FINISHING.md")
	if err != nil {
		t.Fatalf("read FINISHING.md: %v", err)
	}
	chg, err := os.ReadFile("../../CHANGELOG.md")
	if err != nil {
		t.Fatalf("read CHANGELOG.md: %v", err)
	}

	// The newest released version: the first "## [x.y.z] — date" heading, skipping Unreleased.
	rel := regexp.MustCompile(`(?m)^## \[(\d+\.\d+\.\d+)\] — `).FindSubmatch(chg)
	if rel == nil {
		t.Fatal("no released version heading in CHANGELOG.md; the parser is broken, not the header")
	}
	newest := string(rel[1])

	// "Where we are today" is the block the rule is about.
	i := strings.Index(string(fin), "## Where we are today")
	if i < 0 {
		t.Fatal("FINISHING.md has no 'Where we are today' section; this gate no longer points at anything")
	}
	j := strings.Index(string(fin[i:]), "\n### ")
	if j < 0 {
		j = len(fin) - i
	}
	header := string(fin[i : i+j])

	if !strings.Contains(header, newest) {
		t.Errorf("FINISHING.md's status header does not mention %s, the newest release in CHANGELOG.md.\n"+
			"Rule 2: the header is updated in the SAME COMMIT as the work. Header begins:\n%s",
			newest, firstLines(header, 4))
	}

	// AND THE COUNT IT STATES MUST MATCH THE BOXES. The header claimed "Fifteen items remain
	// open" while seven were unchecked — a number nobody recomputed, in the sentence that tells
	// a reader how much is left.
	open := strings.Count(string(fin), "\n- [ ] ")
	if m := regexp.MustCompile(`(?i)\b(\d+|one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve|thirteen|fourteen|fifteen|sixteen|seventeen|eighteen|nineteen|twenty)\s+items?\s+remain`).FindStringSubmatch(header); m != nil {
		if got := wordNum(m[1]); got != open {
			t.Errorf("FINISHING.md's header says %q items remain; %d checkboxes are unchecked.\n"+
				"Recount when you tick one — the sentence telling a reader how much is left is the "+
				"one nobody recomputes.", m[1], open)
		}
	} else {
		t.Errorf("FINISHING.md's header no longer states how many items remain open (%d are). "+
			"That sentence is what this gate checks; do not remove it to silence the check.", open)
	}
}

func firstLines(s string, n int) string {
	parts := strings.SplitN(s, "\n", n+1)
	if len(parts) > n {
		parts = parts[:n]
	}
	return strings.Join(parts, "\n")
}

func wordNum(s string) int {
	words := map[string]int{"one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6,
		"seven": 7, "eight": 8, "nine": 9, "ten": 10, "eleven": 11, "twelve": 12, "thirteen": 13,
		"fourteen": 14, "fifteen": 15, "sixteen": 16, "seventeen": 17, "eighteen": 18,
		"nineteen": 19, "twenty": 20}
	if n, ok := words[strings.ToLower(s)]; ok {
		return n
	}
	n, _ := strconv.Atoi(s)
	return n
}
