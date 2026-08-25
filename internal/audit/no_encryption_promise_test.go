package audit

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestNothingPromisesSnapshotEncryption fails when operator-facing text offers a control Ken
// removed in 2.0.0.
//
// *** THE WORST INSTANCE OF THIS PROJECT'S HOUSE DEFECT, BECAUSE THE CONTROL WAS ENCRYPTION. ***
//
// FINISHING.md names the class: "text asserting a control that does not exist is the class that
// propagates." Four separate places told an operator they could encrypt their nightly snapshots:
//
//	docs/INSTALL.md        a numbered procedure — install age, escrow a keypair, set a recipient
//	scripts/install.sh     the same procedure, printed on the terminal after every install
//	deploy/ken-snapshot.service   Description=Ken nightly ENCRYPTED snapshot, and a recommendation
//	docs/BACKUP.md         "Encryption is opt-in and OFF by default — on both off-box tiers"
//
// None of it was true. 2.0.0 retired KEN_AGE_RECIPIENT; scripts/ken-snapshot-lib.sh says "IT NO
// LONGER ENCRYPTS" in its own comment. The INSTALL procedure even warned that the step "fails
// closed" and keeps no snapshot if `age` is missing — false in the other direction: the plaintext
// is written and KEPT. And it linked to BACKUP.md#encryption-turning-it-on, a section that does
// not exist.
//
// **An operator who followed it escrowed a key, believed their off-box backups were ciphertext,
// and had plaintext** — of the whole knowledge base, the curator accounts, and every station vault
// secret. Found 2026-08-25 by an audit for this exact class, in a lens that had to be re-run after
// it died on a server error and its absence was nearly reported as "found nothing".
//
// The check is deliberately narrow: it fires on text that OFFERS the control, and ignores text
// that records its removal — the removal is what the documents are supposed to say.
func TestNothingPromisesSnapshotEncryption(t *testing.T) {
	// Offering it: an imperative or a recommendation aimed at the operator.
	offers := regexp.MustCompile(`(?i)(set an age recipient|configure an age recipient|until you configure|to encrypt them|encrypted snapshot|encryption is opt-in|age-keygen)`)
	// Recording that it is gone, judged ON THE SAME LINE — not over a window.
	//
	// A window was tried first and is WRONG here, for a reason worth keeping: a live false claim
	// sitting next to a historical note inherits the note's exemption. Verified by reintroducing
	// the exact sentence that shipped ("To encrypt them, install age and configure an age
	// recipient") beside the correction paragraph — the windowed check passed it. **An exemption
	// that spreads to its neighbours is not an exemption, it is a hole.**
	//
	// Blockquotes are exempt instead, which is this project's own convention for "here is what
	// this document used to say and why it was wrong" — the shape every correction note in the
	// tree already takes. That keeps the historical record writable without letting a live
	// imperative hide behind one.
	records := regexp.MustCompile(`(?i)(retired|no longer|cannot|not encrypt|removed|used to|does not exist|` +
		`was false|plaintext instead|is gone|2\.0\.0|said|carried a procedure|gave a procedure|` +
		`recommended|cost real production pain|\d{4}-\d{2}-\d{2})`)
	// A quoted historical note: markdown blockquote, or a shell/systemd comment that is quoting.
	quoted := regexp.MustCompile(`^\s*(>|#\s*(>|"))`)

	roots := []string{"../../docs", "../../scripts", "../../deploy", "../../configs"}
	var bad []string
	scanned := 0
	for _, root := range roots {
		_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			switch filepath.Ext(path) {
			case ".md", ".sh", ".service", ".timer", ".yml", ".yaml":
			default:
				return nil
			}
			b, rerr := os.ReadFile(path)
			if rerr != nil {
				return nil
			}
			scanned++
			for n, line := range strings.Split(string(b), "\n") {
				if offers.MatchString(line) && !records.MatchString(line) && !quoted.MatchString(line) {
					bad = append(bad, filepath.Base(path)+":"+itoa(n+1)+"  "+strings.TrimSpace(line))
				}
			}
			return nil
		})
	}
	// POSITIVE CONTROL. A walk that stops finding files passes by scanning nothing, which is the
	// failure mode this whole family of checks exists to prevent — and which this very audit hit
	// when a sweep agent died and returned an empty result indistinguishable from a clean one.
	if scanned < 20 {
		t.Fatalf("only %d files scanned across %v; the walk is broken, not the docs", scanned, roots)
	}
	for _, b := range bad {
		t.Errorf("operator-facing text offers snapshot encryption, which Ken removed in 2.0.0:\n    %s\n"+
			"Ken cannot encrypt a snapshot and no setting turns it on. If the line records the REMOVAL, "+
			"say so in the same line; if it offers the control, delete it — an operator who believes this "+
			"escrows a key and ships plaintext.", b)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
