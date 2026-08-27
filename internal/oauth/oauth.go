// Package oauth implements the stateless endpoints of Ken's optional OAuth 2.1
// authorization server: discovery metadata (RFC 8414 + RFC 9728), dynamic client
// registration (RFC 7591), and the token endpoint (authorization_code +
// refresh_token, PKCE-S256). The interactive /authorize + consent step lives in
// internal/web (it needs the human session); MCP access-token validation lives in
// the store (ValidateOAuthAccessToken) so the hot path never imports this package.
//
// Always mounted — OAuth is how a human registers Ken ONCE on their account and
// reaches it from every client afterwards, so it is not something to be missing.
// Purpose: let claude.ai add Ken as a
// remote-MCP "custom connector" (OAuth-only on personal accounts). A connector
// authenticated this way gets the standard agent capability set (read |
// write-draft | propose) — never curate; curation stays human-only in the web UI.
package oauth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Quest-ICT/ken/internal/store"
)

// Config carries the token lifetimes. Zero values fall back to the defaults in
// New. Access tokens are deliberately short (claude.ai refreshes proactively);
// grant revocation is instant regardless, since every MCP call re-checks it.
type Config struct {
	AccessTTL  time.Duration
	RefreshTTL time.Duration
	CodeTTL    time.Duration
}

// Server holds the collaborators the stateless OAuth endpoints need.
type Server struct {
	st      *store.Store
	baseURL func(*http.Request) string // canonical origin "https://host" (no trailing slash)
	cfg     Config
}

// scopesSupported is what the AS advertises, and since 3.25.0 it is LOAD-BEARING.
//
// *** THIS COMMENT USED TO END "so the exact strings here are cosmetic to Ken", AND THAT SENTENCE
// SURVIVED THE RELEASE THAT MADE IT FALSE. *** Ken did map every connector to a fixed
// read/write-draft/propose set; §10 step 2 replaced that with store.GrantedCapabilities(scope),
// which reads these strings. The advertisement was left behind.
//
// ken-prod-ops measured the consequence on the live deployment: **8 grants, every one
// `read write offline_access`, because that is all any document ever offered.** A client doing
// everything correctly asks for exactly what is advertised, lands in the legacy branch by
// construction, and is refused at the end of a flow that could never have produced anything else.
// The capability was implemented, tested and unreachable.
//
// So the ken: scopes are advertised here. `read` and `write` stay because claude.ai and other
// clients send them, and `offline_access` is what makes a client request a refresh token.
var scopesSupported = []string{
	"read", "write", "offline_access",
	store.ScopeKB, store.ScopeCommSet, store.ScopeStation,
}

// New builds a Server. baseURL must return the canonical https origin for a
// request (used verbatim as the issuer and to build every endpoint URL).
func New(st *store.Store, baseURL func(*http.Request) string, cfg Config) *Server {
	if cfg.AccessTTL <= 0 {
		cfg.AccessTTL = time.Hour
	}
	if cfg.RefreshTTL <= 0 {
		cfg.RefreshTTL = 90 * 24 * time.Hour
	}
	if cfg.CodeTTL <= 0 {
		cfg.CodeTTL = 60 * time.Second
	}
	return &Server{st: st, baseURL: baseURL, cfg: cfg}
}

// ResourceMetadataURL is the RFC 9728 protected-resource-metadata URL for the MCP
// endpoint — the value the MCP endpoint advertises in its 401 WWW-Authenticate
// header so a client can discover this authorization server.
func (s *Server) ResourceMetadataURL(r *http.Request) string {
	return s.baseURL(r) + "/.well-known/oauth-protected-resource/mcp"
}

// ResourceMetadataURLFor builds the discovery challenge target for one MCP surface.
//
// Every protected surface must answer its 401 with a WWW-Authenticate naming ITS OWN metadata.
// /mcp did; /comm/mcp and /station/mcp returned a bare "missing bearer token" with no challenge at
// all, so a client had nothing to follow — measured on the live deployment, and the first of three
// walls between a correct client and a workspace.
//
// Worth keeping from that measurement, because it decides where fixes for this class belong: the
// session on the other side could not see the difference. Its own words — "a 401-without-
// WWW-Authenticate is indistinguishable from a 401 with one: both render as the same 'needs
// authorization' notice. The diagnostic detail that would let someone fix the server is not
// propagated to me at all." **A client can never diagnose this. The server has to advertise.**
func (s *Server) ResourceMetadataURLFor(surface string) func(*http.Request) string {
	return func(r *http.Request) string {
		return s.baseURL(r) + "/.well-known/oauth-protected-resource" + surface
	}
}

