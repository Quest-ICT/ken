package i18n

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// THE CONSENT SCREEN MUST NOT OFFER A CHOICE THE PAGE DOES NOT HAVE.
//
// It said "All of Ken, unless you untick something" and "Untick this to keep a cloud-hosted
// connector out of your local sessions" on a page with ZERO checkbox inputs — and a passing test
// asserted `name="ken_surface"` was absent, so the markup and the prose were each verified against
// something other than each other. It also promised "recorded on this approval, so you can see
// later exactly what was granted" while no template renders the grant's scope.
//
// This is the worst place in the product for text to be wrong. It is the one screen where a human
// makes a security decision, and the false sentence described the NARROWING action a cautious
// operator would reach for: someone who wanted to keep a cloud connector away from their local
// sessions was told to untick a box that does not exist, and approved the whole grant believing
// they had limited it.
//
// So the gate is the relationship, not the wording: if the prose offers per-surface choice, the
// template must render per-surface inputs.
func TestConsentTextDoesNotOfferChoicesThePageLacks(t *testing.T) {
	tpl, err := os.ReadFile(filepath.Join("..", "web", "templates", "consent.html"))
	if err != nil {
		t.Fatal(err)
	}
	hasInputs := strings.Contains(string(tpl), `type="checkbox"`)

	files, err := filepath.Glob(filepath.Join("locales", "messages*.properties"))
	if err != nil || len(files) < 3 {
		t.Fatalf("expected three locale files, found %d (%v)", len(files), err)
	}
	// Words that promise the reader a per-surface choice, in each shipped language.
	choice := []string{"untick", "unticked", "desmarcar", "desmarque", "décocher", "décochez"}
	var checked int
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(b), "\n") {
			key, value, ok := strings.Cut(line, " = ")
			if !ok || !strings.HasPrefix(strings.TrimSpace(key), "consent.") {
				continue
			}
			checked++
			low := strings.ToLower(value)
			for _, w := range choice {
				if strings.Contains(low, w) && !hasInputs {
					t.Errorf("%s: %s tells the operator to untick something, and consent.html renders "+
						"no checkbox:\n    %s\nThe sentence describes the NARROWING action a cautious "+
						"operator would take, so being wrong here means they approve everything "+
						"believing they limited it.", filepath.Base(f), strings.TrimSpace(key), strings.TrimSpace(value))
				}
			}
		}
	}
	// POSITIVE CONTROL: a filter that stopped matching would make every assertion above vacuous.
	if checked < 60 {
		t.Fatalf("only %d consent strings inspected across three locales; the parser is broken", checked)
	}
}
