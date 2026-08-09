package web

import (
	"strings"
	"testing"

	"github.com/Quest-ICT/ken/internal/i18n"
	"github.com/Quest-ICT/ken/internal/settings"
)

// A validation error must name its fields the way the FORM names them, in whatever
// language the form was rendered in.
//
// The defect this pins was found by Vlad from the console, minutes after 1.6.0: he
// went looking for a field the error told him to change and it was not there. The
// message was built in internal/settings out of the Go registry label, while the form
// beside it resolves labels through the translation bundle — so the error named
// "Lifetime after delivery" on a page whose field was labelled "Message lifetime",
// and the field he actually had to change was sitting directly above the message
// under a different name.
//
// It misnamed 2 of 43 fields for an English operator and 31 of 43 for every Spanish
// or French one, because es/fr translate the labels and could never match a literal
// English string.
func TestValidationErrorsNameFieldsAsTheFormDoes(t *testing.T) {
	tr := i18n.New("")

	errs := []settings.FieldError{{
		Key:        "comm_undelivered_ttl_sec",
		Refs:       []string{"comm_message_ttl_sec"},
		Message:    "{0} (604800s) must be at least {1} (2592000s) — lower {1} first.",
		Standalone: true,
	}}

	// Resolve the labels the same way the form does, so the expectation is not a
	// second hardcoded copy of the very thing that drifted.
	wantKey := trOr(tr, "en", "settings.field.comm_undelivered_ttl_sec.label", "")
	wantRef := trOr(tr, "en", "settings.field.comm_message_ttl_sec.label", "")
	if wantKey == "" || wantRef == "" {
		t.Fatal("setup: the bundle has no label for one of these fields, so this test would pass by comparing empty strings")
	}

	got := renderFieldErrors(tr, "en", errs)
	if len(got) != 1 {
		t.Fatalf("got %d rendered errors, want 1", len(got))
	}
	for _, want := range []string{wantKey, wantRef} {
		if !strings.Contains(got[0], want) {
			t.Errorf("the rendered error does not name %q — the operator is told to change a field that is not on their screen.\ngot: %s", want, got[0])
		}
	}
	if strings.Contains(got[0], "{0}") || strings.Contains(got[0], "{1}") {
		t.Errorf("a placeholder survived into the operator's message: %s", got[0])
	}

	// The half that only shows up in another language. Spanish translates these
	// labels, so a message carrying English names would be visibly wrong — and it is
	// the case that was wrong for 31 of 43 fields.
	es := renderFieldErrors(tr, "es", errs)
	esKey := trOr(tr, "es", "settings.field.comm_undelivered_ttl_sec.label", "")
	if esKey == wantKey {
		t.Skip("the Spanish bundle does not translate this label, so this half cannot discriminate")
	}
	if !strings.Contains(es[0], esKey) {
		t.Errorf("the Spanish error names the field in English (%q), not as the Spanish form labels it (%q).\ngot: %s", wantKey, esKey, es[0])
	}
	if strings.Contains(es[0], wantKey) {
		t.Errorf("the Spanish error still carries the English label %q: %s", wantKey, es[0])
	}
}

// A per-field error carries a bare reason and must be prefixed with the field's
// resolved label — the opposite of a cross-field one, which names itself via {0}.
// Getting this backwards produces either an unattributed sentence or a doubled name.
func TestPerFieldErrorIsPrefixedWithTheResolvedLabel(t *testing.T) {
	tr := i18n.New("")
	got := renderFieldErrors(tr, "en", []settings.FieldError{
		{Key: "comm_message_ttl_sec", Message: "must be between 60 and 2592000"},
	})
	if len(got) != 1 {
		t.Fatalf("got %d, want 1", len(got))
	}
	want := trOr(tr, "en", "settings.field.comm_message_ttl_sec.label", "")
	if !strings.HasPrefix(got[0], want+": ") {
		t.Errorf("a per-field error is not attributed to its field.\ngot:  %s\nwant prefix: %s: ", got[0], want)
	}
	if strings.Count(got[0], want) != 1 {
		t.Errorf("the field name appears %d times — Standalone is being applied backwards: %s", strings.Count(got[0], want), got[0])
	}
}

// An error with no field at all (a save failure) must pass through untouched rather
// than being labelled with something invented.
func TestUnattributedErrorIsNotLabelled(t *testing.T) {
	tr := i18n.New("")
	got := renderFieldErrors(tr, "en", []settings.FieldError{
		{Message: "could not save: disk full", Standalone: true},
	})
	if len(got) != 1 || got[0] != "could not save: disk full" {
		t.Errorf("an unattributed error was rewritten: %v", got)
	}
}