// --- CORS (claude.ai fetches discovery/registration/token from the browser) ---

// writeCORS sets permissive CORS for the machine-to-machine OAuth endpoints.
// No credentials are used (the token travels in the body/Authorization header),
// so "*" is safe. WWW-Authenticate is exposed for the discovery handshake.
func writeCORS(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Mcp-Protocol-Version")
	h.Set("Access-Control-Expose-Headers", "WWW-Authenticate")
	h.Set("Access-Control-Max-Age", "3600")
}

// preflight answers an OPTIONS request with CORS headers and 204; returns true if
// it handled the request.
func preflight(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodOptions {
		return false
	}
	writeCORS(w)
	w.WriteHeader(http.StatusNoContent)
	return true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	writeCORS(w)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// oauthError writes an RFC 6749 §5.2 error object with the right status.
func oauthError(w http.ResponseWriter, code int, err, desc string) {
	writeJSON(w, code, map[string]string{"error": err, "error_description": desc})
}

// --- discovery ---

// HandleASMetadata serves RFC 8414 authorization-server metadata at
// /.well-known/oauth-authorization-server.
func (s *Server) HandleASMetadata(w http.ResponseWriter, r *http.Request) {
	if preflight(w, r) {
		return
	}
	base := s.baseURL(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                base,
		"authorization_endpoint":                base + "/oauth/authorize",
		"token_endpoint":                        base + "/oauth/token",
		"registration_endpoint":                 base + "/oauth/register",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"scopes_supported":                      scopesSupported,
	})
}

// HandlePRMetadata serves RFC 9728 protected-resource metadata. Registered for
// BOTH /.well-known/oauth-protected-resource and
// /.well-known/oauth-protected-resource/mcp (claude.ai probes the path-suffixed
// form first). resource MUST equal the MCP URL the user typed into Claude.
func (s *Server) HandlePRMetadata(w http.ResponseWriter, r *http.Request) {
	if preflight(w, r) {
		return
	}
	base := s.baseURL(r)
	writeJSON(w, http.StatusOK, map[string]any{
		// RESOURCE MUST BE THE MCP URL THE CLIENT IS TALKING TO, not always /mcp.
		//
		// Ken serves three MCP surfaces and this document described exactly one of them, so
		// /.well-known/oauth-protected-resource/comm/mcp and .../station/mcp were 404s. A client
		// that guessed the RFC 9728 location for the surface it wanted found nothing, which is
		// one of the three walls ken-prod-ops measured between a correct client and a station.
		"resource":                 base + resourceFor(r),
		"authorization_servers":    []string{base},
		"scopes_supported":         scopesSupported,
		"bearer_methods_supported": []string{"header"},
	})
}

// resourceFor maps a protected-resource metadata request to the MCP surface it describes.
//
// RFC 9728 puts the resource's path after the well-known prefix. There is now exactly ONE
// protected resource — /mcp, carrying every tool — so every form answers for it.
//
// THE PER-SURFACE SUFFIXES USED TO ECHO THEMSELVES BACK, and they must not any more: returning
// "/station/mcp" would describe an endpoint that no longer exists, and a client following it would
// authorise against a 404. A stale connector asking the old question now gets the right answer
// instead of a confident wrong one.
func resourceFor(r *http.Request) string { return "/mcp" }

// --- dynamic client registration (RFC 7591) ---

