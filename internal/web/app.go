package web

import (
	"context"
	"crypto/subtle"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Quest-ICT/ken/internal/clientip"
	"github.com/Quest-ICT/ken/internal/comm"
	"github.com/Quest-ICT/ken/internal/i18n"
	"github.com/Quest-ICT/ken/internal/model"
	"github.com/Quest-ICT/ken/internal/passwd"
	"github.com/Quest-ICT/ken/internal/settings"
	"github.com/Quest-ICT/ken/internal/store"
	"github.com/Quest-ICT/ken/internal/version"
)

//go:embed templates/*.html
var tplFS embed.FS

//go:embed static
var staticFS embed.FS

const (
	sessionTTL     = 12 * time.Hour
	loginMaxFails  = 5
	loginLockout   = 5 * time.Minute
	lcsrfCookie    = "ken_lcsrf"
	scsrfCookie    = "ken_scsrf"
	minPasswordLen = 8
	maxPasswordLen = 1024     // don't run Argon2id over an unbounded input
	maxFormBody    = 64 << 10 // cap login/setup request bodies (64 KiB)
)

type app struct {
	store        *store.Store
	pages        map[string]*template.Template
	secure       bool
	cookieName   string
	guard        *loginGuard
	setupToken   string                     // one-time token gating /setup (proves server-host access)
	ip           *clientip.Resolver         // static resolver (used when settings is nil, e.g. tests)
	clientIPFn   func(*http.Request) string // live resolver when settings is configured
	settings     *settings.Live             // live runtime settings (nil in tests / when unset)
	provisioned  atomic.Bool                // flips true once a human user exists (wizard done)
	oauthEnabled bool                       // mounts the OAuth consent flow + Connectors UI
	i18n         *i18n.Manager              // reloadable UI translations
	comm         *comm.Store                // inter-session comms; nil = feature off, console + nav hidden
	// stationsEnabled mounts the /stations console. Gated on the stations flag ALONE
	// and never on comm: stations work with COMM off, and hiding the operator surface
	// for a running feature is worse than showing a page with one section idle.
	stationsEnabled bool
}

func newApp(d Deps) *app {
	name := "ken_sess"
	if d.SecureCookies {
		name = "__Host-ken_sess"
	}
	setupTok := d.SetupToken
	if setupTok == "" {
		setupTok, _ = randToken()
	}
	a := &app{
		store:           d.Store,
		pages:           parsePages(),
		secure:          d.SecureCookies,
		cookieName:      name,
		setupToken:      setupTok,
		ip:              clientip.NewResolver(d.TrustedProxies),
		settings:        d.Settings,
		oauthEnabled:    d.OAuthEnabled,
		i18n:            d.I18n,
		comm:            d.Comm,
		stationsEnabled: d.StationsEnabled,
	}
	if a.i18n == nil {
		a.i18n = i18n.New("") // embedded en+es defaults, no external override dir
	}
	a.guard = &loginGuard{fails: map[string]failInfo{}, limits: a.loginLimits}
	if d.Settings != nil {
		a.clientIPFn = func(r *http.Request) string { return d.Settings.Current().Resolver.IP(r) }
	}
	return a
}

func parsePages() map[string]*template.Template {
	m := map[string]*template.Template{}
	funcs := template.FuncMap{
		"shortdate":     shortDate,
		"localtime":     func(iso string) template.HTML { return timeEl(iso, "") },
		"localdeadline": func(iso string) template.HTML { return timeEl(iso, "relative") },
	}
	for _, p := range []string{"login", "setup", "dashboard", "search", "browse", "entry", "proposals", "tokens", "settings", "consent", "comm", "stations"} {
		m[p] = template.Must(template.New("base.html").Funcs(funcs).
			ParseFS(tplFS, "templates/base.html", "templates/"+p+".html"))
	}
	return m
}

// shortDate trims a stored ISO-8601 timestamp ("2026-07-20T12:34:56.789Z") to a
// compact "2026-07-20 12:34". Note it drops the trailing Z, so the RESULT NO LONGER
// SAYS WHICH TIMEZONE IT IS — only use it where "UTC" is appended (see timeEl) or
// where the value is machine-facing. Anything too short passes through as-is.
func shortDate(iso string) string {
	if len(iso) >= 16 {
		return strings.Replace(iso[:16], "T", " ", 1)
	}
	return iso
}

