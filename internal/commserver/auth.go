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
	"os"
	"strconv"
	"strings"

	"github.com/Quest-ICT/ken/internal/metrics"
	"github.com/Quest-ICT/ken/internal/ratelimit"
	"github.com/Quest-ICT/ken/internal/store"
)

// ScopeComm is the capability required by every tool on this endpoint.
//
// ScopeCommFile gates file exchange, and it is ENFORCED — at commserver.go's file tool
// handlers and again at files.go's byte-relay HTTP surface. Both were checked when this
// comment was corrected.
//
// IT SAID "RESERVED and required by nothing yet" UNTIL 2026-08-24, long after it started
// being required by two things. A reader auditing the scope model from this block would
// have concluded those checks were dead and deleted them, with this sentence as the
// justification — which is why a stale comment on a security control is worse than none.
//
// The reserve-early rule it records is still sound and still the reason the scope exists
// separately: splitting a shipped `comm` scope into two later would be a MAJOR under
// COMPATIBILITY.md, while merging two into one is free.
//
// NOTE, and it is a live question rather than a settled one: this is a SECOND optionality
// gate. Even with FilesEnabled on, a token minted `--scopes comm` cannot move a byte.
// Whether "no Ken feature is optional" reaches a per-token scope is a security decision
// and has not been made.
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
func authMiddleware(st *store.Store, limiter ratelimit.Limiter, reg *metrics.Registry, skipTouch bool, next http.Handler) http.Handler {
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
			authFailReq(w, r, reg, "missing bearer token")
			return
		}
		p, err := authenticate(r.Context(), st, tok, ScopeComm)
		if err != nil {
			authFailReq(w, r, reg, "invalid token")
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
		if !skipTouch {
			st.TouchToken(r.Context(), p.TokenID)
		}

		next.ServeHTTP(w, r.WithContext(withPrincipal(r.Context(), p)))
	})
}

// authenticate resolves a bearer to a comm principal from EITHER a `ken_<id>_<secret>` API token
// or an OAuth access token, in both cases requiring requiredScope (`comm` for the MCP transport,
// `comm-file` for the byte-relay HTTP surface).
//
// *** OAUTH IS ACCEPTED HERE AS OF §10 STEP 2, AND THAT IS THE POINT OF THE STEP. ***
//
// This function used to open with `if !strings.HasPrefix(tok, "ken_") { return … "comm requires a
// dedicated ken_ API token" }`, so a human-approved connector could reach the knowledge base and
// was refused by messaging — one identity could not span two surfaces. docs/IDENTITY.md §9.2 names
// what that costs: the binding-voucher chain, its TTL, single-use, endpoint pinning, actor
// matching, hash-at-rest and sweep exist SOLELY so a station key never crosses to this surface as
// a tool argument. Nothing to hand across, nothing to hand it with.
//
// THE HUMAN DECIDES PER SURFACE, AT GRANT TIME. docs/IDENTITY-CONTROLS.md put that condition on
// this exact removal: the withholding "has to be re-expressed as an explicit per-surface capability
// decision at grant time, not inherited from the fact that three files exist." So the capability
// set comes from store.GrantedCapabilities(grant.scope) — shared with /mcp and /station/mcp — and a
// connector reaches messaging only if its grant says ken:comm. Grants approved before this shipped
// carry no ken: scope and are refused here, which is what their humans agreed to.
//
// UNPROBEABILITY IS UNCHANGED. Both paths fail with the same opaque "invalid token" at the
// middleware; nothing here tells a caller which shape it got wrong.
func authenticate(ctx context.Context, st *store.Store, tok, requiredScope string) (*principal, error) {
	// THE DEV BYPASS IS HONOURED HERE TOO, BECAUSE /mcp REQUIRES EVERY CAPABILITY.
	//
	// It lived only in mcpserver, which was correct while /mcp served the knowledge base alone. On
	// the collapsed endpoint all three middlewares run, so a dev token authenticated against the
	// first and was refused by this one — measured as a 401 on the very quickstart README hands a
	// new user. A bypass that works on a third of the chain is a bypass that works nowhere.
	//
	// It stays as unsafe as it always was and no more: static, unrevocable, DEV ONLY, and main.go
	// refuses to start with it set alongside any TLS posture.
	if isDevToken(tok) {
		// A REAL ACTOR, for the reason stationserver gives at its own dev branch: a principal with
		// ActorID 0 satisfies no foreign key, and a mailbox records its owner.
		actorID, err := st.FindOrCreateActor(ctx, "ai", "dev-token")
		if err != nil {
			return nil, err
		}
		return &principal{
			TokenID: "dev",
			ActorID: actorID,
			Scopes:  map[string]bool{ScopeComm: true, ScopeCommFile: true},
		}, nil
	}
	if !strings.HasPrefix(tok, "ken_") {
		return authenticateOAuth(ctx, st, tok, requiredScope)
	}
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
	err := st.R.QueryRowContext(ctx, `
SELECT t.actor_id, t.secret_sha256, t.scopes, t.revoked_at
FROM api_token t
WHERE t.token_id = ?`, tokenID).
		Scan(&actorID, &secretHash, &scopesJSON, &revoked)
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
	return &principal{ActorID: actorID, TokenID: tokenID, Scopes: set}, nil
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}

