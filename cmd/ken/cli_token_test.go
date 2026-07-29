package main

import "testing"

// The scope-family matrix from docs/STATIONS.md §6. The case that motivated the
// three-way split: the previous two-way version bucketed every non-comm scope as
// knowledge-base, so adding `station` would have let it mix with kb scopes silently
// while refusing the one combination that is legitimate.
func TestScopeMixThreeFamilies(t *testing.T) {
	for _, tc := range []struct {
		name   string
		scopes []string
		ok     bool
	}{
		{"kb alone", []string{"read", "write-draft", "propose"}, true},
		{"comm alone", []string{"comm", "comm-file"}, true},
		{"station alone", []string{"station", "station-locker"}, true},
		{"station + comm is the permitted pair", []string{"station", "comm"}, true},
		{"station + kb is refused", []string{"read", "write-draft", "propose", "station"}, false},
		{"comm + kb is refused", []string{"comm", "read"}, false},
		{"station-locker + kb is refused", []string{"station-locker", "curate"}, false},
	} {
		err := checkScopeMix(tc.scopes)
		if tc.ok && err != nil {
			t.Errorf("%s: %v should be allowed, got %v", tc.name, tc.scopes, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("%s: %v should be refused, was allowed", tc.name, tc.scopes)
		}
	}
}