// isoStamp matches the timestamp shape Ken stores (SQLite writes UTC ISO-8601 with a
// Z). Anything else is rendered as escaped text rather than markup — these values are
// machine-generated, but a template helper that emits raw HTML must not assume it.
var isoStamp = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T[\d:.]{4,15}Z?$`)

// timeEl renders a stored UTC timestamp as a <time> element that app.js rewrites into
// the VIEWER's timezone and locale (mode "" → an absolute local datetime; mode
// "relative" → "in 8 minutes" / "3 hours ago", which is what a reader of a DEADLINE
// actually wants — no timezone to supply, no arithmetic, and nothing to misread).
//
// Why client-side: the browser already knows the reader's timezone and locale, so this
// needs no server setting and stays correct when two people in different timezones use
// one instance. The stored value goes out verbatim in the datetime attribute, so the
// machine-readable fact stays UTC and unambiguous — the split this whole class of bug
// turns on: UTC for machines (filenames, API fields, logs), local for humans.
//
// Without JavaScript the element still renders its server-side fallback — and that
// fallback is explicitly suffixed "UTC", because an unmarked timestamp in the wrong
// frame is exactly what made a curator misread a deadline by six hours.
func timeEl(iso, mode string) template.HTML {
	if !isoStamp.MatchString(iso) {
		return template.HTML(template.HTMLEscapeString(iso))
	}
	attr := " data-localtime"
	if mode != "" {
		attr += `="` + mode + `"`
	}
	return template.HTML(`<time datetime="` + iso + `"` + attr + `>` + shortDate(iso) + ` UTC</time>`)
}

func (a *app) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /static/{file}", a.handleStatic)
	mux.HandleFunc("GET /favicon.ico", a.handleFavicon)
	mux.HandleFunc("GET /lang", a.handleSetLang)
	mux.HandleFunc("GET /login", a.handleLoginForm)
	mux.HandleFunc("POST /login", a.handleLoginSubmit)
	mux.HandleFunc("POST /logout", a.handleLogout)
	mux.HandleFunc("GET /{$}", a.requireAuth(a.handleDashboard))
	mux.HandleFunc("GET /search", a.requireAuth(a.handleSearch))
	mux.HandleFunc("GET /browse", a.requireAuth(a.handleBrowse))
	mux.HandleFunc("GET /entry/{slug}", a.requireAuth(a.handleEntry))
	mux.HandleFunc("POST /entry/{slug}/revert/{vid}", a.requireAuth(a.handleRevert))
	mux.HandleFunc("GET /proposals", a.requireAuth(a.handleProposals))
	mux.HandleFunc("GET /proposals/count", a.requireAuth(a.handleProposalsCount))
	mux.HandleFunc("POST /proposals/{vid}/promote", a.requireAuth(a.handlePromote))
	mux.HandleFunc("POST /proposals/{vid}/reject", a.requireAuth(a.handleReject))
	mux.HandleFunc("GET /tokens", a.requireAuth(a.handleTokens))
	mux.HandleFunc("POST /tokens", a.requireAuth(a.handleTokenCreate))
	mux.HandleFunc("POST /tokens/{id}/revoke", a.requireAuth(a.handleTokenRevoke))
	mux.HandleFunc("GET /settings", a.requireAuth(a.handleSettings))
	mux.HandleFunc("POST /settings", a.requireAuth(a.handleSettingsSave))
	if a.stationsEnabled {
		mux.HandleFunc("GET /stations", a.requireAuth(a.handleStations))
		mux.HandleFunc("GET /stations/count", a.requireAuth(a.handleStationsCount))
		mux.HandleFunc("GET /stations/{id}/locker", a.requireAuth(a.handleStationLocker))
		mux.HandleFunc("POST /stations/requests/{id}/approve", a.requireAuth(a.handleStationApprove))
		mux.HandleFunc("POST /stations/requests/{id}/deny", a.requireAuth(a.handleStationDeny))
		mux.HandleFunc("POST /stations/{id}/key", a.requireAuth(a.handleStationKey))
		mux.HandleFunc("POST /stations/keys/{id}/retire", a.requireAuth(a.handleStationKeyRetire))
		mux.HandleFunc("POST /stations/{id}/rename", a.requireAuth(a.handleStationRename))
		mux.HandleFunc("POST /stations/{id}/publish", a.requireAuth(a.handleStationPublish))
		mux.HandleFunc("POST /stations/{id}/archive", a.requireAuth(a.handleStationArchive))
		mux.HandleFunc("POST /stations/{id}/transfer", a.requireAuth(a.handleStationTransfer))
		mux.HandleFunc("POST /stations/links/{id}/revoke", a.requireAuth(a.handleStationLinkRevoke))
		mux.HandleFunc("POST /stations/promotions/{id}/resolve", a.requireAuth(a.handlePromotionResolve))
		mux.HandleFunc("POST /stations/{id}/vault/reveal", a.requireAuth(a.handleStationVaultReveal))
		mux.HandleFunc("POST /stations/{id}/vault/restore", a.requireAuth(a.handleStationVaultRestore))
		mux.HandleFunc("POST /rooms", a.requireAuth(a.handleRoomCreate))
		mux.HandleFunc("POST /rooms/{id}/members", a.requireAuth(a.handleRoomMember))
		mux.HandleFunc("POST /rooms/{id}/archive", a.requireAuth(a.handleRoomArchive))
	}
	if a.comm != nil {
		mux.HandleFunc("GET /comm", a.requireAuth(a.handleComm))
		mux.HandleFunc("GET /comm/count", a.requireAuth(a.handleCommCount))
		mux.HandleFunc("POST /comm/pair", a.requireAuth(a.handleCommPair))
		mux.HandleFunc("POST /comm/channels/{id}/revoke", a.requireAuth(a.handleCommRevokeChannel))
		mux.HandleFunc("POST /comm/endpoints/{id}/revoke", a.requireAuth(a.handleCommRevokeEndpoint))
		mux.HandleFunc("POST /comm/endpoints/{id}/rotate", a.requireAuth(a.handleCommRotateEndpoint))
	}
	mux.HandleFunc("GET /setup", a.handleSetupForm)
	mux.HandleFunc("POST /setup", a.handleSetupSubmit)
	if a.oauthEnabled {
		// The OAuth consent step (needs the human session + CSRF + render). Auth is
		// handled inside the handlers so /oauth/authorize can bounce to login with a
		// return URL. The stateless discovery/register/token endpoints live on the
		// top-level mux (main.go), not here.
		mux.HandleFunc("GET /oauth/authorize", a.handleOAuthAuthorize)
		mux.HandleFunc("POST /oauth/authorize", a.handleOAuthAuthorizeDecision)
		mux.HandleFunc("POST /connectors/{id}/revoke", a.requireAuth(a.handleConnectorRevoke))
	}
	return a.securityHeaders(a.wizardGate(mux))
}

// --- first-run wizard (mode DERIVED from one fact: zero human users) ---

func (a *app) wizardActive(ctx context.Context) bool {
	if a.provisioned.Load() {
		return false
	}
	n, err := a.store.CountHumanUsers(ctx)
	if err != nil {
		return false // fail closed to normal login on error — never open setup blindly
	}
	if n > 0 {
		a.provisioned.Store(true)
		return false
	}
	return true
}

// wizardGate funnels every request to /setup while no admin exists.
func (a *app) wizardGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p := r.URL.Path; p != "/healthz" && p != "/setup" && p != "/favicon.ico" && p != "/lang" && !strings.HasPrefix(p, "/static/") && a.wizardActive(r.Context()) {
			http.Redirect(w, r, "/setup", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *app) handleSetupForm(w http.ResponseWriter, r *http.Request) {
	if !a.wizardActive(r.Context()) {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	// /setup is served on all interfaces but is gated by a one-time token printed
	// to the server log at startup — so only someone with server-host (log/file)
	// access can complete setup, not any network client that reaches the port.
	// Fail CLOSED on an empty token: constEq("","") is true, so an empty a.setupToken
	// (e.g. if randToken failed at startup) would otherwise open the gate to any client.
	if a.setupToken == "" || !constEq(r.URL.Query().Get("token"), a.setupToken) {
		a.render(w, r, nil, "setup", map[string]any{"Authorized": false})
		return
	}
	v, _ := randToken()
	http.SetCookie(w, &http.Cookie{
		Name: scsrfCookie, Value: v, Path: "/setup", HttpOnly: true,
		Secure: a.secure, SameSite: http.SameSiteStrictMode, MaxAge: 1800,
	})
	a.render(w, r, nil, "setup", map[string]any{"Authorized": true, "SCSRF": v, "Token": a.setupToken})
}

func (a *app) handleSetupSubmit(w http.ResponseWriter, r *http.Request) {
	if !a.wizardActive(r.Context()) {
		flashRedirect(w, r, "/login", "flash.already_setup", "")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBody)
	_ = r.ParseForm()
	if a.setupToken == "" || !constEq(r.FormValue("token"), a.setupToken) {
		http.Error(w, "invalid setup token", http.StatusForbidden)
		return
	}
	setupURL := "/setup?token=" + urlq(a.setupToken)
	c, err := r.Cookie(scsrfCookie)
	if err != nil || !constEq(r.FormValue("scsrf"), c.Value) {
		flashRedirect(w, r, setupURL, "flash.session_expired", "")
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	pw := r.FormValue("password")
	if name == "" || len(pw) < minPasswordLen || len(pw) > maxPasswordLen || pw != r.FormValue("password2") {
		flashRedirect(w, r, setupURL, "flash.setup_invalid_fields", "")
		return
	}
	hash, err := passwd.Hash(pw, passwd.High)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	created, err := a.store.CreateFirstAdmin(r.Context(), name, hash)
	if err != nil {
		flashRedirect(w, r, setupURL, "flash.admin_create_failed", err.Error())
		return
	}
	a.provisioned.Store(true)
	a.clearCookie(w, scsrfCookie, "/setup")
	if !created {
		flashRedirect(w, r, "/login", "flash.already_setup", "")
		return
	}
	flashRedirect(w, r, "/login", "flash.admin_created", name)
}

// --- view + rendering ---

type view struct {
	Session   *store.Session
	Flash     string
	PropCount int
	// StationCount is open tasks marked blocked_on=human across every station. Zero when
	// stations are off, so the badge simply never renders.
	StationCount int
	Version      string
	SourceURL    string // AGPL-3.0 §13: link to the Corresponding Source, shown in the footer
	Data         any
	Lang         string // resolved UI language for this request
	Path         string // current request URI, for the language selector's return link
	Chrome       string // "app" (full nav) or "auth" (centered public pages: login/setup/consent)
	Theme        string // "light"|"dark" from the ken_theme cookie, else "" (media query decides)
	// CommEnabled gates the inter-session communication nav entry. The page 404s
	// when the feature is off, so the link must not be shown then.
	CommEnabled bool
	// StationsEnabled gates the stations nav entry; the page 404s when off.
	StationsEnabled bool
	tr              *i18n.Manager // translator (unexported; reached via .T / .TN / .Langs)
}

// T translates a message key for the current request language (templates: {{.T "key"}};
// with args: {{.T "nav.logout" .Session.ActorName}}). Falls back to English, then the key.
func (v view) T(key string, args ...any) string {
	if v.tr == nil {
		return key
	}
	return v.tr.T(v.Lang, key, args...)
}

// TN is T with a simple plural (key.one / key.other; {0} defaults to n).
func (v view) TN(key string, n int, args ...any) string {
	if v.tr == nil {
		return key
	}
	return v.tr.TN(v.Lang, key, n, args...)
}

// Enum translates a controlled-vocabulary domain value (kind/state/staleness/
// lifecycle/event/sort) for DISPLAY, under the key "enum.<class>.<value>". It
// falls back to the raw value if no translation exists, so a new or unmapped enum
// value is shown verbatim, never as an ugly key. Templates: {{$.Enum "state" .State}}.
// The raw value must still be used for CSS hooks, option values and conditionals —
// only the visible label goes through Enum.
func (v view) Enum(class, value string) string {
	if v.tr == nil || value == "" {
		return value
	}
	key := "enum." + class + "." + value
	if s := v.tr.T(v.Lang, key); s != key {
		return s
	}
	return value
}

// Langs lists the available languages for the selector (English first).
func (v view) Langs() []i18n.Lang {
	if v.tr == nil {
		return nil
	}
	return v.tr.Languages()
}

func (a *app) render(w http.ResponseWriter, r *http.Request, sess *store.Session, page string, data any) {
	a.i18n.MaybeReload() // pick up dropped-in files before resolving/rendering
	lang := a.resolveLang(r)
	pc := 0
	if sess != nil {
		if rows, err := a.store.ListProposals(r.Context()); err == nil {
			pc = len(rows)
		}
	}
	// THE STATION PILE NEEDS A PULL, not just a page. §11.8 built the cross-station view for
	// the human's question — "what is everyone waiting on me for?" — and then left it somewhere
	// nothing points at, so it answers that question only for a human who already thought to
	// ask it. A session briefs its human on ONE station's obligations; nothing brings up the
	// others, including stations whose session is not running. The badge is what makes the
	// view reachable without remembering it exists.
	//
	// Counted on every render, like PropCount, and for the same reason: a badge computed once
	// and cached is a badge that goes stale silently. Both are one indexed COUNT.
	//
	// A COUNT OF WHAT IS RECORDED, not of what is owed. `blocked_on` is set when a task is
	// created and nothing revisits it, so this includes asks the human already satisfied. That
	// is exactly why the badge links to the list rather than asserting a debt — the human can
	// resolve it in one click, which no counter can.
	sc := 0
	if sess != nil && a.stationsEnabled {
		if n, err := a.store.CountCrossStationTasks(r.Context(), spaceForSession, "human"); err == nil {
			sc = n
		}
	}
	// A flash carries a message KEY (handlers redirect via flashRedirect with
	// ?flash=<key>[&fa=<arg>]); T translates it, substituting the optional arg into
	// {0}. An unknown value passes through unchanged (T returns it).
	flash := r.URL.Query().Get("flash")
	if flash != "" {
		if fa := r.URL.Query().Get("fa"); fa != "" {
			flash = a.i18n.T(lang, flash, fa)
		} else {
			flash = a.i18n.T(lang, flash)
		}
	}
	chrome := "app"
	switch page {
	case "login", "setup", "consent":
		chrome = "auth" // centered public layout, no primary nav
	}
	theme := ""
	if c, err := r.Cookie(themeCookie); err == nil && (c.Value == "light" || c.Value == "dark") {
		theme = c.Value
	}
	v := view{Session: sess, Flash: flash, PropCount: pc, StationCount: sc, Version: version.Version, SourceURL: version.SourceURL(), Data: data,
		Lang: lang, Path: r.URL.RequestURI(), Chrome: chrome, Theme: theme, CommEnabled: a.commEnabled(), StationsEnabled: a.stationsEnabled, tr: a.i18n}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := a.pages[page].ExecuteTemplate(w, "base.html", v); err != nil {
		log.Printf("web: render %s: %v", page, err)
	}
}

// langCookie stores the user's chosen UI language; themeCookie stores light|dark
// (the app.js toggle sets it; the server renders <html data-theme> from it so the
// first paint is correct with no flash).
const (
	langCookie  = "ken_lang"
	themeCookie = "ken_theme"
)

// resolveLang picks the request language: the ken_lang cookie if valid, else the
// first available match from Accept-Language, else English.
// flashRedirect issues a 303 redirect to dst carrying a translatable flash message
// KEY (resolved by render through i18n) plus an optional single argument substituted
// into {0}. dst may already carry a query string.
func flashRedirect(w http.ResponseWriter, r *http.Request, dst, key, arg string) {
	sep := "?"
	if strings.Contains(dst, "?") {
		sep = "&"
	}
	u := dst + sep + "flash=" + urlq(key)
	if arg != "" {
		u += "&fa=" + urlq(arg)
	}
	http.Redirect(w, r, u, http.StatusSeeOther)
}

func (a *app) resolveLang(r *http.Request) string {
	if c, err := r.Cookie(langCookie); err == nil && a.i18n.Has(c.Value) {
		return c.Value
	}
	for _, part := range strings.Split(r.Header.Get("Accept-Language"), ",") {
		code := strings.TrimSpace(part)
		if i := strings.IndexByte(code, ';'); i >= 0 {
			code = strings.TrimSpace(code[:i])
		}
		if code == "" {
			continue
		}
		if a.i18n.Has(code) {
			return code
		}
		if p, _, ok := strings.Cut(code, "-"); ok && a.i18n.Has(p) {
			return p
		}
	}
	return i18n.DefaultLang
}

// handleSetLang sets the language cookie from ?lang= and returns to ?next=.
func (a *app) handleSetLang(w http.ResponseWriter, r *http.Request) {
	if lang := r.URL.Query().Get("lang"); a.i18n.Has(lang) {
		http.SetCookie(w, &http.Cookie{
			Name: langCookie, Value: lang, Path: "/", HttpOnly: true,
			Secure: a.secure, SameSite: http.SameSiteLaxMode, MaxAge: 365 * 24 * 3600,
		})
	}
	http.Redirect(w, r, orRoot(safeNext(r.URL.Query().Get("next"))), http.StatusSeeOther)
}

// --- auth plumbing ---

type authHandler func(w http.ResponseWriter, r *http.Request, sess *store.Session)

func (a *app) requireAuth(h authHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess := a.currentSession(r)
		if sess == nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		h(w, r, sess)
	}
}

func (a *app) currentSession(r *http.Request) *store.Session {
	c, err := r.Cookie(a.cookieName)
	if err != nil {
		return nil
	}
	sess, err := a.store.SessionByID(r.Context(), c.Value)
	if err != nil {
		return nil
	}
	return sess
}

func (a *app) checkCSRF(r *http.Request, sess *store.Session) bool {
	return sess != nil && constEq(r.FormValue("csrf"), sess.CSRF)
}

func (a *app) setSessionCookie(w http.ResponseWriter, id string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: a.cookieName, Value: id, Path: "/", HttpOnly: true,
		Secure: a.secure, SameSite: http.SameSiteStrictMode, MaxAge: maxAge,
	})
}

// --- handlers ---

func (a *app) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	next := safeNext(r.URL.Query().Get("next"))
	if a.currentSession(r) != nil {
		http.Redirect(w, r, orRoot(next), http.StatusSeeOther)
		return
	}
	lcsrf := a.setLoginCSRF(w)
	a.render(w, r, nil, "login", map[string]any{"LCSRF": lcsrf, "Next": next})
}

// safeNext returns a post-login redirect target only if it is a local path
// ("/…", not "//…" and no scheme), else "". This blocks open-redirect via ?next=.
func safeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return ""
	}
	if strings.Contains(next, "\\") || strings.ContainsAny(next, "\r\n") {
		return ""
	}
	return next
}

func orRoot(next string) string {
	if next == "" {
		return "/"
	}
	return next
}

func (a *app) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBody)
	_ = r.ParseForm()
	ip := clientip.Key(a.clientIP(r)) // key the guard by network (IPv6 -> /64)
	if a.guard.blocked(ip) {
		http.Redirect(w, r, "/login?flash=flash.too_many_attempts", http.StatusSeeOther)
		return
	}
	// Login CSRF: double-submit cookie.
	c, err := r.Cookie(lcsrfCookie)
	if err != nil || !constEq(r.FormValue("lcsrf"), c.Value) {
		http.Redirect(w, r, "/login?flash=flash.session_expired", http.StatusSeeOther)
		return
	}

	name := r.FormValue("name")
	password := r.FormValue("password")
	if len(password) > maxPasswordLen {
		a.guard.fail(ip)
		http.Redirect(w, r, "/login?flash=flash.invalid_credentials", http.StatusSeeOther)
		return
	}
	cred, err := a.store.HumanByName(r.Context(), name)
	if err == nil && cred.PwHash != "" {
		if ok, _ := passwd.Verify(password, cred.PwHash); ok {
			a.guard.reset(ip)
			sess, err := a.store.CreateSession(r.Context(), cred.ActorID, a.sessionTTLDur())
			if err != nil {
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			a.clearCookie(w, lcsrfCookie, "/login")
			a.setSessionCookie(w, sess.ID, int(a.sessionTTLDur().Seconds()))
			http.Redirect(w, r, orRoot(safeNext(r.FormValue("next"))), http.StatusSeeOther)
			return
		}
	} else {
		// Verify against a dummy hash anyway to keep timing uniform.
		_, _ = passwd.Verify(password, dummyHash)
	}
	a.guard.fail(ip)
	http.Redirect(w, r, "/login?flash=flash.invalid_credentials", http.StatusSeeOther)
}

func (a *app) handleLogout(w http.ResponseWriter, r *http.Request) {
	sess := a.currentSession(r)
	if sess == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	// Require a valid CSRF token so a forged POST /logout can't force-logout the user.
	if !a.checkCSRF(r, sess) {
		http.Error(w, "bad CSRF token", http.StatusForbidden)
		return
	}
	_ = a.store.DeleteSession(r.Context(), sess.ID)
	a.clearCookie(w, a.cookieName, "/")
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (a *app) handleDashboard(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	ctx := r.Context()
	entries, _ := a.store.CountEntries(ctx)
	versions, _ := a.store.CountVersions(ctx)
	tokens, _ := a.store.CountActiveTokens(ctx)
	recent, _ := a.store.RecentContext(ctx, 30, 8, "")
	a.render(w, r, sess, "dashboard", map[string]any{
		"Entries":  entries,
		"Versions": versions,
		"Tokens":   tokens,
		"Recent":   recent,
	})
}

func (a *app) handleSearch(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	q := r.URL.Query().Get("q")
	scope := r.URL.Query().Get("scope")
	if scope == "" {
		scope = "curated"
	}
	var results []model.SearchResult
	if q != "" {
		results, _ = a.store.Search(r.Context(), q, store.SearchOpts{Scope: scope, K: 25})
	}
	a.render(w, r, sess, "search", map[string]any{"Query": q, "Scope": scope, "Results": results})
}

// browsePerPage bounds a browse page; the listing is a lightweight grid over the
// denormalized entry table (no version join), so a generous page is cheap.
const browsePerPage = 50

func (a *app) handleBrowse(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	q := r.URL.Query()
	f := store.BrowseFilter{
		Category:  q.Get("category"),
		Kind:      q.Get("kind"),
		Staleness: q.Get("staleness"),
		Lifecycle: q.Get("lifecycle"),
		Sort:      q.Get("sort"),
		Limit:     browsePerPage,
	}
	page := 0
	if p, err := strconv.Atoi(q.Get("page")); err == nil && p > 0 {
		page = p
	}
	f.Offset = page * browsePerPage

	rows, hasMore, err := a.store.ListEntries(r.Context(), f)
	if err != nil {
		log.Printf("web: browse: %v", err)
	}
	cats, _ := a.store.DistinctCategories(r.Context())

	// Build filter-preserving pagination URLs so paging never drops the filters.
	base := url.Values{}
	for k, v := range map[string]string{"category": f.Category, "kind": f.Kind, "staleness": f.Staleness, "lifecycle": f.Lifecycle, "sort": f.Sort} {
		if v != "" {
			base.Set(k, v)
		}
	}
	mkURL := func(p int) string {
		v := url.Values{}
		for k := range base {
			v.Set(k, base.Get(k))
		}
		if p > 0 {
			v.Set("page", strconv.Itoa(p))
		}
		if enc := v.Encode(); enc != "" {
			return "/browse?" + enc
		}
		return "/browse"
	}
	prevURL, nextURL := "", ""
	if page > 0 {
		prevURL = mkURL(page - 1)
	}
	if hasMore {
		nextURL = mkURL(page + 1)
	}

	a.render(w, r, sess, "browse", map[string]any{
		"Rows":        rows,
		"Count":       len(rows),
		"Page":        page,
		"PageNum":     page + 1,
		"PrevURL":     prevURL,
		"NextURL":     nextURL,
		"Categories":  cats,
		"Kinds":       []string{"user", "feedback", "project", "reference"},
		"Stalenesses": []string{"fresh", "aging", "stale", "refuted"},
		"Lifecycles":  []string{"draft", "active", "deprecated"},
		"Sorts":       []string{"updated", "title", "used", "created", "kind"},
		"F":           f,
	})
}

func (a *app) handleEntry(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	slug := r.PathValue("slug")
	entry, err := a.store.GetEntry(r.Context(), slug)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	hist, _ := a.store.History(r.Context(), slug)
	var review *store.ReviewData
	if entry.HasProvisional {
		review, _ = a.store.ProvisionalReview(r.Context(), slug)
	}
	a.render(w, r, sess, "entry", map[string]any{"Entry": entry, "History": hist, "Review": review})
}

// curationLangs returns the configured curation languages (nil when settings are
// unset or the feature is off).
func (a *app) curationLangs() []string {
	if a.settings == nil {
		return nil
	}
	return a.settings.Current().CurationLangSet
}

// proposalView decorates a store proposal row with the review-queue's
// out-of-language flag (computed against the live curation languages).
type proposalView struct {
	store.ProposalRow
	Foreign bool
}

func (a *app) handleProposals(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	props, _ := a.store.ListProposals(r.Context())
	langs := a.curationLangs()
	views := make([]proposalView, len(props))
	for i, p := range props {
		views[i] = proposalView{ProposalRow: p, Foreign: store.LangForeign(p.LatestLang, langs)}
	}
	a.render(w, r, sess, "proposals", map[string]any{"Proposals": views})
}

// handleProposalsCount answers the Proposals page poller with the current
// pending-proposal count as JSON. Read-only and cheap (one COUNT); behind
// requireAuth like the page itself, so it is not an unauthenticated info leak.
func (a *app) handleProposalsCount(w http.ResponseWriter, r *http.Request, _ *store.Session) {
	n, err := a.store.CountProposals(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprintf(w, `{"count":%d}`, n)
}

func (a *app) handlePromote(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !a.checkCSRF(r, sess) {
		http.Error(w, "bad CSRF token", http.StatusForbidden)
		return
	}
	vid, err := strconv.ParseInt(r.PathValue("vid"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	slug := r.FormValue("slug")
	if err := a.store.Promote(r.Context(), store.PromoteInput{
		Slug: slug, VersionID: vid, ActorID: sess.ActorID, ActorKind: "human",
		Note: "promoted by " + sess.ActorName, CurationLangs: a.curationLangs(),
	}); err != nil {
		if errors.Is(err, store.ErrForeignLang) {
			flashRedirect(w, r, "/proposals", "flash.promote_foreign_lang", "")
			return
		}
		flashRedirect(w, r, "/proposals", "flash.promote_failed", err.Error())
		return
	}
	flashRedirect(w, r, "/proposals", "flash.promoted", slug)
}

func (a *app) handleReject(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !a.checkCSRF(r, sess) {
		http.Error(w, "bad CSRF token", http.StatusForbidden)
		return
	}
	vid, err := strconv.ParseInt(r.PathValue("vid"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	slug := r.FormValue("slug")
	if err := a.store.Reject(r.Context(), slug, vid, sess.ActorID, "human", "rejected by "+sess.ActorName); err != nil {
		flashRedirect(w, r, "/proposals", "flash.reject_failed", err.Error())
		return
	}
	flashRedirect(w, r, "/proposals", "flash.rejected", slug)
}

// handleRevert re-promotes a historical (superseded/rejected) version back to the
// curated head — the human recovery path when promotions regressed the head.
func (a *app) handleRevert(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !a.checkCSRF(r, sess) {
		http.Error(w, "bad CSRF token", http.StatusForbidden)
		return
	}
	slug := r.PathValue("slug")
	vid, err := strconv.ParseInt(r.PathValue("vid"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := a.store.Repromote(r.Context(), store.PromoteInput{
		Slug: slug, VersionID: vid, ActorID: sess.ActorID, ActorKind: "human",
		Note: "reverted by " + sess.ActorName, CurationLangs: a.curationLangs(),
	}); err != nil {
		if errors.Is(err, store.ErrForeignLang) {
			flashRedirect(w, r, "/entry/"+slug, "flash.revert_foreign_lang", "")
			return
		}
		flashRedirect(w, r, "/entry/"+slug, "flash.revert_failed", err.Error())
		return
	}
	flashRedirect(w, r, "/entry/"+slug, "flash.reverted", slug)
}

// --- agent tokens (superadmin) ---

// agentScopes are the KNOWLEDGE-BASE scopes an agent token may hold from the web UI.
// 'curate' is deliberately excluded — that is the human-only curation gate.
var agentScopes = []string{"read", "write-draft", "propose"}

// consoleCommScopes are the COMM scopes the web UI may mint.
//
// UNTIL 3.10.0 THE CONSOLE COULD NOT MINT THESE AT ALL, and the omission looked decided but
// was not: the comment above justifies excluding `curate` and says nothing about comm, because
// this list was written when knowledge-base scopes were all there were. The comm and station
// families were added to the CLI later and nobody came back.
//
// The cost was not theoretical. Ken's stated posture is that the console is the main method for
// any operation and the CLI is a last resort — and an operator following it minted a token,
// handed it to a session, and watched comm_register refuse it for a missing scope. Worse, the
// handler DROPPED the unknown scope silently, so nothing said why.
//
// Station scopes are still not mintable here, for the reason `ken token add` gives: /station/mcp
// requires a `kens_` key BOUND to a station, and this path issues an unbound `ken_` token, so it
// would authenticate nowhere while looking exactly like a working credential. Station keys are
// minted on the Stations page.
var consoleCommScopes = []string{"comm", "comm-file"}

func agentScopeOK(s string) bool {
	for _, a := range agentScopes {
		if a == s {
			return true
		}
	}
	for _, a := range consoleCommScopes {
		if a == s {
			return true
		}
	}
	return false
}

func (a *app) handleTokens(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	a.renderTokens(w, r, sess, "", "")
}

// renderTokens shows the token list; newSecret (when non-empty) is a just-created
// bearer token shown ONCE.
func (a *app) renderTokens(w http.ResponseWriter, r *http.Request, sess *store.Session, newSecret, newActor string) {
	rows, err := a.store.ListTokens(r.Context())
	if err != nil {
		log.Printf("web: list tokens: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	mcpURL := ""
	if newSecret != "" {
		mcpURL = a.publicMCPURL(r)
	}
	var connectors []store.OAuthGrantRow
	if a.oauthEnabled {
		connectors, _ = a.store.ListOAuthGrants(r.Context())
	}
	a.render(w, r, sess, "tokens", map[string]any{
		"Tokens": rows, "Scopes": agentScopes, "CommScopes": consoleCommScopes, "NewSecret": newSecret, "NewActor": newActor, "MCPURL": mcpURL,
		"OAuthEnabled": a.oauthEnabled, "Connectors": connectors,
	})
}

// publicMCPURL builds the externally-reachable MCP endpoint (e.g.
// https://kb.example.com/mcp) for a copy-paste registration example, derived from the
// current request: the Host header — or a trusted proxy's validated
// X-Forwarded-Host — and an https scheme when TLS terminates here, secure cookies
// are on, or a trusted proxy reports X-Forwarded-Proto: https. Falls back to a
// <ken-host> placeholder when the host is missing or carries characters unsafe to
// splice into the shell command the operator will copy.
func (a *app) publicMCPURL(r *http.Request) string {
	host := r.Host
	scheme := "http"
	if r.TLS != nil || a.secure {
		scheme = "https"
	}
	res := a.ip
	if a.settings != nil {
		res = a.settings.Current().Resolver
	}
	if res != nil && res.TrustedPeer(r) {
		if xh := firstField(r.Header.Get("X-Forwarded-Host")); xh != "" {
			host = xh
		}
		if strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
			scheme = "https"
		}
	}
	if host = sanitizeHost(host); host == "" {
		host = "<ken-host>"
	}
	return scheme + "://" + host + "/mcp"
}

// handleStatic serves embedded assets under /static/ (the copy-button script and
// the favicon set). Same-origin so scripts satisfy the strict default-src 'self'
// CSP. {file} is a single path segment (no slashes), and embed.FS rejects any
// ".." path, so traversal outside the embedded static/ tree is impossible.
func (a *app) handleStatic(w http.ResponseWriter, r *http.Request) {
	b, err := staticFS.ReadFile("static/" + r.PathValue("file"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", staticContentType(r.PathValue("file")))
	h.Set("Cache-Control", "public, max-age=3600") // overrides the no-store default for static assets
	_, _ = w.Write(b)
}

// handleFavicon answers the browser's implicit /favicon.ico request (some clients
// fetch it before parsing the <link> tags).
func (a *app) handleFavicon(w http.ResponseWriter, r *http.Request) {
	b, err := staticFS.ReadFile("static/favicon.ico")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "image/x-icon")
	h.Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(b)
}

func staticContentType(name string) string {
	switch {
	case strings.HasSuffix(name, ".js"):
		return "text/javascript; charset=utf-8"
	case strings.HasSuffix(name, ".css"):
		return "text/css; charset=utf-8"
	case strings.HasSuffix(name, ".svg"):
		return "image/svg+xml"
	case strings.HasSuffix(name, ".png"):
		return "image/png"
	case strings.HasSuffix(name, ".ico"):
		return "image/x-icon"
	default:
		return "application/octet-stream"
	}
}

// firstField returns the first comma-separated token of s, trimmed (X-Forwarded-Host
// may list several hops; the client-facing host is first).
func firstField(s string) string {
	if i := strings.IndexByte(s, ','); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// sanitizeHost returns h if it is a plausible host[:port] / [IPv6] made only of
// characters safe to place verbatim in a shell command, else "" so the caller can
// fall back to a placeholder. It never trusts an arbitrary Host into a copied command.
func sanitizeHost(h string) string {
	if h = strings.TrimSpace(h); h == "" || len(h) > 255 {
		return ""
	}
	for i := 0; i < len(h); i++ {
		c := h[i]
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '.' || c == '-' || c == ':' || c == '[' || c == ']'
		if !ok {
			return ""
		}
	}
	return h
}

func (a *app) handleTokenCreate(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBody) // before checkCSRF, which parses the form
	if !a.checkCSRF(r, sess) {
		http.Error(w, "bad CSRF token", http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	name := strings.TrimSpace(r.FormValue("actor"))
	label := strings.TrimSpace(r.FormValue("label"))
	var scopes []string
	for _, s := range r.Form["scope"] {
		// REFUSE an unrecognised scope rather than dropping it. The old code filtered
		// silently, so a form carrying `comm` minted a knowledge-base token and said
		// nothing — the operator found out when the session could not register.
		if !agentScopeOK(s) {
			flashRedirect(w, r, "/tokens", "flash.token_scope_unknown", s)
			return
		}
		scopes = append(scopes, s)
	}
	if name == "" || len(scopes) == 0 {
		flashRedirect(w, r, "/tokens", "flash.token_fields_required", "")
		return
	}
	// The SAME rule the CLI enforces, from the same place — a token is dedicated to one
	// surface family. Before 3.10.0 this path could not violate it only because its menu was
	// too narrow to express a violation, which is safety by accident rather than by rule.
	if err := store.CheckScopeMix(scopes); err != nil {
		flashRedirect(w, r, "/tokens", "flash.token_scope_mix", err.Error())
		return
	}
	if len(name) > 190 || len(label) > 190 {
		flashRedirect(w, r, "/tokens", "flash.token_fields_too_long", "")
		return
	}
	actorID, err := a.store.FindOrCreateActor(r.Context(), "ai", name)
	if err != nil {
		flashRedirect(w, r, "/tokens", "flash.agent_create_failed", err.Error())
		return
	}
	secret, err := a.store.IssueToken(r.Context(), actorID, scopes, label)
	if err != nil {
		flashRedirect(w, r, "/tokens", "flash.token_issue_failed", err.Error())
		return
	}
	// Render directly (no redirect) so the one-time secret survives to the page.
	a.renderTokens(w, r, sess, secret, name)
}

func (a *app) handleTokenRevoke(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !a.checkCSRF(r, sess) {
		http.Error(w, "bad CSRF token", http.StatusForbidden)
		return
	}
	id := r.PathValue("id")
	if err := a.store.RevokeToken(r.Context(), id); err != nil {
		flashRedirect(w, r, "/tokens", "flash.token_revoke_failed", err.Error())
		return
	}
	// S6: revoking a station key severs the endpoints it bound. The authoritative
	// enforcement is at USE (store.IsStationKeyRevoked), because `ken token revoke`
	// runs in a separate process and can never reach comm.db — but doing it eagerly
	// HERE, where a comm handle exists, also RELEASES the claims those endpoints
	// hold. A severed reader is never coming back to ack, so leaving its claims to
	// expire would hide those messages from the station's other readers for the rest
	// of the lease.
	if a.comm != nil {
		if n, err := a.comm.SeverEndpointsBoundBy(r.Context(), id); err != nil {
			log.Printf("web: severing endpoints bound by %s: %v", id, err)
		} else if n > 0 {
			log.Printf("COMM: revoking station key %s severed %d bound endpoint(s) and released their claims", id, n)
		}
	}
	flashRedirect(w, r, "/tokens", "flash.token_revoked", id)
}

// --- runtime settings (superadmin, live-applied) ---

type settingsField struct {
	Key, Label, Help, Type, Value string
	ReadOnly, Live, Checked       bool
}

type settingsGroup struct {
	Name   string
	Fields []settingsField
}

// trOr returns the i18n catalog value for key in lang (falling back to English),
// or fallback when the key is absent from every catalog. It lets the settings
// registry's English Label/Help/Group be translated through the reloadable catalog
// while the registry text stays the ultimate fallback if a key is ever missing.
func trOr(tr *i18n.Manager, lang, key, fallback string) string {
	if tr == nil {
		return fallback
	}
	if v := tr.T(lang, key); v != key { // T returns the key itself when unresolved
		return v
	}
	return fallback
}

// settingsGroupKey maps a registry group display name to its i18n key
// ("Rate limiting" -> "settings.group.rate_limiting").
//
// Hyphens collapse to underscores too, and that is a FIX rather than tidiness: the
// only hyphenated group is "Inter-session comms", which derived
// `settings.group.inter-session_comms` while all three bundles carried
// `inter_session_comms`. The key never matched, so the Spanish and French headings
// sat in the files unreachable and every operator saw the English one — silently,
// because trOr falls back to the group's display name, which in English is
// indistinguishable from success. Found by the test that asserts every group has a
// heading; nothing else could see it, since the visible result was correct English.
func settingsGroupKey(group string) string {
	s := strings.ToLower(group)
	return "settings.group." + strings.NewReplacer(" ", "_", "-", "_").Replace(s)
}

func buildSettingsGroups(v settings.Values, tr *i18n.Manager, lang string) []settingsGroup {
	// A group missing from this slice renders NOTHING — buildSettingsGroups iterates
	// `order`, not the registry — so adding a Field with a new Group means adding it
	// here too. Silent, and easy to miss.
	order := []string{"Rate limiting", "Login", "Session", "Network", "TLS", "Curation", "Inter-session comms", "Stations"}
	byGroup := map[string][]settingsField{}
	for _, f := range settings.Fields {
		val := f.Get(v)
		help := f.Help
		if help != "" {
			help = trOr(tr, lang, "settings.field."+f.Key+".help", f.Help)
		}
		byGroup[f.Group] = append(byGroup[f.Group], settingsField{
			Key:   f.Key,
			Label: trOr(tr, lang, "settings.field."+f.Key+".label", f.Label),
			Help:  help,
			Type:  f.Type, Value: val,
			ReadOnly: f.ReadOnly, Live: f.Live, Checked: f.Type == "bool" && val != "",
		})
	}
	var out []settingsGroup
	for _, g := range order {
		if fs := byGroup[g]; len(fs) > 0 {
			out = append(out, settingsGroup{Name: trOr(tr, lang, settingsGroupKey(g), g), Fields: fs})
		}
	}
	return out
}

func (a *app) handleSettings(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	a.renderSettings(w, r, sess, nil)
}

func (a *app) renderSettings(w http.ResponseWriter, r *http.Request, sess *store.Session, errs []settings.FieldError) {
	if a.settings == nil {
		http.NotFound(w, r)
		return
	}
	lang := a.resolveLang(r)
	a.render(w, r, sess, "settings", map[string]any{
		"Groups": buildSettingsGroups(a.settings.Current().Values, a.i18n, lang), "Errors": renderFieldErrors(a.i18n, lang, errs),
	})
}

// renderFieldErrors turns key-addressed validation failures into the sentences an
// operator reads, resolving every field name the SAME way the form resolved it.
//
// This is the whole reason settings.FieldError carries keys instead of labels. Built
// on the settings side out of f.Label, an error names the field as the Go registry
// calls it, while the form beside it shows whatever the translation bundle says — so
// the message pointed at a name that was nowhere on the page. In English that misnamed
// 2 of 43 fields; in Spanish and French, 31 of 43.
func renderFieldErrors(tr *i18n.Manager, lang string, errs []settings.FieldError) []string {
	if len(errs) == 0 {
		return nil
	}
	label := func(key string) string {
		for _, f := range settings.Fields {
			if f.Key == key {
				return trOr(tr, lang, "settings.field."+key+".label", f.Label)
			}
		}
		return key
	}
	out := make([]string, 0, len(errs))
	for _, e := range errs {
		msg := e.Message
		if e.Key != "" {
			msg = strings.ReplaceAll(msg, "{0}", label(e.Key))
		}
		for i, ref := range e.Refs {
			msg = strings.ReplaceAll(msg, "{"+strconv.Itoa(i+1)+"}", label(ref))
		}
		if !e.Standalone && e.Key != "" {
			msg = label(e.Key) + ": " + msg
		}
		out = append(out, msg)
	}
	return out
}

func (a *app) handleSettingsSave(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if a.settings == nil {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBody) // before checkCSRF, which parses the form
	if !a.checkCSRF(r, sess) {
		http.Error(w, "bad CSRF token", http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	form := map[string]string{}
	for _, f := range settings.Fields {
		if f.ReadOnly {
			continue
		}
		if f.Type == "bool" { // an unchecked checkbox is simply absent
			if r.FormValue(f.Key) != "" {
				form[f.Key] = "on"
			} else {
				form[f.Key] = ""
			}
			continue
		}
		form[f.Key] = r.FormValue(f.Key)
	}
	if _, errs := a.settings.Apply(r.Context(), form, sess.ActorName); len(errs) > 0 {
		a.renderSettings(w, r, sess, errs)
		return
	}
	flashRedirect(w, r, "/settings", "flash.settings_applied", "")
}

// --- cookies / CSRF / helpers ---

func (a *app) setLoginCSRF(w http.ResponseWriter) string {
	v, _ := randToken()
	http.SetCookie(w, &http.Cookie{
		Name: lcsrfCookie, Value: v, Path: "/login", HttpOnly: true,
		Secure: a.secure, SameSite: http.SameSiteStrictMode, MaxAge: 600,
	})
	return v
}

func (a *app) clearCookie(w http.ResponseWriter, name, path string) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: "", Path: path, HttpOnly: true,
		Secure: a.secure, SameSite: http.SameSiteStrictMode, MaxAge: -1,
	})
}

func (a *app) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "SAMEORIGIN")
		// The OAuth consent page's form submits to 'self', but the post-approve
		// response redirects the browser to the client's callback (e.g. claude.ai).
		// Browsers enforce form-action on that redirect too, so a bare 'self' would
		// silently block it ("click Approve, nothing happens"). Widen form-action to
		// the client's redirect origin — which the handler still validates by exact
		// match against the registered client, so reflecting the origin adds no risk.
		formAction := "'self'"
		if r.URL.Path == "/oauth/authorize" {
			if o := redirectURIOrigin(r.URL.Query().Get("redirect_uri")); o != "" {
				formAction = "'self' " + o
			}
		}
		h.Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; form-action "+formAction+"; frame-ancestors 'self'; base-uri 'none'")
		h.Set("Referrer-Policy", "same-origin")
		// Never cache authenticated pages — the /tokens page shows a one-time secret.
		h.Set("Cache-Control", "no-store")
		if a.secure {
			// Only meaningful (and safe) behind TLS, which is what a.secure signals.
			h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

func constEq(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

// clientIP returns the request's client IP for the login guard (trusted-proxy
// aware — live when settings is configured, else a static resolver).
func (a *app) clientIP(r *http.Request) string {
	if a.clientIPFn != nil {
		return a.clientIPFn(r)
	}
	return a.ip.IP(r)
}

// loginLimits returns the current login brute-force lockout parameters (live from
// settings, else the compiled defaults).
func (a *app) loginLimits() (int, time.Duration) {
	if a.settings != nil {
		s := a.settings.Current()
		return s.LoginMaxFails, time.Duration(s.LoginLockoutSec) * time.Second
	}
	return loginMaxFails, loginLockout
}

// sessionTTLDur returns the current session lifetime (live from settings, else the default).
func (a *app) sessionTTLDur() time.Duration {
	if a.settings != nil {
		return time.Duration(a.settings.Current().SessionTTLHours) * time.Hour
	}
	return sessionTTL
}

// --- login brute-force guard (per-IP) ---

const guardMaxEntries = 4096

type failInfo struct {
	n     int
	until time.Time
	seen  time.Time
}

type loginGuard struct {
	mu     sync.Mutex
	fails  map[string]failInfo
	limits func() (int, time.Duration) // live max-fails/lockout (nil -> compiled defaults)
}

func (g *loginGuard) lim() (int, time.Duration) {
	if g.limits != nil {
		return g.limits()
	}
	return loginMaxFails, loginLockout
}

func (g *loginGuard) blocked(ip string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	maxFails, _ := g.lim()
	f := g.fails[ip]
	return f.n >= maxFails && time.Now().Before(f.until)
}

func (g *loginGuard) fail(ip string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	f, exists := g.fails[ip]
	if !exists {
		if len(g.fails) >= guardMaxEntries {
			g.sweepLocked(now) // reclaim expired/idle before deciding to stop tracking
		}
		if len(g.fails) >= guardMaxEntries {
			return // still full of active lockouts — bound memory, don't track new keys
		}
	}
	if !f.until.IsZero() && now.After(f.until) {
		f = failInfo{} // lockout window elapsed -> decay to a fresh count
	}
	maxFails, lockout := g.lim()
	f.n++
	f.seen = now
	if f.n >= maxFails {
		f.until = now.Add(lockout)
	}
	g.fails[ip] = f
}

func (g *loginGuard) reset(ip string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.fails, ip)
}

// sweepLocked drops expired lockouts and idle accumulating entries (caller holds g.mu).
func (g *loginGuard) sweepLocked(now time.Time) {
	_, lockout := g.lim()
	for ip, f := range g.fails {
		if (!f.until.IsZero() && now.After(f.until)) || now.Sub(f.seen) > lockout {
			delete(g.fails, ip)
		}
	}
}
