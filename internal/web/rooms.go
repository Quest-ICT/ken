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

// syncRoomMirror pushes ken.db's membership into comm.db's projection.
//
// CALLED AFTER EVERY MEMBERSHIP WRITE, and failure is logged rather than returned. The
// decision is already durable in ken.db by the time this runs; a failed mirror means
// sends are refused until the next rebuild, which is the safe direction — the unsafe one
// would be a stale mirror that still lets a removed station send.
//
// It also means COMM being off is not an error here: rooms are a ken.db concept and a
// human may well organise them before the messaging surface is in use.
func (a *app) syncRoomMirror(r *http.Request) {
	if a.comm == nil {
		return
	}
	ctx := r.Context()
	rows, err := a.store.RoomMirrorRows(ctx)
	if err != nil {
		log.Printf("web: read room membership: %v", err)
		return
	}
	epoch, err := a.store.RosterEpoch(ctx)
	if err != nil {
		log.Printf("web: read roster epoch: %v", err)
		return
	}
	if err := a.comm.ReplaceRoomMirror(ctx, rows, epoch); err != nil {
		log.Printf("web: sync room mirror: %v", err)
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
	if _, err := a.store.CreateRoom(r.Context(), spaceForSession, name,
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
