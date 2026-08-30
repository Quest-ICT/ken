// Package tooldoc holds the FULL rules for every Ken tool, and hands the tool list a short line.
//
// *** WHY THIS EXISTS: DETAIL IN A DESCRIPTION IS FROZEN; DETAIL IN A RESULT IS CURRENT. ***
//
// The 3.26.0 refit moved each tool's rules into its tool DESCRIPTION, which was right at the time:
// the connect-time instructions field is truncated by clients at ~2048 characters, and descriptions
// are not. But a description pins when a CONVERSATION begins and never refreshes — not on
// reconnect, not on upgrade — so every rule written there is permanently as old as the session
// reading it. Every stale-guidance defect this project has shipped is a description defect. Two
// machines were found serving pre-3.22.0 tool text against a fully patched server, in conversations
// that began after the upgrade.
//
// A TOOL RESULT is the one channel that is neither frozen nor truncated. So the rules live here and
// arrive through ken_instructions{tool:"…"}, and the description carries one line plus a pointer.
//
// *** ONE STRING PER TOOL, NOT TWO. ***
//
// The obvious shape — a Brief field and a Full field — is two strings that mean the same thing,
// which is the shape that drifts. Instead each tool defines its text ONCE, in full, at its
// registration site; Brief() computes the short line from it. A summary that is a literal prefix of
// the detail cannot disagree with it.
//
// Registration is a side effect of addTool in each server package, so no call site changed and no
// tool can be documented here and missing there, or the reverse.
package tooldoc

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

var (
	mu     sync.RWMutex
	docs   = map[string]string{}
	always = map[string]string{}
)

// Register records a tool's full rules under its name. The newest registration wins.
//
// *** IT PANICKED ON A CHANGED TEXT FOR ABOUT TEN MINUTES, AND THAT WAS WRONG. ***
//
// The idea was to catch two registration sites disagreeing about one tool. The first run found
// something better: kb_save's description is REBUILT AT RUNTIME from the configured curation
// languages, and mcpserver.SetCurationLangs constructs a whole new server when an operator changes
// them. So the same tool legitimately arrives here with different text, and refusing it would have
// crashed the process on a settings edit.
//
// Last-write-wins is not a retreat, it is the correct rule once that is known: the newest text is
// by definition the current one, and a live-rebuilt server is exactly the case where serving the
// older copy would be the defect. Drift between two SITES is not a live risk here — every tool has
// exactly one registration site — and the collision that was real (three packages registering
// ken_instructions under one name) is fixed where it belongs, by registering it once.
func Register(name, full string) {
	mu.Lock()
	defer mu.Unlock()
	docs[name] = full
}

// Full returns a tool's complete rules.
func Full(name string) (string, bool) {
	mu.RLock()
	defer mu.RUnlock()
	d, ok := docs[strings.TrimSpace(name)]
	return d, ok
}

// Names lists every tool that can be asked about, sorted so the answer is stable.
func Names() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(docs))
	for n := range docs {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// MustArrive marks one line as too important to wait for a pull, so Brief carries it into the tool
// list alongside the first sentence.
//
// *** IT EXISTS FOR EXACTLY ONE SHAPE, AND THE SHAPE IS WORTH NAMING. ***
//
// Nearly every rule is better delivered through ken_instructions, because a result is current and a
// description is frozen. The exception is a rule a session must obey the FIRST time it calls the
// tool, whether or not it thought to ask — where being frozen costs less than being unread.
//
// The curation language is the one that earned it. An operator declares that this knowledge base is
// curated in French; a session that writes an entry in English produces a proposal the curator
// cannot read and can never promote, and there is no error — the write succeeds. The session had no
// reason to suspect a language requirement existed, so "it could have called ken_instructions" is
// not a defence: you cannot pull an answer to a question you do not know to ask.
//
// This rule has already been lost once by being put somewhere plausible. It lived in the connect
// instructions, which the client TRUNCATES, and it was the first thing cut — so the operator who
// configured the feature was the only person whose sessions were guaranteed never to hear about it.
//
// KEEP THE LIST SHORT. Every line here is charged against the tool list on every conversation, and
// a "must arrive" that is merely useful reinstates the bloat this package was built to remove.
func MustArrive(name, line string) {
	mu.Lock()
	defer mu.Unlock()
	if strings.TrimSpace(line) == "" {
		delete(always, name)
		return
	}
	always[name] = strings.TrimSpace(line)
}

// pointer is appended to every short description. It is the only part of a tool's list entry that
// is not a prefix of its own rules, and it is what makes the shortening safe: a session reading a
// one-line description must be able to find out that there is more, and how to get it.
const pointer = " — FULL RULES: ken_instructions{tool:\"%s\"}, which are CURRENT (this line was captured when your conversation began and never refreshes)."

// Brief renders the one-line description for the tool list: the first sentence of the rules, plus
// the pointer to the rest.
//
// FIRST SENTENCE, because every one of these texts was written with its point up front — they were
// connect-time prose before they were tool descriptions. Where a first sentence would not stand
// alone, the fix is to rewrite the opening sentence of the RULES, which improves both renderings at
// once; there is deliberately no override table, because an override is the second string this
// package exists to avoid.
//
// The cut is on a sentence boundary followed by a space or end-of-text, so "kb_*." and "3.26.0."
// mid-sentence do not split it.
func Brief(name, full string) string {
	mu.RLock()
	extra := always[name]
	mu.RUnlock()
	b := firstSentence(full)
	if extra != "" {
		b += " " + extra
	}
	return b + fmt.Sprintf(pointer, name)
}

// firstSentence returns text up to and including the first sentence-ending punctuation that is
// followed by whitespace or the end of the string.
//
// A HARD FLOOR OF 40 CHARACTERS before it will accept a break, so an abbreviation in the opening
// words ("Ken 3.40.0. …") cannot truncate a description to nothing — the failure that would be
// invisible, because a too-short description still renders as a description.
//
// AND IT REFUSES TO BREAK ON A KNOWN ABBREVIATION, which the floor alone did not cover. kb_diff's
// shipped one-liner was "Field-by-field diff of two revisions of an entry (rev_a vs rev_b) — e.g."
// — 66 characters, comfortably over the floor, and a fragment ending mid-thought. The floor catches
// a stub; nothing caught a sentence that merely ENDS BADLY, because a fragment renders exactly like
// a sentence. Handled here rather than by rewriting the prose: an extractor that splits English
// wrongly will do it again to the next tool, and the next author will not know to avoid "e.g.".
func firstSentence(s string) string {
	s = strings.TrimSpace(s)
	for i, r := range s {
		if r != '.' && r != '!' && r != '?' {
			continue
		}
		if i < 40 {
			continue
		}
		rest := s[i+1:]
		if rest != "" && rest[0] != ' ' && rest[0] != '\n' {
			continue
		}
		if endsInAbbreviation(s[:i+1]) {
			continue
		}
		return strings.TrimSpace(s[:i+1])
	}
	return s
}

// abbreviations are the sentence-enders that are not sentence ends. Deliberately short: each entry
// is a place the extractor would otherwise cut mid-thought, and a long list would start swallowing
// genuine sentence breaks that happen to follow a short word.
var abbreviations = []string{"e.g.", "i.e.", "etc.", "cf.", "vs.", "approx.", "Fig.", "no."}

func endsInAbbreviation(s string) bool {
	low := strings.ToLower(s)
	for _, a := range abbreviations {
		if strings.HasSuffix(low, strings.ToLower(a)) {
			return true
		}
	}
	return false
}
