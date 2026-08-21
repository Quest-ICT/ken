package web

import (
	"errors"
	"fmt"
	"github.com/Quest-ICT/ken/internal/comm"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/Quest-ICT/ken/internal/store"
)

// The stations console (docs/STATIONS.md §10).
//
// This page exists for the same reason the COMM console does: the design withholds a
// capability from agents, and without a human surface there is no way to exercise the
// gate. A session can ask for a station; only here does one come into existence, and
// only here does a human type its name.
//
// Gated on the stations flag ALONE and never on COMM. Stations work with COMM off —
// the notebook and the task list need no peers — and tying the console to COMM would
// hide the operator surface for a feature that is running.
//
// The cross-station task view is the part that earns the page. Everything else has a
// CLI equivalent; the whole-pile view does not, and it is the only surface where a
// human sees every task waiting on them at once instead of whatever the session they
// happen to be talking to remembers to mention.

// stationsPageSize bounds the cross-station task list. Generous, because this view's
// whole purpose is to show the pile rather than a sample of it — but bounded, because
// an unbounded render is how a console page becomes the slowest thing in the app.
const stationsPageSize = 200

func (a *app) handleStations(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	a.renderStations(w, r, sess, "", nil)
}

// renderStations draws the console. newKey, when non-empty, is a just-minted station
// key shown ONCE — only its hash is stored, so it can never be shown again.
func (a *app) renderStations(w http.ResponseWriter, r *http.Request, sess *store.Session, newKey string, revealed *store.StationVaultEntry) {
	if !a.stationsEnabled {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()

	stations, err := a.store.ListStations(ctx, spaceForSession)
	if err != nil {
		log.Printf("web: stations list: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	requests, err := a.store.PendingStationRequests(ctx, spaceForSession)
	if err != nil {
		log.Printf("web: station requests: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	links, err := a.store.ListStationLinks(ctx, spaceForSession)
	if err != nil {
		log.Printf("web: station links: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Promotion requests: a station asking a human to turn a notebook page into
	// knowledge. station_note_promote has written these since stations shipped and
	// NOTHING read them — no store function, no route, no template — so every request
	// a session filed went into a drawer nobody could open, while the tool told the
	// session it had asked.
	promotions, err := a.store.ListPendingPromotions(ctx, spaceForSession)
	if err != nil {
		log.Printf("web: pending promotions: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// blocked_on defaults to `human` because that is the question this page answers:
	// what is waiting on ME. Any other value is opt-in via the query string.
	blockedOn := r.URL.Query().Get("blocked_on")
	if blockedOn == "" {
		blockedOn = "human"
	}
	if blockedOn == "any" {
		blockedOn = ""
	}
	tasks, err := a.store.CrossStationHumanTasks(ctx, spaceForSession, blockedOn, stationsPageSize)
	if err != nil {
		log.Printf("web: cross-station tasks: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// HOW MUCH OF THE PILE THIS IS. The list is capped at stationsPageSize and rendered
	// with no total, which makes a truncated pile look exactly like a complete one — on
	// the one page built so the human can see the whole pile. The vault trail on this same
	// page already states "the last 20 of 2,318" for precisely this reason.
	taskTotal, err := a.store.CountCrossStationTasks(ctx, spaceForSession, blockedOn)
	if err != nil {
		log.Printf("web: cross-station task count: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Per-station detail is assembled here rather than in the template, so the
	// template stays a renderer and the N+1 is explicit and bounded by station count.
	type stationView struct {
		store.Station
		Usage *store.StationUsage
		Keys  []store.StationKey
		// Vault is metadata only — ListStationVault cannot return a value, so no
		// rendering mistake here can spill one. A secret reaches this page only
		// through handleStationVaultReveal, which logs the read.
		Vault      []store.StationVaultEntry
		VaultReads []store.StationVaultRead
		VaultTotal int
		// Which names have something to restore. Asked of the store rather than
		// inferred from rev, which is wrong in both directions — see
		// StationVaultRecoverableNames.
		Recoverable map[string]bool
	}
	views := make([]stationView, 0, len(stations))
	for _, s := range stations {
		usage, err := a.store.StationAssetUsage(ctx, s.StationID)
		if err != nil {
			log.Printf("web: station usage %s: %v", s.StationID, err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		keys, err := a.store.ListStationKeys(ctx, s.StationID)
		if err != nil {
			log.Printf("web: station keys %s: %v", s.StationID, err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		vault, err := a.store.ListStationVault(ctx, s.StationID)
		if err != nil {
			log.Printf("web: station vault %s: %v", s.StationID, err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		// The retained trail plus the TRUE total. They differ once the log has been
		// pruned, and the template shows both — "the last 20 of 2,318" is a bound an
		// operator can reason about; "20 reads" would be a lie.
		reads, total, err := a.store.StationVaultReads(ctx, s.StationID, vaultReadsShown)
		if err != nil {
			log.Printf("web: station vault reads %s: %v", s.StationID, err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		recoverable, err := a.store.StationVaultRecoverableNames(ctx, s.StationID)
		if err != nil {
			log.Printf("web: station vault history %s: %v", s.StationID, err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		views = append(views, stationView{Station: s, Usage: usage, Keys: keys,
			Vault: vault, VaultReads: reads, VaultTotal: total, Recoverable: recoverable})
	}

	// Task rows already carry StationName — the cross-station query joins it — so the
	// only thing left is §10's "archived stations marked". A task on an archived
	// station is still waiting on the human, and hiding that it came from a station
	// nobody is staffing would misrepresent why it has sat there.
	archived := make(map[string]bool, len(stations))
	for _, s := range stations {
		if s.State == "archived" {
			archived[s.StationID] = true
		}
	}

	// Each link carries the live traffic revoking it would end, so the blast radius is
	// in front of the human BEFORE the click rather than discovered after it. Same
	// bounded N+1 as the per-station detail above, and for the same reason.
	//
	// COUNTED FOR REVOKED LINKS TOO. Skipping them hid the single state this column
	// exists to expose: a revocation whose channel sweep failed leaves the permission
	// gone and the conversation running, and rendering that as an em-dash made the
	// half-finished case look exactly like the finished one.
	//
	// KnownLive distinguishes "zero" from "not asked". With COMM off this package
	// holds no comm handle, so the count is UNKNOWN — comm.db and every open channel
	// in it outlive the server flag, and reporting 0 would assert a fact nobody
	// checked. Two fields rather than one because a bare int cannot say "unknown".
	type linkView struct {
		store.StationLink
		LiveChannels int
		KnownLive    bool
	}
	linkViews := make([]linkView, 0, len(links))
	for _, l := range links {
		v := linkView{StationLink: l}
		if a.comm != nil {
			n, err := a.comm.CountOpenChannelsBetweenStations(ctx, l.StationA, l.StationB)
			if err != nil {
				// DEGRADE, never 500. comm.db is the EXPENDABLE database and this
				// page is gated on the stations flag alone — tying the whole
				// operator surface (pending requests, station keys, the
				// cross-station task list) to a comm.db failure would hide a
				// feature that is running perfectly well.
				log.Printf("web: link live channels %s: %v", l.LinkID, err)
			} else {
				v.LiveChannels, v.KnownLive = n, true
			}
		}
		linkViews = append(linkViews, v)
	}

	// Rooms and their members, for the section that is the ONLY way one comes to exist.
	rooms, err := a.store.ListRooms(ctx, spaceForSession)
	if err != nil {
		log.Printf("web: list rooms: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// D1 — THE ROOT DEFECT OF THE ROOMS DEBUGGING, and the one neither agent found
	// because both experienced the symptom rather than the cause.
	//
	// A station with NO BOUND ENDPOINT can be added to a room, the console flashes
	// success, and the station is a member on paper and deaf in practice: room
	// membership is keyed on the party `s:<station_id>`, and an unbound endpoint
	// resolves to `e:<rowid>`, which can never match. ken-promo was added at
	// 2026-08-13T15:39:59Z with zero bound endpoints and concluded from the resulting
	// silence that rooms were RECEIVE-ONLY — a wrong belief about the product, reported
	// upward, by the station whose charter is describing the product.
	//
	// ADMITTED AND FLAGGED rather than refused, because Vlad's specification is that
	// membership is durable: "once a room is created and parties are added, they should
	// permanently be able to use it". Adding a station before its session binds is
	// legitimate and the membership is correct — what was missing is any surface saying
	// the station cannot yet hear. Refusing would make the console wrong about a
	// legitimate order of operations; staying silent makes it wrong about the outcome.
	type roomMemberView struct {
		store.RoomMember
		// Bound is meaningless unless BoundKnown — with COMM off this package has no
		// endpoint table to ask, and reporting "not bound" would be asserting a fact
		// nobody checked. Same discipline as the link live-channel count.
		Bound      bool
		BoundKnown bool
	}
	type roomView struct {
		store.Room
		Members []roomMemberView
		// Deaf is how many members cannot receive. Surfaced on the room itself so an
		// operator sees it without expanding the member list.
		Deaf int
	}

	// One read for the whole page rather than per member.
	var staffing map[string]comm.Staffing
	if a.comm != nil {
		if sf, err := a.comm.StaffingByStation(ctx); err != nil {
			log.Printf("web: staffing for room members: %v", err)
		} else {
			staffing = sf
		}
	}

	roomViews := make([]roomView, 0, len(rooms))
	for _, rm := range rooms {
		members, err := a.store.RoomMembers(ctx, rm.RoomID)
		if err != nil {
			log.Printf("web: room members %s: %v", rm.RoomID, err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		rv := roomView{Room: rm, Members: make([]roomMemberView, 0, len(members))}
		for _, m := range members {
			mv := roomMemberView{RoomMember: m}
			if staffing != nil {
				mv.BoundKnown = true
				mv.Bound = staffing[m.StationID].Endpoints > 0
				if !mv.Bound {
					rv.Deaf++
				}
			}
			rv.Members = append(rv.Members, mv)
		}
		roomViews = append(roomViews, rv)
	}

	a.render(w, r, sess, "stations", map[string]any{
		"Stations": views, "Requests": requests, "Links": linkViews, "Promotions": promotions,
		"Rooms": roomViews,
		"Tasks": tasks, "TaskTotal": taskTotal, "TaskShown": len(tasks),
		"TaskCapped": taskTotal > len(tasks), "Archived": archived,
		"BlockedOn": r.URL.Query().Get("blocked_on"),
		"NewKey":    newKey,
		"Revealed":  revealed,
	})
}

// handleStationLinkRevoke ends a relationship AND the live traffic it authorised.
//
// Two writes, in this order and not the other: the link first, so the permission is
// gone even if the channel sweep fails, then the channels. A failure after the link
// write leaves a revoked permission with live traffic.
//
// An earlier version of this comment claimed that state was "visible, reported, and
// fixable", and all three were false: the page skipped the channel count for revoked
// links, the template hid the button, and a retry short-circuited on ErrNotFound
// BEFORE reaching the sweep. The half-finished revocation was invisible and
// permanently unrecoverable from this page. It is now RETRYABLE — an already-revoked
// link falls through to the sweep instead of erroring — which is what makes the
// ordering argument true rather than merely plausible.
//
// S9's approval is per relationship, so its withdrawal is too. This is the operator
// brake that a durable roster needs before it can replace a pairing code: a
// membership list nobody can take away is not a stronger gate than a bearer code, it
// is a weaker one that lasts longer.
func (a *app) handleStationLinkRevoke(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !a.stationsEnabled {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBody)
	if !a.checkCSRF(r, sess) {
		http.Error(w, "bad CSRF token", http.StatusForbidden)
		return
	}
	id := r.PathValue("id")

	// Read before write: once the row says 'revoked' it can still be named, but the
	// station ids are needed for the channel sweep and a missing link should 404
	// rather than half-execute.
	link, err := a.store.StationLinkByID(r.Context(), id)
	if err != nil {
		flashRedirect(w, r, "/stations", "flash.station_link_revoke_failed", err.Error())
		return
	}
	// ErrNotFound here means "already revoked" — StationLinkByID above proved the row
	// exists. Falling through is deliberate: it is what makes a retry able to finish a
	// sweep that failed the first time. Treating it as an error is what made the
	// half-done state permanent.
	if err := a.store.RevokeStationLink(r.Context(), id); err != nil && !errors.Is(err, store.ErrNotFound) {
		flashRedirect(w, r, "/stations", "flash.station_link_revoke_failed", err.Error())
		return
	}

	pair := link.NameA + " / " + link.NameB
	closed := 0
	// P2: WITHDRAW THE PAIR SCOPE FIRST, before anything that can fail. Every exit path
	// below returns, and one of them returns on an error — so a refresh placed after the
	// channel sweep would be skipped exactly when the sweep is having trouble, leaving a
	// revoked link still authorising sends. This is a no-op when COMM is off.
	a.syncRoomMirror(r)
	if a.comm == nil {
		// COMM is off in THIS server, which says nothing about comm.db: open channels
		// outlive the flag. Do not claim none were open — that is a fact nobody
		// checked, and re-enabling COMM later would resume a conversation the
		// operator was told had been fully withdrawn.
		flashRedirect(w, r, "/stations", "flash.station_link_revoked_comm_off", pair)
		return
	}
	n, err := a.comm.RevokeChannelsBetweenStations(r.Context(), link.StationA, link.StationB)
	if err != nil {
		// The permission is already gone; say so, and say what did NOT happen, so the
		// operator knows to retry — which now works.
		log.Printf("web: revoke channels for link %s: %v", id, err)
		flashRedirect(w, r, "/stations", "flash.station_link_revoked_channels_failed", pair)
		return
	}
	closed = n
	// Two keys rather than one with a spliced count: flash carries a single argument,
	// and a number formatted into the argument would arrive in the operator's language
	// with an English "channels closed" glued to it. The exact count is on the page.
	if closed > 0 {
		flashRedirect(w, r, "/stations", "flash.station_link_revoked_channels", pair)
		return
	}
	flashRedirect(w, r, "/stations", "flash.station_link_revoked", pair)
}

// handleStationApprove is the curation gate for identities: a request becomes a
// station, named by the human at this moment. The agent's name_hint is displayed on
// the page and carries no weight here.
func (a *app) handleStationApprove(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !a.stationsEnabled {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBody) // before checkCSRF, which parses the form
	if !a.checkCSRF(r, sess) {
		http.Error(w, "bad CSRF token", http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	id := r.PathValue("id")

	// A LINK request has no name to type — the human is approving a relationship
	// between two stations that already exist, not creating one. Routed on the form's
	// declared kind rather than on whether a name happens to be present, so a
	// mis-filled form fails loudly instead of taking the wrong branch.
	if r.FormValue("kind") == "link" {
		l, err := a.store.ApproveLinkRequest(r.Context(), id, sess.ActorID)
		switch {
		case errors.Is(err, store.ErrRequestNotPending):
			flashRedirect(w, r, "/stations", "flash.station_request_gone", "")
		case err != nil:
			flashRedirect(w, r, "/stations", "flash.station_approve_failed", err.Error())
		default:
			// P3: SPEND THE LINK IMMEDIATELY. A link recorded and never materialised is a
			// human decision that still costs a pairing code to use — which is the step the
			// link exists to remove. Approving is the gate; there is nothing left to wait
			// for, so the conversation comes into existence here.
			//
			// BEST EFFORT, AND NEVER FATAL TO THE APPROVAL. Both stations must be staffed
			// for a channel to exist at all, and one may not be — `proxmox-servers` had a
			// station and no endpoint for five days. The link is still correct and still
			// worth recording; comm_open_channel materialises it later when someone shows
			// up. Failing the human's decision over a messaging detail would be the wrong
			// thing to fail on.
			//
			// P2: THE MIRROR REFRESH IS THE PART THAT ALWAYS WORKS. Opening a channel
			// needs both stations staffed; authorising the PAIR SCOPE needs neither,
			// because a pair conversation is addressed by name and has no row to
			// create. So the approval lands as a usable permission even when nobody is
			// connected — which is the case P3 had to log and move past.
			a.syncRoomMirror(r)
			flash := "flash.link_approved"
			if a.comm != nil {
				epA, errA := a.comm.LiveEndpointForStation(r.Context(), l.StationA)
				epB, errB := a.comm.LiveEndpointForStation(r.Context(), l.StationB)
				if errA == nil && errB == nil && epA != nil && epB != nil {
					if _, err := a.comm.OpenLinkedChannel(r.Context(), epA, epB, sess.ActorID,
						l.NameA+" ↔ "+l.NameB); err == nil {
						flash = "flash.link_approved_open"
					} else {
						log.Printf("stations: link %s approved but channel not opened: %v", id, err)
					}
				}
			}
			flashRedirect(w, r, "/stations", flash, l.NameA+" ↔ "+l.NameB)
		}
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		flashRedirect(w, r, "/stations", "flash.station_name_required", "")
		return
	}
	st, err := a.store.ApproveStationRequest(r.Context(), id, name, sess.ActorID)
	switch {
	case errors.Is(err, store.ErrRequestNotPending):
		flashRedirect(w, r, "/stations", "flash.station_request_gone", "")
		return
	case errors.Is(err, store.ErrStationNameTaken):
		flashRedirect(w, r, "/stations", "flash.station_name_taken", name)
		return
	case err != nil:
		flashRedirect(w, r, "/stations", "flash.station_approve_failed", err.Error())
		return
	}
	flashRedirect(w, r, "/stations", "flash.station_approved", st.Name)
}

// handleStationDeny records a refusal WITH a reason. The reason is required by the
// store, not merely by this form, so the CLI cannot bypass it.
func (a *app) handleStationDeny(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !a.stationsEnabled {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBody)
	if !a.checkCSRF(r, sess) {
		http.Error(w, "bad CSRF token", http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	reason := strings.TrimSpace(r.FormValue("reason"))
	if reason == "" {
		flashRedirect(w, r, "/stations", "flash.station_reason_required", "")
		return
	}
	err := a.store.DenyStationRequest(r.Context(), r.PathValue("id"), reason, sess.ActorID)
	switch {
	case errors.Is(err, store.ErrRequestNotPending):
		flashRedirect(w, r, "/stations", "flash.station_request_gone", "")
	case err != nil:
		flashRedirect(w, r, "/stations", "flash.station_deny_failed", err.Error())
	default:
		flashRedirect(w, r, "/stations", "flash.station_denied", "")
	}
}

// handleStationKey mints a key for an existing station. Shown once, then never again —
// so this renders the page directly instead of redirecting, exactly as pairing-code
// minting and token creation do.
func (a *app) handleStationKey(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !a.stationsEnabled {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBody)
	if !a.checkCSRF(r, sess) {
		http.Error(w, "bad CSRF token", http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	label := strings.TrimSpace(r.FormValue("label"))
	if label == "" {
		flashRedirect(w, r, "/stations", "flash.station_key_label_required", "")
		return
	}
	// Both scopes, always. The locker is part of a station rather than an extra, and
	// the server now gates it on `station` alone — station-locker is written only so
	// the key's recorded scope list keeps describing what it can do.
	scopes := []string{"station", "station-locker"}
	key, err := a.store.IssueStationKey(r.Context(), sess.ActorID, r.PathValue("id"), label, scopes)
	if err != nil {
		flashRedirect(w, r, "/stations", "flash.station_key_failed", err.Error())
		return
	}
	a.renderStations(w, r, sess, key, nil)
}

// handleStationKeyRetire stops a key binding new endpoints without touching live ones.
// The severing verb (revoke) goes through the ordinary token path so it cannot diverge
// from `ken token revoke` — see S6.
func (a *app) handleStationKeyRetire(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !a.stationsEnabled {
		http.NotFound(w, r)
		return
	}
	if !a.checkCSRF(r, sess) {
		http.Error(w, "bad CSRF token", http.StatusForbidden)
		return
	}
	if err := a.store.RetireStationKey(r.Context(), r.PathValue("id")); err != nil {
		flashRedirect(w, r, "/stations", "flash.station_key_retire_failed", err.Error())
		return
	}
	flashRedirect(w, r, "/stations", "flash.station_key_retired", "")
}

// handleStationRename gives the human the one thing they always own about a station: what
// it is called. Vlad stated it as a requirement, `RenameStation` was written for it, and
// until now NOTHING CALLED THAT FUNCTION — not the console, not the CLI, despite its own
// comment claiming both. An implemented requirement with no route is an unimplemented one.
//
// It is safe to do at any moment because a name is not an address (COMM.md §3): ids do the
// routing, comm.db's mirrors carry ids only, and every displayed name is resolved or joined
// at read time. So there is no mirror to push here and no channel to reopen — deliberately
// unlike archive, which changes who receives and therefore moves the roster epoch.
func (a *app) handleStationRename(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !a.stationsEnabled {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBody)
	if !a.checkCSRF(r, sess) {
		http.Error(w, "bad CSRF token", http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		flashRedirect(w, r, "/stations", "flash.station_name_required", "")
		return
	}
	err := a.store.RenameStation(r.Context(), r.PathValue("id"), name)
	switch {
	case errors.Is(err, store.ErrStationNameTaken):
		// Reported WITH the colliding name, for the same reason the transfer handler does
		// it: a bare refusal leaves the operator guessing which station already holds it.
		flashRedirect(w, r, "/stations", "flash.station_name_taken", name)
	case err != nil:
		flashRedirect(w, r, "/stations", "flash.station_rename_failed", err.Error())
	default:
		flashRedirect(w, r, "/stations", "flash.station_renamed", name)
	}
}

// handleStationPublish flips the directory listing. Publishing is a claim a station
// makes about itself being discoverable; it is not authorization, and nothing about
// reachability follows from it.
func (a *app) handleStationPublish(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !a.stationsEnabled {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBody)
	if !a.checkCSRF(r, sess) {
		http.Error(w, "bad CSRF token", http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	pub := r.FormValue("published") == "1"
	if err := a.store.SetStationPublished(r.Context(), r.PathValue("id"), pub); err != nil {
		flashRedirect(w, r, "/stations", "flash.station_publish_failed", err.Error())
		return
	}
	flashRedirect(w, r, "/stations", "flash.station_saved", "")
}

// handleStationArchive is reversible by design (S3): the name is held, links go
// dormant rather than revoked, and unarchiving restores them. That is why this is one
// handler with a boolean and not two verbs.
func (a *app) handleStationArchive(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !a.stationsEnabled {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBody)
	if !a.checkCSRF(r, sess) {
		http.Error(w, "bad CSRF token", http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	archived := r.FormValue("archived") == "1"
	if err := a.store.ArchiveStation(r.Context(), r.PathValue("id"), archived); err != nil {
		flashRedirect(w, r, "/stations", "flash.station_archive_failed", err.Error())
		return
	}
	// PUSH THE ROSTER, exactly as the room archive handler does. Archiving changes which
	// parties a room delivers to, and without this the change does not reach comm.db until
	// the next boot rebuild or the next unrelated room-console write — so the operator sees
	// "archived", the station keeps receiving, and nothing connects the two.
	a.syncRoomMirror(r)
	if archived {
		flashRedirect(w, r, "/stations", "flash.station_archived", "")
		return
	}
	flashRedirect(w, r, "/stations", "flash.station_unarchived", "")
}

// handleStationTransfer moves assets between stations — "the session is gone and its
// work should not be", and "this machine is being replaced".
//
// A collision is reported with the colliding NAMES, because a bare refusal leaves the
// operator with nothing to act on and a `handoff`-on-`handoff` clash is the common case.
func (a *app) handleStationTransfer(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !a.stationsEnabled {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBody)
	if !a.checkCSRF(r, sess) {
		http.Error(w, "bad CSRF token", http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	from := r.PathValue("id")
	to := strings.TrimSpace(r.FormValue("to"))
	if to == "" {
		flashRedirect(w, r, "/stations", "flash.station_transfer_needs_target", "")
		return
	}
	res, err := a.store.TransferStationAssets(r.Context(), from, to,
		r.FormValue("notes") == "1", r.FormValue("tasks") == "1", r.FormValue("locker") == "1")

	var collision *store.ErrTransferCollision
	switch {
	case errors.As(err, &collision):
		flashRedirect(w, r, "/stations", "flash.station_transfer_collision",
			collision.Class+": "+strings.Join(collision.Colliding, ", "))
	case err != nil:
		flashRedirect(w, r, "/stations", "flash.station_transfer_failed", err.Error())
	default:
		flashRedirect(w, r, "/stations", "flash.station_transferred",
			fmt.Sprintf("%d notes, %d tasks, %d files", res.Notes, res.Tasks, res.Locker))
	}
}

// handleStationsCount feeds the same generic poller Proposals and COMM use, so a
// request filed while the operator is looking at the page surfaces without a reload.
// Behind requireAuth like the page, so it is not an unauthenticated info leak.
func (a *app) handleStationsCount(w http.ResponseWriter, r *http.Request, _ *store.Session) {
	if !a.stationsEnabled {
		http.NotFound(w, r)
		return
	}
	reqs, err := a.store.PendingStationRequests(r.Context(), spaceForSession)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprintf(w, `{"count":%d}`, len(reqs))
}

// handleStationLocker serves a locker blob to the operator. Stations are told never to
// put a credential here and Ken cannot enforce it, so this download is also the
// mechanism by which a human can CHECK — which is the only control the design actually
// has (S11).
func (a *app) handleStationLocker(w http.ResponseWriter, r *http.Request, _ *store.Session) {
	if !a.stationsEnabled {
		http.NotFound(w, r)
		return
	}
	blob, err := a.store.GetStationLockerBlob(r.Context(), r.PathValue("id"), r.URL.Query().Get("name"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// text/plain with nosniff and an attachment disposition: a locker blob is agent-
	// authored content, and rendering it inline would make the console a delivery
	// vehicle for whatever a session decided to store.
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", "attachment; filename="+strconv.Quote(blob.Name))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(blob.Bytes)
}

// handlePromotionResolve closes a station's promotion request as converted or
// discarded.
//
// IT RECORDS THE DECISION AND NEVER PERFORMS IT. Converting a notebook page into
// knowledge is a kb_save; routing it through a console button would let this page
// write curated content, which is the single capability the whole design withholds.
// The human reads the page here, decides, and does the conversion by the ordinary
// path — this only closes the loop so the request stops waiting and the station can
// see it was answered.
func (a *app) handlePromotionResolve(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !a.stationsEnabled {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBody) // before checkCSRF, which parses the form
	if !a.checkCSRF(r, sess) {
		http.Error(w, "bad CSRF token", http.StatusForbidden)
		return
	}
	state := "discarded"
	if r.FormValue("decision") == "converted" {
		state = "converted"
	}
	err := a.store.ResolvePromotion(r.Context(), r.PathValue("id"), state, strings.TrimSpace(r.FormValue("slug")))
	switch {
	case errors.Is(err, store.ErrNotFound):
		// Already decided — a second tab, or a double click. Not an error worth a page.
		flashRedirect(w, r, "/stations", "flash.promotion_already_decided", "")
	case err != nil:
		log.Printf("web: resolve promotion: %v", err)
		flashRedirect(w, r, "/stations", "flash.promotion_failed", err.Error())
	default:
		flashRedirect(w, r, "/stations", "flash.promotion_"+state, "")
	}
}

// vaultReadsShown bounds what the console renders per station. The store keeps more
// (station_vault_read_log) and the per-secret count is exact regardless, so this is a
// display choice rather than a retention one — and the template states it, because a
// truncated list that does not say it is truncated is the notebook's silent pruning
// wearing a different hat.
const vaultReadsShown = 20

// vaultLimits reads the live bounds, falling back to the defaults when settings are
// unwired (tests). The console needs them because revealing a secret is a READ, and a
// read prunes the audit log to its configured length.
func (a *app) vaultLimits() store.StationVaultLimits {
	if a.settings == nil {
		return store.DefaultStationVaultLimits()
	}
	v := a.settings.Current().Values
	return store.StationVaultLimits{
		MaxSecretBytes:    v.StationVaultSecretKiB << 10,
		MaxEntries:        v.StationVaultEntries,
		MaxHistoryPerName: v.StationVaultHistoryRev,
		MaxReadLog:        v.StationVaultReadLog,
	}
}

// handleStationVaultReveal shows one secret to the human who owns the instance, and
// LOGS that it did — the same trail a station_vault_get call lands in, marked 'console'.
//
// A POST, and the value is rendered straight into the response rather than redirected
// to. Both halves matter. A GET would put the reveal in browser history and one prefetch
// away from firing without a human deciding to, and the read would be recorded as
// deliberate — corrupting the only record that makes a vault worth keeping. Passing the
// value through flashRedirect would put the secret itself in a URL, which is the same
// mistake with extra steps.
func (a *app) handleStationVaultReveal(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !a.stationsEnabled {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBody)
	if !a.checkCSRF(r, sess) {
		http.Error(w, "bad CSRF token", http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	e, err := a.store.GetStationVaultSecret(r.Context(), a.vaultLimits(), r.PathValue("id"),
		strings.TrimSpace(r.FormValue("name")), "console", "", sess.ActorID)
	if err != nil {
		flashRedirect(w, r, "/stations", "flash.station_vault_reveal_failed", err.Error())
		return
	}
	a.renderStations(w, r, sess, "", e)
}

// handleStationVaultRestore brings a secret back to its previous value — the console
// half of "every vault write is reversible".
//
// Deliberately a HUMAN action with no station-side equivalent: a session that has just
// destroyed something by mistake is not the party to decide what to put back, and the
// station surface offers no restore tool for the same reason.
func (a *app) handleStationVaultRestore(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !a.stationsEnabled {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBody)
	if !a.checkCSRF(r, sess) {
		http.Error(w, "bad CSRF token", http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	if _, err := a.store.RestoreStationVaultSecret(r.Context(), r.PathValue("id"),
		strings.TrimSpace(r.FormValue("name")), "", sess.ActorID); err != nil {
		flashRedirect(w, r, "/stations", "flash.station_vault_restore_failed", err.Error())
		return
	}
	flashRedirect(w, r, "/stations", "flash.station_vault_restored", r.FormValue("name"))
}
