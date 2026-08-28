package main

import (
	"reflect"
	"strings"
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
		CommMetadataTTLSec: 1004, CommReplyDeadlineS: 1005,
		CommUndeliveredTTLSec: 1017, CommBodyRetentionSec: 1018,
		CommPollWaitMaxSec: 1007, CommProvenanceWindowSec: 1008,
		CommFilesEnabled: true, CommFileMaxMB: 1010, CommFileBudgetMB: 1011,
		CommFileMinFreeMB: 1012, CommFileTTLSec: 1013, CommGrantTTLSec: 1014,
		CommClaimLeaseSec: 1016,
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

// The SIBLING guard to the one above, and the reason it exists is the more useful
// half: TestCommLimitsMapsEverySetting passed while commRelevant() silently omitted
// two settings, because the two are independent hand-maintained lists of the same
// fields and only one had a test.
//
// commLimits() decides WHAT the store is told. commRelevant() decides WHETHER it is
// told at all — it is the change key that gates SetLimits(). A field missing here
// means the console saves the value, reports success, and the running process
// ignores it until restart. That is the failure mode this project keeps shipping: a
// remedy that is inert, with nothing to distinguish it from a remedy that worked.
//
// Asserting a fixed field count would rot; this mutates each Comm* field in turn and
// requires the key to move, so a new setting is covered by existing.
func TestCommRelevantSeesEveryCommSetting(t *testing.T) {
	base := settings.Values{}
	rv := reflect.ValueOf(&base).Elem()
	rt := rv.Type()

	checked := 0
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if !strings.HasPrefix(f.Name, "Comm") {
			continue
		}
		before := commRelevant(base)

		mutated := base
		mv := reflect.ValueOf(&mutated).Elem().Field(i)
		switch mv.Kind() {
		case reflect.Int, reflect.Int64:
			mv.SetInt(mv.Int() + 7919)
		case reflect.Bool:
			mv.SetBool(!mv.Bool())
		case reflect.String:
			mv.SetString(mv.String() + "x")
		default:
			t.Fatalf("%s has kind %s — extend this test rather than skipping it, or the field is unguarded", f.Name, mv.Kind())
		}

		if commRelevant(mutated) == before {
			t.Errorf("changing %s does not move the commRelevant key — the console will save it, "+
				"report success, and the running store will never be told", f.Name)
		}
		checked++
	}
	// A control on the test itself: if the prefix filter matched nothing, every
	// assertion above would vacuously pass.
	if checked < 10 {
		t.Fatalf("only %d Comm* fields inspected — the filter is broken and this test proves nothing", checked)
	}
}
