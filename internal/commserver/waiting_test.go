package commserver

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Quest-ICT/ken/internal/clientip"
	"github.com/Quest-ICT/ken/internal/ratelimit"
	"github.com/Quest-ICT/ken/internal/store"
)

const (
	knownWithMail = "AAAAAAAAAAAAAAA1"
	knownNoMail   = "AAAAAAAAAAAAAAA2"
	unknownID     = "ZZZZZZZZZZZZZZZ9"
	brokenID      = "BBBBBBBBBBBBBBB1" // the lookup fails with a database error
)

type fakeStations struct{}

func (fakeStations) StationByID(_ context.Context, id string) (*store.Station, error) {
	switch id {
	case knownWithMail, knownNoMail:
		return &store.Station{StationID: id}, nil
	case brokenID:
		return nil, errors.New("database is locked")
	}
	return nil, store.ErrNotFound
}

type fakeMail struct{ asked []string }

func (f *fakeMail) StationHasMail(_ context.Context, id string) (int, error) {
	f.asked = append(f.asked, id)
	if id == knownWithMail {
		return 2, nil
	}
	return 0, nil
}

// waitingFixture builds the handler over fakes and a real ProbeGuard with the given miss
// limit and a bucket too large to matter.
func waitingFixture(t *testing.T, missLimit int) (*WaitingHandler, *fakeMail) {
	t.Helper()
	g := &ratelimit.ReloadableProbeGuard{}
	g.Store(ratelimit.NewProbeGuard(ratelimit.ProbeConfig{
		Enabled: true, PerMin: 1_000_000, Burst: 1_000_000,
		MissLimit: missLimit, MissWindow: time.Hour, Lockout: time.Hour,
	}, clientip.NewResolver("")))
	m := &fakeMail{}
	return &WaitingHandler{guard: g, stations: fakeStations{}, mail: m}, m
}

func ask(h http.Handler, addr, method, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, WaitingPath, strings.NewReader(body))
	r.RemoteAddr = addr
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	return rr
}

func stationBody(id string) string { return `{"station":"` + id + `"}` }

func TestWaitingAnswersOnlyYesOrNo(t *testing.T) {
	h, _ := waitingFixture(t, 10)
	for _, tc := range []struct{ id, want string }{
		{knownWithMail, `{"mail":true}`},
		{knownNoMail, `{"mail":false}`},
	} {
		rr := ask(h, "203.0.113.5:1", http.MethodPost, stationBody(tc.id))
		if rr.Code != http.StatusOK || strings.TrimSpace(rr.Body.String()) != tc.want {
			t.Fatalf("%s: %d %q, want 200 %s", tc.id, rr.Code, rr.Body.String(), tc.want)
		}
		if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
			t.Fatalf("Content-Type %q, want application/json", ct)
		}
		if cc := rr.Header().Get("Cache-Control"); cc != "no-store" {
			t.Fatalf("Cache-Control %q, want no-store", cc)
		}
	}
}

// AN UNKNOWN ID GETS BYTE-FOR-BYTE THE ANSWER A REAL, EMPTY STATION GETS — and the mail
// store is never asked about it.
func TestWaitingUnknownStationLooksLikeAnEmptyOne(t *testing.T) {
	h, m := waitingFixture(t, 10)
	empty := ask(h, "203.0.113.5:1", http.MethodPost, stationBody(knownNoMail))
	unknown := ask(h, "203.0.113.6:1", http.MethodPost, stationBody(unknownID))
	if unknown.Code != empty.Code || unknown.Body.String() != empty.Body.String() {
		t.Fatalf("unknown: %d %q; empty station: %d %q — the answer tells them apart",
			unknown.Code, unknown.Body.String(), empty.Code, empty.Body.String())
	}
	for _, id := range m.asked {
		if id == unknownID {
			t.Fatal("the mail store was asked about an id that names no station")
		}
	}
}

func TestWaitingRefusesEveryOtherBodyBeforeAnyLookup(t *testing.T) {
	h, m := waitingFixture(t, 0) // never block: this test is about the 400s
	bodies := map[string]string{
		"empty":            ``,
		"not json":         `station=` + knownWithMail,
		"array":            `["` + knownWithMail + `"]`,
		"no station field": `{}`,
		"null station":     `{"station":null}`,
		"number station":   `{"station":1234567890123456}`,
		"extra field":      `{"station":"` + knownWithMail + `","session_key":"x"}`,
		"trailing data":    stationBody(knownWithMail) + `{}`,
		"15 chars":         stationBody(knownWithMail[:15]),
		"17 chars":         stationBody(knownWithMail + "A"),
		"not base62":       stationBody("AAAAAAAAAAAAAAA-"),
		"uuid":             stationBody("2e415ff7-c612-4af3-bd13-a438979ee957"),
		"oversized":        `{"station":"` + knownWithMail + `"` + strings.Repeat(" ", 300) + `}`,
	}
	for name, body := range bodies {
		rr := ask(h, "203.0.113.5:1", http.MethodPost, body)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %q, want 400", name, rr.Code, rr.Body.String())
		}
	}
	if len(m.asked) != 0 {
		t.Fatalf("malformed bodies reached the mail store: %v", m.asked)
	}
	// Whitespace around a valid object is still the valid object.
	if rr := ask(h, "203.0.113.5:1", http.MethodPost, " \n"+stationBody(knownWithMail)+"\n"); rr.Code != http.StatusOK {
		t.Fatalf("a valid body with surrounding whitespace: %d %q", rr.Code, rr.Body.String())
	}
}

