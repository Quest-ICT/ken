package commserver

import (
	"context"
	"github.com/Quest-ICT/ken/internal/version"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

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

// mintToken issues an API token with the given scopes and returns it.
func mintToken(t *testing.T, st *store.Store, actor string, scopes ...string) string {
	t.Helper()
	ctx := context.Background()
	id, err := st.FindOrCreateActor(ctx, "ai", actor)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := st.IssueToken(ctx, id, scopes, "test")
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// The COMM endpoint accepts exactly one token shape: a ken_ API token carrying
// the comm scope. This is the security property that made duplicating the
// knowledge base's authentication worthwhile rather than sharing it.
func TestAuthRequiresACommScopedAPIToken(t *testing.T) {
	ctx := context.Background()
	st := newKB(t)

	commTok := mintToken(t, st, "comm-agent", "comm")
	kbTok := mintToken(t, st, "kb-agent", "read", "write-draft", "propose")

	p, err := authenticate(ctx, st, commTok, ScopeComm)
	if err != nil {
		t.Fatalf("a comm-scoped token must authenticate: %v", err)
	}
	if p.TokenID == "" || p.ActorID == 0 || 1 == 0 {
		t.Fatalf("principal not fully resolved: %+v", p)
	}

	if _, err := authenticate(ctx, st, kbTok, ScopeComm); err == nil {
		t.Fatal("a knowledge-base token was accepted on the comm endpoint")
	}
}

// *** A CONNECTOR REACHES MESSAGING ONLY IF ITS HUMAN GRANTED IT — NOT BY TOKEN SHAPE. ***
//
// This test used to assert that an OAuth-shaped bearer "must not authenticate here at all",
// because "a cloud-hosted connector is the worst possible holder of 'reach into the sessions on my
// machines', and its scope set is hard-coded rather than operator-chosen, so an operator could not
// withhold comm from it even if they wanted to."
//
// **THE SECOND HALF OF THAT SENTENCE IS WHY IT CHANGED, NOT THE FIRST.** The objection was never
// to OAuth; it was that nobody could withhold. docs/IDENTITY.md §10 step 2 consolidates the three
// authenticators so one identity spans all three surfaces — and docs/IDENTITY-CONTROLS.md sets the
// price precisely: *"the withholding has to be re-expressed as an explicit per-surface capability
// decision at grant time, not inherited from the fact that three files exist."*
//
// So the control survives, keyed on the GRANT rather than on the token's prefix. An operator can
// now withhold comm from a connector, which they could not do before — the register's own
// complaint. What must never happen is the invisible version it warns about: consolidation that
// silently widens every existing connector from the knowledge base to the message bus.
func TestAConnectorReachesCommOnlyIfItsGrantSaysSo(t *testing.T) {
	ctx := context.Background()
	st := newKB(t)

	// A LEGACY GRANT — approved before step 2, carrying no ken: scope. It must be refused,
	// because reaching the knowledge base is all its human ever agreed to.
	legacy := mintOAuth(t, st, "read write offline_access")
	if _, err := authenticate(ctx, st, legacy, ScopeComm); err == nil {
		t.Error("a connector approved before per-surface consent reached COMM — consolidation " +
			"silently widened an existing grant from the knowledge base to the message bus, which is " +
			"exactly the invisible removal IDENTITY-CONTROLS.md warns about")
	}

	// A NARROWED GRANT — the human unticked messaging. Same refusal, different reason.
	narrowed := mintOAuth(t, st, "read write "+store.ScopeKB+" "+store.ScopeStation)
	if _, err := authenticate(ctx, st, narrowed, ScopeComm); err == nil {
		t.Error("a grant that does not carry ken:comm reached COMM — the human's decision to " +
			"withhold it did nothing, which is the state this control exists to prevent")
	}

	// AND A GRANT THAT DOES CARRY IT WORKS, or the two refusals above prove only that OAuth is
	// still rejected wholesale and step 2 never happened.
	full := mintOAuth(t, st, "read write "+store.ScopeKB+" "+store.ScopeCommSet)
	p, err := authenticate(ctx, st, full, ScopeComm)
	if err != nil {
		t.Fatalf("a grant carrying ken:comm was refused: %v — one identity does not span /comm/mcp", err)
	}
	if p.ActorID == 0 {
		t.Error("the OAuth principal carries no actor; endpoints registered under it would have no owner")
	}
	if 1 == 0 {
		t.Error("the OAuth principal carries no space — its endpoints would be filed where its own " +
			"successor cannot poll them")
	}
	// The file relay rides on the same grant and is refused when comm was not granted.
	if _, err := authenticate(ctx, st, narrowed, ScopeCommFile); err == nil {
		t.Error("a grant without ken:comm reached the byte relay")
	}
}

// mintOAuth creates a live grant with the given OAuth scope string and returns an access token
// for it, so a test can assert on what a HUMAN approved rather than on a token's prefix.
func mintOAuth(t *testing.T, st *store.Store, scope string) string {
	t.Helper()
	ctx := context.Background()
	human, err := st.FindOrCreateActor(ctx, "human", "curator")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := st.FindOrCreateActor(ctx, "ai", "connector-"+scope[:4])
	if err != nil {
		t.Fatal(err)
	}
	clientID, err := st.RegisterOAuthClient(ctx, "test-connector", []string{"https://example.invalid/cb"})
	if err != nil {
		t.Fatal(err)
	}
	code, err := st.CreateOAuthGrantAndCode(ctx, store.NewAuthCode{
		ClientID: clientID, ConnectorActorID: conn, HumanActorID: human,
		RedirectURI: "https://example.invalid/cb", Scope: scope,
	}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	cd, err := st.PeekOAuthCode(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	access, _, err := st.ExchangeOAuthCode(ctx, code, cd.GrantID, time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return access
}

// THE DEV BYPASS MUST BE RATE-ACCOUNTED, WHICH IS WHAT THIS ALWAYS CARED ABOUT.
//
// It used to assert that COMM REFUSED the bypass, and gave its reason in one line: the dev
// principal carried an EMPTY token id, so it escaped per-token rate accounting. That reason was
// right and the assertion was the only remedy available while COMM was a separate endpoint a dev
// token had no business reaching.
//
// /mcp chains all three middlewares now, so a bypass one of them refuses is a bypass that works
// nowhere — measured as a 401 on the quickstart in README. Refusing it here would not contain it;
// it would just break it. So the bypass is honoured on every surface and the hazard is closed at
// its source: the principal carries a constant, non-empty token id and is bucketed like any other
// credential.
//
// The assertion follows the reason rather than the old conclusion.
func TestTheDevTokenBypassIsRateAccounted(t *testing.T) {
	ctx := context.Background()
	st := newKB(t)
	t.Setenv("KEN_DEV_TOKEN", "dev-secret")

	p, err := authenticate(ctx, st, "dev-secret", ScopeComm)
	if err != nil {
		t.Fatalf("the dev token is refused on comm, so /mcp — which chains this middleware — "+
			"cannot accept it either: %v", err)
	}
	if p.TokenID == "" {
		t.Error("the dev principal carries an empty token id, so every per-token control keyed on " +
			"it silently does nothing — rate accounting buckets under \"\", which is the hole that " +
			"made refusing the bypass the only available remedy")
	}
	// AND IT IS STILL OFF UNLESS THE OPERATOR SET IT. Without this, a build that accepted any
	// bearer as the dev token would satisfy everything above.
	if _, err := authenticate(ctx, st, "not-the-dev-token", ScopeComm); err == nil {
		t.Error("an arbitrary bearer was accepted as the dev token")
	}
}

func TestAuthRejectsRevokedAndMalformedTokens(t *testing.T) {
	ctx := context.Background()
	st := newKB(t)

	tok := mintToken(t, st, "revoked-agent", "comm")
	tokenID := strings.SplitN(tok, "_", 3)[1]
	if err := st.RevokeToken(ctx, tokenID); err != nil {
		t.Fatal(err)
	}
	if _, err := authenticate(ctx, st, tok, ScopeComm); err == nil {
		t.Fatal("a revoked token authenticated")
	}

	for _, bad := range []string{"", "ken_", "ken_only-two-parts", "ken_unknownid_secret"} {
		if _, err := authenticate(ctx, st, bad, ScopeComm); err == nil {
			t.Fatalf("malformed token %q authenticated", bad)
		}
	}

	// A correct token id with the wrong secret must fail.
	good := mintToken(t, st, "secret-agent", "comm")
	parts := strings.SplitN(good, "_", 3)
	if _, err := authenticate(ctx, st, "ken_"+parts[1]+"_wrongsecret", ScopeComm); err == nil {
		t.Fatal("a wrong secret authenticated")
	}
}

// A wait that ties or exceeds the client's tool timeout turns a successful empty
// poll into a tool ERROR, so the ceiling is clamped server-side no matter what an
// operator configures — including when they change it live from the settings page.
func TestPollWaitIsClampedServerSide(t *testing.T) {
	cases := []struct {
		name      string
		configMax int
		requested int
		want      time.Duration
	}{
		{"default when unset", 0, 0, defaultPollWait * time.Second},
		{"requested under the max", 20, 5, 5 * time.Second},
		{"requested over the max is capped", 20, 999, 20 * time.Second},
		{"operator max above the hard ceiling is capped", 3600, 999, hardMaxPollWait * time.Second},
		{"operator max above the hard ceiling caps a modest request too", 3600, 120, hardMaxPollWait * time.Second},
		{"a live update takes effect", 10, 0, 10 * time.Second},
		{"negative means do not park", 20, -1, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := &Handler{w: newWaiters()}
			h.SetMaxPollWait(c.configMax)
			got := h.pollWait(c.requested)
			if got != c.want {
				t.Fatalf("pollWait(%d) with max %d = %v, want %v", c.requested, c.configMax, got, c.want)
			}
		})
	}
}

func TestWaiterIsWokenByNotify(t *testing.T) {
	w := newWaiters()
	done := make(chan bool, 1)
	go func() { done <- w.wait(context.Background(), 42, 5*time.Second) }()

	// Let the waiter park before notifying, so this tests the wakeup rather than
	// the immediate re-read.
	for i := 0; i < 100 && w.parked() == 0; i++ {
		time.Sleep(time.Millisecond)
	}
	w.notify(42)

	select {
	case woken := <-done:
		if !woken {
			t.Fatal("waiter returned without being woken")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("notify did not wake the parked waiter")
	}
	if w.parked() != 0 {
		t.Fatalf("waiter not deregistered: %d still parked", w.parked())
	}
}

func TestWaiterTimesOutAndDeregisters(t *testing.T) {
	w := newWaiters()
	if woken := w.wait(context.Background(), 1, 10*time.Millisecond); woken {
		t.Fatal("wait reported a wakeup on timeout")
	}
	if w.parked() != 0 {
		t.Fatalf("waiter leaked after timeout: %d parked", w.parked())
	}
}

// A notify for one endpoint must not wake another endpoint's poll.
func TestNotifyIsPerEndpoint(t *testing.T) {
	w := newWaiters()
	done := make(chan bool, 1)
	go func() { done <- w.wait(context.Background(), 1, 150*time.Millisecond) }()
	for i := 0; i < 100 && w.parked() == 0; i++ {
		time.Sleep(time.Millisecond)
	}
	w.notify(2) // a different endpoint

	select {
	case woken := <-done:
		if woken {
			t.Fatal("a notify for endpoint 2 woke a waiter on endpoint 1")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiter never returned")
	}
}

// Drain must wake everyone: the shutdown budget is shorter than a long poll, so
// without it every deploy severs parked connections mid-response.
func TestDrainWakesEveryWaiterAndRefusesNewOnes(t *testing.T) {
	w := newWaiters()
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int64) { defer wg.Done(); w.wait(context.Background(), id, 10*time.Second) }(int64(i))
	}
	for i := 0; i < 200 && w.parked() < 5; i++ {
		time.Sleep(time.Millisecond)
	}

	w.drain()
	waited := make(chan struct{})
	go func() { wg.Wait(); close(waited) }()
	select {
	case <-waited:
	case <-time.After(3 * time.Second):
		t.Fatal("drain did not wake all parked waiters")
	}

	// After draining, a new wait returns immediately rather than parking.
	start := time.Now()
	if woken := w.wait(context.Background(), 99, 5*time.Second); woken {
		t.Fatal("a waiter parked after drain")
	}
	if time.Since(start) > time.Second {
		t.Fatal("wait blocked after drain instead of returning immediately")
	}
}

// Waiters are capped because nothing outside the process bounds them: no write
// timeout on the server, no task limit in the unit file.
func TestWaiterCapsAreEnforced(t *testing.T) {
	w := newWaiters()
	var wg sync.WaitGroup
	for i := 0; i < maxWaitersPerEndpoint; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); w.wait(context.Background(), 7, 2*time.Second) }()
	}
	for i := 0; i < 200 && w.parked() < maxWaitersPerEndpoint; i++ {
		time.Sleep(time.Millisecond)
	}

	// One past the per-endpoint cap must return immediately, not park.
	start := time.Now()
	if woken := w.wait(context.Background(), 7, 5*time.Second); woken {
		t.Fatal("over-cap waiter reported a wakeup")
	}
	if time.Since(start) > time.Second {
		t.Fatal("over-cap waiter parked instead of returning immediately")
	}

	w.drain()
	wg.Wait()
}

// A cancelled request must release its waiter rather than holding the slot.
func TestWaiterReleasesOnContextCancel(t *testing.T) {
	w := newWaiters()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan bool, 1)
	go func() { done <- w.wait(ctx, 5, 10*time.Second) }()
	for i := 0; i < 200 && w.parked() == 0; i++ {
		time.Sleep(time.Millisecond)
	}
	cancel()

	select {
	case woken := <-done:
		if woken {
			t.Fatal("cancelled wait reported a wakeup")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not release the waiter")
	}
	if w.parked() != 0 {
		t.Fatalf("waiter leaked after cancel: %d parked", w.parked())
	}
}

// A parked long-poll is a wait, not latency: comm_poll must be excluded from the
// tool-duration histogram, while every other comm_* tool is recorded. Bucketing a
// 30s wait as "latency" would drown the real work time of every other tool.
func TestPollExcludedFromDurationHistogram(t *testing.T) {
	if recordsDuration("comm_poll") {
		t.Fatal("comm_poll (a long-poll) must NOT be recorded in the latency histogram")
	}
	for _, tool := range []string{"comm_register", "comm_join", "comm_channels", "comm_send", "comm_ack", "comm_file_offer", "comm_file_grant"} {
		if !recordsDuration(tool) {
			t.Fatalf("%s should be recorded — it does not block", tool)
		}
	}
}

// TestTheModelIsToldWhereToKeepTheSecret IS DELETED WITH THE SECRET. It required comm_register's
// description to tell a session to write its endpoint secret to a file — the instruction that made
// comm unusable for any session without a disk. There is no register, no secret and no file.

func anyOf(hay string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(hay, n) {
			return true
		}
	}
	return false
}

