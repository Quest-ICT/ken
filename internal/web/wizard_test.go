package web

import (
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Quest-ICT/ken/internal/store"
)

// TestWebFirstRunWizard: with no admin, every page funnels to /setup; creating
// the admin ends the wizard and enables normal login.
func TestWebFirstRunWizard(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	const tok = "test-setup-token"
	srv := httptest.NewServer(Handler(Deps{Store: st, SetupToken: tok}))
	defer srv.Close()
	jar, _ := cookiejar.New(nil)
	cli := &http.Client{Jar: jar}

	// No user yet: the dashboard funnels to /setup — but WITHOUT the token the form
	// is withheld (only the token hint is shown).
	if b := get(t, cli, srv.URL+"/"); strings.Contains(b, `name="password2"`) {
		t.Fatalf("setup form must require the token; it was shown without one: %s", trunc(b))
	}

	// With the token, the form is served; create the admin (double-submit scsrf).
	scsrf := extract(t, cli, srv.URL+"/setup?token="+tok, `name="scsrf" value="([^"]+)"`)
	if b := postForm(t, cli, srv.URL+"/setup", url.Values{
		"name": {"admin"}, "password": {"supersecret"}, "password2": {"supersecret"}, "scsrf": {scsrf}, "token": {tok},
	}); !strings.Contains(b, "Sign in") {
		t.Fatalf("after setup should land on login: %s", trunc(b))
	}

	// Wizard is gone: / now goes to login (not setup), and the admin can sign in.
	if b := get(t, cli, srv.URL+"/"); !strings.Contains(b, "Sign in") || strings.Contains(b, `name="password2"`) {
		t.Fatalf("wizard should be finished: %s", trunc(b))
	}
	lcsrf := extract(t, cli, srv.URL+"/login", `name="lcsrf" value="([^"]+)"`)
	if body := postForm(t, cli, srv.URL+"/login", url.Values{"name": {"admin"}, "password": {"supersecret"}, "lcsrf": {lcsrf}}); !strings.Contains(body, "Recent activity") {
		t.Fatalf("admin login after setup failed: %s", trunc(body))
	}
}
