package web

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Quest-ICT/ken/internal/passwd"
	"github.com/Quest-ICT/ken/internal/store"
)

func oauthTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	return st
}

// TestOAuthConsentFlow drives the interactive half end-to-end: log in, view the
// consent page, approve, and confirm a single-use code is issued back to the
// registered redirect URI — and that the code redeems in the store.
func TestOAuthConsentFlow(t *testing.T) {
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

	// Log in.
	lcsrf := extract(t, cli, srv.URL+"/login", `name="lcsrf" value="([^"]+)"`)
	postForm(t, cli, srv.URL+"/login", url.Values{"name": {"admin"}, "password": {"supersecret"}, "lcsrf": {lcsrf}})

	authURL := srv.URL + "/oauth/authorize?" + url.Values{
		"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {redir},
		"code_challenge": {"chal-xyz"}, "code_challenge_method": {"S256"},
		"state": {"st4te"}, "scope": {"read write offline_access"}, "resource": {"https://host/mcp"},
	}.Encode()

	// The consent page renders with an Approve control; grab its CSRF token.
	page := get(t, cli, authURL)
	if !strings.Contains(page, "Authorize connection") || !strings.Contains(page, "Approve") {
		t.Fatalf("consent page not shown: %s", trunc(page))
	}
	// Anti-phishing: the page MUST surface the redirect destination host so the
	// human can tell the real connector from an impostor, and mark the name as
	// self-reported (the redirect host is the only unforgeable identity).
	if !strings.Contains(page, "claude.ai") || !strings.Contains(page, "self-reported") {
		t.Fatalf("consent page must show the redirect destination + self-reported caveat: %s", trunc(page))
	}
	// Regression: the consent page's CSP form-action MUST include the client
	// redirect origin, or the browser blocks the post-approve redirect ("Approve
	// does nothing"). A bare form-action 'self' is the bug.
	if hr, err := cli.Get(authURL); err == nil {
		hr.Body.Close()
		csp := hr.Header.Get("Content-Security-Policy")
		if !strings.Contains(csp, "form-action 'self' https://claude.ai") {
			t.Fatalf("consent CSP must allow the redirect to the client origin, got: %q", csp)
		}
	}
	csrf := extract(t, cli, authURL, `name="csrf" value="([^"]+)"`)

	// Approve — the browser is 303-redirected to the client's callback with a code;
	// don't follow it (claude.ai is not reachable in a test).
	noRedir := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noRedir.PostForm(srv.URL+"/oauth/authorize", url.Values{
		"csrf": {csrf}, "decision": {"approve"},
		"client_id": {clientID}, "redirect_uri": {redir}, "response_type": {"code"},
		"code_challenge": {"chal-xyz"}, "code_challenge_method": {"S256"},
		"state": {"st4te"}, "scope": {"read write offline_access"}, "resource": {"https://host/mcp"},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("approve want 303, got %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.HasPrefix(loc, redir) {
		t.Fatalf("must redirect to the registered callback, got %q", loc)
	}
	u, _ := url.Parse(loc)
	code := u.Query().Get("code")
	if code == "" || u.Query().Get("state") != "st4te" {
		t.Fatalf("callback missing code/state: %q", loc)
	}
	// The issued code is real: it redeems in the store.
	if cd, err := st.PeekOAuthCode(ctx, code); err != nil || cd.ClientID != clientID {
		t.Fatalf("issued code should redeem: %+v %v", cd, err)
	}
}

// TestOAuthDenyRedirectsWithError: denying consent bounces back with
// error=access_denied and no code.
func TestOAuthDenyRedirectsWithError(t *testing.T) {
	st := oauthTestStore(t)
	ctx := context.Background()
	hash, _ := passwd.Hash("supersecret", passwd.Standard)
	st.CreateHumanUser(ctx, "admin", hash)
	const redir = "https://claude.ai/api/mcp/auth_callback"
	clientID, _ := st.RegisterOAuthClient(ctx, "Claude", []string{redir})

	srv := httptest.NewServer(Handler(Deps{Store: st, OAuthEnabled: true}))
	defer srv.Close()
	jar, _ := cookiejar.New(nil)
	cli := &http.Client{Jar: jar}
	lcsrf := extract(t, cli, srv.URL+"/login", `name="lcsrf" value="([^"]+)"`)
	postForm(t, cli, srv.URL+"/login", url.Values{"name": {"admin"}, "password": {"supersecret"}, "lcsrf": {lcsrf}})
	authURL := srv.URL + "/oauth/authorize?" + url.Values{
		"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {redir},
		"code_challenge": {"c"}, "code_challenge_method": {"S256"}, "state": {"s"},
	}.Encode()
	csrf := extract(t, cli, authURL, `name="csrf" value="([^"]+)"`)

	noRedir := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, _ := noRedir.PostForm(srv.URL+"/oauth/authorize", url.Values{
		"csrf": {csrf}, "decision": {"deny"},
		"client_id": {clientID}, "redirect_uri": {redir}, "code_challenge": {"c"}, "code_challenge_method": {"S256"}, "state": {"s"},
	})
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	if !strings.HasPrefix(loc, redir) || !strings.Contains(loc, "error=access_denied") {
		t.Fatalf("deny should redirect with access_denied, got %q", loc)
	}
}

// TestOAuthDisabledNoRoute: with OAuth off, the consent route is not mounted.
func TestOAuthDisabledNoRoute(t *testing.T) {
	st := oauthTestStore(t)
	ctx := context.Background()
	hash, _ := passwd.Hash("supersecret", passwd.Standard)
	st.CreateHumanUser(ctx, "admin", hash)

	srv := httptest.NewServer(Handler(Deps{Store: st, OAuthEnabled: false}))
	defer srv.Close()
	cli := &http.Client{}
	resp, err := cli.Get(srv.URL + "/oauth/authorize?client_id=x")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("OAuth disabled: /oauth/authorize want 404, got %d", resp.StatusCode)
	}
}
