package store

import (
	"context"
	"path/filepath"
	"testing"
)

// viaCommOf reads the raw column so the test asserts the stored value, not a
// round-tripped struct field.
func viaCommOf(t *testing.T, st *Store, vid int64) any {
	t.Helper()
	var v any
	if err := st.R.QueryRow(`SELECT via_comm FROM entry_version WHERE id=?`, vid).Scan(&v); err != nil {
		t.Fatalf("read via_comm: %v", err)
	}
	return v
}

func viaCommStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	return st
}

func saveWith(t *testing.T, st *Store, slug string, viaComm bool) SaveResult {
	t.Helper()
	r, err := st.Save(context.Background(), SaveInput{
		Slug: slug, Kind: "reference",
		Content:    Content{Title: "T " + slug, Summary: "S", Problem: "P", Solution: "S"},
		AuthorKind: "ai", Confidence: 0.5, ViaComm: viaComm,
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	return r
}

// The hearsay mark reaches the stored version, and an unmarked version stores
// NULL rather than 0 — the column means "no signal", not "known first-hand", so an
// unmarked row must be indistinguishable from every row predating the feature.
func TestViaCommIsStoredOnSave(t *testing.T) {
	st := viaCommStore(t)

	marked := saveWith(t, st, "marked", true)
	if got := viaCommOf(t, st, marked.VersionID); got != int64(1) {
		t.Fatalf("marked version: via_comm = %v (%T), want 1", got, got)
	}

	plain := saveWith(t, st, "plain", false)
	if got := viaCommOf(t, st, plain.VersionID); got != nil {
		t.Fatalf("unmarked version: via_comm = %v, want NULL", got)
	}
}

func TestViaCommIsStoredOnProposeEnhancement(t *testing.T) {
	ctx := context.Background()
	st := viaCommStore(t)
	base := saveWith(t, st, "base", false)

	note := "relayed from another session"
	sum := "S2"
	r, err := st.ProposeEnhancement(ctx, ProposeInput{
		Slug: base.Slug, ChangeNote: note, AuthorKind: "ai", Confidence: 0.6,
		ViaComm: true, Patch: Patch{Summary: &sum},
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if got := viaCommOf(t, st, r.VersionID); got != int64(1) {
		t.Fatalf("proposed version: via_comm = %v, want 1", got)
	}
	// The base version is untouched: the mark is per-version provenance.
	if got := viaCommOf(t, st, base.VersionID); got != nil {
		t.Fatalf("base version was modified: via_comm = %v, want NULL", got)
	}
}

// The mark is frozen like every other provenance column: an UPDATE that changes it
// must abort. A mutable marker could simply be updated away, which would defeat it.
func TestViaCommIsImmutable(t *testing.T) {
	st := viaCommStore(t)
	r := saveWith(t, st, "frozen", true)

	if _, err := st.W.Exec(`UPDATE entry_version SET via_comm=NULL WHERE id=?`, r.VersionID); err == nil {
		t.Fatal("clearing via_comm was allowed — the mark can be updated away")
	}
	if _, err := st.W.Exec(`UPDATE entry_version SET via_comm=1 WHERE id=?`, saveWith(t, st, "frozen2", false).VersionID); err == nil {
		t.Fatal("setting via_comm after the fact was allowed")
	}
	// A legitimate status update on the same row still works — the trigger must not
	// have become over-broad.
	if _, err := st.W.Exec(`UPDATE entry_version SET verify_ttl_days=30 WHERE id=?`, r.VersionID); err != nil {
		t.Fatalf("a mutable-status update was wrongly blocked: %v", err)
	}
}

// The schema permits only NULL or 1: a 0 would read as "known first-hand", which
// the design explicitly does not claim.
func TestViaCommRejectsZero(t *testing.T) {
	st := viaCommStore(t)
	r := saveWith(t, st, "checked", false)
	if _, err := st.W.Exec(`INSERT INTO entry_version(entry_id,rev_no,state,title,summary,via_comm)
		SELECT entry_id, 99, 'proposed', 'x', 'y', 0 FROM entry_version WHERE id=?`, r.VersionID); err == nil {
		t.Fatal("via_comm=0 was accepted; only NULL or 1 are meaningful")
	}
}

// The review queue surfaces the mark, which is the whole point: the curator sees it
// before promoting.
func TestProposalRowCarriesViaComm(t *testing.T) {
	ctx := context.Background()
	st := viaCommStore(t)
	saveWith(t, st, "hearsay", true)
	saveWith(t, st, "firsthand", false)

	rows, err := st.ListProposals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, r := range rows {
		seen[r.Slug] = r.LatestViaComm
	}
	if !seen["hearsay"] {
		t.Fatal("the marked proposal is not flagged on the review queue")
	}
	if seen["firsthand"] {
		t.Fatal("an unmarked proposal was flagged")
	}
}

// THE SHARPER MARKER WAS THE UNFROZEN ONE.
//
// Migration 0010 rebuilt the immutability trigger for the sole purpose of freezing
// `via_comm`, arguing that "a mutable marker could simply be UPDATEd away — which would
// defeat the point". Migration 0018 then split that boolean into a KIND — directed versus
// broadcast — because one send to a nine-station room marked nine actors and a badge that
// is almost always on says less than no badge. `via_comm_kind` is written and read exactly
// like its sibling, and was never added to the frozen set.
//
// So the field that distinguishes "somebody addressed YOU" from "you were in the room" was
// the one that could be quietly rewritten. The rule was right; it did not travel to the
// field that superseded the one it was written for.
func TestViaCommKindIsImmutable(t *testing.T) {
	st := viaCommStore(t)
	// Saved directly rather than through saveWith, which never sets ViaCommKind — the
	// control below caught that, and a fixture with an empty column would have made this
	// whole test pass against a trigger that froze nothing.
	r, err := st.Save(context.Background(), SaveInput{
		Slug: "kind-frozen", Kind: "reference",
		Content:    Content{Title: "T kind-frozen", Summary: "S", Problem: "P", Solution: "S"},
		AuthorKind: "ai", Confidence: 0.5, ViaComm: true, ViaCommKind: "directed",
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	// CONTROL: it has a value to begin with, so an abort below is the trigger firing and
	// not an update that matched no rows.
	var kind string
	if err := st.R.QueryRow(`SELECT COALESCE(via_comm_kind,'') FROM entry_version WHERE id=?`,
		r.VersionID).Scan(&kind); err != nil {
		t.Fatal(err)
	}
	if kind == "" {
		t.Fatalf("setup: via_comm_kind is empty on a marked version, so this test would " +
			"pass against a trigger that froze nothing")
	}

	if _, err := st.W.Exec(`UPDATE entry_version SET via_comm_kind='broadcast' WHERE id=?`,
		r.VersionID); err == nil {
		t.Errorf("rewriting via_comm_kind was allowed — the marker that distinguishes a "+
			"directed message from a room broadcast can be edited after the fact, which is "+
			"exactly what freezing via_comm (%q here) exists to prevent", kind)
	}
	if _, err := st.W.Exec(`UPDATE entry_version SET via_comm_kind=NULL WHERE id=?`,
		r.VersionID); err == nil {
		t.Error("clearing via_comm_kind was allowed")
	}

	// AND THE TRIGGER IS NOT OVER-BROAD: a mutable status column on the same row still
	// updates. Without this arm, a trigger that rejected every UPDATE would read as a pass.
	if _, err := st.W.Exec(`UPDATE entry_version SET verify_ttl_days=30 WHERE id=?`, r.VersionID); err != nil {
		t.Fatalf("a mutable-status update was wrongly blocked: %v", err)
	}
}
