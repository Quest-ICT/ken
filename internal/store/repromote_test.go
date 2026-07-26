package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

// TestRepromoteRecoversWrongOrderPromotion reproduces the "promoted newest-first so
// the head regressed" bug and verifies Repromote restores the intended head.
func TestRepromoteRecoversWrongOrderPromotion(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	sp := func(s string) *string { return &s }

	// rev1 (draft) then two stacked enhancements rev2, rev3.
	r1, err := st.Save(ctx, SaveInput{Kind: "reference", Content: Content{Title: "T", Summary: "sum", Solution: "S1"}, AuthorKind: "ai"})
	if err != nil {
		t.Fatal(err)
	}
	r2, err := st.ProposeEnhancement(ctx, ProposeInput{Slug: r1.Slug, ChangeNote: "to S2", AuthorKind: "ai", Patch: Patch{Solution: sp("S2")}})
	if err != nil {
		t.Fatal(err)
	}
	r3, err := st.ProposeEnhancement(ctx, ProposeInput{Slug: r1.Slug, ChangeNote: "to S3", AuthorKind: "ai", Patch: Patch{Solution: sp("S3")}})
	if err != nil {
		t.Fatal(err)
	}

	// Promote newest-first (the bug): each older promote regresses the head → rev1.
	for _, vid := range []int64{r3.VersionID, r2.VersionID, r1.VersionID} {
		if err := st.Promote(ctx, PromoteInput{Slug: r1.Slug, VersionID: vid, ActorKind: "human", Note: "promote"}); err != nil {
			t.Fatalf("promote %d: %v", vid, err)
		}
	}
	if e, _ := st.GetEntry(ctx, r1.Slug); e.Head == nil || e.Head.Solution != "S1" {
		t.Fatalf("expected head to have regressed to S1, got %+v", e.Head)
	}

	// Repromote rev3 to recover.
	if err := st.Repromote(ctx, PromoteInput{Slug: r1.Slug, VersionID: r3.VersionID, ActorKind: "human", Note: "revert to rev3"}); err != nil {
		t.Fatalf("repromote: %v", err)
	}
	if e, _ := st.GetEntry(ctx, r1.Slug); e.Head == nil || e.Head.Solution != "S3" || e.Head.RevNo != 3 {
		t.Fatalf("expected head restored to S3/rev3, got %+v", e.Head)
	}
	// The re-curated head must not carry a stale superseded_by pointer (rev3 had been
	// superseded by rev2 during the wrong-order promotion).
	var sb sql.NullInt64
	if err := st.R.QueryRowContext(ctx, `SELECT superseded_by_version_id FROM entry_version WHERE id=?`, r3.VersionID).Scan(&sb); err != nil {
		t.Fatal(err)
	}
	if sb.Valid {
		t.Errorf("re-curated head still has superseded_by_version_id=%d, want NULL", sb.Int64)
	}

	// Guards.
	if err := st.Repromote(ctx, PromoteInput{Slug: r1.Slug, VersionID: r3.VersionID, ActorKind: "human"}); !errors.Is(err, ErrBadVersion) {
		t.Errorf("repromote of the current head must be ErrBadVersion, got %v", err)
	}
	r4, _ := st.ProposeEnhancement(ctx, ProposeInput{Slug: r1.Slug, ChangeNote: "to S4", AuthorKind: "ai", Patch: Patch{Solution: sp("S4")}})
	if err := st.Repromote(ctx, PromoteInput{Slug: r1.Slug, VersionID: r4.VersionID, ActorKind: "human"}); !errors.Is(err, ErrBadVersion) {
		t.Errorf("repromote of a still-proposed version must be ErrBadVersion (use Promote), got %v", err)
	}
	if err := st.Repromote(ctx, PromoteInput{Slug: r1.Slug, VersionID: 999999, ActorKind: "human"}); !errors.Is(err, ErrBadVersion) {
		t.Errorf("repromote of a bogus version must be ErrBadVersion, got %v", err)
	}
	_ = r2
}
