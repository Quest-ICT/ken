// Command i18nsync keeps the settings translations honest against the Go registry.
//
// THE DEFECT THIS EXISTS FOR. The settings form resolves each field's label and help
// through the translation bundle FIRST, with the Go registry text only as a fallback
// (internal/web/app.go). So a bundle entry does not merely duplicate the registry —
// it OVERRIDES it. Release 1.6.0 renamed and rewrote registry entries and left the
// bundles alone, and the console went on rendering the old names and, worse, the old
// SEMANTICS: `comm_metadata_ttl_sec.help` told operators in three languages that
// "message bodies are deleted at acknowledgement", which is the exact behaviour that
// release existed to remove. Five fields, seven entries. The two fields ADDED in the
// same release rendered correctly, precisely because nobody had translated them yet.
//
// A translation that is merely MISSING is safe — it falls back and tells the truth. A
// translation that is PRESENT and stale is a lie the software repeats confidently.
// Ken's existing checks looked for absence, so they could not see this at all.
//
// WHAT THIS TOOL DOES, and the asymmetry that shapes it:
//
//   - ENGLISH is a pure duplicate of the registry, so it is GENERATED. Run this and
//     every `settings.field.*` entry in messages.properties is rewritten from
//     settings.Fields. English drift stops being something to detect and becomes
//     something that cannot be represented: the file is derived, and the test
//     regenerates it in memory and diffs.
//
//   - es/fr are TRANSLATIONS. They are SUPPOSED to differ from their source, so no
//     string comparison can tell a good translation from a stale one. Each entry
//     instead carries a `#@src <hash>` comment recording the English it was
//     translated FROM. When the registry text changes, the hash no longer matches and
//     the test fails naming the key, the language, and both English texts.
//
// The hash is over the REGISTRY string, never over the English bundle entry. Anchor
// it to the bundle and it stays green through the very commit that causes the drift —
// because that commit changes the registry and leaves the bundle alone, which is
// exactly what happened in 1.6.0.
//
// WHAT THIS TOOL DELIBERATELY WILL NOT DO: update a `#@src` in bulk. A stamp says "a
// human or a session read the new English and confirmed this translation still says
// it". A `-stamp-all` flag would make that claim about 58 strings in one keystroke,
// and the first person facing a wall of failures would reach for it — converting a
// guard into a green tick that means nothing. Stamping is one key at a time, on
// purpose, and the friction IS the feature. There is no flag to remove it.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Quest-ICT/ken/internal/settings"
)

const (
	localesDir = "internal/i18n/locales"
	englishFwd = "messages.properties"
	stampMark  = "#@src "
	keyPrefix  = "settings.field."
)

// translated lists the non-English bundles that carry stamps.
var translated = []string{"es", "fr"}

// srcHash fingerprints the English source a translation was made from. Truncated
// because it is a change detector, not a security primitive: 12 hex characters is
// ~48 bits, and the population is under a hundred strings.
func srcHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:12]
}

// registryText returns the English for every settings.field.* key.
func registryText() map[string]string {
	out := map[string]string{}
	for _, f := range settings.Fields {
		out[keyPrefix+f.Key+".label"] = f.Label
		out[keyPrefix+f.Key+".help"] = f.Help
	}
	return out
}

func main() {
	stampLang := flag.String("stamp", "", "language of a single entry to re-stamp, after retranslating it (es|fr)")
	stampKey := flag.String("key", "", "the settings.field.* key to re-stamp; one key per run, by design")
	check := flag.Bool("check", false, "report drift and exit non-zero, changing nothing")
	flag.Parse()

	root, err := repoRoot()
	if err != nil {
		fail(err)
	}
	reg := registryText()

	if *stampLang != "" || *stampKey != "" {
		if *stampLang == "" || *stampKey == "" {
			fail(fmt.Errorf("-stamp and -key go together: name the language AND the one key you retranslated"))
		}
		if err := stampOne(root, *stampLang, *stampKey, reg); err != nil {
			fail(err)
		}
		fmt.Printf("stamped %s [%s]\n", *stampKey, *stampLang)
		return
	}

	problems, err := run(root, reg, *check)
	if err != nil {
		fail(err)
	}
	for _, p := range problems {
		fmt.Fprintln(os.Stderr, p)
	}
	if len(problems) > 0 {
		os.Exit(1)
	}
	fmt.Println("settings translations are in sync with the registry")
}

// run regenerates English and reports translation drift. With check=true it writes
// nothing, which is what the test and CI use.
func run(root string, reg map[string]string, check bool) ([]string, error) {
	var problems []string

	// English: generated. Rewritten in place, key by key, rather than into a
	// banner-delimited block — the entries are interleaved with group keys, enum
	// values and comments across 80 other lines, so a block would mean restructuring
	// the file for no gain. In-place keeps the diff to the lines that actually changed.
	enPath := filepath.Join(root, localesDir, englishFwd)
	body, err := os.ReadFile(enPath)
	if err != nil {
		return nil, err
	}
	rewritten, changed, missing := regenerateEnglish(string(body), reg)
	if len(changed) > 0 || len(missing) > 0 {
		if check {
			for _, k := range changed {
				problems = append(problems, fmt.Sprintf("en: %s disagrees with the Go registry", k))
			}
			for _, k := range missing {
				problems = append(problems, fmt.Sprintf("en: %s is absent", k))
			}
		} else if err := os.WriteFile(enPath, []byte(rewritten), 0o644); err != nil {
			return nil, err
		}
	}

	// Translations: never rewritten, only checked. Rewriting one would mean writing a
	// translation, which this tool cannot do.
	for _, lang := range translated {
		p := filepath.Join(root, localesDir, "messages_"+lang+".properties")
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		for _, d := range driftIn(string(b), reg) {
			problems = append(problems, fmt.Sprintf("%s: %s\n    was translated from: %s\n    registry now says : %s\n    fix the translation, then: go run ./internal/i18n/i18nsync -stamp %s -key %s",
				lang, d.key, d.stampedFrom, d.now, lang, d.key))
		}
	}
	return problems, nil
}

