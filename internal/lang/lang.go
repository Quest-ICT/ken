// Package lang detects the dominant human language of prose, for Ken's
// curation-language guardrail (a curator can only promote what they can read).
//
// It wraps whatlanggo behind a tiny interface so the detector is swappable and
// unit-testable, and so callers depend on the interface rather than the library.
// Results are lowercased BCP-47 PRIMARY subtags ("en", "es", "fr", "zh") or
// [Und] when the text is too short or the guess is not reliable. Detection is
// open-set (it can recognize a language that is NOT one the curator reads), which
// is what a membership/flagging test actually needs — a biased classifier over a
// small known set could not tell "foreign" from "one of mine".
package lang

import (
	"strings"
	"unicode"

	"github.com/abadojack/whatlanggo"
)

// Und is the "undetermined" language tag: too short, no letters, or a
// low-confidence guess. The guardrail treats Und (and a NULL content_lang) as
// never-flagged and always-promotable — detection failures fail OPEN for
// availability, never trapping a proposal the curator could actually read.
const Und = "und"

// minLetters is the floor below which whatlanggo is too noisy to trust. A one- or
// two-word fragment routinely mis-detects; a dozen letters is a pragmatic cut.
const minLetters = 12

// Detector identifies the dominant language of prose text.
type Detector interface {
	// Detect returns a lowercased BCP-47 primary subtag, or [Und] when it can't
	// decide. It must be a pure function of text (no I/O, no state) so writes stay
	// cheap and deterministic.
	Detect(text string) string
}

// New returns the default whatlanggo-backed detector.
func New() Detector { return whatlang{} }

type whatlang struct{}

// Detect reports the dominant language of text as a lowercased ISO-639-1 code,
// or [Und] when the text is too short, has no letters, the guess is unreliable,
// or the detected language has no two-letter code (so it can never match a
// curation-language set keyed on primary subtags — treat as undetermined).
func (whatlang) Detect(text string) string {
	text = strings.TrimSpace(text)
	if letters(text) < minLetters {
		return Und
	}
	info := whatlanggo.Detect(text)
	if !info.IsReliable() {
		return Und
	}
	code := strings.ToLower(strings.TrimSpace(info.Lang.Iso6391()))
	if code == "" {
		return Und
	}
	return code
}

func letters(s string) int {
	n := 0
	for _, r := range s {
		if unicode.IsLetter(r) {
			n++
		}
	}
	return n
}
