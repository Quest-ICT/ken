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
	a.render(w, r, sess, "consent", map[string]any{
		"CSRF":         sess.CSRF,
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

	// One shared 'ai' actor per client name authors this connector's MCP writes.
	connActor, err := a.store.FindOrCreateActor(r.Context(), "ai", clientDisplayName(client))
	if err != nil {
		log.Printf("web: oauth connector actor: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	scope := strings.TrimSpace(p.Scope)
	if scope == "" {
		scope = "read write offline_access"
	}
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
