package mcpserver

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Quest-ICT/ken/internal/metrics"
	"github.com/Quest-ICT/ken/internal/ratelimit"
	"github.com/Quest-ICT/ken/internal/store"
)

// Scope names (capabilities). See docs/MCP-TOOLS.md.
//
// scopeCurate is intentionally not required by any MCP tool: moving the curated
// head is a human-only act performed in the web UI, never over MCP. It remains a
// mintable token scope (a curator-review UI or a future MCP curation tool may
// require it) so the vocabulary is stable — not dead code.
const (
	scopeRead       = "read"
	scopeWriteDraft = "write-draft"
	scopePropose    = "propose"
	scopeCurate     = "curate"
)

// maxMCPBody caps an MCP request body (kb_save fields are already length-limited
// in the store; this bounds the overall JSON payload).
const maxMCPBody = 4 << 20 // 4 MiB

type principal struct {
	ActorID  int64
	TokenID  string // empty for the dev-token bypass; "oauth-<grantID>" for OAuth
	Scopes   map[string]bool
	APIToken bool // true only for a ken_ api_token (so last_used_at is bumped)
}

type ctxKey struct{}

func withPrincipal(ctx context.Context, p *principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

func principalFrom(ctx context.Context) *principal {
	p, _ := ctx.Value(ctxKey{}).(*principal)
	return p
}

// requireScope enforces that the authenticated principal holds scope.
func requireScope(ctx context.Context, scope string) error {
	p := principalFrom(ctx)
	if p == nil {
		return errors.New("unauthenticated")
	}
	if !p.Scopes[scope] {
		return errors.New("forbidden: token is missing the '" + scope + "' scope")
	}
	return nil
}

func scopeSet(list []string) map[string]bool {
	m := make(map[string]bool, len(list))
	for _, s := range list {
		m[s] = true
	}
	return m
}

// authMiddleware validates the bearer token and attaches the principal to the
// request context, which the MCP handler propagates to tool handlers. It also
// sets CORS (claude.ai's browser fetches /mcp cross-origin) and, when OAuth is
// enabled, emits the RFC 9728 WWW-Authenticate discovery challenge on 401.
func authMiddleware(st *store.Store, tt *touchThrottle, limiter ratelimit.Limiter, reg *metrics.Registry, resourceMeta func(*http.Request) string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setMCPCORS(w)
		if r.Method == http.MethodOptions { // CORS preflight
			w.WriteHeader(http.StatusNoContent)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxMCPBody)
		tok := bearerToken(r)
		if tok == "" {
			unauthorized(w, r, reg, resourceMeta, "missing bearer token")
			return
		}
		p, err := authenticate(r.Context(), st, tok)
		if err != nil {
			unauthorized(w, r, reg, resourceMeta, "invalid token")
			return
		}
		if p.TokenID != "" {
			// Per-token rate limit (keyed by token id, so it survives an IP change).
			if limiter != nil {
				if ok, retry := limiter.Allow(p.TokenID); !ok {
					if reg != nil {
						reg.RateLimitRejected()
					}
					w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
					httpError(w, http.StatusTooManyRequests, "rate limited")
					return
				}
			}
			if p.APIToken {
				tt.maybe(r.Context(), st, p.TokenID)
			}
		}
		next.ServeHTTP(w, r.WithContext(withPrincipal(r.Context(), p)))
	})
}

// setMCPCORS allows claude.ai's browser to fetch /mcp and, critically, to READ
// the WWW-Authenticate header (Expose-Headers) that drives OAuth discovery.
func setMCPCORS(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Mcp-Protocol-Version, Mcp-Session-Id, Last-Event-ID")
	// Expose the session id so the browser streamable transport can read it back,
	// and WWW-Authenticate so the OAuth discovery challenge is visible cross-origin.
	h.Set("Access-Control-Expose-Headers", "WWW-Authenticate, Mcp-Session-Id")
}

// unauthorized writes the 401, attaching the RFC 9728 resource-metadata pointer
// when OAuth is enabled so a client can discover the authorization server.
func unauthorized(w http.ResponseWriter, r *http.Request, reg *metrics.Registry, resourceMeta func(*http.Request) string, msg string) {
	if reg != nil {
		reg.AuthFailure("mcp")
	}
	if resourceMeta != nil {
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+resourceMeta(r)+`"`)
	}
	httpError(w, http.StatusUnauthorized, msg)
}

