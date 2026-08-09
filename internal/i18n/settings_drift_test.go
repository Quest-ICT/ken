package i18n_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Quest-ICT/ken/internal/settings"
)

// The guard that can see STALENESS, not merely absence.
//
// Ken already checked that translations were present. Presence was never the problem.
// Release 1.6.0 renamed settings and rewrote their help text in the Go registry, the
// bundles were not touched, and because the settings form resolves through the bundle
// FIRST — registry text is only a fallback (internal/web/app.go) — the console went on
// rendering the old names and the old SEMANTICS. `comm_metadata_ttl_sec.help` told
// operators in three languages that "message bodies are deleted at acknowledgement":
// the exact behaviour that release existed to remove, asserted as current, by the
// software that had stopped doing it.
//
// The fields ADDED in that release rendered correctly, precisely because nobody had
// translated them yet. That is the whole shape of it: a MISSING translation is safe,
// because it falls back and tells the truth. A translation that is PRESENT and stale
// is a lie the software repeats confidently, and a check that looks for absence
// cannot see it.
//
// Two halves, because the two languages are not the same problem:
//
//   - English duplicates the registry, so it is GENERATED and compared exactly.
//   - es/fr are translations and are SUPPOSED to differ from their source, so no
//     string comparison can work. Each carries a `#@src` fingerprint of the English
//     it was translated FROM.
func repoFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("locales", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func srcHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:12]
}

// entries pulls settings.field.* keys and values, ignoring comments the way the real
// parser does.
func entries(body string) map[string]string {
	out := map[string]string{}
	for _, l := range strings.Split(body, "\n") {
		if !strings.HasPrefix(l, "settings.field.") {
			continue
		}
		i := strings.Index(l, "=")
		if i < 0 {
			continue
		}
		out[strings.TrimSpace(l[:i])] = strings.ReplaceAll(strings.TrimSpace(l[i+1:]), `\n`, "\n")
	}
	return out
}

// English is derived from the Go registry, so drift is not merely detectable — it is
// unrepresentable, and this is what makes it so.
func TestEnglishSettingsTextIsGeneratedFromTheRegistry(t *testing.T) {
	got := entries(repoFile(t, "messages.properties"))

	for _, f := range settings.Fields {
		for part, want := range map[string]string{"label": f.Label, "help": f.Help} {
			key := "settings.field." + f.Key + "." + part
			have, ok := got[key]
			if !ok {
				t.Errorf("%s is absent from the English bundle.\n"+
					"  Every registry field must appear, or a translator copying this file cannot discover it.\n"+
					"  Fix: go run ./internal/i18n/i18nsync", key)
				continue
			}
			if have != want {
				t.Errorf("%s renders text the Go registry no longer says.\n"+
					"  console shows: %s\n"+
					"  registry says: %s\n"+
					"  The bundle WINS over the registry, so this is what the operator actually reads.\n"+
					"  Fix: go run ./internal/i18n/i18nsync", key, have, want)
			}
		}
	}
}

// The half no string comparison can do.
//
// A stamp records the English a translation was made FROM. It is hashed over the
// REGISTRY string, never over the English bundle entry — anchor it to the bundle and
// it stays green through the very commit that causes the drift, because such a commit
// changes the registry and leaves the bundle alone. That is not hypothetical; it is
// precisely what 1.6.0 did.
func TestTranslatedSettingsTextRecordsTheEnglishItWasMadeFrom(t *testing.T) {
	want := map[string]string{}
	for _, f := range settings.Fields {
		want["settings.field."+f.Key+".label"] = f.Label
		want["settings.field."+f.Key+".help"] = f.Help
	}

	for _, lang := range []string{"es", "fr"} {
		body := repoFile(t, "messages_"+lang+".properties")
		lines := strings.Split(body, "\n")
		stamp := ""
		checked := 0
		for _, l := range lines {
			trimmed := strings.TrimSpace(l)
			if strings.HasPrefix(trimmed, "#@src ") {
				stamp = strings.TrimSpace(strings.TrimPrefix(trimmed, "#@src "))
				continue
			}
			if !strings.HasPrefix(l, "settings.field.") {
				// A stamp applies to the entry directly beneath it and nothing else.
				if trimmed != "" {
					stamp = ""
				}
				continue
			}
			i := strings.Index(l, "=")
			if i < 0 {
				continue
			}
			key := strings.TrimSpace(l[:i])
			english, known := want[key]
			if !known {
				stamp = ""
				continue
			}
			checked++
			if now := srcHash(english); stamp != now {
				where := "it carries no #@src at all, so nothing records what it was translated from"
				if stamp != "" {
					where = "its #@src is " + stamp + ", but the registry English now hashes to " + now
				}
				t.Errorf("%s [%s] may no longer say what the English says.\n"+
					"  %s\n"+
					"  registry English is now: %s\n"+
					"  This is the ONLY signal for a stale translation — the text itself is supposed to differ,\n"+
					"  so nothing else can tell a good translation from one made before the English changed.\n"+
					"  Read the English, correct the translation, THEN:\n"+
					"    go run ./internal/i18n/i18nsync -stamp %s -key %s",
					key, lang, where, english, lang, key)
			}
			stamp = ""
		}
		// A guard over nothing passes. This is what stops the loop silently matching
		// no lines — a renamed prefix or a reformatted file would otherwise turn this
		// whole test into a green tick asserting nothing at all.
		if checked < 50 {
			t.Fatalf("%s: only %d settings entries were examined, which is far fewer than this bundle carries — "+
				"the scan is matching almost nothing and the test above proves nothing", lang, checked)
		}
	}
}

// Every group heading on the form needs a key too — the same defect one level up, and
// it was live twice over, both times INVISIBLE IN ENGLISH.
//
// "Stations" had no key in any bundle (8 registry groups, 7 keys). "Inter-session
// comms" had a key in all three that could never be reached, because the derivation
// collapsed only spaces and produced `inter-session_comms` against a bundle carrying
// `inter_session_comms`.
//
// Neither showed a symptom, because trOr falls back to the group's DISPLAY NAME — so
// an English operator saw the correct heading either way, and only a Spanish or French
// one saw an English word sitting among translated ones. That is the whole reason this
// test compares against the bundles rather than against a rendered page: the rendered
// page was right for the person most likely to look at it.
func TestEverySettingsGroupHasAHeading(t *testing.T) {
	groups := map[string]bool{}
	for _, f := range settings.Fields {
		groups[f.Group] = true
	}
	for _, lang := range []string{"", "_es", "_fr"} {
		body := repoFile(t, "messages"+lang+".properties")
		for g := range groups {
			// Mirrors internal/web.settingsGroupKey, including the hyphen collapse.
			key := "settings.group." + strings.NewReplacer(" ", "_", "-", "_").Replace(strings.ToLower(g))
			if !strings.Contains(body, key+" =") {
				t.Errorf("messages%s.properties has no %q — the %q heading renders as its own key name", lang, key, g)
			}
		}
	}
}
