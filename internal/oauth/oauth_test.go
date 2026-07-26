package oauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Quest-ICT/ken/internal/store"
)

func testStore(t *testing.T) *store.Store {
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

func testServer(t *testing.T, st *store.Store) *Server {
	return New(st, func(*http.Request) string { return "https://host" }, Config{})
}

func pkcePair() (verifier, challenge string) {
	verifier = "verifier-abcdefghijklmnopqrstuvwxyz-0123456789"
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

// decode reads a JSON body into a map.
func decode(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode %q: %v", rr.Body.String(), err)
	}
	return m
}

func TestMetadataDocuments(t *testing.T) {
	s := testServer(t, testStore(t))

	rr := httptest.NewRecorder()
	s.HandleASMetadata(rr, httptest.NewRequest("GET", "/.well-known/oauth-authorization-server", nil))
	m := decode(t, rr)
	if m["issuer"] != "https://host" || m["authorization_endpoint"] != "https://host/oauth/authorize" ||
		m["token_endpoint"] != "https://host/oauth/token" || m["registration_endpoint"] != "https://host/oauth/register" {
		t.Fatalf("AS metadata wrong: %v", m)
	}
	if cc, _ := m["code_challenge_methods_supported"].([]any); len(cc) != 1 || cc[0] != "S256" {
		t.Fatalf("must advertise S256, got %v", m["code_challenge_methods_supported"])
	}

	rr = httptest.NewRecorder()
	s.HandlePRMetadata(rr, httptest.NewRequest("GET", "/.well-known/oauth-protected-resource/mcp", nil))
	m = decode(t, rr)
	if m["resource"] != "https://host/mcp" {
		t.Fatalf("PRM resource must be the MCP URL, got %v", m["resource"])
	}
	if as, _ := m["authorization_servers"].([]any); len(as) != 1 || as[0] != "https://host" {
		t.Fatalf("PRM authorization_servers wrong: %v", m["authorization_servers"])
	}
}

func TestRegisterAndCORS(t *testing.T) {
	s := testServer(t, testStore(t))

	// CORS preflight → 204 with the exposed WWW-Authenticate header.
	rr := httptest.NewRecorder()
	s.HandleRegister(rr, httptest.NewRequest("OPTIONS", "/oauth/register", nil))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("preflight want 204, got %d", rr.Code)
	}
	if rr.Header().Get("Access-Control-Expose-Headers") != "WWW-Authenticate" {
		t.Fatalf("preflight must expose WWW-Authenticate, got %q", rr.Header().Get("Access-Control-Expose-Headers"))
	}

	// Valid DCR.
	body := `{"redirect_uris":["https://claude.ai/api/mcp/auth_callback"],"client_name":"Claude","token_endpoint_auth_method":"none"}`
	req := httptest.NewRequest("POST", "/oauth/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	s.HandleRegister(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("register want 201, got %d (%s)", rr.Code, rr.Body.String())
	}
	m := decode(t, rr)
	if cid, _ := m["client_id"].(string); cid == "" || m["token_endpoint_auth_method"] != "none" {
		t.Fatalf("register response wrong: %v", m)
	}

	// A non-https, non-loopback redirect URI is rejected.
	bad := `{"redirect_uris":["http://evil.example.com/cb"],"client_name":"x"}`
	req = httptest.NewRequest("POST", "/oauth/register", strings.NewReader(bad))
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	s.HandleRegister(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("bad redirect URI want 400, got %d", rr.Code)
	}
}

// tokenForm POSTs an x-www-form-urlencoded body to the token endpoint.
func tokenForm(t *testing.T, s *Server, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	s.HandleToken(rr, req)
	return rr
}

func TestTokenExchangeAndRefresh(t *testing.T) {
	st := testStore(t)
	s := testServer(t, st)
	ctx := context.Background()

	human, _ := st.FindOrCreateActor(ctx, "human", "curator")
	conn, _ := st.FindOrCreateActor(ctx, "ai", "Claude")
	const redir = "https://claude.ai/api/mcp/auth_callback"
	clientID, _ := st.RegisterOAuthClient(ctx, "Claude", []string{redir})
	verifier, challenge := pkcePair()
	code, _ := st.CreateOAuthGrantAndCode(ctx, store.NewAuthCode{
		ClientID: clientID, ConnectorActorID: conn, HumanActorID: human,
		RedirectURI: redir, CodeChallenge: challenge, CodeChallengeMethod: "S256",
		Scope: "read write offline_access", Resource: "https://host/mcp",
	}, time.Minute)

	// Wrong PKCE verifier → invalid_grant, no token issued.
	rr := tokenForm(t, s, url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "client_id": {clientID},
		"redirect_uri": {redir}, "code_verifier": {"WRONG"},
	})
	if rr.Code != http.StatusBadRequest || decode(t, rr)["error"] != "invalid_grant" {
		t.Fatalf("bad PKCE want 400 invalid_grant, got %d %s", rr.Code, rr.Body.String())
	}

	// Correct exchange.
	rr = tokenForm(t, s, url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "client_id": {clientID},
		"redirect_uri": {redir}, "code_verifier": {verifier}, "resource": {"https://host/mcp"},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("exchange want 200, got %d (%s)", rr.Code, rr.Body.String())
	}
	tok := decode(t, rr)
	access, _ := tok["access_token"].(string)
	refresh, _ := tok["refresh_token"].(string)
	if access == "" || refresh == "" || tok["token_type"] != "Bearer" {
		t.Fatalf("token response wrong: %v", tok)
	}
	// The issued access token validates in the store as the connector actor.
	if p, err := st.ValidateOAuthAccessToken(ctx, access); err != nil || p.ActorID != conn {
		t.Fatalf("issued access token must validate as the connector: %+v %v", p, err)
	}

	// Refresh grant → a fresh, valid access token.
	rr = tokenForm(t, s, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}})
	if rr.Code != http.StatusOK {
		t.Fatalf("refresh want 200, got %d (%s)", rr.Code, rr.Body.String())
	}
	access2, _ := decode(t, rr)["access_token"].(string)
	if _, err := st.ValidateOAuthAccessToken(ctx, access2); err != nil {
		t.Fatalf("refreshed access token must validate: %v", err)
	}

	// Reusing the now-rotated refresh token → invalid_grant.
	rr = tokenForm(t, s, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}})
	if rr.Code != http.StatusBadRequest || decode(t, rr)["error"] != "invalid_grant" {
		t.Fatalf("reused refresh want 400 invalid_grant, got %d %s", rr.Code, rr.Body.String())
	}
}
