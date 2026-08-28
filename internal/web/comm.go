package web

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"

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
	a.renderComm(w, r, sess, "")
}

// The `reveal` type is deleted with secret rotation: nothing is ever shown once any more.

// renderComm draws the console. newCode, when non-empty, is a just-minted pairing
// code shown ONCE — only its hash is stored, so it can never be shown again. rot
// carries a just-rotated endpoint secret under the same one-time contract.
func (a *app) renderComm(w http.ResponseWriter, r *http.Request, sess *store.Session, newCode string) {
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
	channels, err := a.comm.ListChannelsForConsole(ctx)
	if err != nil {
		log.Printf("web: comm channels: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	codes, err := a.comm.ListPendingCodes(ctx)
	if err != nil {
		log.Printf("web: comm codes: %v", err)
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
		OpenChannels int
		ChannelsLine string // human channel names, comma-joined; "" when none
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
	// GROUPED BY STATION WHERE THERE IS ONE, because a channel belongs to the station and
	// every reader of that station is affected by anything done to it. Keyed by endpoint
	// id, the blast radius landed on the wrong row in both directions: the successor —
	// the station's ONLY live reader, which joined nothing — showed zero channels, so the
	// row whose revocation actually silences the station carried no warning at all, while
	// a predecessor that had merely died still carried the full list.
	byEndpoint := map[string][]string{}
	byStation := map[string][]string{}
	for _, ch := range channels {
		if ch.State != "open" {
			continue
		}
		name := ch.Label
		if name == "" {
			name = ch.ChannelID
		}
		for _, seat := range []struct{ ep, station string }{
			{ch.EndpointA, ch.StationA}, {ch.EndpointB, ch.StationB},
		} {
			if seat.station != "" {
				byStation[seat.station] = append(byStation[seat.station], name)
				continue
			}
			if seat.ep != "" {
				byEndpoint[seat.ep] = append(byEndpoint[seat.ep], name)
			}
		}
	}

	eps := make([]epView, 0, len(endpoints))
	for _, ep := range endpoints {
		names := byEndpoint[ep.EndpointID]
		if ep.StationID != "" {
			names = byStation[ep.StationID]
		}
		v := epView{
			Endpoint: ep, OpenChannels: len(names), ChannelsLine: strings.Join(names, ", "),
			Bound: ep.StationID != "",
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
	binders := make([]credential, 0, len(binderIDs))
	for _, id := range binderIDs {
		refs, err := a.comm.EndpointsBoundBy(ctx, id)
		if err != nil {
			log.Printf("web: endpoints bound by %s: %v", id, err)
			continue
		}
		n := len(refs)
		// The station's human name comes from any live key of that station, its own
		// included, and falls back to the raw id when the station has no live key left —
		// which is honest rather than blank: that row is a station whose every key is gone.
		c := credential{TokenID: id, Label: labels[id], Station: stationOf[id], Live: n, Endpoints: refs}
		binders = append(binders, c)
	}

	a.render(w, r, sess, "comm", map[string]any{
		"Endpoints": eps, "Channels": channels, "Codes": codes, "Stats": stats,
		"Owners": owners, "Binders": binders,
		"NewCode": newCode, "CommURL": a.publicCommURL(r), "Fingerprint": fp,
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

// handleCommPair mints a pairing code. This is the human act the whole design
// depends on: the operator gives the code to exactly the two sessions they intend
// to connect, and no agent can produce one.
func (a *app) handleCommPair(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if a.comm == nil {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBody) // before checkCSRF, which parses the form
	if !a.checkCSRF(r, sess) {
		http.Error(w, "bad CSRF token", http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	label := strings.TrimSpace(r.FormValue("label"))
	if len(label) > 190 {
		flashRedirect(w, r, "/comm", "flash.comm_label_too_long", "")
		return
	}
	code, err := a.comm.MintPairingCode(r.Context(), sess.ActorID, label)
	if err != nil {
		flashRedirect(w, r, "/comm", "flash.comm_pair_failed", err.Error())
		return
	}
	// Render directly rather than redirecting, so the one-time code survives to the
	// page — the same reason token creation does.
	a.renderComm(w, r, sess, code)
}

// handleCommRevokeChannel is the brake: it closes a channel permanently.
func (a *app) handleCommRevokeChannel(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if a.comm == nil {
		http.NotFound(w, r)
		return
	}
	if !a.checkCSRF(r, sess) {
		http.Error(w, "bad CSRF token", http.StatusForbidden)
		return
	}
	id := r.PathValue("id")
	if err := a.comm.RevokeChannel(r.Context(), id); err != nil {
		flashRedirect(w, r, "/comm", "flash.comm_revoke_failed", err.Error())
		return
	}
	flashRedirect(w, r, "/comm", "flash.comm_channel_revoked", id)
}

// handleCommRotateEndpoint IS DELETED. It was the console half of secret rotation — the one
// control only a human could reach, existing because a lost secret was otherwise terminal. A
// station comes with a mailbox and holds no secret at all.

// handleCommReassignEndpoint points a mailbox at a CONVERSATION — the comm half of workspace
// recovery.
//
// ROTATE WAS THE ONLY WAY BACK IN AND IT DOES NOT WORK FOR A CHAT SESSION. It mints a fresh secret
// for the human to relay and the session to write to disk (mode 0600, outside any git repo), and a
// claude.ai chat has no disk — that is the ceremony 3.36.0 removed from the register path. So a
// mailbox whose conversation is gone was recoverable only by a session that could keep a file.
//
// With this, recovery is ONE STRING USED TWICE: the session states its conversation key, the human
// pastes it into the workspace form and this one, and the next poll reads the mail that was
// already waiting. Nothing secret is displayed, and the channels, links and queued messages are
// untouched.
func (a *app) handleCommReassignEndpoint(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if a.comm == nil {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBody)
	if !a.checkCSRF(r, sess) {
		http.Error(w, "bad CSRF token", http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	id := r.PathValue("id")
	key := strings.TrimSpace(r.FormValue("session_key"))

	res, err := a.comm.ReassignEndpointToSession(r.Context(), id, key)
	switch {
	case err != nil:
		flashRedirect(w, r, "/comm", "flash.comm_reassign_failed", err.Error())
		return
	case key == "":
		// LOGGED LIKE A ROTATION, and for the same reason: comm.db is expendable and not backed
		// up, so the server log is the record that survives. Who repointed a mailbox is exactly
		// the fact an operator needs when mail turns up somewhere unexpected.
		log.Printf("COMM: endpoint %s released from its conversation by %q (actor %d)",
			id, sess.ActorName, sess.ActorID)
		flashRedirect(w, r, "/comm", "flash.comm_released", id)
	case res.TakenFromID != "":
		log.Printf("COMM: endpoint %s reassigned to a conversation by %q (actor %d) — the key was taken from endpoint %s",
			id, sess.ActorName, sess.ActorID, res.TakenFromID)
		flashRedirect(w, r, "/comm", "flash.comm_reassigned_taken", res.TakenFromID)
	default:
		log.Printf("COMM: endpoint %s reassigned to a conversation by %q (actor %d)",
			id, sess.ActorName, sess.ActorID)
		flashRedirect(w, r, "/comm", "flash.comm_reassigned", id)
	}
}

// THE REPOINT AND REBIND CONSOLE MACHINERY IS DELETED — the handlers, the target pickers and the
// helpers underneath them. Both existed for the per-machine credential model: repoint moved a
// mailbox to another owning TOKEN, rebind moved a binding onto another STATION KEY. There is one
// credential and no binding.

// commTokenOwner resolves a target token through the store, which owns the question.
//
// Deliberately a thin wrapper: the resolution and the refusal both live in
// store.CommTokenOwner, using the SAME query the comm surface uses to build a principal. A
// second resolution here would be a second answer to "who owns this token", and the drift
// would be silent — the endpoint would simply stop authenticating.
func (a *app) commTokenOwner(ctx context.Context, tokenID string) (comm.Owner, bool) {
	actorID, err := a.store.CommTokenOwner(ctx, tokenID)
	if err != nil {
		return comm.Owner{}, false
	}
	return comm.Owner{TokenID: tokenID, ActorID: actorID}, true
}

// handleCommRevokeEndpoint revokes one session's endpoint, denying it further use.
func (a *app) handleCommRevokeEndpoint(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if a.comm == nil {
		http.NotFound(w, r)
		return
	}
	if !a.checkCSRF(r, sess) {
		http.Error(w, "bad CSRF token", http.StatusForbidden)
		return
	}
	id := r.PathValue("id")
	if err := a.comm.RevokeEndpoint(r.Context(), id); err != nil {
		flashRedirect(w, r, "/comm", "flash.comm_revoke_failed", err.Error())
		return
	}
	flashRedirect(w, r, "/comm", "flash.comm_endpoint_revoked", id)
}

// publicCommURL is the externally-reachable MCP endpoint for a copy-paste registration example.
//
// IT IS NOW THE SAME URL AS EVERYTHING ELSE. There is one machine surface, /mcp, carrying every
// tool; /comm/mcp is deleted. This used to derive a second endpoint by swapping the suffix, and a
// console that still offered that URL would hand an operator a connector that 404s.
func (a *app) publicCommURL(r *http.Request) string { return a.publicMCPURL(r) }

// commEnabled reports whether the console should appear in the nav.
func (a *app) commEnabled() bool { return a.comm != nil }
