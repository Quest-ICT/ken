package web

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/Quest-ICT/ken/internal/comm"
	"github.com/Quest-ICT/ken/internal/passwd"
	"github.com/Quest-ICT/ken/internal/store"
	"github.com/Quest-ICT/ken/internal/version"
)

func TestWebLoginBrowsePromote(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	hash, _ := passwd.Hash("supersecret", passwd.Standard)
	if _, err := st.CreateHumanUser(ctx, "admin", hash); err != nil {
		t.Fatal(err)
	}
	// A draft entry = a pending proposal to review.
	sr, err := st.Save(ctx, store.SaveInput{
		Kind:       "project",
		Content:    store.Content{Title: "Widget note", Summary: "about widgets", Solution: "do X"},
		AuthorKind: "ai",
	})
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(Handler(Deps{Store: st}))
	defer srv.Close()
	jar, _ := cookiejar.New(nil)
	cli := &http.Client{Jar: jar}

	// Unauthenticated dashboard redirects to login.
	if body := get(t, cli, srv.URL+"/"); !strings.Contains(body, "Sign in") {
		t.Fatalf("expected redirect to login, got: %s", trunc(body))
	}

	// Bad password is rejected (do this before logging in — once authenticated,
	// GET /login redirects away and has no login form).
	lcsrfBad := extract(t, cli, srv.URL+"/login", `name="lcsrf" value="([^"]+)"`)
	if b := postForm(t, cli, srv.URL+"/login", url.Values{"name": {"admin"}, "password": {"nope"}, "lcsrf": {lcsrfBad}}); !strings.Contains(b, "Invalid credentials") {
		t.Fatalf("bad password should be rejected: %s", trunc(b))
	}

	// Log in (double-submit login CSRF via the lcsrf cookie set on GET /login).
	lcsrf := extract(t, cli, srv.URL+"/login", `name="lcsrf" value="([^"]+)"`)
	body := postForm(t, cli, srv.URL+"/login", url.Values{"name": {"admin"}, "password": {"supersecret"}, "lcsrf": {lcsrf}})
	if !strings.Contains(body, "Recent activity") {
		t.Fatalf("login did not land on dashboard: %s", trunc(body))
	}

	// Proposal queue shows the draft.
	if pbody := get(t, cli, srv.URL+"/proposals"); !strings.Contains(pbody, sr.Slug) {
		t.Fatalf("proposals page missing slug %q: %s", sr.Slug, trunc(pbody))
	}

	vid := strconv.FormatInt(sr.VersionID, 10)

	// Promote with a bad CSRF token -> 403 (before touching the store).
	resp := rawPostForm(t, cli, srv.URL+"/proposals/"+vid+"/promote", url.Values{"csrf": {"wrong"}, "slug": {sr.Slug}})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("bad CSRF should be 403, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Promote with the real CSRF token from the entry page (the review page was
	// folded into it — the promote/reject forms now live on /entry/{slug}).
	csrf := extract(t, cli, srv.URL+"/entry/"+sr.Slug, `name="csrf" value="([^"]+)"`)
	postForm(t, cli, srv.URL+"/proposals/"+vid+"/promote", url.Values{"csrf": {csrf}, "slug": {sr.Slug}})

	e, err := st.GetEntry(ctx, sr.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if e.Lifecycle != "active" || e.CuratedRev != 1 {
		t.Fatalf("promote via web did not take effect: %+v", e)
	}

	// Forced-logout protection (review #7): a forged POST /logout without the CSRF
	// token is a 403 and must NOT clear the session.
	if r := rawPostForm(t, cli, srv.URL+"/logout", url.Values{"csrf": {"wrong"}}); r.StatusCode != http.StatusForbidden {
		r.Body.Close()
		t.Fatalf("bad-CSRF logout should be 403, got %d", r.StatusCode)
	} else {
		r.Body.Close()
	}
	if b := get(t, cli, srv.URL+"/"); !strings.Contains(b, "Recent activity") {
		t.Fatal("session must survive a forged logout")
	}
}

// TestWebTokenReveal exercises the one-time-secret reveal on /tokens: it must show
// the freshly issued secret, a copy-paste registration command carrying the request's
// real host (not a placeholder), both per-field copy buttons, and the static copy
// script must be served same-origin as JavaScript.
func TestWebTokenReveal(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	hash, _ := passwd.Hash("supersecret", passwd.Standard)
	if _, err := st.CreateHumanUser(ctx, "admin", hash); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(Handler(Deps{Store: st}))
	defer srv.Close()
	jar, _ := cookiejar.New(nil)
	cli := &http.Client{Jar: jar}

	lcsrf := extract(t, cli, srv.URL+"/login", `name="lcsrf" value="([^"]+)"`)
	postForm(t, cli, srv.URL+"/login", url.Values{"name": {"admin"}, "password": {"supersecret"}, "lcsrf": {lcsrf}})

	csrf := extract(t, cli, srv.URL+"/tokens", `name="csrf" value="([^"]+)"`)
	body := postForm(t, cli, srv.URL+"/tokens", url.Values{
		"csrf": {csrf}, "actor": {"claude-code@laptop"},
		"scope": {"read", "write-draft", "propose"},
	})

	for _, want := range []string{
		`class="reveal"`,                                        // theme-safe reveal box (not the old invisible one)
		`data-copy-target="#tok-secret"`,                        // copy button for the token
		`data-copy-target="#tok-register"`,                      // copy button for the command
		`claude mcp add --transport http ken 'http://127.0.0.1`, // real host, single-quoted (shell-safe), not <ken-host>
		`/mcp' --header "Authorization: Bearer ken_`,            // fully copy-paste-ready
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("token reveal missing %q: %s", want, trunc(body))
		}
	}
	if strings.Contains(body, "&lt;ken-host&gt;") {
		t.Fatalf("registration example still shows a placeholder host: %s", trunc(body))
	}

	// The copy script is served same-origin (so it passes default-src 'self') as JS.
	resp, err := cli.Get(srv.URL + "/static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	ct := resp.Header.Get("Content-Type")
	js := readBody(resp)
	if !strings.Contains(ct, "javascript") {
		t.Fatalf("app.js Content-Type = %q, want javascript", ct)
	}
	if !strings.Contains(js, "data-copy-target") {
		t.Fatalf("app.js does not look like the copy helper: %s", trunc(js))
	}
}

// TestWebStaticAssetsAndFooter checks the favicon set and copy script are served
// same-origin with correct content types, that a missing asset 404s, and that the
// running version renders in the page footer.
func TestWebStaticAssetsAndFooter(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	hash, _ := passwd.Hash("supersecret", passwd.Standard)
	if _, err := st.CreateHumanUser(context.Background(), "admin", hash); err != nil {
		t.Fatal(err) // an admin exists → not in wizard mode, so /login renders normally
	}
	srv := httptest.NewServer(Handler(Deps{Store: st}))
	defer srv.Close()
	cli := &http.Client{}

	// Footer version + favicon link render on a public page (login), no auth needed.
	login := get(t, cli, srv.URL+"/login")
	if want := "Ken v" + version.Version; !strings.Contains(login, want) {
		t.Fatalf("footer %q not shown: %s", want, trunc(login))
	}
	if !strings.Contains(login, `href="/static/ken-logo.svg?v=`) {
		t.Fatalf("favicon link missing from <head>: %s", trunc(login))
	}

	for _, tc := range []struct{ path, ct string }{
		{"/static/ken-logo.svg", "image/svg+xml"},
		{"/static/favicon-32.png", "image/png"},
		{"/static/favicon.ico", "image/x-icon"},
		{"/static/app.js", "javascript"},
		{"/favicon.ico", "image/x-icon"},
	} {
		resp, err := cli.Get(srv.URL + tc.path)
		if err != nil {
			t.Fatal(err)
		}
		ct := resp.Header.Get("Content-Type")
		body := readBody(resp)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s -> %d", tc.path, resp.StatusCode)
		}
		if !strings.Contains(ct, tc.ct) {
			t.Fatalf("%s Content-Type = %q, want %q", tc.path, ct, tc.ct)
		}
		if len(body) == 0 {
			t.Fatalf("%s served an empty body", tc.path)
		}
	}

	// A missing static asset 404s (the handler doesn't leak or panic).
	resp, err := cli.Get(srv.URL + "/static/nope.txt")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing asset -> %d, want 404", resp.StatusCode)
	}
}