// regenerateEnglish rewrites every settings.field.* line from the registry and
// appends any that are absent. Returns the new body plus what it had to touch.
func regenerateEnglish(body string, reg map[string]string) (string, []string, []string) {
	lines := strings.Split(body, "\n")
	seen := map[string]bool{}
	var changed []string
	for i, l := range lines {
		if !strings.HasPrefix(l, keyPrefix) {
			continue
		}
		k, _, ok := splitEntry(l)
		if !ok {
			continue
		}
		want, known := reg[k]
		if !known {
			// A bundle key with no registry field: a setting that was removed. Left
			// alone rather than deleted — this tool's job is drift, and silently
			// dropping a line an operator may have translated is a different act.
			continue
		}
		seen[k] = true
		if want != valueOf(l) {
			changed = append(changed, k)
			lines[i] = k + " = " + escapeValue(want)
		}
	}
	var missing []string
	for k := range reg {
		if !seen[k] {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		add := []string{"", "# Added by i18nsync from settings.Fields — English here is GENERATED, not authored.", "# Edit the Go registry, then run: go run ./internal/i18n/i18nsync"}
		for _, k := range missing {
			add = append(add, k+" = "+escapeValue(reg[k]))
		}
		body = strings.TrimRight(strings.Join(lines, "\n"), "\n") + "\n" + strings.Join(add, "\n") + "\n"
		return body, changed, missing
	}
	return strings.Join(lines, "\n"), changed, missing
}

type drift struct{ key, stampedFrom, now string }

// driftIn reports entries whose recorded source no longer matches the registry, and
// entries carrying no stamp at all. An unstamped entry counts as drift: "we do not
// know what this was translated from" and "it is stale" are the same actionable
// state, and treating absence as fine is the exact hole this tool exists to close.
func driftIn(body string, reg map[string]string) []drift {
	lines := strings.Split(body, "\n")
	stamp := ""
	var out []drift
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, stampMark) {
			stamp = strings.TrimSpace(strings.TrimPrefix(t, stampMark))
			continue
		}
		if !strings.HasPrefix(l, keyPrefix) {
			if t != "" {
				stamp = "" // a stamp only applies to the line directly after it
			}
			continue
		}
		k, _, ok := splitEntry(l)
		if !ok {
			continue
		}
		now, known := reg[k]
		if !known {
			stamp = ""
			continue
		}
		if want := srcHash(now); stamp != want {
			from := "(never stamped)"
			if stamp != "" {
				from = "an earlier English text (stamp " + stamp + ", registry is now " + want + ")"
			}
			out = append(out, drift{key: k, stampedFrom: from, now: now})
		}
		stamp = ""
	}
	return out
}

// stampOne records that ONE key's translation has been checked against the current
// English. One key per invocation is the whole point; see the package comment.
func stampOne(root, lang, key string, reg map[string]string) error {
	want, ok := reg[key]
	if !ok {
		return fmt.Errorf("%s is not a settings field in the Go registry", key)
	}
	p := filepath.Join(root, localesDir, "messages_"+lang+".properties")
	b, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	lines := strings.Split(string(b), "\n")
	for i, l := range lines {
		if !strings.HasPrefix(l, key+" ") && !strings.HasPrefix(l, key+"=") {
			continue
		}
		mark := stampMark + srcHash(want)
		if i > 0 && strings.HasPrefix(strings.TrimSpace(lines[i-1]), stampMark) {
			lines[i-1] = mark
		} else {
			lines = append(lines[:i], append([]string{mark}, lines[i:]...)...)
		}
		return os.WriteFile(p, []byte(strings.Join(lines, "\n")), 0o644)
	}
	return fmt.Errorf("%s has no entry for %s — nothing to stamp", lang, key)
}

func splitEntry(l string) (key, val string, ok bool) {
	i := strings.Index(l, "=")
	if i < 0 {
		return "", "", false
	}
	return strings.TrimSpace(l[:i]), strings.TrimSpace(l[i+1:]), true
}

func valueOf(l string) string {
	_, v, _ := splitEntry(l)
	return unescapeValue(v)
}

// escapeValue/unescapeValue handle the only escape the settings text can contain: a
// literal newline would break the one-line-per-entry format. Accents travel as
// literal UTF-8 — every bundle here already does that and says so in its header.
func escapeValue(s string) string {
	return strings.ReplaceAll(s, "\n", `\n`)
}
func unescapeValue(s string) string {
	return strings.ReplaceAll(s, `\n`, "\n")
}

// repoRoot walks up for go.mod so the tool works from any directory.
func repoRoot() (string, error) {
	d, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d, nil
		}
		p := filepath.Dir(d)
		if p == d {
			return "", fmt.Errorf("no go.mod above %s — run this from inside the repository", d)
		}
		d = p
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "i18nsync:", err)
	os.Exit(1)
}
