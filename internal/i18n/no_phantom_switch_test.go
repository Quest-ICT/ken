package i18n

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestNoStringClaimsAFeatureIsSwitchedOff fails when operator-facing text tells someone a Ken
// feature is turned off, or can be.
//
// *** NOTHING TURNS COMM, STATIONS OR OAUTH OFF. *** cmd/ken/main.go says it outright — "THERE IS
// NO SWITCH. Ken shipped this opt-in, then briefly kept KEN_COMM_ENABLED as an opt-OUT; both are
// gone" — and cmd/ken/surfaces_core_test.go fails the build if anything reads a retired gate. When
// `a.comm` is nil it is because comm.db would not open, which main.go logs as "COMM: DEGRADED …
// This is a failure, not a setting".
//
// THREE STRINGS SAID OTHERWISE, IN ALL THREE LOCALES, AND THE WORST ONE WAS READ AT AN
// IRREVERSIBLE ACT: an operator who had just revoked a station link was told COMM was "switched
// off in this server", that live channels might still be open, and to "re-check from the Comm
// console once COMM is on." There is no switch to turn on — and `/comm` is not even routed while
// `a.comm` is nil, so the console they were sent to 404s. The real cause and the real remedy were
// named nowhere.
//
// `docs/FINISHING.md` records why the 3.10.0 sweep missed these: it grepped for the retired
// VARIABLE NAMES, and these strings describe the vanished switch without naming it. So this check
// reads the prose instead.
func TestNoStringClaimsAFeatureIsSwitchedOff(t *testing.T) {
	// Asserting a switch: "COMM is off", "switched off", "once COMM is on", "turn it on".
	claims := regexp.MustCompile(`(?i)(COMM (is|was) (switched )?off|COMM is on\b|stations? (is|are) (switched )?off|` +
		`OAuth is off|(switch|turn) (COMM|stations|OAuth) (back )?on|desactivad[oa].{0,12}COMM|COMM.{0,12}(apagad|désactiv)|` +
		`(éteint|apagado).{0,12}COMM)`)
	// Denying it in the same breath is the correct shape and must stay writable.
	denies := regexp.MustCompile(`(?i)(nothing turns|no switch|not a setting|fault, not|avería, no|panne, pas|` +
		`rien n.éteint|nada apaga|retired|removed|no longer|said|until 2026)`)

	files, err := filepath.Glob("locales/messages*.properties")
	if err != nil || len(files) < 3 {
		t.Fatalf("found %d locale files (%v); this project ships three — the glob is broken, not the strings", len(files), err)
	}
	var bad []string
	checked := 0
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(b), "\n") {
			if !strings.Contains(line, " = ") {
				continue
			}
			checked++
			if claims.MatchString(line) && !denies.MatchString(line) {
				k, _, _ := strings.Cut(line, " = ")
				bad = append(bad, filepath.Base(f)+": "+strings.TrimSpace(k))
			}
		}
	}
	// POSITIVE CONTROL on the parser, for the reason this whole family of checks exists.
	if checked < 1500 {
		t.Fatalf("only %d strings inspected across %d files; the parser is broken, not the strings", checked, len(files))
	}
	for _, b := range bad {
		t.Errorf("an operator-facing string says a Ken feature is switched off, or can be switched on:\n    %s\n"+
			"Nothing turns COMM, stations or OAuth off. If the surface is unavailable the database did not "+
			"open — say THAT, and point at the COMM: DEGRADED line, not at a switch the operator will hunt for "+
			"and never find.", b)
	}
}