// TestWebRevertAndDowngrade covers the two recovery features: a "Revert to this"
// action on superseded history rows (re-promoting a past version), and a
// downgrade confirm on the entry-page Promote when the pending rev is older than
// the curated head.
func TestWebRevertAndDowngrade(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	sp := func(s string) *string { return &s }
	hash, _ := passwd.Hash("supersecret", passwd.Standard)
	admin, err := st.CreateHumanUser(ctx, "admin", hash)
	if err != nil {
		t.Fatal(err)
	}

	// Entry A: rev1 curated, then rev2 curated (so rev1 is superseded → revertable).
	a1, _ := st.Save(ctx, store.SaveInput{Kind: "reference", Content: store.Content{Title: "A", Summary: "s", Solution: "S1"}, AuthorKind: "ai"})
	must(t, st.Promote(ctx, store.PromoteInput{Slug: a1.Slug, VersionID: a1.VersionID, ActorID: admin, ActorKind: "human", Note: "p"}))
	a2, _ := st.ProposeEnhancement(ctx, store.ProposeInput{Slug: a1.Slug, ChangeNote: "S2", AuthorKind: "ai", Patch: store.Patch{Solution: sp("S2")}})
	must(t, st.Promote(ctx, store.PromoteInput{Slug: a1.Slug, VersionID: a2.VersionID, ActorID: admin, ActorKind: "human", Note: "p"}))

	// Entry B: rev1..rev3 proposed; promote the tip → provisional becomes rev2 (older than head).
	b1, _ := st.Save(ctx, store.SaveInput{Kind: "reference", Content: store.Content{Title: "B", Summary: "s", Solution: "S1"}, AuthorKind: "ai"})
	st.ProposeEnhancement(ctx, store.ProposeInput{Slug: b1.Slug, ChangeNote: "S2", AuthorKind: "ai", Patch: store.Patch{Solution: sp("S2")}})
	b3, _ := st.ProposeEnhancement(ctx, store.ProposeInput{Slug: b1.Slug, ChangeNote: "S3", AuthorKind: "ai", Patch: store.Patch{Solution: sp("S3")}})
	must(t, st.Promote(ctx, store.PromoteInput{Slug: b1.Slug, VersionID: b3.VersionID, ActorID: admin, ActorKind: "human", Note: "p"}))

	srv := httptest.NewServer(Handler(Deps{Store: st}))
	defer srv.Close()
	jar, _ := cookiejar.New(nil)
	cli := &http.Client{Jar: jar}
	lcsrf := extract(t, cli, srv.URL+"/login", `name="lcsrf" value="([^"]+)"`)
	postForm(t, cli, srv.URL+"/login", url.Values{"name": {"admin"}, "password": {"supersecret"}, "lcsrf": {lcsrf}})

	// A's superseded rev1 shows a "Revert to this" form; the downgrade confirm...
	bodyA := get(t, cli, srv.URL+"/entry/"+a1.Slug)
	if !strings.Contains(bodyA, `action="/entry/`+a1.Slug+`/revert/`) || !strings.Contains(bodyA, "Revert to this") {
		t.Fatalf("entry A missing revert form for the superseded rev:\n%s", trunc(bodyA))
	}
	// B's promote button warns because the pending rev (2) is older than the head (3).
	bodyB := get(t, cli, srv.URL+"/entry/"+b1.Slug)
	if !strings.Contains(bodyB, `data-confirm="This promotes rev 2, which is OLDER`) {
		t.Fatalf("entry B promote button missing the downgrade confirm:\n%s", trunc(bodyB))
	}

	// Bad-CSRF revert is refused.
	revert := srv.URL + "/entry/" + a1.Slug + "/revert/" + strconv.FormatInt(a1.VersionID, 10)
	if r := rawPostForm(t, cli, revert, url.Values{"csrf": {"wrong"}}); r.StatusCode != http.StatusForbidden {
		r.Body.Close()
		t.Fatalf("bad-CSRF revert should be 403, got %d", r.StatusCode)
	} else {
		r.Body.Close()
	}
	// Real revert of A's rev1 restores S1 as the head.
	csrf := extract(t, cli, srv.URL+"/entry/"+a1.Slug, `name="csrf" value="([^"]+)"`)
	postForm(t, cli, revert, url.Values{"csrf": {csrf}})
	if e, _ := st.GetEntry(ctx, a1.Slug); e.Head == nil || e.Head.Solution != "S1" {
		t.Fatalf("revert did not restore rev1 (S1) as head: %+v", e.Head)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestSanitizeHost(t *testing.T) {
	for _, h := range []string{"kb.example.com", "example.com:8443", "127.0.0.1:9000", "[::1]:443", "host-1.local"} {
		if got := sanitizeHost(h); got != h {
			t.Errorf("sanitizeHost(%q) = %q, want it accepted unchanged", h, got)
		}
	}
	for _, h := range []string{"", "evil.com; rm -rf /", "a b.com", "x`whoami`.com", "h\"ost", "a\nb.com", strings.Repeat("a", 300)} {
		if got := sanitizeHost(h); got != "" {
			t.Errorf("sanitizeHost(%q) = %q, want rejected (\"\")", h, got)
		}
	}
}

// TestWebEntryReviewPanel checks the consolidated review UI: the entry page shows
// a pending ENHANCEMENT proposal (curated head above, proposed content in the
// panel, promote/reject forms), and the queue no longer has a separate Review link.
func TestWebEntryReviewPanel(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	hash, _ := passwd.Hash("supersecret", passwd.Standard)
	admin, err := st.CreateHumanUser(ctx, "admin", hash)
	if err != nil {
		t.Fatal(err)
	}

	// A curated entry: save a draft, then promote it.
	sr, err := st.Save(ctx, store.SaveInput{Kind: "reference", Content: store.Content{Title: "Widget", Summary: "s", Solution: "original solution"}, AuthorKind: "ai"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Promote(ctx, store.PromoteInput{Slug: sr.Slug, VersionID: sr.VersionID, ActorID: admin, ActorKind: "human", Note: "promote"}); err != nil {
		t.Fatal(err)
	}
	// Then a pending enhancement (rev 2, proposed) that revises the solution.
	newSol := "revised solution XYZZY"
	if _, err := st.ProposeEnhancement(ctx, store.ProposeInput{Slug: sr.Slug, ChangeNote: "improve it", AuthorKind: "ai", Patch: store.Patch{Solution: &newSol}}); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(Handler(Deps{Store: st}))
	defer srv.Close()
	jar, _ := cookiejar.New(nil)
	cli := &http.Client{Jar: jar}
	lcsrf := extract(t, cli, srv.URL+"/login", `name="lcsrf" value="([^"]+)"`)
	postForm(t, cli, srv.URL+"/login", url.Values{"name": {"admin"}, "password": {"supersecret"}, "lcsrf": {lcsrf}})

	body := get(t, cli, srv.URL+"/entry/"+sr.Slug)
	for _, want := range []string{
		`id="review"`,                  // the review panel section
		"rev 2 · proposed",             // proposal identity in the review header
		"Curated rev 1 is shown above", // the enhancement (HasCurated) branch
		"original solution",            // curated head still shown above the panel
		newSol,                         // proposed content shown inside the panel
		`action="/proposals/`,          // promote/reject act on the proposal here
		"Promote to curated",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("entry review panel missing %q: %s", want, trunc(body))
		}
	}

	// The queue lost its separate Review column / link.
	q := get(t, cli, srv.URL+"/proposals")
	if strings.Contains(q, "Review →") || strings.Contains(q, `"/proposals/`) {
		t.Fatalf("proposals queue still links to a separate review page: %s", trunc(q))
	}
}

// --- helpers ---

func get(t *testing.T, cli *http.Client, u string) string {
	t.Helper()
	resp, err := cli.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	return readBody(resp)
}

func postForm(t *testing.T, cli *http.Client, u string, v url.Values) string {
	t.Helper()
	return readBody(rawPostForm(t, cli, u, v))
}

func rawPostForm(t *testing.T, cli *http.Client, u string, v url.Values) *http.Response {
	t.Helper()
	resp, err := cli.PostForm(u, v)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func readBody(resp *http.Response) string {
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func extract(t *testing.T, cli *http.Client, u, pat string) string {
	t.Helper()
	m := regexp.MustCompile(pat).FindStringSubmatch(get(t, cli, u))
	if len(m) < 2 {
		t.Fatalf("pattern %q not found at %s", pat, u)
	}
	return m[1]
}

func trunc(s string) string {
	if len(s) > 400 {
		return s[:400]
	}
	return s
}

// The Proposals page auto-refresh: /proposals/count must require auth, return the
// live pending count as JSON, and the page must carry the marker the poller keys
// on. Without these the curator's open Proposals tab would never notice new work.
func TestProposalsCountEndpoint(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	hash, _ := passwd.Hash("supersecret", passwd.Standard)
	if _, err := st.CreateHumanUser(ctx, "admin", hash); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(Handler(Deps{Store: st}))
	defer srv.Close()

	// Unauthenticated: bounced to login, never the count.
	noAuth := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noAuth.Get(srv.URL + "/proposals/count")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("unauthenticated count: HTTP %d, want a login redirect", resp.StatusCode)
	}

	jar, _ := cookiejar.New(nil)
	cli := &http.Client{Jar: jar}
	lcsrf := extract(t, cli, srv.URL+"/login", `name="lcsrf" value="([^"]+)"`)
	postForm(t, cli, srv.URL+"/login", url.Values{"name": {"admin"}, "password": {"supersecret"}, "lcsrf": {lcsrf}})

	// Zero pending, and the page marker agrees.
	if got := get(t, cli, srv.URL+"/proposals/count"); strings.TrimSpace(got) != `{"count":0}` {
		t.Fatalf("count with no proposals = %q, want {\"count\":0}", got)
	}
	page := get(t, cli, srv.URL+"/proposals")
	if !strings.Contains(page, `data-live-refresh="0"`) {
		t.Fatalf("proposals page missing the live marker: %s", trunc(page))
	}
	if !strings.Contains(page, `data-live-checked`) {
		t.Fatalf("proposals page missing the last-checked stamp: %s", trunc(page))
	}

	// One pending proposal moves the count, which is what the poller detects.
	sr, err := st.Save(ctx, store.SaveInput{Kind: "reference", Content: store.Content{Title: "W", Summary: "s"}, AuthorKind: "ai"})
	if err != nil {
		t.Fatal(err)
	}
	_ = sr
	if got := get(t, cli, srv.URL+"/proposals/count"); strings.TrimSpace(got) != `{"count":1}` {
		t.Fatalf("count after one save = %q, want {\"count\":1}", got)
	}
	if page := get(t, cli, srv.URL+"/proposals"); !strings.Contains(page, `data-live-refresh="1"`) {
		t.Fatalf("proposals page marker did not update: %s", trunc(page))
	}
}

// The Comm console's live auto-refresh mirrors the Proposals one: /comm/count must
// require auth and return the console fingerprint as JSON under the "count" key (so
// the single generic poller serves both pages), the page must carry the marker and
// the "last checked" stamp, and a console-visible change must move the number the
// poller compares. Only mounted when Comm is enabled.
func TestCommCountEndpoint(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	cs, err := comm.Open(filepath.Join(t.TempDir(), "comm.db"), comm.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	if err := cs.Migrate(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	hash, _ := passwd.Hash("supersecret", passwd.Standard)
	if _, err := st.CreateHumanUser(ctx, "admin", hash); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(Handler(Deps{Store: st, Comm: cs}))
	defer srv.Close()

	// Unauthenticated: bounced to login, never the count.
	noAuth := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noAuth.Get(srv.URL + "/comm/count")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("unauthenticated comm count: HTTP %d, want a login redirect", resp.StatusCode)
	}

	jar, _ := cookiejar.New(nil)
	cli := &http.Client{Jar: jar}
	lcsrf := extract(t, cli, srv.URL+"/login", `name="lcsrf" value="([^"]+)"`)
	postForm(t, cli, srv.URL+"/login", url.Values{"name": {"admin"}, "password": {"supersecret"}, "lcsrf": {lcsrf}})

	// Empty console: fingerprint 0, and the page carries the marker + stamp.
	if got := get(t, cli, srv.URL+"/comm/count"); strings.TrimSpace(got) != `{"count":0}` {
		t.Fatalf("comm count on empty console = %q, want {\"count\":0}", got)
	}
	page := get(t, cli, srv.URL+"/comm")
	if !strings.Contains(page, `data-live-refresh="0"`) {
		t.Fatalf("comm page missing the live marker: %s", trunc(page))
	}
	if !strings.Contains(page, `data-live-checked`) {
		t.Fatalf("comm page missing the last-checked stamp: %s", trunc(page))
	}

	// A registered endpoint (space 1, the console's space) is a console-visible
	// change: the number the poller compares must move off 0.
	if _, err := cs.MailboxFor(ctx, "solo", comm.Owner{TokenID: "tok", ActorID: 7}); err != nil {
		t.Fatal(err)
	}
	if got := get(t, cli, srv.URL+"/comm/count"); strings.TrimSpace(got) == `{"count":0}` {
		t.Fatalf("comm count did not move after an endpoint registered: %q", got)
	}
}

// Human-facing timestamps must go out as a <time> element carrying the UTC value in
// datetime (machine-readable, unambiguous) with a Z-marked fallback for no-JS readers —
// app.js converts the text to the viewer's timezone. The bug this prevents: rendering a
// bare "2026-07-20 17:53" that reads as local time and silently shifts by the reader's
// offset (a curator misread a deadline by six hours that way).
func TestTimeElRendersUTCWithLocalHook(t *testing.T) {
	got := string(timeEl("2026-07-20T17:53:56.789Z", ""))
	for _, want := range []string{
		`<time datetime="2026-07-20T17:53:56.789Z"`, // the UTC fact travels verbatim
		` data-localtime>`,                          // the hook app.js converts
		`2026-07-20 17:53 UTC</time>`,               // no-JS fallback SAYS it is UTC
	} {
		if !strings.Contains(got, want) {
			t.Errorf("timeEl missing %q: %s", want, got)
		}
	}

	// A deadline renders relative client-side; the server marks it for that treatment.
	if d := string(timeEl("2026-07-20T17:53:56.789Z", "relative")); !strings.Contains(d, `data-localtime="relative"`) {
		t.Errorf("deadline not marked relative: %s", d)
	}

	// Anything that is not a stored timestamp is escaped text, never markup — a helper
	// that emits raw HTML must not assume its input.
	if got := string(timeEl(`<script>alert(1)</script>`, "")); strings.Contains(got, "<script>") {
		t.Errorf("timeEl must escape non-timestamp input: %s", got)
	}
	if got := string(timeEl("", "")); got != "" {
		t.Errorf("empty timestamp should render empty, got %q", got)
	}
}
