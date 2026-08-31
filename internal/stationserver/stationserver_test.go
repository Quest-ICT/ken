package stationserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

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
// harnessKey is the conversation the harness claims its station under. Tests declare it exactly as
// a real session does — the credential no longer names a station, so something has to.
const harnessKey = "conv-harness"

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
	station, err := st.CreateStation(ctx, "prod-ops", "production operations", actorID)
	if err != nil {
		t.Fatal(err)
	}
	// A `ken_` API TOKEN CARRYING THE STATION SCOPE, not a `kens_` station key — those are
	// retired. The credential no longer names a station either: it says whose estate this is, and
	// the session declares WHICH station with session_key. Tests that need a specific station
	// claim it, as a real session does.
	key, err := st.IssueToken(ctx, actorID, scopes, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.ClaimStationForSession(ctx, harnessKey, "prod-ops", actorID, station.StationID); err != nil {
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

// TestRetiredKeyIsRefusedLikeAnUnknownOne IS DELETED WITH STATION KEYS.

// The instructions delivered on connect must carry the fourth sentence — the one that
// makes the feature work rather than merely exist. A briefing the model reads and does
// not relay is the original failure with extra steps (§11.9).
func TestInstructionsCarryTheRelaySentence(t *testing.T) {
	// The corpus is the block PLUS every tool description: the block is truncated at
	// version.InstructionBudget and per-tool rules were moved to where they arrive intact, so
	// asserting on the block alone would force text back into the field that cuts it.
	corpus := stationCorpus(t)
	for _, want := range []string{
		"TELL YOUR HUMAN IN WORDS", // the relay duty
		"blocked_on is required",   // the enum, defined where it is used
		"CLOSE the moment a thing is done",
		"do NOT drop something your human", // the protected pile
		"handoff",                          // the continuity convention
		"NEVER a token, key or password",   // the locker rule Ken cannot enforce
	} {
		if !strings.Contains(corpus, want) {
			t.Errorf("nothing a session receives carries %q — not the connect-time block and not any "+
				"tool description", want)
		}
	}
}

// The locker belongs to the STATION, not to a key. Every station key reaches it.
//
// It shipped behind its own withholdable scope so a key could keep notes and tasks
// without storing files. That made a station's capabilities depend on which key a
// session happened to be handed, so "does this station have a locker" had no answer —
// only "does this key" — and a session finding it absent could not tell a deliberately
// restricted key from a misconfigured one. The locker is where a fresh session on a
// new machine finds what it needs to reconstitute itself, which is the worst place for
// that ambiguity.
//
// ScopeStationLocker is deliberately still in the vocabulary: existing keys carry it,
// and COMPATIBILITY.md reserves the pair precisely so they can be merged ("splitting a
// shipped scope is a MAJOR, merging two is free"). This asserts the merge in both
// directions — a key WITHOUT the old scope now passes, and one carrying it still does.
func TestLockerIsReachableFromAnyStationKey(t *testing.T) {
	ctx := context.Background()

	// The case that used to be refused, and is the whole point of the change.
	bare := &principal{StationID: "s1", Scopes: map[string]bool{ScopeStation: true}}
	if _, err := requireLocker(context.WithValue(ctx, ctxKey{}, bare), nil, ""); err != nil {
		t.Fatalf("a station key without the legacy locker scope cannot reach the locker: %v", err)
	}

	// An existing key that carries the old scope must not regress.
	legacy := &principal{StationID: "s1", Scopes: map[string]bool{ScopeStation: true, ScopeStationLocker: true}}
	if _, err := requireLocker(context.WithValue(ctx, ctxKey{}, legacy), nil, ""); err != nil {
		t.Fatalf("a key carrying the legacy locker scope was refused: %v", err)
	}

	// CONTROL: the station gate itself still holds. Without this, "the locker accepts
	// everything" would pass for the wrong reason — a locker reachable with no station
	// at all is a different and worse bug than the one being fixed.
	stationless := &principal{StationID: "", Scopes: map[string]bool{ScopeStation: true}}
	if _, err := requireLocker(context.WithValue(ctx, ctxKey{}, stationless), nil, ""); err == nil {
		t.Fatal("a station-less key reached the locker — the locker now gates on nothing at all")
	}
}

// A session with no station still cannot reach workinstance-wide tools — and the refusal must
// now point at something it can do BY ITSELF.
//
// IT USED TO POINT AT station_request, AND THAT WAS THE DEADLOCK IN ONE SENTENCE. The tool's own
// description called it "the only tool a key with no station may call", which was true and
// concealed the constraint: a key with no station could call it, and a session with no KEY could
// not — which is every session being onboarded. The console could not create a station either, and
// a console-minted key was issued under the operator's actor so it could never bind the session it
// was minted for. Vlad, at the console and unable to give a session Station: "It is absurd the way
// it works now."
//
// docs/IDENTITY.md §5 replaced the ask with a fact: call station_me and you have a station,
// immediately, with nothing to approve. So the refusal points there, and this test asserts the
// PROPERTY — that a session reading it can act without waiting for a human — rather than a
// sentence, because the sentence is what changed.
func TestASessionWithNoStationIsToldWhatItCanDoAlone(t *testing.T) {
	ctx := context.Background()
	c := context.WithValue(ctx, ctxKey{}, &principal{StationID: "", Scopes: map[string]bool{ScopeStation: true}})
	_, err := requireStation(c, nil, "")
	if err == nil {
		t.Fatal("a session with no station must not reach station-scoped tools")
	}
	if !strings.Contains(err.Error(), "station_me") {
		t.Errorf("the refusal does not name the call that fixes it, so the session waits: %v", err)
	}
	// *** THIS LIST LOST "ask your human", DELIBERATELY, ON 2026-08-26. ***
	//
	// It forbade any mention of a human because §5 removed the deadlock where a session had to
	// WAIT for one. That was right for the mint and it is still right: minting costs zero
	// approvals and happens on the session's own next call.
	//
	// But the acceptance run on a clean Windows VM proved the promise cannot be kept for USE on
	// the path Ken actually recommends. claude.ai connectors refuse custom header names —
	// "Only approved header names are accepted" — so a session can mint a station and then
	// reach nothing, and the only remedy is a human putting ?station=<id> in the connector URL.
	//
	// Forbidding the words does not remove the dependency; it only stops the refusal from
	// MENTIONING it, which would leave the session to discover the same wall with less
	// information. So the assertion changed from "never name a human" to what actually protects
	// the session: never send it to the DELETED path, and never advise the LOOP.
	for _, stale := range []string{"station_request", "they give you"} {
		if strings.Contains(err.Error(), stale) {
			t.Errorf("the refusal routes through the deleted request-and-wait path (%q) — that is the "+
				"deadlock §5 removed: %v", stale, err)
		}
	}

	// *** AND THE ASSERTION THAT REPLACED IT: THE REFUSAL MUST NOT ADVISE A LOOP. ***
	//
	// Until 3.34.0 it said "call station_me" as the fix. A session that obeyed got a SECOND
	// station, the same refusal, and one more orphan station — every time. The VM session
	// spotted it and stopped: "The error's advice is a loop. It tells me to call station_me
	// again, which would mint a second station and leave me in the same place, because the gap
	// is the header, not the mint."
	//
	// The refusal must therefore say what actually closes the gap — DECLARING a station — and must
	// warn against the re-mint rather than recommend it.
	//
	// IT USED TO REQUIRE "?station=" AS THE REMEDY. That was correct when a connector had no
	// other way to declare one; both the URL form and the header are now deleted, measured as never
	// used, and session_key replaced them. Requiring the old string here would pin advice a session
	// cannot act on.
	if !strings.Contains(err.Error(), "session_key") {
		t.Errorf("the refusal does not name session_key, which is the only remedy a session can act "+
			"on by itself: %v", err)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "not the fix") &&
		!strings.Contains(strings.ToLower(err.Error()), "mints a new station") {
		t.Errorf("the refusal does not warn that re-calling station_me with no key mints ANOTHER "+
			"station; a session following it accumulates orphans: %v", err)
	}
	// AND IT MUST NOT RESURRECT THE DELETED ADVICE.
	if strings.Contains(err.Error(), "?station=") || strings.Contains(err.Error(), "X-Ken-Workspace") {
		t.Errorf("the refusal still points at a transport mechanism that no longer exists: %v", err)
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

// TestUsingAStationKeyRecordsThatItWasUsed IS DELETED WITH STATION KEYS.

// stationCorpus is everything a session receives from /station/mcp: the connect-time block plus
// every tool description. See the identical helper in internal/commserver for why the union, not
// the block, is the honest place to assert that a session was told something.
func stationCorpus(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("stationserver.go")
	if err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	sb.WriteString(instructions)
	lit := regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`)
	n := 0
	for _, m := range regexp.MustCompile(`Description:\s*((?:"(?:[^"\\]|\\.)*"\s*\+?\s*)+)`).FindAllSubmatch(b, -1) {
		for _, pp := range lit.FindAllSubmatch(m[1], -1) {
			sb.Write(pp[1])
			sb.WriteString(" ")
		}
		n++
	}
	if n < 10 {
		t.Fatalf("only %d tool descriptions parsed; the scanner is broken, not the text", n)
	}
	return sb.String()
}

// prime does what every real session does first: calls station_me with its conversation key, which
// binds the connection so later tools need no argument.
//
// The fixtures used to get this for free — a `kens_` key NAMED a station, so the principal arrived
// carrying one. Credentials no longer name stations; the conversation declares which. Priming is
// the test's version of the instruction every session is given.
func prime(t *testing.T, sess *mcp.ClientSession) {
	t.Helper()
	if _, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "station_me", Arguments: map[string]any{"session_key": harnessKey},
	}); err != nil {
		t.Fatalf("prime: %v", err)
	}
}
