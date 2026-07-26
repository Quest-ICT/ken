package i18n

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEmbeddedDefaultsAndFallback(t *testing.T) {
	m := New("") // embedded only, no external dir
	if got := m.T("en", "nav.search"); got != "Search" {
		t.Fatalf("en nav.search = %q", got)
	}
	if got := m.T("es", "nav.search"); got != "Buscar" {
		t.Fatalf("es nav.search = %q", got)
	}
	// Missing key in es → English fallback.
	// (Use a key present only conceptually; pick one that exists in en.)
	if got := m.T("es", "action.top"); got != "Volver arriba" {
		t.Fatalf("es action.top = %q", got)
	}
	// Unknown key → returns the key itself (visible, never blank).
	if got := m.T("en", "no.such.key"); got != "no.such.key" {
		t.Fatalf("unknown key = %q", got)
	}
	// Unknown language → English fallback.
	if got := m.T("de", "nav.search"); got != "Search" {
		t.Fatalf("de fallback = %q", got)
	}
}

func TestPlaceholders(t *testing.T) {
	m := New("")
	// TN resolves key.one for n==1, key.other otherwise; {0} defaults to n.
	if got := m.TN("en", "nav.proposals_pending", 1); got != "1 pending" {
		t.Fatalf("en one = %q", got)
	}
	if got := m.TN("en", "nav.proposals_pending", 3); got != "3 pending" {
		t.Fatalf("en other = %q", got)
	}
	// Spanish agrees the noun in number: singular vs plural.
	if got := m.TN("es", "nav.proposals_pending", 1); got != "1 pendiente" {
		t.Fatalf("es one = %q", got)
	}
	if got := m.TN("es", "nav.proposals_pending", 3); got != "3 pendientes" {
		t.Fatalf("es other = %q", got)
	}
}

func TestLanguagesListEnglishFirst(t *testing.T) {
	m := New("")
	langs := m.Languages()
	if len(langs) < 2 || langs[0].Code != "en" {
		t.Fatalf("languages = %+v (want en first)", langs)
	}
	// Endonyms come from lang.self_name.
	names := map[string]string{}
	for _, l := range langs {
		names[l.Code] = l.Name
	}
	if names["en"] != "English" || names["es"] != "Español" {
		t.Fatalf("endonyms = %+v", names)
	}
}

func TestExternalOverrideAndReload(t *testing.T) {
	dir := t.TempDir()
	m := New(dir)
	// No external file yet → embedded value.
	if got := m.T("en", "nav.search"); got != "Search" {
		t.Fatalf("pre-override = %q", got)
	}

	// Drop an external override that (a) overrides English and (b) adds a new language.
	if err := os.WriteFile(filepath.Join(dir, "messages.properties"),
		[]byte("nav.search = Find\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "messages_fr.properties"),
		[]byte("lang.self_name = Français\nnav.search = Rechercher\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Force the throttle to elapse, then reload.
	m.mu.Lock()
	m.lastCheck = time.Now().Add(-time.Hour)
	m.mu.Unlock()
	m.MaybeReload()

	if got := m.T("en", "nav.search"); got != "Find" {
		t.Fatalf("override not applied: %q", got)
	}
	if !m.Has("fr") || m.T("fr", "nav.search") != "Rechercher" {
		t.Fatalf("new language fr not picked up")
	}
	// fr inherits English fallback for keys it didn't define.
	if got := m.T("fr", "login.submit"); got != "Sign in" {
		t.Fatalf("fr fallback = %q", got)
	}
	// The new language shows in the selector with its endonym.
	found := false
	for _, l := range m.Languages() {
		if l.Code == "fr" && l.Name == "Français" {
			found = true
		}
	}
	if !found {
		t.Fatal("fr missing from Languages()")
	}
}

func TestParseEscapesAndComments(t *testing.T) {
	kv := parse([]byte("# comment\n! also comment\n\nkey.a = line one\\nline two\nkey.b : colon-sep\nkey.c = caf\\u00e9\nbad line no sep\n"))
	if kv["key.a"] != "line one\nline two" {
		t.Fatalf("escape \\n: %q", kv["key.a"])
	}
	if kv["key.b"] != "colon-sep" {
		t.Fatalf("colon sep: %q", kv["key.b"])
	}
	if kv["key.c"] != "café" {
		t.Fatalf("unicode escape: %q", kv["key.c"])
	}
	if _, ok := kv["bad line no sep"]; ok {
		t.Fatal("line without separator should be skipped")
	}
}
