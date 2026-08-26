package web

import (
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Quest-ICT/ken/internal/store"
)

// This file implements the interactive half of Ken's OAuth authorization server:
// the /authorize consent step, where a logged-in human approves (or denies) a
// client's request to connect. The stateless discovery/register/token endpoints
// live in internal/oauth (mounted on the top-level mux); MCP token validation
// lives in the store. A connector approved here authors MCP writes as a shared
// 'ai' actor and gets read | write-draft | propose — never curate.

// oauthAuthCodeTTL bounds how long an issued authorization code is redeemable.
// Short by design (single-use anyway); claude.ai exchanges it immediately.
const oauthAuthCodeTTL = 60 * time.Second

// authParams are the OAuth authorization-request parameters, carried verbatim
// from the query into the consent form's hidden fields and re-validated on POST.
type authParams struct {
	ResponseType        string
	ClientID            string
	RedirectURI         string
	CodeChallenge       string
	CodeChallengeMethod string
	State               string
	Scope               string
	Resource            string
}

func authParamsFrom(get func(string) string) authParams {
	return authParams{
		ResponseType:        get("response_type"),
		ClientID:            get("client_id"),
		RedirectURI:         get("redirect_uri"),
		CodeChallenge:       get("code_challenge"),
		CodeChallengeMethod: get("code_challenge_method"),
		State:               get("state"),
		Scope:               get("scope"),
		Resource:            get("resource"),
	}
}

// handleOAuthAuthorize renders the consent page (GET /oauth/authorize). It
// validates the client + redirect_uri BEFORE trusting them, requires a logged-in
// human (bouncing to /login with a return URL otherwise), and enforces PKCE-S256.
func (a *app) handleOAuthAuthorize(w http.ResponseWriter, r *http.Request) {
	p := authParamsFrom(r.URL.Query().Get)

	client, err := a.store.OAuthClientByID(r.Context(), p.ClientID)
	if err != nil || !redirectAllowed(client, p.RedirectURI) {
		// Never redirect to an unvalidated URI — show an on-site error instead.
		a.oauthErrorPage(w, r, "Unknown OAuth client, or the redirect URI is not registered for it.")
		return
	}
	// redirect_uri is now trusted → parameter errors go back to the client.
	if p.ResponseType != "code" {
		a.redirectAuthError(w, r, p, "unsupported_response_type", "only response_type=code is supported")
		return
	}
	if p.CodeChallenge == "" || p.CodeChallengeMethod != "S256" {
		a.redirectAuthError(w, r, p, "invalid_request", "PKCE with code_challenge_method=S256 is required")
		return
	}

	sess := a.currentSession(r)
	if sess == nil {
		http.Redirect(w, r, "/login?next="+urlq(r.URL.RequestURI()), http.StatusSeeOther)
		return
	}

	// The redirect destination is the ONLY unforgeable identity on this page (the
	// client name is attacker-chosen via open DCR); surface its host so the curator
	// can tell the real claude.ai connector from an impostor before approving.
	redirHost := p.RedirectURI
	if u, err := url.Parse(p.RedirectURI); err == nil && u.Host != "" {
		redirHost = u.Host
	}
	// Offered so the operator can point this connector at the actor that holds their
	// comm token. A failure here must not block consent — the picker is an
	// improvement to provenance, not a precondition for connecting.
	actors, err := a.store.ActorsWithCommStatus(r.Context())
	if err != nil {
		log.Printf("web: oauth consent actors: %v", err)
	}
	a.render(w, r, sess, "consent", map[string]any{
		"CSRF":         sess.CSRF,
		"Actors":       actors,
		"Surfaces":     consentSurfaces(),
		"ClientName":   clientDisplayName(client),
		"RedirectHost": redirHost,
		"RedirectURI":  p.RedirectURI,
		"P":            p,
	})
}

