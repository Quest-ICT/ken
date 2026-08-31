package stationserver

import (
	"context"
	"crypto/subtle"
	"errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"os"

	"github.com/Quest-ICT/ken/internal/station"
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
	// StationID is empty for a station-less key. Such a key may call exactly one tool,
	// station_request, which is how a session with no station asks for one (S3).
	StationID string
	Scopes    map[string]bool
}

type ctxKey struct{}

// storeCtxKey carries the durable store to requireStation, which needs it for station.Resolve.
//
// It is set in addTool for EVERY station tool, unconditionally — deliberately not in withCaller,
// which early-returns on an in-process transport, a missing bearer and a stale key. A store that
// is present only on the paths withCaller completes is a resolver that works in production and
// not in a test harness, which is the shape of defect this package has already paid for twice.
type storeCtxKey struct{}

func withStore(ctx context.Context, st *store.Store) context.Context {
	return context.WithValue(ctx, storeCtxKey{}, st)
}

func storeFrom(ctx context.Context) *store.Store {
	st, _ := ctx.Value(storeCtxKey{}).(*store.Store)
	return st
}

func principalFrom(ctx context.Context) *principal {
	p, _ := ctx.Value(ctxKey{}).(*principal)
	return p
}

// requireStation resolves the caller's station, refusing a station-less key. Every tool
// except station_request goes through it.
//
// *** IT TAKES THE CONVERSATION'S OWN KEY, AND THAT IS THE WHOLE POINT. ***
//
// This used to read station.Bound(req) alone — the map keyed on the MCP session id, written by
// station_me. station_me, comm_poll, comm_send and comm_directory accepted a session_key and
// resolved fine; the other nineteen station tools did not declare the field at all, so
// `additionalProperties:false` REJECTED it, and they had nothing to fall back on but that map.
//
// The map is per CONNECTION, and ken-prod-ops measured a client that re-initialises between
// messages: station_me succeeded, and a station_note_write seconds later in the same conversation
// with the same key was refused with "this connection has not said which station it is". That put
// the notebook, tasks, locker and vault behind whether an MCP session happened to persist.
//
// station.Resolve already preferred the key and fell back to the binding — the station surface
// simply never handed it one. It also asks the liveness and archived questions this function did
// not, so an archived station now gets the refusal that names its remedy instead of a generic one.
func requireStation(ctx context.Context, req *mcp.CallToolRequest, sessionKey string) (*principal, error) {
	p := principalFrom(ctx)
	if p == nil {
		return nil, errors.New("unauthenticated")
	}
	if p.StationID == "" {
		sid, err := station.Resolve(ctx, storeFrom(ctx), req, p.ActorID, sessionKey)
		if err != nil {
			return nil, err
		}
		p = p.withStation(sid)
	}
	if p.StationID == "" {
		// THIS USED TO SAY "call station_me" — WHICH IS A LOOP THAT MINTS A SECOND STATION.
		// A session that followed it got another id, the same refusal, and one more orphan
		// each time. The gap is never the mint; it is that this connection has not said which
		// station it is. ONE WORDING, shared with comm, so the two surfaces cannot drift into
		// answering the same miss differently.
		return nil, station.ErrNoStation
	}
	return p, nil
}

// *** THE BINDING MAP, THE HEADER READER AND THE `?station=` PROMOTION HAVE ALL MOVED OR GONE. ***
//
// The map that remembered which station a connection had claimed now lives in internal/station,
// shared with comm rather than reachable from it by accident: the promotion used to live in THIS
// middleware, and comm handlers saw its effect only because allserver wired station's middleware
// inside comm's — a wiring order nothing asserted. See station.Resolve for what replaced it.
//
// ONE LESSON FROM THE DELETED READER IS KEPT, because it cost an hour and would cost it again.
// The SDK does NOT hand a tool handler the HTTP request's context; it hands it the request, with
// Extra.Header on it. Resolving something in a middleware and writing it onto the principal looks
// right, runs, and logs correctly — `hdr="glZ..." -> station="glZ..."`, three times — while the
// handler still sees an empty value. The mechanism that does work already existed in
// internal/commserver and was not looked at first.

// withStation returns a copy of the principal working as the declared station, or the
// principal unchanged when the declaration is empty or names nothing live.
//
// A COPY, because the principal in the context is shared across a session and a per-call
// declaration must not leak into the next call.
func (p *principal) withStation(stationID string) *principal {
	if stationID == "" {
		return p
	}
	c := *p
	c.StationID = stationID
	return &c
}