type registrationRequest struct {
	RedirectURIs            []string `json:"redirect_uris"`
	ClientName              string   `json:"client_name"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	Scope                   string   `json:"scope"`
}

// HandleRegister implements DCR. It is intentionally open (per RFC 7591 and how
// claude.ai self-registers), but bounded: redirect URIs must be https or
// loopback, and it sits behind Ken's per-IP rate-limit guard. A registered
// client is inert until a human approves it at /oauth/authorize.
func (s *Server) HandleRegister(w http.ResponseWriter, r *http.Request) {
	if preflight(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		oauthError(w, http.StatusMethodNotAllowed, "invalid_request", "POST required")
		return
	}
	var req registrationRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "malformed JSON body")
		return
	}
	if len(req.RedirectURIs) == 0 {
		oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect_uris is required")
		return
	}
	for _, u := range req.RedirectURIs {
		if !validRedirectURI(u) {
			oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect URIs must be https or loopback: "+u)
			return
		}
	}
	name := strings.TrimSpace(req.ClientName)
	clientID, err := s.st.RegisterOAuthClient(r.Context(), name, req.RedirectURIs)
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "could not register client")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"client_id":                  clientID,
		"client_name":                name,
		"redirect_uris":              req.RedirectURIs,
		"token_endpoint_auth_method": "none",
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"scope":                      strings.Join(scopesSupported, " "),
	})
}

// validRedirectURI accepts absolute https URLs and loopback http URLs (RFC 8252),
// rejecting anything else (no wildcards, no fragments).
func validRedirectURI(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Fragment != "" || u.Host == "" {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	if u.Scheme == "http" {
		host := u.Hostname()
		return host == "localhost" || host == "127.0.0.1" || host == "::1"
	}
	return false
}

// --- token endpoint ---

// HandleToken implements the token endpoint (application/x-www-form-urlencoded).
func (s *Server) HandleToken(w http.ResponseWriter, r *http.Request) {
	if preflight(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		oauthError(w, http.StatusMethodNotAllowed, "invalid_request", "POST required")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "malformed form body")
		return
	}
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		s.grantAuthCode(w, r)
	case "refresh_token":
		s.grantRefresh(w, r)
	default:
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type", "grant_type must be authorization_code or refresh_token")
	}
}

func (s *Server) grantAuthCode(w http.ResponseWriter, r *http.Request) {
	f := r.PostForm
	code := f.Get("code")
	if code == "" {
		oauthError(w, http.StatusBadRequest, "invalid_request", "code is required")
		return
	}
	cd, err := s.st.PeekOAuthCode(r.Context(), code)
	if err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "authorization code invalid or expired")
		return
	}
	// Bind the exchange to the same client + redirect_uri the code was issued to.
	if f.Get("client_id") != cd.ClientID {
		oauthError(w, http.StatusBadRequest, "invalid_client", "client_id mismatch")
		return
	}
	if f.Get("redirect_uri") != cd.RedirectURI {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "redirect_uri mismatch")
		return
	}
	// PKCE (S256 only).
	verifier := f.Get("code_verifier")
	if verifier == "" || !verifyPKCE(cd.CodeChallengeMethod, verifier, cd.CodeChallenge) {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "PKCE verification failed")
		return
	}
	access, refresh, err := s.st.ExchangeOAuthCode(r.Context(), code, cd.GrantID, s.cfg.AccessTTL, s.cfg.RefreshTTL)
	if err != nil {
		// Lost the single-use race, or expired between peek and consume.
		oauthError(w, http.StatusBadRequest, "invalid_grant", "authorization code could not be redeemed")
		return
	}
	s.writeTokens(w, access, refresh, cd.Scope)
}

func (s *Server) grantRefresh(w http.ResponseWriter, r *http.Request) {
	rt := r.PostForm.Get("refresh_token")
	if rt == "" {
		oauthError(w, http.StatusBadRequest, "invalid_request", "refresh_token is required")
		return
	}
	access, refresh, rr, err := s.st.RotateOAuthRefresh(r.Context(), rt, s.cfg.AccessTTL, s.cfg.RefreshTTL)
	if err != nil {
		// Both a bad/expired token and a reuse-kill surface as invalid_grant so we
		// don't leak which case occurred.
		if errors.Is(err, store.ErrOAuthReuseKill) || errors.Is(err, store.ErrOAuthBadToken) {
			oauthError(w, http.StatusBadRequest, "invalid_grant", "refresh token invalid")
			return
		}
		oauthError(w, http.StatusInternalServerError, "server_error", "could not refresh")
		return
	}
	scope := ""
	if rr != nil {
		scope = rr.Scope
	}
	s.writeTokens(w, access, refresh, scope)
}

// writeTokens renders the RFC 6749 token response.
func (s *Server) writeTokens(w http.ResponseWriter, access, refresh, scope string) {
	resp := map[string]any{
		"access_token":  access,
		"token_type":    "Bearer",
		"expires_in":    int(s.cfg.AccessTTL.Seconds()),
		"refresh_token": refresh,
	}
	if scope != "" {
		resp["scope"] = scope
	}
	writeJSON(w, http.StatusOK, resp)
}

// verifyPKCE returns true iff method is S256 and BASE64URL(SHA256(verifier))
// equals the stored challenge. Plain/other methods are rejected (S256 required).
func verifyPKCE(method, verifier, challenge string) bool {
	if method != "S256" {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:]) == challenge
}
