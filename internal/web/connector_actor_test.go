package web

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Quest-ICT/ken/internal/passwd"
	"github.com/Quest-ICT/ken/internal/store"
)

// A connector must be able to author its writes as an actor the operator names.
//
// It could not, and that silently disabled Ken's only provenance signal. `viaComm`
// asks whether THIS actor recently received inter-session traffic; COMM traffic
// arrives under the actor a `comm` token was minted with; and an OAuth grant's actor
// was invented here from the client's self-reported display name, so the two could
// never be the same row. A session that read a peer's message and then saved what it
// learned through the connector produced via_comm=NULL, and an absent badge is
// indistinguishable from a checked-and-clean one.
//
// This is the same question as "which actor is this station key minted under", which
// is why both now read the same candidate list.
// oauthHarness is stationsHarness with the consent flow actually mounted. The shared
// harness leaves OAuthEnabled false, so /oauth/authorize 404s there — and a test that
// skips because its route is missing is a test that asserts nothing while looking
// green, which is the failure mode this file exists to remove elsewhere.
func oauthHarness(t *testing.T) (*store.Store, context.Context, *http.Client, string) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	hash, _ := passwd.Hash("supersecret", passwd.Standard)
	if _, err := st.CreateHumanUser(ctx, "admin", hash); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(Handler(Deps{Store: st, OAuthEnabled: true}))
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	cli := &http.Client{Jar: jar}
	lcsrf := extract(t, cli, srv.URL+"/login", `name="lcsrf" value="([^"]+)"`)
	postForm(t, cli, srv.URL+"/login", url.Values{"name": {"admin"}, "password": {"supersecret"}, "lcsrf": {lcsrf}})
	return st, ctx, cli, srv.URL
}

func TestConnectorWritesAsTheActorTheOperatorChose(t *testing.T) {
	st, ctx, cli, base := oauthHarness(t)

	// The actor that holds this machine's messaging token — the one worth choosing.
	commActor, err := st.FindOrCreateActor(ctx, "ai", "claude@thisbox")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.IssueToken(ctx, commActor, []string{"comm"}, "this machine"); err != nil {
		t.Fatal(err)
	}

	client, err := registerTestClient(t, st, ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Approve, naming the actor.
	form := url.Values{
		"decision":              {"approve"},
		"csrf":                  {extract(t, cli, base+"/", `name="csrf" value="([^"]+)"`)},
		"write_as":              {strconv.FormatInt(commActor, 10)},
		"client_id":             {client.ClientID},
		"redirect_uri":          {client.RedirectURIs[0]},
		"code_challenge":        {"E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"},
		"code_challenge_method": {"S256"},
		"response_type":         {"code"},
		"state":                 {"xyz"},
	}
	// Stop at the redirect. Consent ends by bouncing the browser back to the client's
	// registered URI, which is not a host this test should try to reach.
	noFollow := *cli
	noFollow.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := noFollow.PostForm(base+"/oauth/authorize", form)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 300 || resp.StatusCode > 399 {
		t.Fatalf("consent did not redirect back to the client (HTTP %d), so it was not approved", resp.StatusCode)
	}

	var grantActor int64
	if err := st.R.QueryRowContext(ctx,
		`SELECT actor_id FROM oauth_grant ORDER BY id DESC LIMIT 1`).Scan(&grantActor); err != nil {
		t.Fatalf("consent created no grant, so the operator's choice reached nothing: %v", err)
	}

	if grantActor != commActor {
		var name string
		_ = st.R.QueryRowContext(ctx, `SELECT display_name FROM actor WHERE id=?`, grantActor).Scan(&name)
		t.Fatalf("the connector authors as actor %d (%q), not the actor the operator chose (%d).\n"+
			"Its writes can never be hearsay-marked, because the marker asks whether THAT actor received peer traffic.",
			grantActor, name, commActor)
	}
}

// The default must still work: an operator who ignores the picker gets exactly the
// old behaviour, not a failed consent. Without this, the assertion above could pass
// on a flow that refuses everything it does not recognise.
func TestConnectorFallsBackToItsOwnActorWhenNoneIsChosen(t *testing.T) {
	st, ctx, _, _, _ := stationsHarness(t)
	a := &app{store: st}

	client := &store.OAuthClient{ClientID: "c1", Name: "Some Client"}
	req := httptest.NewRequest("POST", "/oauth/authorize", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	id, err := a.resolveConnectorActor(req, client)
	if err != nil {
		t.Fatalf("an empty choice failed instead of falling back: %v", err)
	}
	var kind, name string
	if err := st.R.QueryRowContext(ctx, `SELECT kind, display_name FROM actor WHERE id=?`, id).Scan(&kind, &name); err != nil {
		t.Fatal(err)
	}
	if kind != "ai" || name != "Some Client" {
		t.Fatalf("fallback produced actor %s:%s, want ai:Some Client", kind, name)
	}
}

// A forged id must not attribute one connector's writes to another identity.
// Authorship is the field a human reads when deciding whether to promote, and it is
// the one kind of wrong the curation gate cannot repair afterwards.
func TestAnUnknownActorIdIsRefusedRatherThanTrusted(t *testing.T) {
	st, ctx, _, _, _ := stationsHarness(t)
	a := &app{store: st}

	body := url.Values{"write_as": {"999999"}}.Encode()
	req := httptest.NewRequest("POST", "/oauth/authorize", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	id, err := a.resolveConnectorActor(req, &store.OAuthClient{ClientID: "c1", Name: "Some Client"})
	if err != nil {
		t.Fatal(err)
	}
	if id == 999999 {
		t.Fatal("an actor id that names no row was accepted from the form")
	}
	var name string
	if err := st.R.QueryRowContext(ctx, `SELECT display_name FROM actor WHERE id=?`, id).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "Some Client" {
		t.Fatalf("a bad id resolved to %q rather than falling back to the client's own actor", name)
	}
}

// registerTestClient registers a client and reads it back, so the test drives the
// same rows the real consent flow would.
func registerTestClient(t *testing.T, st *store.Store, ctx context.Context) (*store.OAuthClient, error) {
	t.Helper()
	id, err := st.RegisterOAuthClient(ctx, "Test Connector", []string{"https://example.invalid/cb"})
	if err != nil {
		return nil, err
	}
	return st.OAuthClientByID(ctx, id)
}