// *** X-Ken-Workspace AND ?station= ARE DELETED. ***
//
// Both carried identity in the TRANSPORT, and a claude.ai connector is added once per account — so
// whatever they carried had exactly one value for every machine and every conversation, forever.
// The header was worse: the client refuses custom header names, so it could not be set at all.
//
// MEASURED BEFORE DELETING, because "probably unused" is not a reason to break something. The
// server logged every successful header claim; across the entire life of the deployment there were
// 91, all one test token claiming one test station inside a single 39-minute window, both retired
// since. Vlad's rule for reading that: if it was only ever used to run tests, it was never used.
//
// What replaces them is session_key — declared in the CALL, per conversation, and proven across a
// real client restart. See station.Resolve.

// principalFromToken is the ONE place a bearer becomes a station principal.
//
// *** IT EXISTS BECAUSE THE TWO PLACES THAT DID THIS HAD ALREADY DRIFTED. *** The middleware
// accepted three credential forms; withCaller — which re-derives the principal PER CALL so a
// per-call bearer wins over the connection's — accepted only `kens_` station keys. Retiring
// station keys would have left withCaller accepting nothing at all, silently: it returns the
// unmodified context on failure, so every per-call token would simply have stopped being honoured
// and the connection-time principal would have won again. That is the defect withCaller was
// written to fix, re-created by a deletion.
//
// STATION KEYS ARE GONE FROM THE LADDER. `kens_` credentials are retired: /mcp requires an OAuth
// grant carrying every capability, so a station key reached nothing even before this.
func principalFromToken(ctx context.Context, st *store.Store, tok string) (*store.StationPrincipal, error) {
	// THE DEV BYPASS, FOR THE SAME REASON IT NOW EXISTS ON THE COMM MIDDLEWARE: /mcp chains all
	// three, so a bypass honoured by one of them is refused by the next and works nowhere. It was
	// correct while /mcp served the knowledge base alone. Still static, unrevocable, DEV ONLY, and
	// still refused at startup alongside any TLS posture.
	//
	// TokenID "dev" is deliberately a constant: it is what a station's session_key will be recorded
	// against, so a dev-token session behaves like one identity rather than a new one per boot.
	if dev := os.Getenv("KEN_DEV_TOKEN"); dev != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(dev)) == 1 {
		// IT NEEDS A REAL ACTOR, and shipping without one made the bypass authenticate and then
		// fail on the first call the server's own instructions mandate. The principal carried
		// ActorID 0; station(created_by_actor_id) is NOT NULL REFERENCES actor(id) and SQLite
		// rowids start at 1, so station_me died with a raw "FOREIGN KEY constraint failed" — and
		// with it every station_* and comm_* call, since both resolve a station first.
		//
		// A 401 would at least have been legible. This was worse: the fix that made the bypass
		// authenticate turned a clear refusal into a driver error, on the path README tells a new
		// user to walk first.
		actorID, err := st.FindOrCreateActor(ctx, "ai", "dev-token")
		if err != nil {
			return nil, err
		}
		return &store.StationPrincipal{
			TokenID: "dev",
			ActorID: actorID,
			Scopes:  []string{"station", "station-locker"},
		}, nil
	}
	if ap, err := st.AuthenticateAPITokenForStation(ctx, tok); err == nil {
		return ap, nil
	}
	op, err := st.ValidateOAuthAccessToken(ctx, tok)
	if err != nil {
		return nil, err
	}
	return &store.StationPrincipal{
		ActorID: op.ActorID,
		TokenID: "oauth-" + strconv.FormatInt(op.GrantID, 10),
		Scopes:  store.GrantedCapabilities(op.Scope),
		// StationID deliberately empty: a grant arrives with no station and station_me mints one.
	}, nil
}

