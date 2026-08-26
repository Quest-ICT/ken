package web

import (
	"log"
	"net/http"
	"strings"

	"github.com/Quest-ICT/ken/internal/store"
)

// The rooms console — the ONLY way a room comes into existence.
//
// There is no agent-facing create path, and that is not an oversight of the tool
// surface: a room is a decision about which posts should be able to talk to each other,
// and there is no version of that decision an agent should make for itself. A session
// that wants a room asks its human in words.

// syncRoomMirror pushes ken.db's membership AND its approved links into comm.db's two
// projections.
//
// CALLED AFTER EVERY MEMBERSHIP WRITE, and failure is logged rather than returned. The
// decision is already durable in ken.db by the time this runs; a failed mirror means
// sends are refused until the next rebuild, which is the safe direction — the unsafe one
// would be a stale mirror that still lets a removed station send.
//
// It also means COMM being off is not an error here: rooms are a ken.db concept and a
// human may well organise them before the messaging surface is in use.
//
// BOTH PROJECTIONS, ONE EPOCH READ. The room mirror and the station-link mirror are
// stamped with the same generation because they are refreshed together and describe the
// same roster; reading the epoch once means the two can never be stamped with different
// values from the same call. The name stayed `syncRoomMirror` on purpose — five callers
// exist and a rename would have been the whole diff — but it is now the ONE place either
// projection is pushed, which is the property worth having.
func (a *app) syncRoomMirror(r *http.Request) {
	if a.comm == nil {
		return
	}
	ctx := r.Context()
	epoch, err := a.store.RosterEpoch(ctx)
	if err != nil {
		log.Printf("web: read roster epoch: %v", err)
		return
	}
	roomOK := false
	rows, err := a.store.RoomMirrorRows(ctx)
	if err != nil {
		log.Printf("web: read room membership: %v", err)
	} else if err := a.comm.ReplaceRoomMirror(ctx, rows, epoch); err != nil {
		log.Printf("web: sync room mirror: %v", err)
	} else {
		roomOK = true
	}
	// INDEPENDENT OF THE ROOM PUSH, not chained to it. A failure reading rooms must not
	// silently skip the link refresh: they are separate authorities over separate
	// scopes, and the one that would be skipped here is the one that gates revocation.
	pairs, err := a.store.LinkMirrorRows(ctx)
	if err != nil {
		log.Printf("web: read station links: %v", err)
		return
	}
	linkOK := true
	if err := a.comm.ReplaceLinkMirror(ctx, pairs, epoch); err != nil {
		log.Printf("web: sync station-link mirror: %v", err)
		linkOK = false
	}

	// STAMPED ONCE, AND ONLY IF BOTH HALVES LANDED — see StampMirrorEpoch. The independence
	// above is why: a surviving half stamping for both made a partial rebuild read as fresh.
	if roomOK && linkOK {
		if err := a.comm.StampMirrorEpoch(ctx, epoch); err != nil {
			log.Printf("web: stamp mirror epoch: %v", err)
		}
	} else {
		log.Printf("web: a mirror half did not sync — roster epoch left behind so the projection reads as stale")
	}
}

func (a *app) handleRoomCreate(w http.ResponseWriter, r *http.Request, sess *store.Session) {
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
		flashRedirect(w, r, "/stations", "flash.room_name_required", "")
		return
	}
	if _, err := a.store.CreateRoom(r.Context(), name,
		strings.TrimSpace(r.FormValue("purpose")), sess.ActorID); err != nil {
		flashRedirect(w, r, "/stations", "flash.room_create_failed", err.Error())
		return
	}
	a.syncRoomMirror(r)
	flashRedirect(w, r, "/stations", "flash.room_created", name)
}

func (a *app) handleRoomMember(w http.ResponseWriter, r *http.Request, sess *store.Session) {
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
	roomID, station := r.PathValue("id"), strings.TrimSpace(r.FormValue("station_id"))
	var err error
	if r.FormValue("remove") == "1" {
		err = a.store.RemoveRoomMember(r.Context(), roomID, station)
	} else {
		err = a.store.AddRoomMember(r.Context(), roomID, station, sess.ActorID)
	}
	if err != nil {
		flashRedirect(w, r, "/stations", "flash.room_member_failed", err.Error())
		return
	}
	a.syncRoomMirror(r)
	flashRedirect(w, r, "/stations", "flash.room_membership_saved", "")
}

func (a *app) handleRoomArchive(w http.ResponseWriter, r *http.Request, sess *store.Session) {
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
	if err := a.store.ArchiveRoom(r.Context(), r.PathValue("id"), r.FormValue("archived") == "1"); err != nil {
		flashRedirect(w, r, "/stations", "flash.room_archive_failed", err.Error())
		return
	}
	// Archiving REMOVES a room from the mirror entirely (RoomMirrorRows filters on
	// state='active'), so an archived room stops accepting sends immediately rather
	// than at the next restart. Its history keeps its addresses.
	a.syncRoomMirror(r)
	flashRedirect(w, r, "/stations", "flash.room_archived", "")
}
