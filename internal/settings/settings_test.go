package settings_test

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/Quest-ICT/ken/internal/settings"
	"github.com/Quest-ICT/ken/internal/store"
)

// testDefaults must be a VALID configuration for every registered field, because
// Apply validates the whole registry on each save: a field left at its zero value
// here fails its own bounds check and fails every test in this file, not just the
// one exercising it. Keep it in step with settings.Fields.
var testDefaults = settings.Values{
	RLEnabled: true, IPPerMin: 120, IPBurst: 120, TokenPerMin: 120, TokenBurst: 60,
	BlockAfter: 100, LockoutSec: 900, LoginMaxFails: 5, LoginLockoutSec: 300,
	SessionTTLHours: 12, TLSMode: "off",
	CommMaxBodyBytes: 64 * 1024, CommMaxUnacked: 64,
	CommMessageTTLSec: 24 * 3600, CommMetadataTTLSec: 7 * 24 * 3600,
	CommReplyDeadlineS: 3600, CommPairingCodeTTLS: 900,
	CommPollWaitMaxSec: 15, CommProvenanceWindowSec: 3600,
	CommFileMaxMB: 16, CommFileBudgetMB: 256, CommFileMinFreeMB: 512,
	CommFileTTLSec: 24 * 3600, CommGrantTTLSec: 300,
	CommEndpointIdleTTLSec: 7 * 24 * 3600,
}

func testStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	return st
}

// formFrom builds a full form (all editable fields at their current value).
func formFrom(v settings.Values) map[string]string {
	m := map[string]string{}
	for _, f := range settings.Fields {
		if f.ReadOnly {
			continue
		}
		m[f.Key] = f.Get(v)
	}
	return m
}

func TestApplyPersistsAndReloads(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	l := settings.New(st, testDefaults)

	form := formFrom(l.Defaults())
	form["rl_ip_rpm"] = "300"
	snap, errs := l.Apply(ctx, form, "admin")
	if len(errs) > 0 {
		t.Fatalf("apply: %v", errs)
	}
	if snap.IPPerMin != 300 {
		t.Fatalf("live snapshot IPPerMin = %d, want 300", snap.IPPerMin)
	}

	// A fresh Live loading from the same store must see the override.
	l2 := settings.New(st, testDefaults)
	if err := l2.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if l2.Current().IPPerMin != 300 {
		t.Fatalf("reloaded IPPerMin = %d, want 300 (override not persisted)", l2.Current().IPPerMin)
	}

	// Resetting a field to its default removes the override row.
	form["rl_ip_rpm"] = "120"
	if _, errs := l.Apply(ctx, form, "admin"); len(errs) > 0 {
		t.Fatalf("apply reset: %v", errs)
	}
	over, _ := st.GetSettings(ctx)
	if _, ok := over["rl_ip_rpm"]; ok {
		t.Fatal("a value equal to the default should not be stored as an override")
	}
}

func TestApplyValidationRejectsBadInput(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	l := settings.New(st, testDefaults)

	for _, bad := range []map[string]string{
		{"rl_ip_rpm": "-5"},         // below min
		{"rl_ip_rpm": "notanumber"}, //
		{"trusted_proxies": "nope"}, // bad CIDR
		{"tls_domains": "no dots"},  // bad hostname
	} {
		form := formFrom(l.Defaults())
		for k, v := range bad {
			form[k] = v
		}
		_, errs := l.Apply(ctx, form, "admin")
		if len(errs) == 0 {
			t.Fatalf("expected a validation error for %v", bad)
		}
	}
	if over, _ := st.GetSettings(ctx); len(over) > 0 {
		t.Fatalf("nothing should be persisted when validation fails, got %v", over)
	}
}

