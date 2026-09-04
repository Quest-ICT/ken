package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func versionLang(t *testing.T, st *Store, vid int64) string {
	t.Helper()
	var l sql.NullString
	if err := st.R.QueryRow(`SELECT content_lang FROM entry_version WHERE id=?`, vid).Scan(&l); err != nil {
		t.Fatalf("read content_lang: %v", err)
	}
	return l.String
}

// TestContentLangDetectedOnSave: a new English entry is tagged 'en'.
func TestContentLangDetectedOnSave(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	sr, err := st.Save(ctx, SaveInput{
		Kind: "project", AuthorKind: "ai", Confidence: 0.6,
		Content: Content{
			Title:    "Fix the flaky login integration test",
			Summary:  "The login integration test fails intermittently because of a race on the session cookie value.",
			Solution: "Wait for the session cookie to be present before asserting the redirect target.",
		},
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if got := versionLang(t, st, sr.VersionID); got != "en" {
		t.Fatalf("content_lang = %q, want en", got)
	}
}

// TestSearchExposesLanguage: kb_search results carry the detected content
// language so a polyglot agent can spot a stranded foreign entry.
func TestSearchExposesLanguage(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	_, err := st.Save(ctx, SaveInput{
		Kind: "project", AuthorKind: "ai",
		Content: Content{
			Title:    "Fix the flaky login integration test",
			Summary:  "The login integration test fails intermittently because of a race on the session cookie value.",
			Solution: "Wait for the session cookie to be present before asserting the redirect target.",
		},
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	res, err := st.Search(ctx, "flaky login integration test session cookie race", SearchOpts{})
	if err != nil || len(res) == 0 {
		t.Fatalf("search: %v (%d results)", err, len(res))
	}
	if res[0].Language != "en" {
		t.Fatalf("SearchResult.Language = %q, want en", res[0].Language)
	}
}

// TestContentLangDeltaOnPropose: an enhancement's language is detected over the
// DELTA — a French addition to an English entry is tagged 'fr', not hidden by the
// merged English text; and a non-prose enhancement inherits the base language.
func TestContentLangDeltaOnPropose(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	sr, err := st.Save(ctx, SaveInput{
		Kind: "project", AuthorKind: "ai",
		Content: Content{
			Title:    "Fix the flaky login integration test",
			Summary:  "The login integration test fails intermittently because of a race on the session cookie value.",
			Solution: "Wait for the session cookie to be present before asserting the redirect target.",
		},
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	// A French caveats addition — the delta is French even though the merged entry
	// is mostly English.
	fr := "Attention : cette solution ne fonctionne pas avec les anciens navigateurs qui ne gèrent pas correctement les cookies de session."
	pr, err := st.ProposeEnhancement(ctx, ProposeInput{
		Slug: sr.Slug, ChangeNote: "add caveat", AuthorKind: "ai",
		Patch: Patch{Caveats: &fr},
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if got := versionLang(t, st, pr.VersionID); got != "fr" {
		t.Fatalf("delta content_lang = %q, want fr", got)
	}

	// A non-prose enhancement (tags only) inherits the base version's language.
	tags := []string{"login", "flaky"}
	pr2, err := st.ProposeEnhancement(ctx, ProposeInput{
		Slug: sr.Slug, BasedOnRev: sr.RevNo, ChangeNote: "retag", AuthorKind: "ai",
		Patch: Patch{Tags: &tags},
	})
	if err != nil {
		t.Fatalf("propose tags: %v", err)
	}
	if got := versionLang(t, st, pr2.VersionID); got != "en" {
		t.Fatalf("non-prose enhancement content_lang = %q, want inherited en", got)
	}
}

// TestPromoteComprehensionGate: a version in a non-curation language is refused;
// the same version promotes once its language is allowed; undetermined/off never
// block.
func TestComprehensionGateGuardsTheHumanNotTheWrite(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	frEntry := func(t *testing.T) SaveResult {
		sr, err := st.Save(ctx, SaveInput{
			Kind: "project", AuthorKind: "ai",
			Content: Content{
				Title:    "Corriger le test de connexion instable",
				Summary:  "Le test d'intégration de connexion échoue par intermittence à cause d'une condition de course sur le cookie de session.",
				Solution: "Attendre que le cookie de session soit présent avant de vérifier la redirection.",
			},
		})
		if err != nil {
			t.Fatalf("save fr: %v", err)
		}
		if got := versionLang(t, st, sr.VersionID); got != "fr" {
			t.Fatalf("expected fr entry, got content_lang=%q", got)
		}
		return sr
	}

	// *** THE COMPREHENSION GATE MOVED, AND IT NOW GUARDS THE HUMAN'S ACTION ONLY. ***
	//
	// It used to refuse a PROMOTE, which meant an out-of-language write was accepted, stored, and
	// then stranded forever: the only call that could publish it would always refuse. Under 6.0.0
	// a write is never refused for its language — refusing a write loses knowledge, which is the
	// one thing this release must not do — so the check survives exactly where it costs nothing:
	// on SetHead, a human deliberately choosing which version serves.
	sr := frEntry(t)
	sr2 := frEntry(t)
	_ = sr2

	// A French write LANDS under an English-only curation setting. It is the head immediately.
	if e, _ := st.GetEntry(ctx, sr.Slug); e.Head == nil || e.Head.RevNo != 1 {
		t.Fatal("a French entry did not land as its own head — the language check refused a write")
	}
	// Give it a second version so there is a non-head version to point at.
	fr2, err := st.ProposeEnhancement(ctx, ProposeInput{Slug: sr.Slug, ChangeNote: "suite",
		AuthorKind: "ai", Patch: Patch{Solution: strptr("Attendre le cookie de session avant de vérifier la redirection du navigateur.")}})
	if err != nil {
		t.Fatalf("fr revision: %v", err)
	}
	_ = fr2
	// The human CANNOT set the head back to a French version while curating only in English…
	if err := st.SetHead(ctx, PromoteInput{Slug: sr.Slug, VersionID: sr.VersionID, ActorKind: "human", CurationLangs: []string{"en"}}); !errors.Is(err, ErrForeignLang) {
		t.Fatalf("SetHead to fr under [en] = %v, want ErrForeignLang", err)
	}
	// …and CAN once French is a curation language.
	if err := st.SetHead(ctx, PromoteInput{Slug: sr.Slug, VersionID: sr.VersionID, ActorKind: "human", CurationLangs: []string{"fr", "en"}}); err != nil {
		t.Fatalf("SetHead to fr under [fr en] = %v, want ok", err)
	}

	// Undetermined language (short prose ⇒ 'und') fails open.
	su, err := st.Save(ctx, SaveInput{Kind: "reference", AuthorKind: "ai", Content: Content{Title: "TZ", Summary: "UTC"}})
	if err != nil {
		t.Fatalf("save short: %v", err)
	}
	if got := versionLang(t, st, su.VersionID); got != "und" {
		t.Fatalf("short prose content_lang = %q, want und", got)
	}
	// Undetermined fails OPEN, which is what keeps the entire legacy corpus and every short entry
	// reachable. Asserted through the write, since that is the path everything takes now.
	if e, _ := st.GetEntry(ctx, su.Slug); e.Head == nil {
		t.Fatal("an undetermined-language entry has no head — the check failed closed")
	}
}

func strptr(v string) *string { return &v }
