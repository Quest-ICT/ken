package web

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestEveryPostRouteHasAConsoleSurface fails when the mux registers a POST route that
// no template and no script can reach.
//
// WHY THIS GATE EXISTS, and it is the project's recurring defect class one level up from
// where the other gates watch for it. `internal/audit/reachability_test.go` fails when an
// exported store method has no production caller; the refusal gate fails when a
// caller-facing refusal arrives as "internal error". Both watch the seam BELOW the HTTP
// surface. Neither can see a route that is wired end to end — handler, validation, store
// primitive, i18n strings, tests — and has no button.
//
// THAT IS NOT HYPOTHETICAL. `POST /comm/tokens/{id}/repoint` shipped in 3.19.0 complete:
// route, handler, `RepointEndpointsOfToken`, a flash string in three locales, a console
// test that exercised it through the mux. Nothing rendered a form that posts to it. The
// bulk verb is the one that matters — eleven live endpoints hang off one token on the live
// estate, and the per-endpoint control is exactly the ceremony the bulk verb exists to
// remove — so the half that was unreachable was the half worth having.
//
// It also breaks a standing rule: the console is the main method for any operation, and a
// CLI subcommand is a fallback rather than the deliverable. A route only curl can reach
// is not a console operation.
//
// A test through the mux cannot catch this, because it POSTs the path itself — it proves
// the handler works, never that anything offers it. This test reads the TEMPLATES instead.
func TestEveryPostRouteHasAConsoleSurface(t *testing.T) {
	src, err := os.ReadFile("app.go")
	if err != nil {
		t.Fatalf("read app.go: %v", err)
	}
	routeRe := regexp.MustCompile(`mux\.HandleFunc\("POST ([^"]+)"`)
	var routes []string
	for _, m := range routeRe.FindAllStringSubmatch(string(src), -1) {
		routes = append(routes, m[1])
	}
	// POSITIVE CONTROL ON THE INSTRUMENT. A regexp that silently stops matching — a
	// refactor to a router helper, a rename — would make this test pass by finding
	// nothing to check, which is the failure mode it exists to prevent elsewhere.
	if len(routes) < 25 {
		t.Fatalf("found only %d POST routes in app.go; the route scanner is broken, not the routes", len(routes))
	}

	// Everything a browser could post from: the templates and the one script.
	//
	// ONLY `action="…"` COUNTS, and that anchoring is the whole strength of this gate. The
	// first version searched the corpus for the path anywhere at all, which meant a nav link
	// `href="/settings"` in base.html satisfied the route `POST /settings`. Verified by
	// deleting settings.html's only form action: the gate stayed green. Every route whose
	// surface is a page-level form — /settings, /setup, /tokens, /rooms — was effectively
	// unwatched, while the per-row routes passed for the right reason by accident (nothing
	// links to `/comm/endpoints/{id}/rotate`).
	//
	// A gate that reports success because of a link to the page a form lives on is this
	// project's defect class wearing the uniform of the check built to catch it — found by an
	// adversarial review of the very commit that added it.
	var surface strings.Builder
	err = filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		switch filepath.Ext(path) {
		case ".html", ".js":
			b, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			surface.Write(b)
			surface.WriteString("\n")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk templates: %v", err)
	}
	hay := surface.String()
	// THE ANCHOR IS A FORM THAT MUST KEEP EXISTING, and it earned a note by failing honestly.
	// It used to be `action="/comm/endpoints/`, and it fired the moment those forms were deleted —
	// correctly: the anchor's whole job is to prove the walk found real markup before any absence
	// below is read as evidence. Re-anchored on the station rename form, which is the console's
	// most permanent control (a human naming a post is the one act the design never delegates).
	if !strings.Contains(hay, `action="/stations/`) {
		t.Fatal("the template corpus does not contain a known-present form action; the walk is broken, not the routes")
	}

	// Routes reachable by a path the console never spells out literally. Each needs a
	// REASON, and the test fails in the other direction too — an entry that turns out to
	// be reachable must be deleted, so the list cannot quietly become a graveyard.
	//
	// IT IS EMPTY, and it was written non-empty. The first draft exempted `/oauth/authorize`
	// on the reasoning that the consent form posts to the request URL with its query string
	// rather than to a literal path. The consent template spells the path out; the
	// both-directions check said so on the first run. Kept as a mechanism because the next
	// route reached from JS by string concatenation will need it — and kept honest by the
	// same check, which fails when an entry stops being true.
	allowed := map[string]string{}

	var unreachable []string
	for _, route := range routes {
		// A route pattern's wildcards stand for template expressions ({{.EndpointID}}),
		// which never contain a slash or a quote.
		pat := regexp.QuoteMeta(route)
		pat = regexp.MustCompile(`\\\{[a-z]+\\\}`).ReplaceAllString(pat, `[^"/]*`)
		found := regexp.MustCompile(`action="` + pat + `"`).MatchString(hay)
		reason, exempt := allowed[route]
		switch {
		case found && exempt:
			t.Errorf("route %s is in the allowlist (%q) but the console does reach it — delete the entry", route, reason)
		case !found && !exempt:
			unreachable = append(unreachable, route)
		}
	}
	sort.Strings(unreachable)
	for _, route := range unreachable {
		t.Errorf("POST %s has a handler but no console surface: nothing in any template or script posts to it. "+
			"Render a control for it, or add it to the allowlist with the reason it is reached another way.", route)
	}
}
