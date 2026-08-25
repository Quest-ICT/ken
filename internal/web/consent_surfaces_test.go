package web

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Quest-ICT/ken/internal/passwd"
	"github.com/Quest-ICT/ken/internal/store"
)

// *** UNTICKING A SURFACE ON THE CONSENT SCREEN ACTUALLY WITHHOLDS IT. ***
//
// §10 step 2 consolidates three authenticators into one OAuth identity, which removes the control
// that said "/comm/mcp accepts a ken_ token and NOTHING else". docs/IDENTITY-CONTROLS.md permits
// that on one condition, stated exactly: the withholding must be "re-expressed as an EXPLICIT
// PER-SURFACE CAPABILITY DECISION AT GRANT TIME, not inherited from the fact that three files
// exist."
//
// So the checkbox is not a nicety — it IS the control. If it renders and does nothing, the
// register's warning has come true in its worst form: "the removal is invisible; every surface
// keeps working, better even, and the day a connector is compromised the blast radius has quietly
// grown from the knowledge base to the message bus and the vault."
//
// Mutation found this gap: making the handler ignore the posted selection left the whole suite
// green, because nothing exercised the consent POST with a narrowed set.
func TestUntickingASurfaceOnConsentWithholdsIt(t *testing.T) {
	st := oauthTestStore(t)
	ctx := context.Background()
	hash, _ := passwd.Hash("supersecret", passwd.Standard)
	if _, err := st.CreateHumanUser(ctx, "admin", hash); err != nil {
		t.Fatal(err)
	}
	const redir = "https://claude.ai/api/mcp/auth_callback"
	clientID, err := st.RegisterOAuthClient(ctx, "Claude", []string{redir})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(Handler(Deps{Store: st, OAuthEnabled: true}))
	defer srv.Close()
	jar, _ := cookiejar.New(nil)
	cli := &http.Client{Jar: jar}
	lcsrf := extract(t, cli, srv.URL+"/login", `name="lcsrf" value="([^"]+)"`)
	postForm(t, cli, srv.URL+"/login", url.Values{"name": {"admin"}, "password": {"supersecret"}, "lcsrf": {lcsrf}})

	authURL := srv.URL + "/oauth/authorize?" + url.Values{
		"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {redir},
		"code_challenge": {"chal-xyz"}, "code_challenge_method": {"S256"},
		"state": {"st4te"}, "scope": {"read write offline_access"},
	}.Encode()

	// THE SCREEN OFFERS EVERY SURFACE, TICKED. A human who reads nothing grants everything,
	// because no Ken feature is optional or off by default.
	page := get(t, cli, authURL)
	for _, sc := range store.DefaultGrantScopes() {
		if !strings.Contains(page, `value="`+sc+`" checked`) {
			t.Errorf("the consent screen does not offer %q pre-ticked; a human who reads nothing must "+
				"still grant everything", sc)
		}
	}

	// APPROVE WITH MESSAGING UNTICKED — the browser omits the unchecked box entirely.
	csrf := extract(t, cli, authURL, `name="csrf" value="([^"]+)"`)
	noRedir := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noRedir.PostForm(srv.URL+"/oauth/authorize", url.Values{
		"csrf": {csrf}, "decision": {"approve"},
		"client_id": {clientID}, "redirect_uri": {redir}, "response_type": {"code"},
		"code_challenge": {"chal-xyz"}, "code_challenge_method": {"S256"},
		"state": {"st4te"}, "scope": {"read write offline_access"},
		"ken_surface": {store.ScopeKB, store.ScopeStation}, // messaging deliberately absent
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("approve did not redirect: %d", resp.StatusCode)
	}

	// THE GRANT RECORDS WHAT THE HUMAN CHOSE, and the capability mapping honours it.
	grants, err := st.ListOAuthGrants(ctx)
	if err != nil || len(grants) != 1 {
		t.Fatalf("want exactly one grant, got %d (%v)", len(grants), err)
	}
	scope := grants[0].Scope
	if strings.Contains(scope, store.ScopeCommSet) {
		t.Errorf("the grant records %q despite messaging being unticked: %q", store.ScopeCommSet, scope)
	}
	for _, want := range []string{store.ScopeKB, store.ScopeStation} {
		if !strings.Contains(scope, want) {
			t.Errorf("the grant does not record %q, which the human left ticked: %q", want, scope)
		}
	}
	caps := map[string]bool{}
	for _, c := range store.GrantedCapabilities(scope) {
		caps[c] = true
	}
	if caps["comm"] || caps["comm-file"] {
		t.Error("a connector the human withheld messaging from can still reach COMM — the checkbox " +
			"renders and does nothing, which is the invisible removal IDENTITY-CONTROLS.md warns about")
	}
	if !caps["read"] || !caps["station"] {
		t.Errorf("the surfaces the human DID grant were lost too (%v) — this test would then be "+
			"passing because nothing works, not because withholding works", caps)
	}
}