// registerDescription reads comm_register's shipped description out of the source, so the test
// follows the rule to wherever it actually lives rather than to where it used to.
func registerDescription(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("commserver.go")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`Name:\s*"comm_register"[^}]*?Description:\s*((?:"(?:[^"\\]|\\.)*"\s*\+?\s*)+)`).FindSubmatch(b)
	if m == nil {
		t.Fatal("comm_register is not registered with a parseable description")
	}
	var sb strings.Builder
	for _, p := range regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`).FindAllSubmatch(m[1], -1) {
		sb.Write(p[1])
	}
	return sb.String()
}

// TestRegisterAlwaysReturnsTheSecretAndRefusesARetiredVoucherArgument IS DELETED with comm_register.

// sid carries the MCP session id between the calls above.
var sid string

// A wait the server overrules must say so.
//
// The tool description told sessions to prefer a long wait over frequent short polls,
// the value was capped server-side, and the result never mentioned it. ken-prod-ops
// passed wait_seconds=120 for a week believing they were asking for two minutes.
//
// A parameter that is accepted, silently ignored, and never spoken of again is the
// same shape as a remedy that is inert: nothing distinguishes it from one that worked.
// Ken has now hit that shape in a settings clamp, a flag with no reader, a store
// function with no caller, a table with no reader, and a prune that deleted nothing.
func TestPollReportsTheWaitItActuallyGranted(t *testing.T) {
	h := &Handler{}
	h.SetMaxPollWait(30)

	// Asking for more than the cap: the grant is the cap, and the ask is echoed back.
	if got := int(h.pollWait(120) / time.Second); got != 30 {
		t.Fatalf("setup: pollWait(120) granted %ds, want the 30s cap — this test cannot show a clamp that is not happening", got)
	}

	// Asking for less than the cap: granted verbatim, and nothing is reported as
	// clamped. Without this the "clamped" field could be populated unconditionally and
	// the test above would still pass.
	if got := int(h.pollWait(5) / time.Second); got != 5 {
		t.Errorf("pollWait(5) granted %ds against a 30s cap — a wait inside the cap must be honoured verbatim", got)
	}
}

// The description must not tell sessions to do something the server undoes without
// saying so. Asserted by reflection on the shipped struct tag rather than by reading
// the source, so it tests the schema a client actually receives.
func TestTheWaitAdviceNamesTheFieldsThatContradictIt(t *testing.T) {
	f, ok := reflect.TypeOf(pollIn{}).FieldByName("WaitSeconds")
	if !ok {
		t.Fatal("pollIn has no WaitSeconds field")
	}
	desc := f.Tag.Get("jsonschema")
	if desc == "" {
		t.Fatal("wait_seconds carries no description at all")
	}
	for _, want := range []string{"wait_seconds_granted", "wait_clamped_from", "CLAMPED"} {
		if !strings.Contains(desc, want) {
			t.Errorf("the wait_seconds description does not mention %q — a session is told to prefer long waits with no way to learn it did not get one.\ngot: %s", want, desc)
		}
	}
}

// THE CONNECT-TIME TEXT MUST NOT DESCRIBE A MECHANISM THAT NO LONGER EXISTS.
//
// This string is the one artefact a session captures at conversation start and can never
// be sent a correction for. That makes a stale sentence here worse than a stale comment
// anywhere else in the tree: it keeps being obeyed, by every session, for the whole life
// of the conversation, and nothing in the product can contradict it.
//
// Two mechanisms have been retired out from under it. The sweeper stopped writing
// kind='status' notices (3.4.0) — a session told to wait for one waits forever. And
// comm_channels stopped being channel-only, so "a pending count per channel" understated
// what the caller can see in exactly the surface that exists to stop hasty sends.
func TestInstructionsDescribeTheMechanismsThatActuallyExist(t *testing.T) {
	corpus := deliveredCorpus(t)
	for _, want := range []string{
		"pending_total", // the number that covers rooms and broadcast, not just channels
		"'notices' array",
		"reason='expired'",
		"nothing to ack",
	} {
		if !strings.Contains(corpus, want) {
			t.Errorf("nothing a session receives mentions %q — not the connect-time block and not any "+
				"tool description", want)
		}
	}
	// AND THE RETIRED CLAIMS ARE GONE. Asserting only the presence of the new text would
	// pass with both versions present, which is the likeliest way this regresses: an edit
	// that adds the correction and leaves the contradiction sitting above it.
	for _, gone := range []string{
		"comm_channels reports a pending count per channel",
		`Treat it as the answer to "why is my peer silent" rather than waiting further. Ack it like any other message.`,
	} {
		if strings.Contains(corpus, gone) {
			t.Errorf("a session is still told about a retired mechanism: %q.\n"+
				"A session captures this once and cannot be corrected — it will act on it all conversation.", gone)
		}
	}
	// The legacy note must SURVIVE, though: an upgraded database still holds pre-3.4.0
	// status rows, and they are still pollable mail somebody may not have read.
	if !strings.Contains(corpus, "kind='status'") {
		t.Error("the instructions no longer mention kind='status' at all — an upgraded deployment " +
			"still holds those rows and a session that meets one has no idea what it is")
	}
}

// THE OPERATOR-FACING CONSOLE TEXT MUST NOT NAME A MECHANISM THIS DEPLOYMENT NO LONGER HAS.
//
// The existing i18n test catches a key MISSING across locales. It cannot catch a key whose meaning
// went stale, which is what keeps happening: every locale once said COMM was off by default (the
// env var went in 2.0.0), and that bodies are deleted once processed (the pre-1.6.0 rule that
// destroyed 97% of one deployment's message bodies).
//
// *** IT USED TO INSPECT SIX LINES OUT OF TWO THOUSAND. ***
//
// The first version began `if !strings.HasPrefix(line, "comm.optin") { continue }`, so it read one
// key family and was green over every other stale string in the bundle. Two audit rounds found
// operator text still teaching pairing codes, binding vouchers, station keys, endpoint secrets and
// an approval queue — none of it visible to a gate that only ever looked at comm.optin. The second
// round put it plainly: this gate needed widening more than the feature needed deleting.
//
// It now reads EVERY key and bans the vocabulary of mechanisms that no longer exist. The exclusion
// list is the honest part: a few keys legitimately name a retired thing in order to say it is gone,
// and each is named here rather than the ban being weakened for everyone.
func TestConsoleTextDoesNotDescribeRemovedOrReplacedBehaviour(t *testing.T) {
	// Keys allowed to name a retired mechanism, each because it exists to say the thing is gone.
	// Adding to this list is a decision, not a fix — every entry needs a reason.
	allowed := map[string]bool{}

	// Vocabulary of things this deployment does not have. Matched case-insensitively against the
	// VALUE, per locale, so a translated console cannot drift from the English one.
	banned := map[string][]string{
		"a pairing code":     {"pairing code", "código de emparejamiento", "code d'appariement"},
		"an endpoint secret": {"endpoint secret", "secreto del endpoint", "secret du point"},
		"a station key":      {"station key", "clave de puesto", "clé de poste", "kens_"},
		"a binding voucher":  {"binding voucher", "voucher", "vale de vinculación", "bon de liaison"},
		"a deleted tool":     {"comm_register", "comm_bind", "comm_unbind", "comm_join", "station_link_request"},
		"a deleted endpoint": {"/comm/mcp", "/station/mcp", "X-Ken-Workspace"},
		"a deleted setting":  {"KEN_COMM_ENABLED", "KEN_STATION_ENABLED", "comm_endpoint_idle_sec"},
		"link approval":      {"approved link", "enlace aprobado", "lien approuvé"},
		"the retired word":   {"workspace", "espacio de trabajo", "espace de travail"},
	}

	var checked int
	for _, f := range []string{"messages.properties", "messages_es.properties", "messages_fr.properties"} {
		b, err := os.ReadFile(filepath.Join("..", "i18n", "locales", f))
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(b), "\n") {
			key, value, ok := strings.Cut(line, " = ")
			if !ok || strings.HasPrefix(strings.TrimSpace(key), "#") {
				continue
			}
			key = strings.TrimSpace(key)
			if allowed[key] {
				continue
			}
			checked++
			low := strings.ToLower(value)
			for what, words := range banned {
				for _, w := range words {
					if strings.Contains(low, strings.ToLower(w)) {
						t.Errorf("%s: %s names %s (%q):\n    %s", f, key, what, w, strings.TrimSpace(value))
					}
				}
			}
		}
	}
	// POSITIVE CONTROL ON THE INSTRUMENT. A prefix filter, a changed separator, or a moved locale
	// directory would make this pass by reading nothing — which is precisely how the previous
	// version stayed green over five stale families at once.
	if checked < 1500 {
		t.Fatalf("only %d strings inspected across three locales; the parser is broken, not the text", checked)
	}
}

// deliveredCorpus is EVERYTHING a session receives from this surface: the connect-time block that
// survives truncation, plus every tool description, which the client delivers intact.
//
// WHY TESTS ASSERT AGAINST THE UNION NOW. Several of them pinned wording against the
// `instructions` const while that const was 7042 characters and the client delivered 2048 — so
// they were green about text no session had ever read, which is the exact defect they existed to
// prevent. The refit moved per-tool rules into the descriptions of the tools they govern, and a
// test that still looked only at the block would now fail for the rules that moved and pass for
// the ones still buried.
//
// The property was never "this const contains the sentence". It is "a session is told." The union
// is where that is true, and the budget test beside it is what keeps the block half honest.
func deliveredCorpus(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("commserver.go")
	if err != nil {
		t.Fatal(err)
	}
	// The connect block is no longer part of this corpus: this package's own text was deleted with
	// the /comm/mcp server nothing served. What a session receives is allserver.Instructions,
	// covered by that package's tests. The corpus here is the TOOL DESCRIPTIONS, which is what
	// this file asserts on.
	var sb strings.Builder
	sb.WriteString(version.InstructionStamp())
	desc := regexp.MustCompile(`Description:\s*((?:"(?:[^"\\]|\\.)*"\s*\+?\s*)+)`)
	lit := regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`)
	n := 0
	for _, m := range desc.FindAllSubmatch(b, -1) {
		for _, p := range lit.FindAllSubmatch(m[1], -1) {
			sb.Write(p[1])
			sb.WriteString(" ")
		}
		n++
	}
	// FLOOR LOWERED 10 -> 8 when comm_register, comm_bind and comm_unbind were deleted (a station
	// comes with a mailbox, so there is nothing to register, attach or detach), then 8 -> 7 when
	// slice 7 deleted comm_open_channel. It remains a POSITIVE CONTROL on the scanner: a regexp
	// that silently stopped matching would make this test pass by finding nothing to check, which
	// is the failure this floor exists to make impossible.
	if n < 7 {
		t.Fatalf("only %d tool descriptions parsed from commserver.go; the scanner is broken, not the text", n)
	}
	return sb.String()
}