func TestLiveTrustedProxyResolver(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	l := settings.New(st, testDefaults)

	// With no trusted proxies, XFF is ignored.
	req := &http.Request{RemoteAddr: "10.1.2.3:9", Header: http.Header{}}
	req.Header.Set("X-Forwarded-For", "203.0.113.7")
	if got := l.Current().Resolver.IP(req); got != "10.1.2.3" {
		t.Fatalf("default resolver used XFF unexpectedly: %q", got)
	}

	// Trust 10/8 live -> the resolver now honors XFF from that peer.
	form := formFrom(l.Defaults())
	form["trusted_proxies"] = "10.0.0.0/8"
	if _, errs := l.Apply(ctx, form, "admin"); len(errs) > 0 {
		t.Fatalf("apply: %v", errs)
	}
	if got := l.Current().Resolver.IP(req); got != "203.0.113.7" {
		t.Fatalf("after trusting 10/8, resolver should use XFF, got %q", got)
	}
}

func TestOnChangeFires(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	l := settings.New(st, testDefaults)
	got := 0
	l.OnChange(func(*settings.Snapshot) { got++ })
	form := formFrom(l.Defaults())
	form["rl_ip_burst"] = "200"
	if _, errs := l.Apply(ctx, form, "admin"); len(errs) > 0 {
		t.Fatalf("apply: %v", errs)
	}
	if got != 1 {
		t.Fatalf("OnChange fired %d times, want 1", got)
	}
}

func TestClearingNonEmptyDefaultPersists(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	defs := testDefaults
	defs.TrustedProxies = "10.0.0.0/8" // a non-empty boot default
	l := settings.New(st, defs)

	form := formFrom(l.Defaults())
	form["trusted_proxies"] = "" // operator clears it to REMOVE the trust
	if _, errs := l.Apply(ctx, form, "admin"); len(errs) > 0 {
		t.Fatalf("apply: %v", errs)
	}
	if l.Current().TrustedProxies != "" {
		t.Fatal("live value should be empty")
	}
	// The cleared value must survive a restart — NOT revert to the 10/8 default.
	l2 := settings.New(st, defs)
	if err := l2.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if got := l2.Current().TrustedProxies; got != "" {
		t.Fatalf("clearing a non-empty default must persist, got %q (fail-open revert)", got)
	}
}

func TestDisablingRateLimitPersists(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	l := settings.New(st, testDefaults) // RLEnabled default true
	form := formFrom(l.Defaults())
	form["rl_enabled"] = "" // unchecked
	if _, errs := l.Apply(ctx, form, "admin"); len(errs) > 0 {
		t.Fatalf("apply: %v", errs)
	}
	l2 := settings.New(st, testDefaults)
	if err := l2.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if l2.Current().RLEnabled {
		t.Fatal("disabling the rate limiter must persist across a restart")
	}
}

func TestCurationLangsNormalize(t *testing.T) {
	ctx := context.Background()
	l := settings.New(testStore(t), testDefaults)
	form := formFrom(l.Defaults())
	form["curation_langs"] = "fr, ZH , en-US, fr" // mixed case, spacing, region suffix, duplicate
	snap, errs := l.Apply(ctx, form, "admin")
	if len(errs) > 0 {
		t.Fatalf("apply: %v", errs)
	}
	want := []string{"fr", "zh", "en"} // lowercased, primary subtag only, de-duplicated, in order
	got := snap.CurationLangSet
	if len(got) != len(want) {
		t.Fatalf("CurationLangSet = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("CurationLangSet = %v, want %v", got, want)
		}
	}
}

func TestCurationLangsReject(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	l := settings.New(st, testDefaults)
	for _, bad := range []string{"e", "toolong", "1n", "fr!"} {
		form := formFrom(l.Defaults())
		form["curation_langs"] = bad
		if _, errs := l.Apply(ctx, form, "admin"); len(errs) == 0 {
			t.Fatalf("expected a validation error for curation_langs=%q", bad)
		}
	}
	if over, _ := st.GetSettings(ctx); len(over) > 0 {
		t.Fatalf("nothing should be persisted when validation fails, got %v", over)
	}
}

func TestRejectSlashZeroCIDR(t *testing.T) {
	ctx := context.Background()
	l := settings.New(testStore(t), testDefaults)
	for _, bad := range []string{"0.0.0.0/0", "::/0"} {
		form := formFrom(l.Defaults())
		form["trusted_proxies"] = bad
		if _, errs := l.Apply(ctx, form, "admin"); len(errs) == 0 {
			t.Fatalf("a /0 trusted-proxy (%s) must be rejected", bad)
		}
	}
}