// resourceMetaFor is set at construction so a 401 here carries the RFC 9728 discovery challenge,
// exactly as /mcp's does. Without it a client is told "unauthorized" and given nowhere to go.
var resourceMetaFor func(*http.Request) string

// SetResourceMetadata wires the discovery challenge for this surface.
func SetResourceMetadata(f func(*http.Request) string) { resourceMetaFor = f }

func authFail(w http.ResponseWriter, reg *metrics.Registry, msg string) {
	authFailReq(w, nil, reg, msg)
}

func authFailReq(w http.ResponseWriter, r *http.Request, reg *metrics.Registry, msg string) {
	if reg != nil {
		reg.AuthFailure("comm")
	}
	if resourceMetaFor != nil && r != nil {
		// Same header /mcp sends, pointing at THIS surface's metadata. It is what turns a bare
		// 401 into something a client can act on, and its absence is invisible to the caller.
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+resourceMetaFor(r)+`"`)
		w.Header().Set("Access-Control-Expose-Headers", "WWW-Authenticate, Mcp-Session-Id")
	}
	httpError(w, http.StatusUnauthorized, msg)
}

func httpError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(msg + "\n"))
}

// authenticateOAuth resolves an opaque OAuth access token to a comm principal.
//
// THE SPACE COMES FROM THE ACTOR, never a hardcoded 1, because that is where the api_token path
// gets it (the JOIN in authenticate above) and two resolutions of one question is how they drift.
// An endpoint registered under an OAuth identity must land in the same space as one registered
// under that actor's api_token, or its mail is filed somewhere its successor cannot poll.
func authenticateOAuth(ctx context.Context, st *store.Store, tok, requiredScope string) (*principal, error) {
	op, err := st.ValidateOAuthAccessToken(ctx, tok)
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, sc := range store.GrantedCapabilities(op.Scope) {
		set[sc] = true
	}
	if !set[requiredScope] {
		return nil, errors.New("token does not carry the " + requiredScope + " scope")
	}
	return &principal{
		ActorID: op.ActorID,
		// The same handle /mcp uses, so rate limiting and logging read the same identity
		// across surfaces. It is deliberately not an api_token id: TouchToken finds no row
		// and does nothing, which is correct — an OAuth grant's use is not an api_token's.
		TokenID: "oauth-" + strconv.FormatInt(op.GrantID, 10),
		Scopes:  set,
	}, nil
}

// isDevToken reports whether this bearer is the KEN_DEV_TOKEN. False whenever the variable is
// unset, which is the ordinary case.
func isDevToken(tok string) bool {
	dev := os.Getenv("KEN_DEV_TOKEN")
	return dev != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(dev)) == 1
}
