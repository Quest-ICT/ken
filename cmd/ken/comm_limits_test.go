package main

import (
	"reflect"
	"testing"

	"github.com/Quest-ICT/ken/internal/settings"
)

// commLimits copies a dozen fields from the settings snapshot into comm.Limits by
// hand. A field added to comm.Limits but forgotten here defaults to zero — and a
// zero retention window is not a harmless default, it made the sweeper delete every
// idle endpoint the moment it registered (the 1.2.0 EndpointIdleTTLSeconds bug).
//
// This asserts the mapping is TOTAL: given a snapshot whose every comm setting is a
// distinct non-zero value, every field of the resulting comm.Limits is non-zero. A
// future dropped field fails here instead of in production.
func TestCommLimitsMapsEverySetting(t *testing.T) {
	// A snapshot with every Comm* setting set to a distinct, plainly non-zero value.
	v := settings.Values{
		CommMaxBodyBytes: 1001, CommMaxUnacked: 1002, CommMessageTTLSec: 1003,
		CommMetadataTTLSec: 1004, CommReplyDeadlineS: 1005, CommPairingCodeTTLS: 1006,
		CommUndeliveredTTLSec: 1017, CommBodyRetentionSec: 1018,
		CommPollWaitMaxSec: 1007, CommProvenanceWindowSec: 1008,
		CommFilesEnabled: true, CommFileMaxMB: 1010, CommFileBudgetMB: 1011,
		CommFileMinFreeMB: 1012, CommFileTTLSec: 1013, CommGrantTTLSec: 1014,
		CommEndpointIdleTTLSec: 1015, CommClaimLeaseSec: 1016,
	}
	got := commLimits(&settings.Snapshot{Values: v})

	rv := reflect.ValueOf(got)
	for i := 0; i < rv.NumField(); i++ {
		f := rv.Type().Field(i)
		if rv.Field(i).IsZero() {
			t.Errorf("comm.Limits.%s is zero — commLimits() forgot to map a setting, "+
				"and a zero limit is a latent bug (a zero retention window sweeps everything)", f.Name)
		}
	}
}
