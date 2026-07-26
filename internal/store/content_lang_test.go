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
	if err := st.Promote(ctx, PromoteInput{Slug: sr.Slug, VersionID: sr.VersionID, ActorKind: "human"}); err != nil {
		t.Fatalf("promote: %v", err)
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
	if err := st.Promote(ctx, PromoteInput{Slug: sr.Slug, VersionID: sr.VersionID, ActorKind: "human"}); err != nil {
		t.Fatalf("promote: %v", err)
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
func TestPromoteComprehensionGate(t *testing.T) {
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

	// English curator can't promote a French proposal…
	sr := frEntry(t)
	err := st.Promote(ctx, PromoteInput{Slug: sr.Slug, VersionID: sr.VersionID, ActorKind: "human", CurationLangs: []string{"en"}})
	if !errors.Is(err, ErrForeignLang) {
		t.Fatalf("promote of fr under [en] = %v, want ErrForeignLang", err)
	}
	// …but the SAME version promotes once French is a curation language.
	if err := st.Promote(ctx, PromoteInput{Slug: sr.Slug, VersionID: sr.VersionID, ActorKind: "human", CurationLangs: []string{"fr", "en"}}); err != nil {
		t.Fatalf("promote of fr under [fr en] = %v, want ok", err)
	}

	// Feature off (no curation langs) never blocks.
	sr2 := frEntry(t)
	if err := st.Promote(ctx, PromoteInput{Slug: sr2.Slug, VersionID: sr2.VersionID, ActorKind: "human"}); err != nil {
		t.Fatalf("promote with no curation langs = %v, want ok", err)
	}

	// Undetermined language (short prose ⇒ 'und') fails open.
	su, err := st.Save(ctx, SaveInput{Kind: "reference", AuthorKind: "ai", Content: Content{Title: "TZ", Summary: "UTC"}})
	if err != nil {
		t.Fatalf("save short: %v", err)
	}
	if got := versionLang(t, st, su.VersionID); got != "und" {
		t.Fatalf("short prose content_lang = %q, want und", got)
	}
	if err := st.Promote(ctx, PromoteInput{Slug: su.Slug, VersionID: su.VersionID, ActorKind: "human", CurationLangs: []string{"fr"}}); err != nil {
		t.Fatalf("promote of und under [fr] = %v, want ok (fails open)", err)
	}
}
