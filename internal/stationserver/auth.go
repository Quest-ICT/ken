package stationserver

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Quest-ICT/ken/internal/metrics"
	"github.com/Quest-ICT/ken/internal/ratelimit"
	"github.com/Quest-ICT/ken/internal/store"
)

// Authentication for the /station endpoint (docs/STATIONS.md S5).
//
// Built as a copy of internal/commserver/auth.go rather than a shared abstraction, and
// deliberately so: each surface accepts EXACTLY ONE token shape, and one credential path
// per endpoint is easier to audit than a parameterised one. This endpoint accepts
// `kens_` keys and nothing else.
//
// The key is a HEADER credential and never a tool argument. Tool arguments are model
// output: they land in transcripts, harness logs and scrollback, and — via the notebook
// — potentially in a backup. A long-lived credential travels the way the Ken token
// already travels, read by the transport and never spoken by the model.

// ScopeStation is required by every tool here. ScopeStationLocker is RESERVED and gates
// the locker tools, on the reasoning that reserved `comm-file` beside `comm`: splitting
// a shipped scope is a MAJOR, merging two is free.
const (
	ScopeStation       = "station"
	ScopeStationLocker = "station-locker"
)

// maxStationBody caps a request body. The locker is the only tool here that carries
// bytes, and its per-blob cap is far smaller; anything approaching this is a design
// error long before it is a transport one.
const maxStationBody = 1 << 20 // 1 MiB

// principal is the authenticated caller. Unlike COMM — where the token identifies a
// MACHINE and the endpoint pair identifies the session — a station key identifies the
// STATION directly, because that is the durable thing it was minted for.
type principal struct {
	ActorID int64
	TokenID string
	SpaceID int64
	// StationID is empty for a station-less key. Such a key may call exactly one tool,
	// station_request, which is how a session with no station asks for one (S3).
	StationID string
	Scopes    map[string]bool
}

type ctxKey struct{}

func principalFrom(ctx context.Context) *principal {
	p, _ := ctx.Value(ctxKey{}).(*principal)
	return p
}

// requireStation resolves the caller's station, refusing a station-less key. Every tool
// except station_request goes through it.
func requireStation(ctx context.Context) (*principal, error) {
	p := principalFrom(ctx)
	if p == nil {
		return nil, errors.New("unauthenticated")
	}
	if p.StationID == "" {
		return nil, errors.New("this key is not bound to a station yet — call station_request to ask your human to create one, " +
			"then use the key they give you for that station")
	}
	return p, nil
}

// authMiddleware authenticates a `kens_` bearer key and requires the station scope.
func authMiddleware(st *store.Store, limiter ratelimit.Limiter, reg *metrics.Registry, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// No CORS headers: like /comm/mcp this endpoint has no browser client, so a
		// permissive Access-Control-Allow-Origin would be pure attack surface.
		if r.Method == http.MethodOptions {
			httpError(w, http.StatusMethodNotAllowed, "not allowed")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxStationBody)

		tok := bearerToken(r)
		if tok == "" {
			authFail(w, reg, "missing bearer token")
			return
		}
		sp, err := st.AuthenticateStationKey(r.Context(), tok)
		if err != nil {
			// Unknown, retired and revoked keys are refused identically — extending
			// COMM's unprobeability rule. A caller learns WHY only after its own
			// credential has verified, which informs a holder and tells a prober nothing.
			authFail(w, reg, "invalid station key")
			return
		}
		if !hasScope(sp.Scopes, ScopeStation) {
			authFail(w, reg, "this token does not carry the station scope")
			return
		}
		if limiter != nil {
			if ok, retry := limiter.Allow(sp.TokenID); !ok {
				w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
				httpError(w, http.StatusTooManyRequests, "rate limited")
				return
			}
		}

		// Record that this key was used. Until now nothing did: TouchToken was called
		// only from the knowledge-base authenticator, so `last_used_at` was NEVER
		// written for a station key — and the console rendered a last-used column that
		// was permanently blank. An operator reads a blank as "unused" rather than
		// "unmeasured", which is the worse of the two readings, and it made "retire the
		// key nothing is using" unanswerable even in principle.
		//
		// It also means a stolen station key could read an entire notebook, task list
		// and briefing with no trace at all. This is a coarse signal — throttled to
		// about once a minute, no per-read record — but the difference between "no
		// timestamp" and "used four minutes ago" is the difference between an
		// unanswerable incident and a scoped one.
		st.TouchToken(r.Context(), sp.TokenID)

		scopes := map[string]bool{}
		for _, s := range sp.Scopes {
			scopes[s] = true
		}
		p := &principal{
			ActorID: sp.ActorID, TokenID: sp.TokenID, SpaceID: 1,
			StationID: sp.StationID, Scopes: scopes,
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, p)))
	})
}

func hasScope(scopes []string, want string) bool {
	for _, s := range scopes {
		if s == want {
			return true
		}
	}
	return false
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
		reg.AuthFailure("station")
	}
	httpError(w, http.StatusUnauthorized, msg)
}

func httpError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(msg + "\n"))
}
