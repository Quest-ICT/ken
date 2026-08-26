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
)

// *** TestUntickingASurfaceOnConsentWithholdsIt WAS DELETED HERE, 2026-08-26. ***
//
// It asserted that a human could untick a surface on the consent screen and get a grant without
// it. The behaviour is gone, so the test goes with it, in the same commit — see the tombstone in
// internal/store/scopes.go.
//
// Vlad, having stated it more than once: "no ken services (or surfaces) are optional. All
// sessions get everything (they can use)." The consent screen's checkboxes existed only to build
// the state that requirement forbids — a session with no messaging, or no knowledge base — and
// they asked the human to make a decision on every approval with no basis for making it. If
// everything is included, there is nothing to choose.
//
// The list of surfaces REMAINS on the screen, stated rather than offered: a consent screen that
// does not say what it grants is worse than one that does. TestConsentStatesEverySurface below
// is the replacement, and it is the inverse assertion.

// TestConsentStatesEverySurface locks the property that replaced the checkboxes: every surface is
// NAMED on the consent screen, and none of them is presented as a choice.
func TestConsentStatesEverySurface(t *testing.T) {
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
	page := get(t, cli, authURL)

	// NO SURFACE IS A CHECKBOX. Asserted on the input NAME rather than on prose, because the
	// prose is translated and the input is what actually carries a choice to the server.
	if strings.Contains(page, `name="ken_surface"`) {
		t.Error("the consent screen still offers per-surface checkboxes; a human can withhold a " +
			"surface, which builds exactly the session Vlad's standing requirement forbids")
	}

	// AND EVERY SURFACE IS STILL NAMED. Removing the CHOICE must not remove the DISCLOSURE — a
	// consent screen that does not say what it grants is worse than one that does, and deleting
	// the inputs is one careless edit away from deleting the list with them.
	for _, name := range []string{"Knowledge base", "Messaging", "Working identity"} {
		if !strings.Contains(page, name) {
			t.Errorf("the consent screen no longer names %q; the disclosure went out with the "+
				"checkbox, which was not the intent", name)
		}
	}
}
