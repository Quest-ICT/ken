package i18n

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestNoCommStringClaimsRetireSeversCommFails when an operator-facing COMM string says that
// RETIRING a credential ends the sessions it holds.
//
// IT DOES NOT, AND THIS PROJECT HAS NOW GOT IT WRONG IN BOTH DIRECTIONS. store.IsStationKeyRevoked
// reads `revoked_at` and nothing else, so a retired station key stops the STATION surface at the
// holder's next call and leaves every COMM endpoint it bound running. `stations.key_retire_help`
// states that correctly — "COMM endpoints it already bound keep working. Use Revoke instead only
// when you also want those severed."
//
// The first failure was the opposite: six shipped strings promised that retiring left live
// sessions alone, for four releases after the code stopped doing that, because the fix touched no
// .properties file (internal/store/stations.go:405-419 records it). The second was 3.20.0's, which
// promised severing where there is none — in `comm.credentials_help` and `comm.rebind_all_help`,
// in all three locales, on a card whose neighbouring string got it right.
//
// One sentence and one file apart, in opposite directions, three releases apart. Prose is where
// this defect lives, so the gate reads the prose.
func TestNoCommStringClaimsRetireSeversComm(t *testing.T) {
	// Only the COMM surface: `stations.*` legitimately discusses what retire does to a STATION,
	// which is to end it.
	subject := regexp.MustCompile(`^(comm|flash\.comm)[._]`)
	// "retir(e|ing|ed)" within a sentence that also promises an ending.
	retire := regexp.MustCompile(`(?i)\bretir`)
	severs := regexp.MustCompile(`(?i)(sever|revoke[ds]?|disconnect|ends? it|kill|cut off|corta|revoca|desconect|coupe|révoqu|déconnect|termina|met fin)`)
	// A sentence that DENIES severing is the correct one — "Retiring the key does not sever
	// them" is exactly what this gate wants to see survive. Negation is detected by marker
	// rather than parsed, which is approximate on purpose: the failure mode of a missed
	// negation is a human reading the diff, and the assertions this exists to catch
	// ("retiring either one ends it") carry no negation at all.
	denies := regexp.MustCompile(`(?i)(\bnot\b|n.t\b|\bnever\b|\bno l[oa]s?\b|\bne\b.{0,40}\bpas\b|\bsin\b)`)

	files, err := filepath.Glob("locales/messages*.properties")
	if err != nil || len(files) == 0 {
		t.Fatalf("no locale files found (%v) — the gate is broken, not the strings", err)
	}
	if len(files) < 3 {
		t.Fatalf("found %d locale files; this project ships three — the glob is wrong", len(files))
	}

	var bad []string
	var checked int
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(b), "\n") {
			k, v, ok := strings.Cut(line, " = ")
			if !ok || !subject.MatchString(strings.TrimSpace(k)) {
				continue
			}
			checked++
			if !retire.MatchString(v) {
				continue
			}
			// A string may mention retire in order to DISTINGUISH it. That reads as safe only
			// when the sentence carrying "retire" does not also promise an ending.
			for _, sentence := range regexp.MustCompile(`[.:;]\s+`).Split(v, -1) {
				if retire.MatchString(sentence) && severs.MatchString(sentence) && !denies.MatchString(sentence) {
					bad = append(bad, f+": "+strings.TrimSpace(k)+"\n      "+strings.TrimSpace(sentence))
				}
			}
		}
	}
	// POSITIVE CONTROL ON THE INSTRUMENT. A key-prefix change, a separator change, or a glob
	// that stops matching would make this pass by finding nothing to read — which is the failure
	// mode it exists to prevent everywhere else in this tree.
	if checked < 150 {
		t.Fatalf("only %d comm.* strings inspected across %d files; the parser is broken, not the strings", checked, len(files))
	}
	for _, b := range bad {
		t.Errorf("a COMM string says retiring a credential ends something. Retire spares COMM; only revoke "+
			"(or deleting the row) severs it — see stations.key_retire_help, which says so correctly:\n    %s", b)
	}
}
