package i18n

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoOperatorStringNamesARetiredMechanism fails when console text describes something 4.0.0
// deleted.
//
// *** THIS REPLACES A DISCIPLINE, NOT A TEST. ***
//
// v3.42.0 had retire_revoke_test.go, a gate that read the PROSE of comm strings because that is
// where this defect lives — it had caught the same sentence being wrong in both directions three
// releases apart. The 4.0.0 wave deleted it, correctly, because the specific mechanism it policed
// (retiring a key severing comm) no longer exists. Nothing replaced the habit, and the tree then
// shipped 33 strings across three locales naming mechanisms the same release had removed:
// "Registered sessions" for a console where nothing registers, "This endpoint's secret has been
// replaced" for a secret that no longer exists, and — worst, because an operator would act on it —
// rooms.member_deaf_help telling them "the session needs to bind" when binding was deleted.
//
// A wrong label is cosmetic. AN INSTRUCTION TO PERFORM A DELETED ACTION IS NOT: it sends the one
// person who can fix the problem to look for a control that is not there, and they will conclude
// the product is broken rather than the sentence.
//
// PHRASES, NOT WORDS, and each is listed with what replaced it. Single words produce false
// positives that get the gate disabled — "registered" is legitimate for OAuth client registration,
// and "rotate" is legitimate in comm.credentials_help, which says there is nothing here to rotate.
// Scanning VALUES only, because key names like comm.stat_endpoints are internal identifiers no
// operator reads; renaming those is a refactor, not a truthfulness fix.
func TestNoOperatorStringNamesARetiredMechanism(t *testing.T) {
	retired := []struct{ phrase, replacedBy string }{
		{"registered session", "a station's mailbox, created by staffing it — comm_register is deleted"},
		{"sesiones registradas", "el buzón de un puesto — comm_register está eliminado"},
		{"sessions enregistrées", "la boîte d'une station — comm_register est supprimé"},
		{"endpoint's secret", "there is no endpoint secret; a mailbox belongs to a station"},
		{"secreto de este endpoint", "no hay secreto de endpoint"},
		{"secret de cet endpoint", "il n'y a pas de secret d'endpoint"},
		{"pairing code", "nothing — the first message creates the link"},
		{"código de emparejamiento", "nada — el primer mensaje crea el enlace"},
		{"code d'appairage", "rien — le premier message crée le lien"},
		{"voucher", "nothing — the binding voucher chain is deleted"},
		{"station key", "the OAuth grant; session_key selects the station"},
		{"clave de puesto", "la concesión OAuth"},
		{"clé de station", "la concession OAuth"},
		{"workspace", "station"},
		{"needs to bind", "needs a session to staff it once, which creates the mailbox"},
		{"necesita vincularse", "necesita que una sesión lo atienda una vez"},
		{"doit se lier", "doit être occupée une fois par une session"},
	}

	files, err := filepath.Glob("locales/messages*.properties")
	if err != nil || len(files) == 0 {
		t.Fatalf("no locale files found (%v) — the gate is broken, not the strings", err)
	}

	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for n, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			eq := strings.Index(line, "=")
			if eq < 0 {
				continue
			}
			value := strings.ToLower(line[eq+1:])
			for _, r := range retired {
				if strings.Contains(value, r.phrase) {
					t.Errorf("%s:%d names a mechanism 4.0.0 deleted: %q\n  key:  %s\n  say instead: %s",
						filepath.Base(f), n+1, r.phrase, strings.TrimSpace(line[:eq]), r.replacedBy)
				}
			}
		}
	}
}