// authMiddleware authenticates an OAuth grant or a full-capability api_token, requires the station
// scope, and resolves which station the session is working as.
//
// It said "a `kens_` bearer key or an OAuth grant" until 4.0.0. Station keys are retired and no code
// path parses that prefix any more; the station comes from the conversation's session_key.
func authMiddleware(st *store.Store, limiter ratelimit.Limiter, reg *metrics.Registry, skipTouch bool, next http.Handler) http.Handler {
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
			authFailReq(w, r, reg, "missing bearer token")
			return
		}
		// *** EITHER A `kens_` STATION KEY OR AN OAUTH ACCESS TOKEN, AS OF §10 STEP 2. ***
		//
		// This endpoint used to accept `kens_` keys and nothing else, which is half of why
		// one identity could not span the three surfaces. The other half was /comm/mcp
		// refusing anything but `ken_`; both are gone.
		//
		// AN OAUTH PRINCIPAL ARRIVES WITH NO STATION, AND THAT IS NOT A DEGRADED STATE —
		// it is the state `station_request` exists to serve, described in its own tool text
		// as "the only tool a key with no station may call". Until now that sentence was
		// true and useless: a key with no station could call it, but a session with no KEY
		// could not, and that is every session being onboarded. An OAuth session can.
		//
		// What it CANNOT do is anything station-scoped, because requireStation refuses a
		// principal with no station id — so the notebook, tasks, locker and vault stay shut
		// until a human approves a station. That refusal is the same one a station-less
		// `kens_` key already met; nothing about it is new or weaker.
		sp, err := principalFromToken(r.Context(), st, tok)
		if err != nil {
			// Unknown, retired and revoked credentials are refused identically — COMM's
			// unprobeability rule. A caller learns WHY only after its own credential has
			// verified, which informs a holder and tells a prober nothing.
			authFailReq(w, r, reg, "invalid credential")
			return
		}
		if !hasScope(sp.Scopes, ScopeStation) {
			authFailReq(w, r, reg, "this token does not carry the station scope")
			return
		}

		// *** THE STATION HEADER IS DELETED, AND SO IS EVERY BRANCH THAT READ IT. ***
		//
		// This block documented `X-Ken-Workspace` as the live station-selection mechanism, with the
		// reasoning for why an id may sit in a config file in plain sight. The header went in 4.0.0:
		// a claude.ai connector cannot set custom header names, so the population that most needed
		// it could never use it, and the census before deleting found one test token and a single
		// 39-minute window of use. The running binary ignores the header entirely.
		//
		// WHAT SURVIVES IS THE PRINCIPLE, and it now governs `session_key` instead — §4: "The
		// human's OAuth grant proves WHO, and single-user makes that sufficient… There is no other
		// tenant to protect against." The id SELECTS; the credential AUTHORISES. That is why it can
		// travel as an ordinary tool argument: a name tag cannot leak, be burned, expire or rotate.
		//
		// THE RESIDUAL RISK IS CONFUSION, NOT COMPROMISE (§4), mitigated by visibility rather than
		// by credentials: the claim is logged and the console shows which station each conversation
		// holds. Vlad ruled on 2026-08-25 that the vault follows the station like everything else,
		// rather than growing a second factor that would reintroduce the ceremony this design
		// exists to remove.
		if limiter != nil {
			if ok, retry := limiter.Allow(sp.TokenID); !ok {
				w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
				httpError(w, http.StatusTooManyRequests, "rate limited")
				return
			}
		}

		// Record that this credential was used. Nothing did until 3.x: TouchToken was called only
		// from the knowledge-base authenticator, so `last_used_at` was never written for a
		// station-scoped credential and the console rendered a permanently blank column. An
		// operator reads a blank as "unused" rather than "unmeasured", which is the worse of the
		// two readings.
		//
		// It also means a stolen credential could read an entire notebook, task list and briefing
		// with no trace. This is a coarse signal — throttled, no per-read record — but the
		// difference between "no timestamp" and "used four minutes ago" is the difference between
		// an unanswerable incident and a scoped one.
		//
		// SKIPPED WHEN ANOTHER MIDDLEWARE IN THE CHAIN IS RECORDING IT: /mcp runs all three, and
		// three unthrottled writes per request on a single-writer database defeats the throttle.
		if !skipTouch {
			st.TouchToken(r.Context(), sp.TokenID)
		}

		scopes := map[string]bool{}
		for _, s := range sp.Scopes {
			scopes[s] = true
		}
		p := &principal{
			ActorID: sp.ActorID, TokenID: sp.TokenID, StationID: sp.StationID, Scopes: scopes,
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

// resourceMetaFor is set at construction so a 401 here carries the RFC 9728 discovery challenge,
// exactly as /mcp's does. See internal/oauth.ResourceMetadataURLFor for why the server has to
// advertise this rather than the client investigate: the absent header is invisible to the caller.
var resourceMetaFor func(*http.Request) string

// SetResourceMetadata wires the discovery challenge for this surface.
func SetResourceMetadata(f func(*http.Request) string) { resourceMetaFor = f }

func authFailReq(w http.ResponseWriter, r *http.Request, reg *metrics.Registry, msg string) {
	if resourceMetaFor != nil && r != nil {
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+resourceMetaFor(r)+`"`)
		w.Header().Set("Access-Control-Expose-Headers", "WWW-Authenticate, Mcp-Session-Id")
	}
	authFail(w, reg, msg)
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
