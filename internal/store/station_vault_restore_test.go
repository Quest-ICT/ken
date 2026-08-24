package store

import (
	"errors"
	"testing"
)

// RESTORE MUST REACH EVERY RETAINED VALUE, NOT OSCILLATE BETWEEN THE NEWEST TWO.
//
// Measured before the fix, five puts A..E then six restores: D E D E D E. A restore is
// itself a write, so the displaced value went back into history at a higher rev and the
// hardcoded `ORDER BY rev DESC LIMIT 1` picked it up again next time. A, B and C were
// unreachable by any code in the tree while three documents promised sixteen recoverable
// revisions.
func TestRestoreReachesEveryRetainedValue(t *testing.T) {
	st, ctx, station, actor, lim := vaultFixture(t)
	for _, v := range []string{"A", "B", "C", "D", "E"} {
		if _, _, err := st.PutStationVaultSecret(ctx, lim, station, "k", v, "", "tok", actor); err != nil {
			t.Fatal(err)
		}
	}
	hist, err := st.StationVaultHistoryFor(ctx, station, "k")
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 4 {
		t.Fatalf("history holds %d superseded values, want 4 (A..D)", len(hist))
	}

	// Walk the whole list newest-first and demand each one back BY ID. Every entry must be
	// reachable; today only the first would be.
	want := []string{"D", "C", "B", "A"}
	for i, h := range hist {
		if h.ID == 0 {
			t.Fatalf("history entry %d carries no id — it cannot be addressed at all", i)
		}
		if _, _, err := st.RestoreStationVaultSecret(ctx, lim, station, "k", h.ID, "tok", actor); err != nil {
			t.Fatalf("restoring history id %d: %v", h.ID, err)
		}
		got, err := st.GetStationVaultSecret(ctx, lim, station, "k", "console", "tok", actor)
		if err != nil {
			t.Fatal(err)
		}
		if got.Secret != want[i] {
			t.Fatalf("restoring the %d-th newest gave %q, want %q", i, got.Secret, want[i])
		}
	}
}

// A HISTORY ROW BELONGING TO ANOTHER NAME MUST BE REFUSED, not silently swapped for the
// newest. Falling back would restore a value the caller never asked for and report success.
func TestRestoreRefusesAHistoryRowFromAnotherName(t *testing.T) {
	st, ctx, station, actor, lim := vaultFixture(t)
	for _, v := range []string{"a1", "a2"} {
		if _, _, err := st.PutStationVaultSecret(ctx, lim, station, "alpha", v, "", "tok", actor); err != nil {
			t.Fatal(err)
		}
	}
	for _, v := range []string{"b1", "b2"} {
		if _, _, err := st.PutStationVaultSecret(ctx, lim, station, "beta", v, "", "tok", actor); err != nil {
			t.Fatal(err)
		}
	}
	other, err := st.StationVaultHistoryFor(ctx, station, "beta")
	if err != nil || len(other) == 0 {
		t.Fatalf("fixture: beta history %d, err %v", len(other), err)
	}
	if _, _, err := st.RestoreStationVaultSecret(ctx, lim, station, "alpha", other[0].ID, "tok", actor); !errors.Is(err, ErrNotFound) {
		t.Fatalf("restoring beta's history row into alpha: got %v, want ErrNotFound", err)
	}
	// And alpha is untouched — a refused restore must not half-apply.
	got, err := st.GetStationVaultSecret(ctx, lim, station, "alpha", "console", "tok", actor)
	if err != nil {
		t.Fatal(err)
	}
	if got.Secret != "a2" {
		t.Fatalf("a refused restore changed alpha to %q", got.Secret)
	}
}

// *** EXERCISING RECOVERY MUST NOT CONSUME RECOVERY DEPTH. ***
//
// pruneVaultHistory ran on put and on delete but never on restore, so each restore added a
// row and none removed one. Measured with a bound of 3: six restores left NINE history rows,
// all churn duplicates of the same two values — and the next ordinary put then dropped the
// real history to make room. The feature that exists to recover was the one destroying what
// there was to recover.
func TestRestoringDoesNotInflateHistoryPastTheBound(t *testing.T) {
	st, ctx, station, actor, lim := vaultFixture(t)
	lim.MaxHistoryPerName = 3
	for _, v := range []string{"A", "B", "C", "D", "E"} {
		if _, _, err := st.PutStationVaultSecret(ctx, lim, station, "k", v, "", "tok", actor); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 6; i++ {
		if _, _, err := st.RestoreStationVaultSecret(ctx, lim, station, "k", 0, "tok", actor); err != nil {
			t.Fatalf("restore %d: %v", i, err)
		}
		hist, err := st.StationVaultHistoryFor(ctx, station, "k")
		if err != nil {
			t.Fatal(err)
		}
		if len(hist) > lim.MaxHistoryPerName {
			t.Fatalf("after restore %d, history holds %d rows against a bound of %d",
				i, len(hist), lim.MaxHistoryPerName)
		}
	}
}

// AND THE CALLER IS TOLD WHEN A RESTORE SPENT RECOVERY DEPTH. A restore is a write; at the
// bound it pushes the oldest recoverable value out. Silently is how this became invisible.
func TestRestoreReportsWhatItDropped(t *testing.T) {
	st, ctx, station, actor, lim := vaultFixture(t)
	lim.MaxHistoryPerName = 2
	for _, v := range []string{"A", "B", "C"} {
		if _, _, err := st.PutStationVaultSecret(ctx, lim, station, "k", v, "", "tok", actor); err != nil {
			t.Fatal(err)
		}
	}
	_, dropped, err := st.RestoreStationVaultSecret(ctx, lim, station, "k", 0, "tok", actor)
	if err != nil {
		t.Fatal(err)
	}
	if dropped == 0 {
		t.Fatal("at the bound, a restore drops the oldest recoverable value and reported 0")
	}

	// CONTROL: well under the bound, nothing is dropped and the caller is not warned about
	// a loss that did not happen. Without this the test passes against a constant.
	st2, ctx2, station2, actor2, lim2 := vaultFixture(t)
	lim2.MaxHistoryPerName = 50
	for _, v := range []string{"A", "B"} {
		if _, _, err := st2.PutStationVaultSecret(ctx2, lim2, station2, "k", v, "", "tok", actor2); err != nil {
			t.Fatal(err)
		}
	}
	if _, d, err := st2.RestoreStationVaultSecret(ctx2, lim2, station2, "k", 0, "tok", actor2); err != nil || d != 0 {
		t.Fatalf("under the bound: dropped=%d err=%v, want 0 and nil", d, err)
	}
}
