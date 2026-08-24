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
// build the seams, defer the machinery. Every query below is already space-scoped,
// so making this per-user later is a change of one expression rather than a
// rewrite of the page.
const spaceForSession = int64(1)

func (a *app) handleComm(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	a.renderComm(w, r, sess, "", reveal{})
}

// reveal carries a just-rotated endpoint secret to the page. Both fields are set
// together or neither is: a secret with no endpoint id beside it is unusable, since
// every comm tool needs the pair.
type reveal struct {
	EndpointID string
	Secret     string
}

// renderComm draws the console. newCode, when non-empty, is a just-minted pairing
// code shown ONCE — only its hash is stored, so it can never be shown again. rot
// carries a just-rotated endpoint secret under the same one-time contract.
func (a *app) renderComm(w http.ResponseWriter, r *http.Request, sess *store.Session, newCode string, rot reveal) {
	if a.comm == nil {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()

	endpoints, err := a.comm.ListEndpoints(ctx, spaceForSession)
	if err != nil {
		log.Printf("web: comm endpoints: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	channels, err := a.comm.ListChannelsForSpace(ctx, spaceForSession)
	if err != nil {
		log.Printf("web: comm channels: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	codes, err := a.comm.ListPendingCodes(ctx, spaceForSession)
	if err != nil {
		log.Printf("web: comm codes: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	stats, err := a.comm.StatsFor(ctx, spaceForSession)
	if err != nil {
		log.Printf("web: comm stats: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// The value the live-refresh poller compares against: the page reloads when
	// /comm/count later reports a different number. Rendered here so the marker and
	// the poll answer come from the same source of truth.
	fp, err := a.comm.ConsoleFingerprint(ctx, spaceForSession)
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
		eps = append(eps, epView{
			Endpoint: ep, OpenChannels: len(names), ChannelsLine: strings.Join(names, ", "),
			Bound: ep.StationID != "",
		})
	}

	a.render(w, r, sess, "comm", map[string]any{
		"Endpoints": eps, "CommTokens": a.repointTargets(r.Context()), "Channels": channels, "Codes": codes, "Stats": stats,
		"NewCode": newCode, "CommURL": a.publicCommURL(r), "Fingerprint": fp,
		"Rotated": rot,
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
	n, err := a.comm.ConsoleFingerprint(r.Context(), spaceForSession)
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
	code, err := a.comm.MintPairingCode(r.Context(), spaceForSession, sess.ActorID, label)
	if err != nil {
		flashRedirect(w, r, "/comm", "flash.comm_pair_failed", err.Error())
		return
	}
	// Render directly rather than redirecting, so the one-time code survives to the
	// page — the same reason token creation does.
	a.renderComm(w, r, sess, code, reveal{})
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

// handleCommRotateEndpoint replaces an endpoint's secret while keeping its identity
// and every channel it belongs to.
//
// The security property lives in WHERE this handler is, not in what it does: it is
// behind requireAuth + CSRF, so triggering it needs curator authentication — a
// credential no session holds. A session that has the COMM bearer token cannot reach
// it, which is exactly why an equivalent tool was refused. See
// comm.RotateEndpointSecret for the full argument.
//
// Rendered rather than redirected, because the new secret is shown once — the same
// contract as a minted pairing code.
func (a *app) handleCommRotateEndpoint(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if a.comm == nil {
		http.NotFound(w, r)
		return
	}
	if !a.checkCSRF(r, sess) {
		http.Error(w, "bad CSRF token", http.StatusForbidden)
		return
	}
	id := r.PathValue("id")
	secret, err := a.comm.RotateEndpointSecret(r.Context(), id)
	if err != nil {
		flashRedirect(w, r, "/comm", "flash.comm_rotate_failed", err.Error())
		return
	}
	// The authoritative audit record. comm.db is expendable and deliberately not
	// backed up, so the counters on the row are console display state and THIS is
	// the trace that survives — it names who did it, because a rotation an operator
	// did not perform is the signal that matters.
	log.Printf("COMM: endpoint %s secret rotated by %q (actor %d) — the previous secret no longer authenticates",
		id, sess.ActorName, sess.ActorID)
	a.renderComm(w, r, sess, "", reveal{EndpointID: id, Secret: secret})
}

// repointTarget is one token an endpoint may be moved onto.
type repointTarget struct {
	TokenID string
	Label   string
	Actor   string
}

// repointTargets lists the tokens a re-point may legally name, so the console offers a CHOICE
// rather than a free-text field.
//
// Filtered by the SAME rule the operation enforces — present, unrevoked, carrying `comm` —
// because a picker that offers an option the action then refuses teaches an operator that the
// control is unreliable. The store still re-checks: this list is convenience, not the gate,
// and a stale page must fail rather than succeed.
func (a *app) repointTargets(ctx context.Context) []repointTarget {
	rows, err := a.store.ListTokens(ctx)
	if err != nil {
		return nil
	}
	out := make([]repointTarget, 0, len(rows))
	for _, t := range rows {
		if t.RevokedAt != "" {
			continue
		}
		if _, _, err := a.store.CommTokenOwner(ctx, t.TokenID); err != nil {
			continue
		}
		out = append(out, repointTarget{TokenID: t.TokenID, Label: t.Label, Actor: t.ActorName})
	}
	return out
}

// handleCommRepointEndpoint moves ONE endpoint to a different owning token.
//
// The security property is WHERE this handler is, exactly as for rotation: behind
// requireAuth + CSRF, so it needs curator authentication — a credential no session holds and
// none can obtain from the machine. An equivalent MCP tool was refused for rotation with an
// argument that applies here even more strongly: a secret at least has to be handed over,
// while an `endpoint_id` is NOT a secret. It is the routing address, rendered on this very
// page and printed throughout the runbooks. A self-service re-point would let any session on
// a shared machine seize any endpoint on it, with nothing to steal first.
func (a *app) handleCommRepointEndpoint(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	a.repoint(w, r, sess, r.PathValue("id"), "")
}

// handleCommRepointToken moves EVERY live endpoint of one token at once.
//
// Not convenience. Eleven endpoints on one token is the shape that makes a per-endpoint
// control feel like the ceremony it was built to remove, and a half-moved estate is the state
// nobody has a recovery story for.
func (a *app) handleCommRepointToken(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	a.repoint(w, r, sess, "", r.PathValue("id"))
}

// repoint validates the TARGET and then performs one of the two moves.
//
// THE VALIDATION LIVES HERE AND NOWHERE ELSE, and that is a placement decision rather than
// convenience. `internal/comm` cannot check an api_token: it does not import `internal/store`
// and must not learn to, because S7's pointer rule runs comm -> store and never back. This
// package holds both handles already.
//
// AND IT IS PART OF THE AUTHORISATION, not a nicety. Re-pointing onto a revoked or non-comm
// token produces an endpoint that authenticates NOWHERE and fails indistinguishably from one
// whose secret leaked — a missing scope is a 401 at the transport, a revoked target is the bare
// ownership string, and neither says "you re-pointed onto a dead token". That is the hunted
// defect class, manufactured by the control built to cure it.
func (a *app) repoint(w http.ResponseWriter, r *http.Request, sess *store.Session, endpointID, fromToken string) {
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
	target := strings.TrimSpace(r.FormValue("to_token"))
	if target == "" {
		flashRedirect(w, r, "/comm", "flash.comm_repoint_no_target", "")
		return
	}

	to, ok := a.commTokenOwner(r.Context(), target)
	if !ok {
		flashRedirect(w, r, "/comm", "flash.comm_repoint_bad_target", target)
		return
	}

	if endpointID != "" {
		from := strings.TrimSpace(r.FormValue("from_token"))
		if err := a.comm.RepointEndpointOwner(r.Context(), endpointID, from, to); err != nil {
			flashRedirect(w, r, "/comm", "flash.comm_repoint_failed", err.Error())
			return
		}
		// THE AUDIT RECORD, and it is the one that survives: comm.db is expendable and not
		// backed up, so the row carries no history and this line is the trace. It names both
		// tokens because "which credential does this endpoint answer to now" is the whole
		// question a re-point changes.
		log.Printf("COMM: endpoint %s re-pointed from token %s to token %s (actor %d) by %q — id, secret, channels, binding and queued mail unchanged",
			endpointID, from, to.TokenID, to.ActorID, sess.ActorName)
		flashRedirect(w, r, "/comm", "flash.comm_repointed", endpointID)
		return
	}

	n, err := a.comm.RepointEndpointsOfToken(r.Context(), fromToken, to)
	if err != nil {
		flashRedirect(w, r, "/comm", "flash.comm_repoint_failed", err.Error())
		return
	}
	log.Printf("COMM: %d live endpoint(s) re-pointed from token %s to token %s (actor %d) by %q",
		n, fromToken, to.TokenID, to.ActorID, sess.ActorName)
	flashRedirect(w, r, "/comm", "flash.comm_repointed_bulk", fmt.Sprint(n))
}

// commTokenOwner resolves a target token through the store, which owns the question.
//
// Deliberately a thin wrapper: the resolution and the refusal both live in
// store.CommTokenOwner, using the SAME query the comm surface uses to build a principal. A
// second resolution here would be a second answer to "who owns this token", and the drift
// would be silent — the endpoint would simply stop authenticating.
func (a *app) commTokenOwner(ctx context.Context, tokenID string) (comm.Owner, bool) {
	actorID, spaceID, err := a.store.CommTokenOwner(ctx, tokenID)
	if err != nil {
		return comm.Owner{}, false
	}
	return comm.Owner{TokenID: tokenID, ActorID: actorID, SpaceID: spaceID}, true
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

// publicCommURL builds the externally-reachable COMM MCP endpoint for a
// copy-paste registration example, using the same derivation and the same
// <ken-host> fallback as the knowledge base's URL.
func (a *app) publicCommURL(r *http.Request) string {
	base := a.publicMCPURL(r)
	return strings.TrimSuffix(base, "/mcp") + "/comm/mcp"
}

// commEnabled reports whether the console should appear in the nav.
func (a *app) commEnabled() bool { return a.comm != nil }
