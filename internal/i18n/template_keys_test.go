package i18n

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// EVERY KEY A TEMPLATE ASKS FOR MUST EXIST IN THE ENGLISH BUNDLE.
//
// T returns the KEY when a lookup misses (i18n.go:88), which is the right runtime
// behaviour — a page with one raw identifier on it beats a 500 — and it means a
// forgotten string is invisible to every other test. The page renders, the handler
// returns 200, the suite is green, and the console shows `stations.vault_help` to the
// operator.
//
// This is the same shape as the settings drift test next door, moved one layer out: that
// one proves the settings REGISTRY is fully translated, and nothing proved it for the
// templates, which is where most of the visible text lives. It was written after adding
// a console section whose twenty-two strings would have shipped as raw keys with a clean
// test run.
//
// Only the English bundle is required. Translations lag deliberately and fall back to
// English, so a missing Spanish string is a gap; a missing English one is a bug.
func TestEveryTemplateKeyExistsInEnglish(t *testing.T) {
	dir := filepath.Join("..", "web", "templates")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("templates not readable from here: %v", err)
	}

	m := New("")
	// CONTROL: the scan must actually find keys. Without this, a change to the template
	// syntax, the directory, or the regexp would empty the corpus and this test would
	// pass by examining nothing — which is precisely the failure it exists to catch,
	// committed against itself.
	found := 0

	// {{.T "key"}}, {{$.T "key"}}, {{$.T "key" arg}} and the TN plural form.
	re := regexp.MustCompile(`\{\{-?\s*\$?\.T[Nn]?\s+"([a-zA-Z0-9_.]+)"`)

	var missing []string
	seen := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".html") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, mt := range re.FindAllSubmatch(body, -1) {
			key := string(mt[1])
			found++
			if seen[key] {
				continue
			}
			seen[key] = true
			// A plural key is stored as key.one / key.other, so probe a form that
			// exists rather than the stem, which never does.
			if _, ok := m.lookup("en", key); ok {
				continue
			}
			if _, ok := m.lookup("en", key+".other"); ok {
				continue
			}
			missing = append(missing, e.Name()+": "+key)
		}
	}

	if found < 100 {
		t.Fatalf("the scan found only %d template keys — it is looking in the wrong place or the pattern no longer matches, and this test proves nothing", found)
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("%d template keys are absent from the English bundle, so the console renders the KEY where the text should be:\n  %s",
			len(missing), strings.Join(missing, "\n  "))
	}
}
