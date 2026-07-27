package web

import (
	"log"
	"net/http"
	"strings"

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
	a.renderComm(w, r, sess, "")
}

// renderComm draws the console. newCode, when non-empty, is a just-minted pairing
// code shown ONCE — only its hash is stored, so it can never be shown again.
func (a *app) renderComm(w http.ResponseWriter, r *http.Request, sess *store.Session, newCode string) {
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

	a.render(w, r, sess, "comm", map[string]any{
		"Endpoints": endpoints, "Channels": channels, "Codes": codes, "Stats": stats,
		"NewCode": newCode, "CommURL": a.publicCommURL(r),
	})
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
