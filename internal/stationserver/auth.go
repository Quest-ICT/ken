package stationserver

import (
	"context"
	"errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"log"
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

func principalFrom(ctx context.Context) *principal {
	p, _ := ctx.Value(ctxKey{}).(*principal)
	return p
}

// requireStation resolves the caller's station, refusing a station-less key. Every tool
// except station_request goes through it.
func requireStation(ctx context.Context, req *mcp.CallToolRequest) (*principal, error) {
	p := principalFrom(ctx)
	if p == nil {
		return nil, errors.New("unauthenticated")
	}
	if p.StationID == "" {
		p = p.withWorkspace(workspaceFrom(req))
	}
	if p.StationID == "" {
		// THIS USED TO SAY "call station_me" — WHICH IS A LOOP THAT MINTS A SECOND WORKSPACE.
		// A session that followed it got another id, the same refusal, and one more orphan
		// station each time. The gap is never the mint; it is that this connection does not
		// SAY which workspace it is. So the advice has to be about declaring one, and it has to
		// name both ways of declaring it, because a claude.ai connector cannot send a header.
		return nil, errors.New("this connection has not said which workspace it is. Two ways to fix it, " +
			"and calling station_me again is NOT one — that would mint a SECOND workspace and leave you " +
			"here. (1) If you already have a workspace id, your human adds it to this connector's URL as " +
			"?workspace=<id>, or sets the " + WorkspaceHeader + " header if the client allows custom " +
			"headers. (2) If you have no workspace at all, call station_me ONCE to get an id, then ask " +
			"your human to do (1) with it. Either way you reconnect afterwards and everything works")
	}
	return p, nil
}

// workspaceFrom lifts the declared workspace id off the tool call's headers.
//
// *** READ HERE, NOT IN THE MIDDLEWARE, AND THE REASON IS WORTH THE PARAGRAPH. ***
//
// The first implementation resolved the workspace in authMiddleware and wrote it onto the
// principal. The middleware ran, the header was present, the log line fired, the principal was
// built with the right station — and the tool handler still saw an empty one. Measured, not
// guessed: logging both sides of the seam in one run showed `hdr="glZ..." -> station="glZ..."`
// three times, followed by `handler sees station=""`.
//
// The SDK does not hand a tool handler the HTTP request's context. It hands it the request, with
// `Extra.Header` on it — which is exactly how internal/commserver has always lifted its endpoint
// credential (withEndpointCred). The mechanism already existed in this repository; I built a
// second one that could not work and spent the debugging finding out.
//
// IT AUTHORISES NOTHING (docs/IDENTITY.md §9.2). The value only SELECTS which workspace; the
// credential on the request is what authorises, and requireStation still refuses a principal that
// has neither. A station key bound to its own station never consults this — see authMiddleware.
func workspaceFrom(req *mcp.CallToolRequest) string {
	if req == nil || req.Extra == nil || req.Extra.Header == nil {
		return ""
	}
	return strings.TrimSpace(req.Extra.Header.Get(WorkspaceHeader))
}

// withWorkspace returns a copy of the principal working as the declared workspace, or the
// principal unchanged when the declaration is empty or names nothing live.
//
// A COPY, because the principal in the context is shared across a session and a per-call
// declaration must not leak into the next call.
func (p *principal) withWorkspace(ws string) *principal {
	if ws == "" {
		return p
	}
	c := *p
	c.StationID = ws
	return &c
}

// WorkspaceHeader carries the stable opaque workspace id a folder's MCP entry declares.
//
// NOT A SECRET, and docs/IDENTITY.md §4 says that is the whole point: it replaces a per-folder
// station KEY, which was a bearer credential a human had to generate, deliver and protect — and
// whose only delivery path on 2026-08-18 was a prompt, which the same instruction forbade, so the
// key was burned on arrival. "A credential whose delivery path is a prompt is not protecting
// anything."
//
// §9.2 states the condition this design lives under: "A stable opaque workspace id in an MCP header
// authorises nothing, so there is nothing to keep out of a transcript. IF THAT ID EVER GAINS
// AUTHORITY, THIS CONTROL COMES STRAIGHT BACK." It selects a workspace; the OAuth grant is what
// authorises, and single-user is what makes selection sufficient.
const WorkspaceHeader = "X-Ken-Workspace"

// authMiddleware authenticates a `kens_` bearer key or an OAuth grant, requires the station scope,
// and resolves which workspace the session is working as.
func authMiddleware(st *store.Store, limiter ratelimit.Limiter, reg *metrics.Registry, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// No CORS headers: like /comm/mcp this endpoint has no browser client, so a
		// permissive Access-Control-Allow-Origin would be pure attack surface.
		if r.Method == http.MethodOptions {
			httpError(w, http.StatusMethodNotAllowed, "not allowed")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxStationBody)

		// *** THE WORKSPACE MAY ALSO ARRIVE IN THE URL, BECAUSE A CONNECTOR CANNOT SEND A HEADER. ***
		//
		// Found by the 2026-08-26 acceptance run on a clean Windows VM, and it made the entire
		// station surface unreachable by the onboarding path Ken actually recommends. claude.ai
		// CONNECTORS — added once on the account, propagating to every device, which is the flow
		// that finally worked for Vlad — enforce an allowlist of header names and refuse custom
		// ones outright: "Only approved header names are accepted." An already-created connector
		// has no headers field at all.
		//
		// So a session could call station_me, be given a workspace with zero approvals exactly as
		// §6 promises, and then find every other station tool refusing it one call later. Worse,
		// the refusal advised calling station_me again, which mints ANOTHER workspace — a loop
		// that accumulates orphan stations and never reaches a working state.
		//
		// A URL is the one thing a connector lets a user set freely, and connectors are unique PER
		// URL — so ?workspace=A and ?workspace=B are two connectors, which gives per-workspace
		// identity AND account-level propagation at once. Neither was achievable before.
		//
		// SAFE ONLY BECAUSE THE ID AUTHORISES NOTHING. IDENTITY.md §4: "A stable opaque workspace
		// id in an MCP header authorises nothing, so there is nothing to keep out of a transcript."
		// A URL is a worse place for a secret than a header — it lands in proxy logs, browser
		// history and referrers — so this is acceptable for a NAME TAG and would not be for a
		// credential. §9.2's condition governs it unchanged: IF THAT ID EVER GAINS AUTHORITY, THIS
		// GOES WITH IT.
		//
		// The header still WINS when both are present: an explicit per-request header is the more
		// specific signal, and a stale query string on a saved connector must not override it.
		if r.Header.Get(WorkspaceHeader) == "" {
			if q := strings.TrimSpace(r.URL.Query().Get("workspace")); q != "" {
				r.Header.Set(WorkspaceHeader, q)
			}
		}

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
		sp, err := st.AuthenticateStationKey(r.Context(), tok)
		if err != nil {
			if ap, aerr := st.AuthenticateAPITokenForStation(r.Context(), tok); aerr == nil {
				// A plain `ken_` token carrying the station scope. The door for a client that
				// CANNOT run an OAuth flow — Claude Code inside the desktop app is
				// non-interactive, so every other path here was unreachable for exactly the
				// session that reported the deadlock. It arrives with no station, like an
				// OAuth grant, and station_me turns that into a workspace.
				sp = ap
			} else if op, oerr := st.ValidateOAuthAccessToken(r.Context(), tok); oerr == nil {
				sp = &store.StationPrincipal{
					ActorID: op.ActorID,
					TokenID: "oauth-" + strconv.FormatInt(op.GrantID, 10),
					Scopes:  store.GrantedCapabilities(op.Scope),
					// StationID deliberately empty: see above.
				}
			} else {
				// Unknown, retired and revoked keys are refused identically — extending
				// COMM's unprobeability rule. A caller learns WHY only after its own
				// credential has verified, which informs a holder and tells a prober
				// nothing. An unknown OAuth token joins that same answer.
				authFailReq(w, r, reg, "invalid station key")
				return
			}
		}
		if !hasScope(sp.Scopes, ScopeStation) {
			authFailReq(w, r, reg, "this token does not carry the station scope")
			return
		}

		// *** THE WORKSPACE HEADER — docs/IDENTITY.md §4, step 4 of §10. ***
		//
		//	X-Ken-Workspace: lhqBQKBpTSyJoZyu    <- permanent, meaningless, NOT a secret
		//
		// §4: "The human's OAuth grant proves WHO, and single-user makes that sufficient: within
		// one instance there is one human and one Claude account, so a session declaring a
		// workspace is that human's own session. There is no other tenant to protect against."
		// The id selects; the grant authorises. That is why it can live in a config file, in
		// plain sight, forever — "a name tag cannot leak, cannot be burned, never expires and
		// never rotates", which is the whole point of replacing a per-folder station KEY.
		//
		// A CREDENTIAL THAT CARRIES ITS OWN STATION WINS. A `kens_` key is bound to one station
		// and that binding is a fact about the credential, not a preference; letting a header
		// override it would let a station key read a station it was never issued for, which is
		// authority the header must not have. So the header applies only to a principal that
		// arrives with no station — which today means an OAuth grant.
		//
		// THE RESIDUAL RISK IS CONFUSION, NOT COMPROMISE (§4), and it is mitigated by visibility
		// rather than by credentials: the claim is logged, and the console can show which
		// workspace each session claimed. Vlad ruled on 2026-08-25 that the vault follows the
		// workspace like everything else, rather than growing a second factor that would
		// reintroduce the ceremony this design exists to remove.
		if sp.StationID == "" {
			if ws := strings.TrimSpace(r.Header.Get(WorkspaceHeader)); ws != "" {
				ok, err := st.StationExists(r.Context(), ws)
				if err != nil {
					authFailReq(w, r, reg, "invalid station key")
					return
				}
				if !ok {
					// Same opaque answer as an unknown credential. A workspace id is not a
					// secret, but "does this id exist" must not become a probe either — the
					// unprobeability rule is about what an unproven caller can enumerate.
					authFailReq(w, r, reg, "invalid station key")
					return
				}
				sp.StationID = ws
				log.Printf("STATION: session on token %s claimed workspace %s via %s", sp.TokenID, ws, WorkspaceHeader)
			}
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