// handleOAuthAuthorizeDecision processes the consent form (POST /oauth/authorize):
// on approve it records a grant + single-use code and 302s back to the client; on
// deny it 302s back with error=access_denied. Everything is re-validated.
func (a *app) handleOAuthAuthorizeDecision(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBody)
	sess := a.currentSession(r)
	if sess == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if !a.checkCSRF(r, sess) {
		http.Error(w, "bad CSRF token", http.StatusForbidden)
		return
	}
	p := authParamsFrom(r.FormValue)

	client, err := a.store.OAuthClientByID(r.Context(), p.ClientID)
	if err != nil || !redirectAllowed(client, p.RedirectURI) {
		a.oauthErrorPage(w, r, "Unknown OAuth client, or the redirect URI is not registered for it.")
		return
	}
	if p.CodeChallenge == "" || p.CodeChallengeMethod != "S256" {
		a.redirectAuthError(w, r, p, "invalid_request", "PKCE with code_challenge_method=S256 is required")
		return
	}
	if r.FormValue("decision") != "approve" {
		a.redirectAuthError(w, r, p, "access_denied", "the user denied the request")
		return
	}

	// WHICH ACTOR THIS CONNECTOR WRITES AS, AND WHY A HUMAN CHOOSES IT.
	//
	// It used to be fixed: one 'ai' actor per client display name, invented here. That
	// silently disabled the hearsay marker for everything the connector ever wrote.
	// viaComm asks whether THIS ACTOR recently received inter-session traffic
	// (mcpserver/server.go); COMM traffic arrives under the actor a `comm` token was
	// minted with; an actor named after a client's self-reported name is never that
	// actor. So a session that read a peer's message and then saved what it learned
	// through the connector produced via_comm=NULL — and an absent badge is
	// indistinguishable from a checked-and-clean one.
	//
	// Letting the operator point the connector at an existing agent actor closes it.
	// The marker then fires whenever that actor has recent peer traffic, which
	// OVER-reports on a shared actor — and over-reporting is the correct bias, stated
	// where the marker was designed: a false negative silently launders hearsay into
	// the knowledge base, a false positive only asks a human to check a source.
	//
	// Blank keeps the old behaviour, so an operator who does not care is unaffected.
	connActor, err := a.resolveConnectorActor(r, client)
	if err != nil {
		log.Printf("web: oauth connector actor: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	scope := strings.TrimSpace(p.Scope)
	if scope == "" {
		scope = "read write offline_access"
	}
	// *** RECORD WHICH KEN SURFACES THIS APPROVAL COVERS. ***
	//
	// Until §10 step 2 the grant's scope was cosmetic — the schema comment said so — because the
	// capability set was a literal in the authenticator and `/comm/mcp` and `/station/mcp` refused
	// OAuth outright. Consolidating those three authenticators removes a control that
	// docs/IDENTITY-CONTROLS.md calls "the one that says NO to exactly that", and it sets the
	// condition this line meets: the withholding becomes "an explicit per-surface capability
	// decision at grant time, not inherited from the fact that three files exist."
	//
	// EVERYTHING BY DEFAULT, because no Ken feature is optional or off by default and a session
	// should need one approval rather than a negotiation. The human narrows it by unticking a
	// surface on the consent screen; the grant then records exactly what they agreed to, which is
	// what makes narrowing possible and revocation legible.
	// EVERY GRANT CARRIES EVERY SURFACE. The consent screen states them; it no longer offers to
	// withhold them, and this no longer reads a `ken_surface` selection.
	//
	// The narrowing that stood here contradicted the requirement in the comment above it: a human
	// could untick Messaging and mint a session with no way to reach its peers, or untick the
	// knowledge base and mint a session that cannot read the thing Ken exists to hold. Vlad,
	// having said it more than once: "no ken services (or surfaces) are optional. All sessions
	// get everything (they can use)." A control whose only function is to build a forbidden state
	// is not a safety feature.
	granted := store.DefaultGrantScopes()

	// DEDUPED, because a correct client already asks for what Ken advertises in
	// scopes_supported. Concatenating request and grant produced
	// "... ken:kb ken:comm ken:station ken:kb ken:comm ken:station" on the first real consent
	// ever performed, and that doubled string is what gets PERSISTED on the grant and shown
	// wherever the grant is displayed — so the console misreported what the human agreed to.
	// Authorization was never affected (GrantedCapabilities is a whitelist), which is exactly why
	// it survived: it was invisible everywhere except the one place a human reads it.
	seen := map[string]bool{}
	var merged []string
	for _, sc := range append(strings.Fields(scope), granted...) {
		if sc == "" || seen[sc] {
			continue
		}
		seen[sc] = true
		merged = append(merged, sc)
	}
	scope = strings.Join(merged, " ")
	code, err := a.store.CreateOAuthGrantAndCode(r.Context(), store.NewAuthCode{
		ClientID:            p.ClientID,
		ConnectorActorID:    connActor,
		HumanActorID:        sess.ActorID,
		RedirectURI:         p.RedirectURI,
		CodeChallenge:       p.CodeChallenge,
		CodeChallengeMethod: p.CodeChallengeMethod,
		Scope:               scope,
		Resource:            p.Resource,
	}, oauthAuthCodeTTL)
	if err != nil {
		log.Printf("web: oauth grant+code: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, buildRedirect(p.RedirectURI, url.Values{"code": {code}, "state": {p.State}}), http.StatusSeeOther)
}

// handleConnectorRevoke revokes a connector grant (and all its tokens).
func (a *app) handleConnectorRevoke(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBody)
	if !a.checkCSRF(r, sess) {
		http.Error(w, "bad CSRF token", http.StatusForbidden)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := a.store.RevokeOAuthGrant(r.Context(), id); err != nil {
		log.Printf("web: revoke connector %d: %v", id, err)
	}
	http.Redirect(w, r, "/tokens?flash=Connector+access+revoked", http.StatusSeeOther)
}

// --- helpers ---

// redirectAllowed reports whether uri exactly matches one of the client's
// registered redirect URIs (the open-redirect / code-interception guard).
func redirectAllowed(c *store.OAuthClient, uri string) bool {
	if c == nil || uri == "" {
		return false
	}
	for _, u := range c.RedirectURIs {
		if u == uri {
			return true
		}
	}
	return false
}

// redirectURIOrigin returns the "scheme://host" origin of a well-formed http(s)
// redirect URI (used to widen the consent page's form-action CSP so the
// post-approve redirect to the client isn't blocked), or "" for an empty value or
// anything that isn't a clean absolute http/https URL. The origin is spliced into a
// space-separated CSP source-list, and url.Parse leaves CSP-significant characters
// (';' ',' quotes '*' …) in u.Host, so the host is additionally validated to contain
// only host characters — otherwise "" is returned and form-action is NOT widened.
func redirectURIOrigin(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return ""
	}
	if !isPlainHost(u.Host) {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// isPlainHost reports whether h is a bare host[:port] (including a bracketed IPv6
// literal) containing only characters that cannot break out of a CSP source token.
func isPlainHost(h string) bool {
	if h == "" {
		return false
	}
	for _, r := range h {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '-' || r == ':' || r == '[' || r == ']':
		default:
			return false
		}
	}
	return true
}

// clientDisplayName is the connector's shown/actor name — the registered client
// name, trimmed and length-bounded. It is SELF-REPORTED (open DCR lets any client
// pick any name), so it must never be presented as a trust signal; an empty name
// defaults to a deliberately neutral label rather than anything claude.ai-looking.
// resolveConnectorActor picks the actor a connector's writes are authored under.
//
// The form field is an actor id the operator selected from live actors; anything
// unparseable or absent falls back to the historical behaviour of an actor named
// after the client. It is validated against the actor table rather than trusted,
// because a forged id here would attribute a connector's writes to someone else —
// and authorship is the field a human reads when deciding whether to promote.
func (a *app) resolveConnectorActor(r *http.Request, client *store.OAuthClient) (int64, error) {
	if v := strings.TrimSpace(r.FormValue("write_as")); v != "" {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil && id > 0 {
			ok, err := a.store.ActorExists(r.Context(), id)
			if err != nil {
				return 0, err
			}
			if ok {
				return id, nil
			}
		}
	}
	return a.store.FindOrCreateActor(r.Context(), "ai", clientDisplayName(client))
}

func clientDisplayName(c *store.OAuthClient) string {
	name := ""
	if c != nil {
		name = strings.TrimSpace(c.Name)
	}
	if name == "" {
		return "an unnamed application"
	}
	if len(name) > 60 {
		name = name[:60]
	}
	return name
}

// buildRedirect appends the given params to a (validated) redirect URI, preserving
// any query the client already put there.
func buildRedirect(base string, extra url.Values) string {
	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	q := u.Query()
	for k, vs := range extra {
		for _, v := range vs {
			if v != "" {
				q.Set(k, v)
			}
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// redirectAuthError sends an OAuth error back to the (validated) client redirect
// URI per RFC 6749 §4.1.2.1, preserving state.
func (a *app) redirectAuthError(w http.ResponseWriter, r *http.Request, p authParams, code, desc string) {
	http.Redirect(w, r, buildRedirect(p.RedirectURI, url.Values{
		"error":             {code},
		"error_description": {desc},
		"state":             {p.State},
	}), http.StatusSeeOther)
}

// oauthErrorPage renders an on-site error (used only when the redirect URI itself
// can't be trusted, so we must not bounce the error to the client). Rendered at
// 200 — render() owns the response headers/body, so we don't pre-write a status.
func (a *app) oauthErrorPage(w http.ResponseWriter, r *http.Request, msg string) {
	a.render(w, r, a.currentSession(r), "consent", map[string]any{"Error": msg})
}

// consentSurface is one Ken surface the human is granting on the consent screen.
type consentSurface struct{ Scope, LabelKey, HelpKey string }

// consentSurfaces is what the consent screen offers, in the order a human meets them.
//
// THE LIST IS DERIVED FROM store.DefaultGrantScopes() rather than written out again, so a surface
// added there cannot be silently ungrantable here — the mismatch would be invisible, which is the
// failure mode this whole area keeps producing.
func consentSurfaces() []consentSurface {
	labels := map[string]consentSurface{
		store.ScopeKB:      {store.ScopeKB, "consent.surface_kb", "consent.surface_kb_help"},
		store.ScopeCommSet: {store.ScopeCommSet, "consent.surface_comm", "consent.surface_comm_help"},
		store.ScopeStation: {store.ScopeStation, "consent.surface_station", "consent.surface_station_help"},
	}
	out := make([]consentSurface, 0, len(labels))
	for _, sc := range store.DefaultGrantScopes() {
		if cs, ok := labels[sc]; ok {
			out = append(out, cs)
		}
	}
	return out
}
