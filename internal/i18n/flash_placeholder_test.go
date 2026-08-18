package i18n

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// EVERY flash key that carries an argument must use {0}, and the argument must actually appear.
//
// Two keys added in 3.10.0 used `%s` — the wrong syntax entirely, since substitution here is
// positional-brace, not Printf. The mechanism those keys described worked perfectly: the console
// correctly refused to mint a token mixing knowledge-base and comm scopes. The operator was then
// shown a literal `%s` and learned nothing, and said so: "I don't really know what happened."
//
// That is the failure worth pinning. A refusal whose whole purpose is to EXPLAIN itself is not
// half-working when the explanation is missing — the explanation WAS the deliverable, and it is
// the only part an operator can see.
//
// ken-prod-ops found it, and diagnosed one broken key by comparing it against a sibling. BOTH
// were broken; the sibling only looked right because it had prose around its placeholder. So
// this walks every flash key in every shipped locale rather than the two that were reported.
func TestFlashPlaceholdersSubstitute(t *testing.T) {
	m := New("") // embedded bundle only
	const sentinel = "SENTINEL-ARG"

	for _, lang := range []string{"en", "es", "fr"} {
		keys := flashKeysWithArgs(t, lang)
		for _, key := range keys {
			got := m.T(lang, key, sentinel)
			if got == key {
				t.Errorf("%s/%s did not resolve at all", lang, key)
				continue
			}
			if !strings.Contains(got, sentinel) {
				t.Errorf("%s/%s does not substitute its argument: %q", lang, key, got)
			}
			for _, verb := range []string{"%s", "%v", "%d", "{0}"} {
				if strings.Contains(got, verb) {
					t.Errorf("%s/%s left %q in the rendered output: %q", lang, key, verb, got)
				}
			}
		}
	}
}

// flashKeysWithArgs reads the SHIPPED locale file and returns every flash.* key whose text
// expects an argument. Derived from the file rather than hard-coded, so a new key is covered the
// day it is added instead of the day someone remembers to add it here.
func flashKeysWithArgs(t *testing.T, lang string) []string {
	t.Helper()
	name := "messages.properties"
	if lang != "en" {
		name = "messages_" + lang + ".properties"
	}
	f, err := os.Open(filepath.Join("locales", name))
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	defer f.Close()

	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "flash.") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if strings.Contains(v, "{0}") || strings.Contains(v, "%s") {
			out = append(out, strings.TrimSpace(k))
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if len(out) == 0 {
		t.Fatalf("no argument-bearing flash keys found in %s — this test would pass vacuously", name)
	}
	return out
}
