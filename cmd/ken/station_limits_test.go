package main

import (
	"reflect"
	"testing"

	"github.com/Quest-ICT/ken/internal/settings"
)

// The station bounds are BACKUP decisions before they are storage decisions, so a
// zero one is not a small bug: a zero notebook cap refuses every write, and a zero
// retention bound prunes everything. This mirrors the COMM equivalent, which caught
// exactly this class of omission twice while stations were being built.
func TestStationLimitsMapsEverySetting(t *testing.T) {
	v := settings.Values{
		StationNotePageKiB: 11, StationNoteRevisionKiB: 12, StationNotebookKiB: 13,
		StationLockerBlobKiB: 14, StationLockerTotalKiB: 15,
		StationMaxOpenTasks: 16, StationTaskTextBytes: 17,
		StationTaskDetailBytes: 18, StationTaskListLimit: 19,
		StationVaultSecretKiB: 20, StationVaultEntries: 21,
		StationVaultHistoryRev: 22, StationVaultReadLog: 23,
	}
	task, note, locker, vault := stationLimits(&settings.Snapshot{Values: v})

	for _, got := range []any{task, note, locker, vault} {
		rv := reflect.ValueOf(got)
		for i := 0; i < rv.NumField(); i++ {
			if rv.Field(i).IsZero() {
				t.Errorf("%T.%s is zero — stationLimits() forgot to map a setting, and a zero bound "+
					"either refuses every write or prunes everything",
					got, rv.Type().Field(i).Name)
			}
		}
	}
	// KiB in the settings, bytes in the limits: an operator reasons in KiB during a
	// backup conversation, and the store reasons in bytes. A missing shift would
	// silently cap a notebook at 4 KiB instead of 4 MiB.
	if note.MaxPageBytes != 11<<10 {
		t.Errorf("note page = %d bytes, want %d — the KiB conversion is missing or wrong", note.MaxPageBytes, 11<<10)
	}
	if locker.MaxTotalBytes != 15<<10 {
		t.Errorf("locker total = %d bytes, want %d", locker.MaxTotalBytes, 15<<10)
	}
	if vault.MaxSecretBytes != 20<<10 {
		t.Errorf("vault secret = %d bytes, want %d", vault.MaxSecretBytes, 20<<10)
	}
	// The vault's COUNTS are not sizes and must NOT be shifted. A KiB conversion applied
	// to "how many secrets" would turn a 64-entry cap into 65,536, which reads as working
	// and is the same class of error as a missing shift, in the other direction.
	if vault.MaxEntries != 21 || vault.MaxHistoryPerName != 22 || vault.MaxReadLog != 23 {
		t.Errorf("a vault COUNT was converted as if it were a size: entries=%d history=%d readlog=%d, want 21/22/23",
			vault.MaxEntries, vault.MaxHistoryPerName, vault.MaxReadLog)
	}
}