func TestWaitingAcceptsOnlyPOST(t *testing.T) {
	h, _ := waitingFixture(t, 0)
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete} {
		rr := ask(h, "203.0.113.5:1", method, stationBody(knownWithMail))
		if rr.Code != http.StatusMethodNotAllowed || rr.Header().Get("Allow") != http.MethodPost {
			t.Errorf("%s: %d, Allow %q, want 405 with Allow: POST", method, rr.Code, rr.Header().Get("Allow"))
		}
	}
}

// Vlad, 2026-11-17: asking for stations that do not exist gets the address blocked — and
// so do malformed lookups and wrong methods. The block is JSON, closes the connection,
// refuses even a valid lookup from that address, and leaves other addresses alone.
func TestWaitingBlocksAnAddressThatKeepsMissing(t *testing.T) {
	h, _ := waitingFixture(t, 3)
	const prober, other = "203.0.113.5:1", "203.0.113.9:1"
	ask(h, prober, http.MethodPost, stationBody(unknownID)) // miss 1: no such station
	ask(h, prober, http.MethodPost, `{}`)                   // miss 2: malformed
	ask(h, prober, http.MethodGet, "")                      // miss 3: wrong method
	rr := ask(h, prober, http.MethodPost, stationBody(knownWithMail))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("after 3 misses: %d %q, want 403", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("a refusal must be JSON, not %q: a text/plain refusal is what a JSON client reports as invalid content", ct)
	}
	if rr.Header().Get("Connection") != "close" {
		t.Fatal("a refusal must close the connection")
	}
	if rr := ask(h, other, http.MethodPost, stationBody(knownWithMail)); rr.Code != http.StatusOK {
		t.Fatalf("another address: %d, want 200", rr.Code)
	}
}

// Real stations asked over and over are never misses: one machine with many sessions
// checking their own mail must never be blocked for it.
func TestWaitingValidLookupsNeverBlock(t *testing.T) {
	h, _ := waitingFixture(t, 3)
	for i := 0; i < 50; i++ {
		for _, id := range []string{knownWithMail, knownNoMail} {
			if rr := ask(h, "203.0.113.5:1", http.MethodPost, stationBody(id)); rr.Code != http.StatusOK {
				t.Fatalf("lookup %d of %s: %d, want 200", i, id, rr.Code)
			}
		}
	}
}

// A database failure is Ken's fault, not the caller's: 503, and not a miss.
func TestWaitingDatabaseFailureIsNotAMiss(t *testing.T) {
	h, _ := waitingFixture(t, 2)
	for i := 0; i < 5; i++ {
		rr := ask(h, "203.0.113.5:1", http.MethodPost, stationBody(brokenID))
		if rr.Code != http.StatusServiceUnavailable {
			t.Fatalf("attempt %d: %d, want 503", i, rr.Code)
		}
	}
	if rr := ask(h, "203.0.113.5:1", http.MethodPost, stationBody(knownWithMail)); rr.Code != http.StatusOK {
		t.Fatalf("Ken's own failures got the caller blocked: %d", rr.Code)
	}
}

func TestWaitingThrottleIsJSONWithRetryAfter(t *testing.T) {
	g := &ratelimit.ReloadableProbeGuard{}
	g.Store(ratelimit.NewProbeGuard(ratelimit.ProbeConfig{
		Enabled: true, PerMin: 60, Burst: 1, MissLimit: 10, MissWindow: time.Hour, Lockout: time.Hour,
	}, clientip.NewResolver("")))
	h := &WaitingHandler{guard: g, stations: fakeStations{}, mail: &fakeMail{}}
	ask(h, "203.0.113.5:1", http.MethodPost, stationBody(knownNoMail))
	rr := ask(h, "203.0.113.5:1", http.MethodPost, stationBody(knownNoMail))
	if rr.Code != http.StatusTooManyRequests || rr.Header().Get("Retry-After") == "" {
		t.Fatalf("over the burst: %d, Retry-After %q, want 429 with one", rr.Code, rr.Header().Get("Retry-After"))
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("a 429 must be JSON, got %q", ct)
	}
	body, _ := io.ReadAll(rr.Body)
	if !strings.Contains(string(body), `"error"`) {
		t.Fatalf("429 body %q carries no error field", body)
	}
}
