package web

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// *** EVERY FORM IN THE CONSOLE MUST POST TO A ROUTE THAT EXISTS. ***
//
// This gate was written the day it was earned. Station keys were retired: the store functions
// went, the handler went, the route went, and the keys table was cut from stations.html under a
// comment that said "THE KEYS TABLE IS GONE." Four lines below that comment, the MINT form was
// still there, still POSTing to /stations/{id}/key. A human clicking "Mint" would have got a bare
// 404 from a button the console rendered for them.
//
// NOTHING ELSE WOULD HAVE CAUGHT IT. The build cannot: a form action is a string in HTML. The
// suite cannot: no test drives a control it does not know about, and a deleted feature's tests are
// deleted with it. The i18n orphan check cannot: the strings were still referenced — by the dead
// form. And a comment asserting the deletion was complete was the very thing that made it look
// finished. That is this project's recurring shape, a check whose failure renders identically to
// the thing it checks for, and the only escape is an instrument that reads what SHIPS.
//
// SO IT PARSES THE TEMPLATES, not the handlers: it collects every `action="…"` and requires a
// registered pattern to match it. Route variables ({id}, {file}) match a template's own
// interpolation, and both sides collapse to a wildcard segment before comparison — the property
// under test is "some handler claims this path shape", not an exact string.
func TestEveryFormActionHasARoute(t *testing.T) {
	// The route table, read from the source that registers it. Reading app.go rather than a
	// hand-kept list is the point: a list would need updating in the same change that deletes a
	// route, which is exactly the update that gets missed.
	src, err := os.ReadFile("app.go")
	if err != nil {
		t.Fatal(err)
	}
	routeRe := regexp.MustCompile(`mux\.HandleFunc\("(?:GET|POST|PUT|DELETE|HEAD) ([^"]+)"`)
	var routes []string
	for _, m := range routeRe.FindAllStringSubmatch(string(src), -1) {
		routes = append(routes, normalisePath(m[1]))
	}
	// POSITIVE CONTROL. If the regex stops matching — a refactor to a router package, a change in
	// how methods are spelled — every action below would "have no route" and the failure would
	// read as dozens of broken forms rather than one broken test.
	if len(routes) < 20 {
		t.Fatalf("only %d routes parsed out of app.go; the extractor is broken, not the templates", len(routes))
	}

	files, err := filepath.Glob(filepath.Join("templates", "*.html"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no templates found — this test would pass vacuously")
	}
	actionRe := regexp.MustCompile(`action="([^"]*)"`)
	var checked int
	for _, f := range files {
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range actionRe.FindAllStringSubmatch(string(body), -1) {
			raw := m[1]
			// Only same-origin paths. An empty action posts to the current URL and an absolute one
			// leaves this server; neither is a claim about the route table.
			if !strings.HasPrefix(raw, "/") {
				continue
			}
			checked++
			want := normalisePath(strings.SplitN(raw, "?", 2)[0])
			var ok bool
			for _, r := range routes {
				if r == want {
					ok = true
					break
				}
			}
			if !ok {
				t.Errorf("%s posts to %q and no handler is registered for it.\n"+
					"A console that renders a control which 404s is worse than one that omits it: "+
					"the human believes the operation exists.", filepath.Base(f), raw)
			}
		}
	}
	if checked < 10 {
		t.Fatalf("only %d form actions examined; the templates or the extractor changed shape and "+
			"this gate is no longer reading what it claims to read", checked)
	}
}

// normalisePath collapses every variable segment to "*", on both sides, so a route pattern's
// {id} and a template's {{.StationID}} compare equal. Everything else must match literally.
func normalisePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if strings.Contains(s, "{") {
			segs[i] = "*"
		}
	}
	return strings.Join(segs, "/")
}
