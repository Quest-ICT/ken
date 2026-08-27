// Package station answers ONE question for every machine surface: which station is calling?
//
// *** WHY IT IS ITS OWN PACKAGE. ***
//
// The answer used to live in stationserver, unexported, and comm reached it by accident: the
// `?workspace=` promotion sat in the STATION middleware, and comm handlers saw its effect only
// because allserver happened to wire station's middleware inside comm's. A wiring order nothing
// asserted, holding up a load-bearing fact.
//
// Copying the resolver into commserver would have been worse. The connection binding is a map
// keyed on the MCP session id and WRITTEN BY station_me; a second copy in another package is a
// station that answers station_me and does not answer comm_poll. So it moves rather than
// duplicating, and both surfaces read the one map.
//
// It depends on internal/store and nothing else of Ken's, which keeps S7's pointer rule intact:
// the expendable side may point at the durable side, never the reverse.
package station

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Quest-ICT/ken/internal/store"
)

// ErrNoWorkspace is the SINGLE refusal for "this caller may not act as a station", whatever the
// reason — nothing declared, not this human's station, or archived.
//
// ONE WORDING FOR ALL THREE, extending COMM's unprobeability rule: nothing here tells a caller
// which shape it got wrong. The code this replaces had two different answers — a bare "denied"
// on one path and a descriptive sentence on another — which is a probe asymmetry a persistent
// caller can walk.
var ErrNoWorkspace = errors.New("this connection has not said which station it is, or that station is not yours or is archived. " +
	"Call station_me ONCE with session_key — a stable id for THIS conversation — and send the SAME session_key on every call that needs it. " +
	"Calling station_me repeatedly with NO session_key is not the fix: that mints a new station each time and strands the previous one")

// sessionBindings maps an MCP session id to the station that connection is working as.
//
// BOUNDED, because a map keyed on connections that never announce their end would otherwise grow
// for the life of the process. At the cap it is cleared wholesale rather than evicted cleverly:
// the recovery is that each session re-declares on its next station_me, which every session does
// at the start of every conversation anyway. A silently-full cache that stopped accepting new
// bindings would be far worse than one that occasionally makes sessions say who they are again.
var sessionBindings sync.Map

const maxSessionBindings = 4096

var sessionBindingCount atomic.Int64

// Bind records the station this connection is working as. Called by station_me.
func Bind(req *mcp.CallToolRequest, stationID string) {
	if req == nil || req.Session == nil || stationID == "" {
		return
	}
	id := req.Session.ID()
	if id == "" {
		// A transport with no session id (stateless mode). Nothing to key on, so the
		// conversation declares its key on every call instead — correct, just chattier.
		return
	}
	if _, loaded := sessionBindings.LoadOrStore(id, stationID); !loaded {
		if sessionBindingCount.Add(1) > maxSessionBindings {
			sessionBindings.Range(func(k, _ any) bool { sessionBindings.Delete(k); return true })
			sessionBindingCount.Store(0)
		}
		return
	}
	sessionBindings.Store(id, stationID)
}

// Bound returns the station this connection claimed, or "".
func Bound(req *mcp.CallToolRequest) string {
	if req == nil || req.Session == nil {
		return ""
	}
	id := req.Session.ID()
	if id == "" {
		return ""
	}
	if v, ok := sessionBindings.Load(id); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// Resolve is the ONLY way any surface learns which station is calling.
//
// TWO SOURCES, IN THIS ORDER, AND THERE IS NO THIRD:
//
//  1. THE CONVERSATION'S OWN KEY, when the tool takes one. Most specific: it names THIS
//     conversation rather than the connector every conversation on the account shares.
//  2. WHAT station_me BOUND for this MCP connection. Costs the session nothing on later calls.
//
// *** THE HEADER IS GONE, AND SO IS `?workspace=`. *** Both were transport-carried identity, and a
// claude.ai connector is added once per account — so any value they carried had exactly one value
// for every machine and every conversation, forever. The header was worse still: the client
// refuses custom header names, so it could not be set at all. Measured before deleting: 91 uses in
// the deployment's entire life, all one test token claiming one test station inside 39 minutes,
// both retired since. Never used in anger.
//
// IT FAILS CLOSED, where the ownership check it replaces failed open. The old code justified
// failing open with "ken.db being briefly unreadable should not cut messaging for every station at
// once" — a justification that was already void, because the OAuth token is validated against
// ken.db at the transport and refuses every call before a handler runs. The fail-open could not
// fire; it only read as protection.
func Resolve(ctx context.Context, st *store.Store, req *mcp.CallToolRequest, actorID int64, sessionKey string) (string, error) {
	var sid string
	switch {
	case strings.TrimSpace(sessionKey) != "":
		s, err := st.StationBySessionKey(ctx, strings.TrimSpace(sessionKey))
		if err != nil {
			return "", ErrNoWorkspace
		}
		sid = s.StationID
	default:
		sid = Bound(req)
	}
	if sid == "" {
		return "", ErrNoWorkspace
	}
	// ONE OWNERSHIP QUESTION, ASKED ONCE. This replaces three phrasings of the same thing that had
	// drifted apart: an owner-token comparison that was vacuous for every OAuth caller (one grant
	// means one token id, so it compared a value to itself), an archived check reading `state`, and
	// an existence check reading `archived_at`.
	ok, err := st.StationForActor(ctx, sid, actorID)
	if err != nil || !ok {
		return "", ErrNoWorkspace
	}
	return sid, nil
}