// touchThrottle keeps the last_used_at write off the hot read path: it issues
// the DB update at most once per minute per token (the SQL is time-guarded too,
// so this only avoids acquiring the single-writer connection needlessly).
type touchThrottle struct {
	mu   sync.Mutex
	last map[string]time.Time
}

func newTouchThrottle() *touchThrottle { return &touchThrottle{last: map[string]time.Time{}} }

func (t *touchThrottle) maybe(ctx context.Context, st *store.Store, tokenID string) {
	t.mu.Lock()
	now := time.Now()
	if l, ok := t.last[tokenID]; ok && now.Sub(l) < time.Minute {
		t.mu.Unlock()
		return
	}
	t.last[tokenID] = now
	t.mu.Unlock()
	st.TouchToken(ctx, tokenID)
}

func bearerToken(r *http.Request) string { return bearerFromHeader(r.Header) }

// bearerFromHeader is split out because the per-call identity fix (see addTool) has
// only a header to work from, never a *http.Request.
func bearerFromHeader(h http.Header) string {
	v := h.Get("Authorization")
	const prefix = "Bearer "
	if len(v) > len(prefix) && strings.EqualFold(v[:len(prefix)], prefix) {
		return strings.TrimSpace(v[len(prefix):])
	}
	return ""
}

// authenticate resolves a bearer to a principal. Three shapes, dispatched by
// prefix: the KEN_DEV_TOKEN dev bypass; a `ken_<id>_<secret>` api_token (CLI /
// web minted); or an opaque OAuth access token (claude.ai connector). OAuth
// tokens are base62 with no '_', so they never collide with the api_token shape.
func authenticate(ctx context.Context, st *store.Store, tok string) (*principal, error) {
	if dev := os.Getenv("KEN_DEV_TOKEN"); dev != "" && constEq(tok, dev) {
		return &principal{Scopes: scopeSet([]string{scopeRead, scopeWriteDraft, scopePropose})}, nil
	}
	if strings.HasPrefix(tok, "ken_") {
		return authenticateAPIToken(ctx, st, tok)
	}
	// OAuth access token: a human-approved connector gets the standard agent
	// capability set (read | write-draft | propose) — never curate.
	op, err := st.ValidateOAuthAccessToken(ctx, tok)
	if err != nil {
		return nil, err
	}
	return &principal{
		ActorID: op.ActorID,
		TokenID: "oauth-" + strconv.FormatInt(op.GrantID, 10),
		Scopes:  scopeSet([]string{scopeRead, scopeWriteDraft, scopePropose}),
	}, nil
}

// authenticateAPIToken validates a `ken_<tokenID>_<secret>` API token.
func authenticateAPIToken(ctx context.Context, st *store.Store, tok string) (*principal, error) {
	parts := strings.SplitN(tok, "_", 3)
	if len(parts) != 3 || parts[0] != "ken" {
		return nil, errors.New("bad token format")
	}
	tokenID, secret := parts[1], parts[2]

	var (
		actorID    int64
		secretHash string
		scopesJSON string
		revoked    sql.NullString
	)
	err := st.R.QueryRowContext(ctx,
		`SELECT actor_id, secret_sha256, scopes, revoked_at FROM api_token WHERE token_id = ?`, tokenID).
		Scan(&actorID, &secretHash, &scopesJSON, &revoked)
	if err != nil {
		return nil, err
	}
	if revoked.Valid {
		return nil, errors.New("revoked token")
	}
	sum := sha256.Sum256([]byte(secret))
	if !constEq(hex.EncodeToString(sum[:]), secretHash) {
		return nil, errors.New("bad secret")
	}

	var scopes []string
	_ = json.Unmarshal([]byte(scopesJSON), &scopes)
	return &principal{ActorID: actorID, TokenID: tokenID, Scopes: scopeSet(scopes), APIToken: true}, nil
}

func constEq(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func httpError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(code)
	_, _ = io.WriteString(w, msg+"\n")
}
