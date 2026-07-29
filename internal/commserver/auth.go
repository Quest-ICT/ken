package commserver

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Quest-ICT/ken/internal/metrics"
	"github.com/Quest-ICT/ken/internal/ratelimit"
	"github.com/Quest-ICT/ken/internal/store"
)

// ScopeComm is the capability required by every tool on this endpoint.
//
// ScopeCommFile is RESERVED and required by nothing yet: file exchange is
// deferred to a later MINOR (docs/COMM.md §11). It is declared now because
// splitting a shipped `comm` scope into two later would be a MAJOR under
// COMPATIBILITY.md, while merging two into one is free.
const (
	ScopeComm     = "comm"
	ScopeCommFile = "comm-file"
)

// maxCommBody caps a request body on this endpoint. Smaller than the knowledge
// base's cap on purpose: message bodies are separately capped in internal/comm,
// and nothing here legitimately approaches a multi-megabyte payload — tool
// arguments are generated token-by-token by a model, so a large body is a design
// error long before it is a transport one.
const maxCommBody = 1 << 20 // 1 MiB

// principal is the authenticated caller: a token holder, not a session. Session
// identity comes from the endpoint id + secret carried in each tool call, because
// the operating convention is one Ken token per MACHINE.
type principal struct {
	ActorID int64
	TokenID string
	SpaceID int64
	// Scopes carries the token's full comm-family scope set: the transport
	// middleware requires `comm`, but the file tools additionally require
	// `comm-file`, and that second check happens per tool against this set.
	Scopes map[string]bool
}

type ctxKey struct{}

func withPrincipal(ctx context.Context, p *principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

func principalFrom(ctx context.Context) *principal {
	p, _ := ctx.Value(ctxKey{}).(*principal)
	return p
}

// authMiddleware authenticates a bearer token and requires the `comm` scope.
//
// This deliberately does NOT reuse internal/mcpserver's authentication, and the
// duplication is the point rather than an oversight. That path accepts three token
// shapes; this one accepts exactly ONE — a `ken_<id>_<secret>` API token whose
// scopes include `comm`:
//
//   - The OAuth path is excluded because a cloud-hosted connector is the worst
//     possible holder of "reach into the sessions on my machines", and its scope
//     set is hard-coded rather than operator-chosen, so an operator could not
//     withhold comm from it even if they wanted to.
//   - The dev-token bypass is excluded because it is a single static credential
//     with an empty token id, which also means it bypasses per-token rate
//     accounting — any quota keyed on a token id would be unenforceable for it.
//
// Sharing the other package's authenticate() would mean a future token shape added
// there silently gains access here. Keeping them separate makes that impossible.
func authMiddleware(st *store.Store, limiter ratelimit.Limiter, reg *metrics.Registry, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// No CORS headers at all: unlike /mcp, this endpoint has no browser client,
		// so a permissive Access-Control-Allow-Origin would be pure attack surface.
		if r.Method == http.MethodOptions {
			httpError(w, http.StatusMethodNotAllowed, "not allowed")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxCommBody)

		tok := bearerToken(r)
		if tok == "" {
			authFail(w, reg, "missing bearer token")
			return
		}
		p, err := authenticate(r.Context(), st, tok, ScopeComm)
		if err != nil {
			authFail(w, reg, "invalid token")
			return
		}

		// Its own rate accounting, separate from the knowledge base's bucket: the
		// operating convention is one token per machine, so a comm poll loop sharing
		// the KB's budget could starve that machine's kb_* calls (and, past the
		// per-IP strike threshold, lock the machine out entirely).
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
		// Record use here too. Prod observed three comm tokens reporting last_used_at
		// NULL after 102 acknowledged messages, because TouchToken was only ever called
		// from the knowledge-base authenticator. A token whose last use is unknown
		// cannot be reasoned about during an incident.
		st.TouchToken(r.Context(), p.TokenID)

		next.ServeHTTP(w, r.WithContext(withPrincipal(r.Context(), p)))
	})
}

// authenticate resolves a bearer to a comm principal, accepting only a
// `ken_<id>_<secret>` API token carrying requiredScope (`comm` for the MCP
// transport, `comm-file` for the byte-relay HTTP surface).
func authenticate(ctx context.Context, st *store.Store, tok, requiredScope string) (*principal, error) {
	if !strings.HasPrefix(tok, "ken_") {
		return nil, errors.New("comm requires a dedicated ken_ API token")
	}
	parts := strings.SplitN(tok, "_", 3)
	if len(parts) != 3 || parts[0] != "ken" {
		return nil, errors.New("bad token format")
	}
	tokenID, secret := parts[1], parts[2]

	var (
		actorID    int64
		spaceID    int64
		secretHash string
		scopesJSON string
		revoked    sql.NullString
	)
	err := st.R.QueryRowContext(ctx, `
SELECT t.actor_id, a.space_id, t.secret_sha256, t.scopes, t.revoked_at
FROM api_token t JOIN actor a ON a.id = t.actor_id
WHERE t.token_id = ?`, tokenID).
		Scan(&actorID, &spaceID, &secretHash, &scopesJSON, &revoked)
	if err != nil {
		return nil, err
	}
	if revoked.Valid {
		return nil, errors.New("revoked token")
	}
	sum := sha256.Sum256([]byte(secret))
	if subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(secretHash)) != 1 {
		return nil, errors.New("bad secret")
	}

	var scopes []string
	_ = json.Unmarshal([]byte(scopesJSON), &scopes)
	set := make(map[string]bool, len(scopes))
	for _, sc := range scopes {
		set[sc] = true
	}
	// Fails closed: a token without the required scope is refused at the
	// transport, so the surface is unreachable rather than merely erroring per call.
	if !set[requiredScope] {
		return nil, errors.New("token is missing the '" + requiredScope + "' scope")
	}
	return &principal{ActorID: actorID, TokenID: tokenID, SpaceID: spaceID, Scopes: set}, nil
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}

func authFail(w http.ResponseWriter, reg *metrics.Registry, msg string) {
	if reg != nil {
		reg.AuthFailure("comm")
	}
	httpError(w, http.StatusUnauthorized, msg)
}

func httpError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(msg + "\n"))
}
