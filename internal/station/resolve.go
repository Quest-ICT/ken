// Package station answers ONE question for every machine surface: which station is calling?
//
// *** WHY IT IS ITS OWN PACKAGE. ***
//
// The answer used to live in stationserver, unexported, and comm reached it by accident: the
// `?station=` promotion sat in the STATION middleware, and comm handlers saw its effect only
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

// ErrNoStation is the SINGLE refusal for "this caller may not act as a station", whatever the
// reason — nothing declared, not this human's station, or archived.
//
// ONE WORDING FOR ALL THREE, extending COMM's unprobeability rule: nothing here tells a caller
// which shape it got wrong. The code this replaces had two different answers — a bare "denied"
// on one path and a descriptive sentence on another — which is a probe asymmetry a persistent
// caller can walk.
// ErrStationArchived is the one distinguishable refusal — see Resolve for why it is safe to
// distinguish and why it has to be.
var ErrStationArchived = errors.New("that station is ARCHIVED, so it does not send or receive. " +
	"Nothing is lost and nothing needs re-pairing: ask your human to UNARCHIVE it in Ken's console " +
	"at /stations, and the same session carries on with the same mail")

var ErrNoStation = errors.New("this connection has not said which station it is, or that station is not yours or is archived. " +
	"Call station_me ONCE with session_key — a stable id for THIS conversation — and send the SAME session_key on every call that needs it. " +
	"Calling station_me repeatedly with NO session_key is not the fix: that mints a new station each time and strands the previous one")

// ErrAmbiguousConnection is returned when a connection has been claimed by more than one station,
// which proves it carries more than one CONVERSATION and can no longer speak for any of them.
//
// *** THIS EXISTS BECAUSE THE BINDING SILENTLY MISROUTED WRITES ON PRODUCTION. ***
//
// The map below is keyed on the MCP session id, which is per CONNECTION. Claude Desktop holds ONE
// connection for the whole application, so every conversation in it shares one key and one row:
//
//	conversation A: station_me{session_key:A}  -> map[conn] = stationA
//	conversation B: station_me{session_key:B}  -> map[conn] = stationB   (OVERWRITES)
//	conversation A: station_note_write{}       -> Bound(conn) = stationB -> A's note lands on B
//
// ken-prod-ops read the rows on 2026-08-31: one session's notes in another session's notebook, a
// third party's task and handoff filed under the first, and a `mode=replace` stopped one call short
// of destroying a live handoff. It surfaced the hour the estate went from 4 stations to 13, because
// that was the first time this deployment had two conversations on one connection at once — the map
// had been accidentally correct for as long as every session was alone.
//
// A DISTINGUISHABLE, CallerSafe REFUSAL RATHER THAN THE GENERIC ONE. The caller can fix this
// completely and immediately by sending session_key, so the error says exactly that. Rendering it
// as "you have not said which station you are" would send them to station_me — which they have
// already called, successfully, and which will not help.
var ErrAmbiguousConnection = errors.New("this CONNECTION is shared by more than one conversation, " +
	"so it cannot say which station you are — several have claimed it. Send session_key on THIS call " +
	"and every other station call: it names your station directly and cannot be confused with another " +
	"conversation's. Calling station_me again does not fix it; the key is what fixes it")

// binding is what one connection claimed, and whether that claim is still trustworthy.
type binding struct {
	stationID string
	// ambiguous latches TRUE and never clears. Once two stations have claimed one connection, no
	// later call on it can be attributed to either — including calls from the conversation that
	// bound first, which is precisely the one that would otherwise keep writing to somebody else's
	// station believing it was fine.
	ambiguous bool
}

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
	prev, loaded := sessionBindings.LoadOrStore(id, binding{stationID: stationID})
	if !loaded {
		if sessionBindingCount.Add(1) > maxSessionBindings {
			sessionBindings.Range(func(k, _ any) bool { sessionBindings.Delete(k); return true })
			sessionBindingCount.Store(0)
		}
		return
	}
	b, _ := prev.(binding)
	// A SECOND, DIFFERENT STATION ON ONE CONNECTION IS PROOF, not a heuristic: one conversation
	// staffs one station, so two stations mean two conversations sharing a transport.
	if b.ambiguous || b.stationID != stationID {
		sessionBindings.Store(id, binding{stationID: stationID, ambiguous: true})
		return
	}
	sessionBindings.Store(id, binding{stationID: stationID})
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
		if b, ok := v.(binding); ok && !b.ambiguous {
			return b.stationID
		}
	}
	return ""
}

// Ambiguous reports whether this connection has been claimed by more than one station, so a caller
// can raise the refusal that names the remedy instead of the generic one.
func Ambiguous(req *mcp.CallToolRequest) bool {
	if req == nil || req.Session == nil {
		return false
	}
	id := req.Session.ID()
	if id == "" {
		return false
	}
	v, ok := sessionBindings.Load(id)
	if !ok {
		return false
	}
	b, _ := v.(binding)
	return b.ambiguous
}

// Resolve is the ONLY way any surface learns which station is calling.
//
// TWO SOURCES, IN THIS ORDER, AND THERE IS NO THIRD:
//
//  1. THE CONVERSATION'S OWN KEY, when the tool takes one. Most specific: it names THIS
//     conversation rather than the connector every conversation on the account shares.
//  2. WHAT station_me BOUND for this MCP connection. Costs the session nothing on later calls.
//
// *** THE HEADER IS GONE, AND SO IS `?station=`. *** Both were transport-carried identity, and a
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
func Resolve(ctx context.Context, st *store.Store, req *mcp.CallToolRequest, _ int64, sessionKey string) (string, error) {
	var sid string
	switch {
	case strings.TrimSpace(sessionKey) != "":
		// RESOLVED WITHOUT FILTERING ON STATE, so archive stays visible to the check below.
		// StationBySessionKey excludes archived rows — correct for its own callers, wrong here:
		// it turns "your station is archived" into "you never said which station", and those
		// have completely different remedies.
		id, err := st.StationIDBySessionKeyAnyState(ctx, strings.TrimSpace(sessionKey))
		if err != nil {
			return "", ErrNoStation
		}
		sid = id
	default:
		// THE KEY IS ALWAYS PREFERRED, and this branch is only reached when the caller sent none.
		// A shared connection cannot answer for any conversation on it, so say so rather than
		// resolving to whichever station claimed it most recently.
		if Ambiguous(req) {
			return "", ErrAmbiguousConnection
		}
		sid = Bound(req)
	}
	if sid == "" {
		return "", ErrNoStation
	}
	// ONE LIVENESS QUESTION, ASKED ONCE — see StationIsLive for why it does not compare actors.
	ok, err := st.StationIsLive(ctx, sid)
	if err != nil {
		return "", ErrNoStation
	}
	if !ok {
		// *** ARCHIVED IS ANSWERED SEPARATELY, AND THAT IS SAFE HERE. ***
		//
		// Everywhere else in Ken a distinguishable refusal is a probe, and the rule is that a
		// caller learns WHY only after its own credential verifies. Both conditions hold: the
		// caller has already proven an OAuth grant at the transport, and under one human, one
		// account there is no other tenant whose station state could leak. It is the same
		// reasoning that retires StationByNameVisibleTo's enumeration guard.
		//
		// It earns its place because the remedy is real and the session cannot guess it: an
		// archived station is REVERSIBLE, and a session told only "you have not said which
		// station" would re-declare forever against a station that will never answer.
		if archived, aerr := st.StationIsArchived(ctx, sid); aerr == nil && archived {
			return "", ErrStationArchived
		}
		return "", ErrNoStation
	}
	return sid, nil
}
