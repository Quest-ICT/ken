package main

import (
	"strconv"
	"strings"
	"testing"

	"github.com/Quest-ICT/ken/internal/settings"
)

// rlRelevant() is the change key that decides whether a settings edit rebuilds the rate
// limiters. It is a hand-maintained list, the same shape as commRelevant() before that one
// silently omitted two settings (TestCommRelevantSeesEveryCommSetting). A field missing here
// means the console saves the value, reports success, and the running guard never hears of
// it until a restart.
//
// Driven by the settings REGISTRY (every rl_* key) rather than a field-name prefix, because
// the rate-limit fields share no Go-name prefix. Each key is set to a different VALID value
// through its own Set, so the test exercises exactly what the console can do.
func TestEveryRateLimitSettingReachesTheRebuildDetector(t *testing.T) {
	base := settings.DefaultsFromEnv()
	checked := 0
	for _, f := range settings.Fields {
		if !strings.HasPrefix(f.Key, "rl_") || f.ReadOnly || f.Set == nil {
			continue
		}
		mutated := base
		var next string
		switch f.Type {
		case "int":
			n, err := strconv.Atoi(f.Get(base))
			if err != nil {
				t.Fatalf("%s: default %q is not a number", f.Key, f.Get(base))
			}
			next = strconv.Itoa(n + 1)
		case "bool":
			// Rendered as "on" or "" (settings.boolStr), so flip on emptiness.
			if f.Get(base) != "" {
				next = ""
			} else {
				next = "on"
			}
		case "cidrs":
			next = "198.51.100.0/24"
		default:
			t.Fatalf("%s has type %q — extend this test rather than skipping it, or the field is unguarded", f.Key, f.Type)
		}
		if err := f.Set(&mutated, next); err != nil {
			t.Fatalf("%s: setting %q: %v", f.Key, next, err)
		}
		if rlRelevant(mutated) == rlRelevant(base) {
			t.Errorf("changing %s does not move the rlRelevant key — the console will save it, "+
				"report success, and the running guard will never be rebuilt", f.Key)
		}
		checked++
	}
	// A control on the test itself: a filter that matched nothing would pass vacuously.
	if checked < 12 {
		t.Fatalf("only %d rl_* settings inspected — the filter is broken and this test proves nothing", checked)
	}
}
