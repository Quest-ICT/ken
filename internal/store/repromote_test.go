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
func TestSetHeadRecoversFromABadWrite(t *testing.T) {
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

	// Three writes leave the head at rev 3 with no human involved — that is 6.0.0.
	if e, _ := st.GetEntry(ctx, r1.Slug); e.Head == nil || e.Head.Solution != "S3" {
		t.Fatalf("three writes should leave the newest as the head, got %+v", e.Head)
	}

	// A human deliberately puts an OLD version back in front. This is the shape that used to be
	// reachable only by promoting out of order, and it is now the human's ordinary undo: an agent
	// wrote something wrong, and the way to take it back is to point the head at what was there
	// before.
	if err := st.SetHead(ctx, PromoteInput{Slug: r1.Slug, VersionID: r1.VersionID, ActorKind: "human", Note: "S3 was wrong"}); err != nil {
		t.Fatalf("set head back to rev1: %v", err)
	}
	if e, _ := st.GetEntry(ctx, r1.Slug); e.Head == nil || e.Head.Solution != "S1" {
		t.Fatalf("expected the head to be back at S1, got %+v", e.Head)
	}

	// And forward again — recovery is symmetric, so a mistaken undo is not a one-way door.
	if err := st.SetHead(ctx, PromoteInput{Slug: r1.Slug, VersionID: r3.VersionID, ActorKind: "human", Note: "revert to rev3"}); err != nil {
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
	if err := st.SetHead(ctx, PromoteInput{Slug: r1.Slug, VersionID: r3.VersionID, ActorKind: "human"}); !errors.Is(err, ErrBadVersion) {
		t.Errorf("repromote of the current head must be ErrBadVersion, got %v", err)
	}
	r4, _ := st.ProposeEnhancement(ctx, ProposeInput{Slug: r1.Slug, ChangeNote: "to S4", AuthorKind: "ai", Patch: Patch{Solution: sp("S4")}})
	if err := st.SetHead(ctx, PromoteInput{Slug: r1.Slug, VersionID: r4.VersionID, ActorKind: "human"}); !errors.Is(err, ErrBadVersion) {
		t.Errorf("repromote of a still-proposed version must be ErrBadVersion (use Promote), got %v", err)
	}
	if err := st.SetHead(ctx, PromoteInput{Slug: r1.Slug, VersionID: 999999, ActorKind: "human"}); !errors.Is(err, ErrBadVersion) {
		t.Errorf("repromote of a bogus version must be ErrBadVersion, got %v", err)
	}
	_ = r2
}
