package stationserver

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Quest-ICT/ken/internal/store"
)

// *** TestStationToolActsOnTheCallersStationNotTheOpeners IS DELETED, AND HALF ITS SUBJECT WITH IT. ***
//
// It proved that a tool call carrying a SECOND credential acts on that credential's station rather
// than the one whose key opened the connection — a real defect once: "a kb_save presented with
// token B, on a session opened by token A, was written with A as author."
//
// A credential no longer names a station. The CONNECTION holds one, declared once by station_me,
// so "the caller's station" and "the opener's station" are the same station by construction and
// there is nothing left to distinguish.
//
// THE OTHER HALF SURVIVES AND IS WORTH SAYING OUT LOUD: which ACTOR a write is attributed to when a
// second bearer arrives mid-connection. That is still re-derived per call, by withCaller, which now
// goes through principalFromToken — the one resolver both it and the middleware share, added
// precisely because retiring station keys would otherwise have left withCaller silently accepting
// nothing and the per-call bearer quietly ignored.

// The fallback must not become a hole. With no per-call bearer the handler keeps the
// connection principal, which is what makes an in-process transport and the existing
// tests work — but an INVALID key on a call must never silently inherit the opener's
// station either, and the middleware is what guarantees that.
func TestAnInvalidKeyIsRefusedByTheTransportBeforeAnyHandlerRuns(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(testHandler(t, Deps{Store: st}))
	defer srv.Close()

	req, _ := http.NewRequest("POST", srv.URL+"/station/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	req.Header.Set("Authorization", "Bearer kens_not_a_real_key")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("an unknown station key got HTTP %d, want 401 — the per-call fallback is only safe because "+
			"the middleware has already refused anything that does not authenticate", resp.StatusCode)
	}
}
