package stationserver

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Quest-ICT/ken/internal/store"
)

// A station tool must act on the station whose key made THIS call, not on whichever
// station happened to open the MCP session.
//
// The go-sdk binds a session to the INITIALIZE request's context, so anything a
// handler reads from its context is frozen at connect. On the knowledge-base surface
// that misattributed authorship; here it is worse in kind, because a station key IS
// the station: a stale principal means a session writing into another post's notebook,
// closing another post's tasks, and reading another post's locker — the three things
// stations exist to keep separate.
//
// It is latent while a client uses one key per connection. It stops being latent under
// the one-identity model, where one grant serves several capability families a human
// can retarget after consent.
func TestStationToolActsOnTheCallersStationNotTheOpeners(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	actor, err := st.FindOrCreateActor(ctx, "ai", "claude@box")
	if err != nil {
		t.Fatal(err)
	}
	alpha, err := st.CreateStation(ctx, 1, "alpha", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	beta, err := st.CreateStation(ctx, 1, "beta", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	keyA, err := st.IssueStationKey(ctx, actor, alpha.StationID, "alpha-key", []string{"station", "station-locker"})
	if err != nil {
		t.Fatal(err)
	}
	keyB, err := st.IssueStationKey(ctx, actor, beta.StationID, "beta-key", []string{"station", "station-locker"})
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(NewHTTPHandler(Deps{Store: st}))
	defer srv.Close()

	var sess string
	call := func(tok, body string) string {
		t.Helper()
		req, _ := http.NewRequest("POST", srv.URL+"/station/mcp", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if sess != "" {
			req.Header.Set("Mcp-Session-Id", sess)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if s := resp.Header.Get("Mcp-Session-Id"); s != "" {
			sess = s
		}
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}

	// Session opened by ALPHA's key.
	call(keyA, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`)
	call(keyA, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)

	// BETA's key writes a notebook page on that same connection.
	out := call(keyB, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"station_note_write","arguments":{"key":"scratch","body":"written by beta","mode":"replace"}}}`)
	if strings.Contains(out, `"isError":true`) {
		t.Fatalf("setup: the write failed, so ownership cannot be read: %s", out)
	}

	var owner string
	if err := st.R.QueryRowContext(ctx,
		`SELECT station_id FROM station_note WHERE key='scratch'`).Scan(&owner); err != nil {
		t.Fatalf("reading the page back: %v", err)
	}

	if owner == alpha.StationID {
		t.Fatalf("the page landed in station %q (alpha), which only OPENED the session — "+
			"beta's key made the call and presented its own bearer on the very request that wrote it.\n"+
			"A station key IS the station, so this is one post writing into another's notebook.",
			alpha.Name)
	}
	if owner != beta.StationID {
		t.Fatalf("page owned by %q, neither the opener (%s) nor the caller (%s)", owner, alpha.StationID, beta.StationID)
	}
}

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
	srv := httptest.NewServer(NewHTTPHandler(Deps{Store: st}))
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
