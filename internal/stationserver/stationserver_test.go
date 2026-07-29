package stationserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Quest-ICT/ken/internal/store"
)

func newKB(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "ken.db"))
	if err != nil {
		t.Fatalf("open kb: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(); err != nil {
		t.Fatalf("migrate kb: %v", err)
	}
	return st
}

// harness returns a live station endpoint plus a key bound to a fresh station.
func harness(t *testing.T, scopes ...string) (*store.Store, *httptest.Server, string, *store.Station) {
	t.Helper()
	if len(scopes) == 0 {
		scopes = []string{ScopeStation}
	}
	st := newKB(t)
	ctx := context.Background()
	actorID, err := st.FindOrCreateActor(ctx, "human", "curator")
	if err != nil {
		t.Fatal(err)
	}
	station, err := st.CreateStation(ctx, 1, "prod-ops", "production operations", actorID)
	if err != nil {
		t.Fatal(err)
	}
	key, err := st.IssueStationKey(ctx, actorID, station.StationID, "test", scopes)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewHTTPHandler(Deps{Store: st}))
	t.Cleanup(srv.Close)
	return st, srv, key, station
}

func post(t *testing.T, srv *httptest.Server, bearer, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(body))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", "2025-06-18")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

const initBody = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`

// The endpoint accepts exactly ONE credential shape: a kens_ station key carrying the
// station scope. This is the property that made copying commserver's auth worthwhile
// rather than sharing a parameterised one (S5).
func TestAuthAcceptsOnlyAStationKey(t *testing.T) {
	st, srv, key, _ := harness(t)
	ctx := context.Background()

	// No credential.
	if r := post(t, srv, "", initBody); r.StatusCode != http.StatusUnauthorized {
		r.Body.Close()
		t.Fatalf("no token: got %d, want 401", r.StatusCode)
	}

	// A knowledge-base token is not a station key, however valid it is elsewhere.
	actorID, _ := st.FindOrCreateActor(ctx, "ai", "agent")
	kbTok, err := st.IssueToken(ctx, actorID, []string{"read", "write-draft", "propose"}, "kb")
	if err != nil {
		t.Fatal(err)
	}
	if r := post(t, srv, kbTok, initBody); r.StatusCode != http.StatusUnauthorized {
		r.Body.Close()
		t.Fatalf("a ken_ KB token reached the station endpoint: got %d, want 401", r.StatusCode)
	}

	// A comm token is likewise not a station key.
	commTok, err := st.IssueToken(ctx, actorID, []string{"comm"}, "comm")
	if err != nil {
		t.Fatal(err)
	}
	if r := post(t, srv, commTok, initBody); r.StatusCode != http.StatusUnauthorized {
		r.Body.Close()
		t.Fatalf("a comm token reached the station endpoint: got %d, want 401", r.StatusCode)
	}

	// The real key works.
	r := post(t, srv, key, initBody)
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("a valid station key was refused: %d", r.StatusCode)
	}
}

// A revoked or retired key is refused INDISTINGUISHABLY from an unknown one, so the
// endpoint cannot be used to probe which keys exist (§5).
func TestRetiredKeyIsRefusedLikeAnUnknownOne(t *testing.T) {
	st, srv, key, _ := harness(t)
	p, err := st.AuthenticateStationKey(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RetireStationKey(context.Background(), p.TokenID); err != nil {
		t.Fatal(err)
	}

	retired := post(t, srv, key, initBody)
	defer retired.Body.Close()
	unknown := post(t, srv, "kens_aaaaaaaaaaaa_bbbbbbbb", initBody)
	defer unknown.Body.Close()

	if retired.StatusCode != unknown.StatusCode {
		t.Fatalf("retired (%d) and unknown (%d) keys must be indistinguishable", retired.StatusCode, unknown.StatusCode)
	}
	if retired.StatusCode != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", retired.StatusCode)
	}
}

// The instructions delivered on connect must carry the fourth sentence — the one that
// makes the feature work rather than merely exist. A briefing the model reads and does
// not relay is the original failure with extra steps (§11.9).
func TestInstructionsCarryTheRelaySentence(t *testing.T) {
	for _, want := range []string{
		"TELL YOUR HUMAN IN WORDS", // the relay duty
		"blocked_on is required",   // the enum, defined where it is used
		"CLOSE the moment a thing is done",
		"do NOT drop something your human", // the protected pile
		"handoff",                          // the continuity convention
		"NEVER a token, key or password",   // the locker rule Ken cannot enforce
	} {
		if !strings.Contains(instructions, want) {
			t.Errorf("connect-time instructions are missing %q", want)
		}
	}
}

// The locker is gated on its own reserved scope, so a key may keep notes and tasks
// without being able to store files.
func TestLockerRequiresItsOwnScope(t *testing.T) {
	ctx := context.Background()
	p := &principal{StationID: "s1", Scopes: map[string]bool{ScopeStation: true}}
	c := context.WithValue(ctx, ctxKey{}, p)
	if _, err := requireLocker(c); err == nil {
		t.Fatal("the locker must require the station-locker scope")
	}
	p.Scopes[ScopeStationLocker] = true
	if _, err := requireLocker(c); err != nil {
		t.Fatalf("with the scope it should pass: %v", err)
	}
}

// A station-less key may call station_request and nothing else — that is how a session
// with no station asks for one (S3). The refusal must say what to do next.
func TestStationLessKeyIsGatedWithGuidance(t *testing.T) {
	ctx := context.Background()
	c := context.WithValue(ctx, ctxKey{}, &principal{StationID: "", Scopes: map[string]bool{ScopeStation: true}})
	_, err := requireStation(c)
	if err == nil {
		t.Fatal("a station-less key must not reach station-scoped tools")
	}
	if !strings.Contains(err.Error(), "station_request") {
		t.Fatalf("the refusal should point at station_request, got: %v", err)
	}
}

// taskViewOf is a CROSS-LAYER copy, so it is tested: the store can grow a field the view
// silently drops — exactly how a file descriptor once went missing between a completed
// upload and the receiver's poll in COMM.
func TestTaskViewCarriesTheFieldsTheBriefingNeeds(t *testing.T) {
	v := taskViewOf(store.StationTask{
		TaskID: "t-1", Text: "do it", BlockedOn: "human", State: "open",
		BriefedCount: 4, DeferCount: 2, LastBriefedAt: "2026-07-28T00:00:00.000Z",
		HearsayAtWrite: true, StationName: "prod-ops", RemindAfter: "2026-08-01",
	})
	if v.TaskID != "t-1" || v.BlockedOn != "human" || v.BriefedCount != 4 || v.DeferCount != 2 {
		t.Fatalf("view dropped a field the ordering and nags depend on: %+v", v)
	}
	if !v.HearsayAtWrite {
		t.Fatal("the hearsay marking must survive into the view — §7 requires it on every task")
	}
	if v.StationName != "prod-ops" {
		t.Fatal("the cross-station view needs the station name")
	}
	if v.RemindAfter == "" {
		t.Fatal("remind_after drives the Due class; dropping it breaks the ordering contract")
	}
}

// A station key's use must leave a trace. Nothing recorded it before: TouchToken was
// called only from the knowledge-base authenticator, so `last_used_at` was permanently
// NULL for every station key — and the console rendered a last-used column that was
// always blank, which an operator reads as "unused" rather than "unmeasured".
//
// The concrete cost, reported from production: two keys for the same machine were
// indistinguishable in the console at the exact moment the documented rotation says
// "retire the old one". The wider cost: a stolen key could read an entire notebook and
// task list with no trace at all.
func TestUsingAStationKeyRecordsThatItWasUsed(t *testing.T) {
	st, srv, key, station := harness(t)
	ctx := context.Background()

	before, err := st.ListStationKeys(ctx, station.StationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 1 {
		t.Fatalf("fixture has %d keys, want 1", len(before))
	}
	if before[0].LastUsedAt != "" {
		t.Fatalf("last_used_at was already set before any call: %q", before[0].LastUsedAt)
	}

	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authenticated initialize returned HTTP %d", resp.StatusCode)
	}

	after, err := st.ListStationKeys(ctx, station.StationID)
	if err != nil {
		t.Fatal(err)
	}
	if after[0].LastUsedAt == "" {
		t.Fatal("using a station key left NO trace — a leaked key would be undetectable after the fact, " +
			"and the console's last-used column would stay permanently blank")
	}
}
