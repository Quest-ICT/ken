package web

import (
	"strings"
	"testing"
	"time"

	"github.com/Quest-ICT/ken/internal/comm"
	"github.com/Quest-ICT/ken/internal/store"
)

// S6 HAS REQUIRED THIS SINCE STATIONS SHIPPED AND NOTHING RENDERED IT.
//
// Revoking a station key SEVERS every endpoint it bound — the sessions stop at their next
// call and cannot reconnect with it. `CountEndpointsBoundBy` was written for exactly this
// and had one caller: a test whose failure message reads "the console states this number
// before the click", standing in for a surface that did not exist.
//
// With COMM OFF the count is genuinely unknown, and the confirm must say so rather than
// assert a zero nobody measured — the same defect fed8838 repaired one page over.
func TestRevokingAStationKeySaysTheCountIsUnknownWhenCommIsOff(t *testing.T) {
	st, ctx, cli, base, actor := stationsHarness(t) // no comm handle
	s, err := st.CreateStation(ctx, "prod-ops", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.IssueStationKey(ctx, actor, s.StationID, "laptop", []string{"station"}); err != nil {
		t.Fatal(err)
	}
	body := get(t, cli, base+"/tokens")

	if !strings.Contains(body, "UNKNOWN") {
		t.Fatal("with COMM off the confirm does not say the sever count is unknown")
	}
	if strings.Contains(body, "disconnects 0 live session") {
		t.Fatal("the confirm asserts a measured zero for a count that was never taken")
	}
	// CONTROL: the station warning still renders, so a failure above is about the COUNT and
	// not about a confirm that never assembled at all.
	if !strings.Contains(body, "STATION key") {
		t.Fatal("the station warning is missing — the fixture did not produce a station key row")
	}
}

// AND WITH COMM ON IT STATES THE REAL NUMBER, matched to what revoking would actually sever.
func TestRevokingAStationKeyStatesHowManySessionsItSevers(t *testing.T) {
	st, ctx, cli, base, actor := stationsHarnessWithComm(t)
	s, err := st.CreateStation(ctx, "prod-ops", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := st.IssueStationKey(ctx, actor, s.StationID, "laptop", []string{"station"})
	if err != nil {
		t.Fatal(err)
	}
	// The key id is the token id — the same handle SeverEndpointsBoundBy takes.
	keyID := strings.Split(strings.TrimPrefix(secret, "kens_"), "_")[0]
	cs := commOf(t)
	for i := 0; i < 2; i++ {
		ep, _, err := cs.RegisterEndpoint(ctx, comm.Owner{TokenID: keyID, ActorID: actor}, s.StationID, "")
		if err != nil {
			t.Fatal(err)
		}
		if err := cs.BindEndpointToStation(ctx, ep.EndpointID, s.StationID, keyID); err != nil {
			t.Fatal(err)
		}
	}

	// THE ORACLE IS THE ACTION, NOT A LITERAL. Asserting "2" would pass against a count that
	// disagrees with what revoking severs — and those two diverging silently is the whole
	// failure S6 names. Ask the store what the sever would do, then require the page to say it.
	n, err := cs.CountEndpointsBoundBy(ctx, keyID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("fixture bound %d endpoints, want 2", n)
	}
	body := get(t, cli, base+"/tokens")
	if !strings.Contains(body, "disconnects 2 live sessions") {
		t.Fatalf("the confirm does not state the %d sessions revoking would sever", n)
	}
	if strings.Contains(body, "UNKNOWN") {
		t.Fatal("the count was available and the confirm still said unknown")
	}
}

// *** THE CONNECTOR ROW MUST NAME THE CAUSE, NOT JUST THE SYMPTOM. ***
//
// A legacy grant reaches the knowledge base only, whatever URL the connector points at — so a
// session listing kb_* tools and nothing else may be carrying a stale grant rather than a wrong
// URL. Vlad hit exactly that on the live estate and debugged it as a URL mistake, because deleting
// a connector revokes nothing and re-adding silently reuses the grant. The page is where the two
// facts finally meet, so the badge has to render.
func TestTheConnectorRowFlagsALegacyGrantAndShowsItsRedirectHost(t *testing.T) {
	st, ctx, cli, base := oauthHarness(t)

	// A grant approved before per-surface scopes existed, with a loopback redirect — the shape
	// that made an unaccountable client plausible on the live estate.
	clientID, err := st.RegisterOAuthClient(ctx, "old-connector", []string{"http://127.0.0.1:9876/callback"})
	if err != nil {
		t.Fatal(err)
	}
	// A second human to own the grant. The approver's identity is not what this test is about —
	// the badge and the redirect host are — and reusing "admin" collides on the unique name.
	human, err := st.CreateHumanUser(ctx, "approver", "x")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateOAuthGrantAndCode(ctx, store.NewAuthCode{
		ClientID:            clientID,
		ConnectorActorID:    human,
		HumanActorID:        human,
		RedirectURI:         "http://127.0.0.1:9876/callback",
		CodeChallenge:       "x",
		CodeChallengeMethod: "S256",
		Scope:               "read write offline_access",
	}, time.Minute); err != nil {
		t.Fatal(err)
	}
	body := get(t, cli, base+"/tokens")
	if !strings.Contains(body, "legacy grant") {
		t.Error("a grant with no ken: scope renders with no legacy marker, so the page still shows " +
			"the symptom and hides the cause — which is the failure this exists to prevent")
	}
	if !strings.Contains(body, "127.0.0.1:9876") {
		t.Error("the connector row does not show the registered redirect host — the only field on " +
			"it that carries trust, since the application name is self-reported")
	}
}
