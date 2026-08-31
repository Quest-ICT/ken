package web

import (
	"fmt"
	"log"
	"net/http"

	"github.com/Quest-ICT/ken/internal/comm"
	"github.com/Quest-ICT/ken/internal/store"
)

// The COMM console (docs/COMM.md §9).
//
// COMM's security model rests on the human deciding which sessions may talk: an
// agent cannot create a channel, only redeem a code a human minted. That makes
// this page load-bearing rather than a convenience — without it there is no way to
// exercise the gate, and no brake to pull when something is wrong.
//
// Deliberately NOT here: message contents. Bodies are deleted on ack and are not
// the operator's business; this page shows who is connected and how much is
// pending, which is what an operator needs to spot a runaway channel.

// spaceForSession is the space whose COMM objects the console shows.
//
// Hardcoded to the single space that exists today, matching DESIGN.md §7's stance:
// build the seams, defer the machinery. Every query below is already instance-wide,
// so making this per-user later is a change of one expression rather than a
// rewrite of the page.
const spaceForSession = int64(1)

func (a *app) handleComm(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	a.renderComm(w, r, sess)
}

// The `reveal` type is deleted with secret rotation, and the newCode parameter with the pairing
// code: nothing on this page is shown once any more, because nothing on it is a secret.

// renderComm draws the console.
func (a *app) renderComm(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if a.comm == nil {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()

	endpoints, err := a.comm.ListEndpoints(ctx)
	if err != nil {
		log.Printf("web: comm endpoints: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	stats, err := a.comm.StatsFor(ctx)
	if err != nil {
		log.Printf("web: comm stats: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// The value the live-refresh poller compares against: the page reloads when
	// /comm/count later reports a different number. Rendered here so the marker and
	// the poll answer come from the same source of truth.
	fp, err := a.comm.ConsoleFingerprint(ctx)
	if err != nil {
		log.Printf("web: comm fingerprint: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Per-endpoint channel context, assembled here so the template stays a renderer.
	//
	// This is what makes a confirm dialog actionable. Endpoint LABELS are
	// agent-supplied — a session names itself — so after a few weeks the list is a
	// column of similar strings an operator cannot tell apart, and rotating the wrong
	// row is a self-inflicted outage on a session that was working fine. Channels are
	// the thing a human actually recognises: "Ken dev <-> prod" means something, an
	// opaque id and a self-chosen label do not.
	type epView struct {
		comm.Endpoint
		// Bound decides which WARNING the confirm dialog shows. Revoking an UNBOUND
		// endpoint ends the relationship — nobody else can act on its channels, and the
		// peer really must re-pair. Revoking a BOUND one cuts off a reader: the station
		// keeps its channels and a successor inherits them with a voucher. One sentence
		// cannot be true of both, and the one that shipped was the unbound case.
		Bound bool
		// RebindTargets are the OTHER live station keys of this endpoint's own station —
		// the only legal destinations for its binding. Computed here rather than filtered
		// (RebindTargets and RepointTargets are gone with repoint/rebind.)
	}
	// credential is one credential that can end a live endpoint, with how many it would
	// take. Two lists, because there are two welds and they are different questions.
	//
	// WHY THIS BLOCK EXISTS AT ALL. Nobody could see that eleven live endpoints hung off
	// one comm token until ken-prod-ops queried the database by hand on 2026-08-24; the
	// console listed endpoints and never grouped them by the thing whose retirement kills
	// them. The counts are what a REVOKE would take down; a retire takes down none of them.
	// The bulk re-point shipped in 3.19.0 with a route, a handler, a store primitive
	// and flash strings in three locales — and no form anywhere posted to it, so the verb
	// that exists precisely for the eleven-at-once case could be reached only with curl.
	// TestEveryPostRouteHasAConsoleSurface now fails when that happens again.
	type credential struct {
		TokenID string
		Label   string
		Station string // "" for a comm token; the station name for a station key
		Live    int
		// Endpoints are exactly the rows this credential's bulk verb would move, named so
		// the confirm dialog can list them. `Live` is len(Endpoints) rather than a separate
		// COUNT: a number and a list that can disagree is the thing being fixed here, and
		// two queries answering one question is how they drift.
		Endpoints []comm.EndpointRef
		Targets   []any // where its endpoints may be moved: repointTarget or binderTarget
	}
	//
	// *** THE CHANNEL BLAST-RADIUS BLOCK IS DELETED WITH THE CHANNEL (slice 7, 5.0.0). ***
	//
	// It grouped a credential's open channels by station so the console could warn what retiring
	// that credential would silence. There are no channels to silence: a conversation is the LINK,
	// and the reversible control for one is Suspend on the Stations page.

	eps := make([]epView, 0, len(endpoints))
	for _, ep := range endpoints {
		v := epView{
			Endpoint: ep,
			Bound:    ep.StationID != "",
		}
		eps = append(eps, v)
	}

	// THE ROWS ARE DISCOVERED FROM THE ENDPOINTS, NOT FROM THE PICKER LISTS, and that is the
	// difference between a block that reports the estate and one that reports the estate it
	// approves of. `repointTargets`/`binderTargets` are filtered to credentials a move may
	// legally name; a credential that OWNS or BOUND live endpoints and is not a legal target —
	// a revoked station key above all — would be silently absent from a block whose entire
	// purpose is to make such a credential visible before someone retires it.
	//
	// THE REVOKED-KEY ROW IS THE ONE THAT MATTERS. `ken token revoke` runs in a separate
	// process with no comm.db handle, so it cannot sever the endpoints its key bound: they stay
	// live in this list and are refused at use by store.IsStationKeyRevoked, one call at a
	// time, with nothing on any page saying why. Rendering the row — and offering the move —
	// turns that into a repair the operator can make.
	//
	// THE BLAST RADIUS IS A NAMED LIST, NOT A NUMBER, and it comes from the verb's own
	// predicate rather than from the rendered rows. `ListEndpoints` above is instance-wide
	// while the bulk verbs and the revoke that makes them urgent are NOT, so a population
	// derived from the page would be SHORTER than what the button moves.
	//
	// ken-prod-ops put the objection to the number on 2026-08-25: an operator reads eleven,
	// looks at the page, recognises the ones they know, and clicks — and the ones they did
	// not recognise move too. On the live estate those included `runway-prod-admin` and
	// `rb5009-config`, both in use that week. `Live` is now len(Endpoints), so the count
	// beside the button and the list inside its confirm cannot disagree.
	labels := map[string]string{}
	if rows, err := a.store.ListTokens(ctx); err == nil {
		for _, t := range rows {
			labels[t.TokenID] = t.Label
		}
	}
	var ownerIDs, binderIDs []string
	stationOf := map[string]string{}
	seenOwner, seenBinder := map[string]bool{}, map[string]bool{}
	for _, ep := range endpoints {
		if !seenOwner[ep.Owner.TokenID] {
			seenOwner[ep.Owner.TokenID] = true
			ownerIDs = append(ownerIDs, ep.Owner.TokenID)
		}
		if ep.BoundByStationKeyID != "" && !seenBinder[ep.BoundByStationKeyID] {
			seenBinder[ep.BoundByStationKeyID] = true
			binderIDs = append(binderIDs, ep.BoundByStationKeyID)
			stationOf[ep.BoundByStationKeyID] = ep.StationID
		}
	}
	owners := make([]credential, 0, len(ownerIDs))
	for _, id := range ownerIDs {
		refs, err := a.comm.EndpointsOwnedBy(ctx, id)
		if err != nil {
			log.Printf("web: endpoints owned by token %s: %v", id, err)
			continue
		}
		n := len(refs)
		c := credential{TokenID: id, Label: labels[id], Live: n, Endpoints: refs}
		owners = append(owners, c)
	}
	// THE BINDERS SECTION IS GONE. It listed the station KEYS that had bound endpoints and what
	// revoking each would sever — one of the two "credentials these endpoints depend on" lists.
	// Station keys are retired and nothing binds, so the list would always be empty.
	binders := []credential{}

	a.render(w, r, sess, "comm", map[string]any{
		"Endpoints": eps, "Stats": stats,
		"Owners": owners, "Binders": binders,
		"CommURL": a.publicCommURL(r), "Fingerprint": fp,
	})
}

// handleCommCount answers the Comm console's live-refresh poller with the current
// console fingerprint as JSON. Read-only and cheap (a handful of COUNTs behind one
// query); behind requireAuth like the page, so it is not an unauthenticated info
// leak. The JSON key is "count" so the one generic poller in app.js serves both
// this page and Proposals — the number is opaque here (a change detector, not a
// meaningful tally), but the poller only ever compares it for equality.
func (a *app) handleCommCount(w http.ResponseWriter, r *http.Request, _ *store.Session) {
	if a.comm == nil {
		http.NotFound(w, r)
		return
	}
	n, err := a.comm.ConsoleFingerprint(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprintf(w, `{"count":%d}`, n)
}

// handleCommPair IS DELETED, with the pairing code it minted. It was "the human act the whole
// design depends on" — the operator handed a code to exactly the two sessions they intended to
// connect, and no agent could produce one. Vlad removed that gate in the 2026-08-27 wave; a
// relationship is created by the first message and the console's control is Suspend, which unlike
// a code the human can also undo.

// *** handleCommRevokeChannel IS DELETED WITH THE CHANNEL (slice 7, 5.0.0). ***
//
// It was the brake on one channel: close it permanently, from the console. There is nothing left
// to brake — a conversation between two stations is the LINK, and the control for a link is
// Suspend on the Stations page, which is reversible where this was not.

// handleCommRotateEndpoint IS DELETED. It was the console half of secret rotation — the one
// control only a human could reach, existing because a lost secret was otherwise terminal. A
// station comes with a mailbox and holds no secret at all.

// *** handleCommReassignEndpoint IS DELETED. THE DOCS ALREADY SAID SO; THE CODE DID NOT. ***
//
// It pointed a MAILBOX at a conversation, back when a mailbox belonged to a session and a session
// that died took its unread mail out of reach. COMM.md, UPGRADING.md and CHANGELOG all state this
// form was removed in this wave — and the route, handler, store function and template form were all
// still live, mutating endpoint.session_key, a column whose only remaining reader was the reassign
// path itself. Documentation asserting a deletion that never happened is worse than no
// documentation: it is a claim an operator plans around.
//
// A mailbox belongs to a STATION now, so recovery is reassigning the STATION at /stations, which
// moves its mail together with its notebook, tasks, locker and vault — one form instead of two,
// and no way for the two to disagree about who holds what.
//
// The one property worth carrying forward, because any future recovery control needs it: the owner
// token was deliberately NOT touched by a reassignment. Repointing an estate boundary as a side
// effect of a convenience is how a mailbox quietly changes accounts.

// handleCommRevokeEndpoint revokes one session's endpoint, denying it further use.
// *** handleCommRevokeEndpoint IS DELETED, BECAUSE IT COULD NOT DO WHAT ITS BUTTON PROMISED. ***
//
// RevokeEndpoint only stamps revoked_at. Nothing in auth() consults that column any more, and
// MailboxFor recreates the mailbox on the station's next call — so an operator clicking Revoke to
// cut off a session got a success flash and no effect whatsoever. Its confirmation dialog also
// promised a binding voucher and a ROTATE control, both deleted in this wave.
//
// A SECURITY-SHAPED BUTTON THAT LIES IS WORSE THAN NO BUTTON. An operator who believes they have
// cut off a session stops looking for another way to do it.
//
// The controls that genuinely stop a session are on /stations (archive the station) and /tokens
// (withdraw the OAuth authorization — the master switch, and the only credential left).

// publicCommURL is the externally-reachable MCP endpoint for a copy-paste registration example.
//
// IT IS NOW THE SAME URL AS EVERYTHING ELSE. There is one machine surface, /mcp, carrying every
// tool; /comm/mcp is deleted. This used to derive a second endpoint by swapping the suffix, and a
// console that still offered that URL would hand an operator a connector that 404s.
func (a *app) publicCommURL(r *http.Request) string { return a.publicMCPURL(r) }

// commEnabled reports whether the console should appear in the nav.
func (a *app) commEnabled() bool { return a.comm != nil }
