package commserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"

	"github.com/Quest-ICT/ken/internal/comm"
	"github.com/Quest-ICT/ken/internal/ratelimit"
	"github.com/Quest-ICT/ken/internal/store"
)

// WaitingPath is where the mail-waiting check is served. cmd/ken routes it AROUND the
// outer IPGuard, because this endpoint carries its own guard (see ratelimit.ProbeGuard).
const WaitingPath = "/comm/waiting"

// waitingMaxBody bounds what is read from a request. The one valid body is
// {"station":"<16 chars>"}, under 40 bytes; the slack allows whitespace and nothing more.
const waitingMaxBody = 256

// stationIDLen is the length of every station id: store.CreateStation mints them with
// randBase62(16). The format check rejects anything else before any database is touched.
const stationIDLen = 16

// ProbeGuard is what the waiting handler needs from its rate limiter.
type ProbeGuard interface {
	Admit(*http.Request) ratelimit.Verdict
	Miss(key string)
}

// stationLookup is the one ken.db read the handler makes.
type stationLookup interface {
	StationByID(ctx context.Context, stationID string) (*store.Station, error)
}

// mailCounter is the one comm.db read the handler makes.
type mailCounter interface {
	StationHasMail(ctx context.Context, stationID string) (int, error)
}

// WaitingHandler serves POST /comm/waiting: "is mail waiting for this station?",
// answered yes or no, to anyone, without authentication.
//
// THE DESIGN IS VLAD'S (2026-11-17), and each restriction below is part of it:
//
//   - THE ANSWER IS ONE BOOLEAN. {"mail":true} or {"mail":false}. Never the messages,
//     never a count, never a sender. With no authentication, the answer must be worth
//     nothing beyond "go and poll".
//   - NO AUTHENTICATION, so it works with no MCP connection and no Anthropic proxy in the
//     path: a hook or a background loop with curl can ask. The first slice of making
//     Claude Code sessions independent of the claude.ai connector (task t-RTv78Q8R).
//   - THE CALLER SENDS THE STATION ID, never the session key. A station id is public among
//     Ken's users (comm_directory lists it); a session key selects a station's
//     conversation and must never be accepted by an unauthenticated endpoint, where it
//     could be tested by guessing.
//   - POST, with the id in the body: it stays out of URLs, so out of logs, histories and
//     caches, and browsers, link previews and scanners, which send GET, never get a lookup.
//   - THE BODY IS EXACTLY {"station":"<16 base-62 characters>"}. Anything else is refused
//     before a database is touched.
//   - AN ID THAT NAMES NO STATION GETS THE SAME {"mail":false} as a real station with no
//     mail, so the answer itself cannot be used to test whether an id exists.
//   - EVERYTHING THAT IS NOT A WELL-FORMED LOOKUP OF AN EXISTING STATION IS A MISS, and
//     misses get the address blocked from this endpoint (Vlad: "block anyone asking for
//     non-existing stations"). The block is the one place an existing id can be told
//     from a missing one: a prober can learn whether one candidate exists for ten
//     requests and a lockout. Against randomly minted 16-character ids, that confirms a
//     leaked id; it cannot find one.
//
// "MAIL WAITING" MEANS QUEUED AND STILL DELIVERABLE — comm.Store.StationHasMail, the same
// meaning as comm_channels' pending_total. Delivered-but-unacked mail is not "waiting":
// counting it would nag a session on every prompt while it works on a message it has.
// The check reads only: it delivers nothing and starts no expiry clock.
type WaitingHandler struct {
	guard    ProbeGuard
	stations stationLookup
	mail     mailCounter
}

// NewWaitingHandler builds the handler. guard may be a ReloadableProbeGuard holding nil
// (rate limiting disabled); stations and mail must be non-nil.
func NewWaitingHandler(guard ProbeGuard, st *store.Store, cs *comm.Store) *WaitingHandler {
	return &WaitingHandler{guard: guard, stations: st, mail: cs}
}

func (h *WaitingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	v := h.guard.Admit(r)
	switch v.Status {
	case 0:
	case http.StatusTooManyRequests:
		if v.RetryAfter > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(v.RetryAfter.Seconds())+1))
		}
		waitingRefuse(w, v.Status, "rate limited")
		return
	default:
		waitingRefuse(w, v.Status, "blocked: too many lookups that named no station")
		return
	}

	if r.Method != http.MethodPost {
		h.guard.Miss(v.Key)
		w.Header().Set("Allow", http.MethodPost)
		waitingJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "use POST"})
		return
	}
	stationID, ok := readWaitingBody(w, r)
	if !ok {
		h.guard.Miss(v.Key)
		waitingJSON(w, http.StatusBadRequest, map[string]string{
			"error": `the body must be exactly {"station":"<16-character station id>"}`})
		return
	}

	ctx := r.Context()
	if _, err := h.stations.StationByID(ctx, stationID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			h.guard.Miss(v.Key)
			waitingJSON(w, http.StatusOK, map[string]bool{"mail": false})
			return
		}
		log.Printf("comm/waiting: station lookup failed: %v", err)
		waitingJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "temporarily unavailable"})
		return
	}
	n, err := h.mail.StationHasMail(ctx, stationID)
	if err != nil {
		log.Printf("comm/waiting: mail count failed: %v", err)
		waitingJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "temporarily unavailable"})
		return
	}
	waitingJSON(w, http.StatusOK, map[string]bool{"mail": n > 0})
}

// readWaitingBody returns the station id from a body that is exactly one JSON object with
// one field, "station", holding a 16-character base-62 string. Anything else — another
// field, trailing data, an oversized body, a wrong length or alphabet — is false.
func readWaitingBody(w http.ResponseWriter, r *http.Request) (string, bool) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, waitingMaxBody))
	if err != nil {
		return "", false
	}
	var body struct {
		Station *string `json:"station"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || body.Station == nil {
		return "", false
	}
	if _, err := dec.Token(); err != io.EOF {
		return "", false // trailing data after the object
	}
	if !isStationID(*body.Station) {
		return "", false
	}
	return *body.Station, true
}

// isStationID reports whether s has the shape of a minted station id.
func isStationID(s string) bool {
	if len(s) != stationIDLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return true
}

// waitingJSON writes a small JSON answer that no cache may keep.
func waitingJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// waitingRefuse is a guard refusal: JSON like every other answer here (a text/plain
// refusal is what a client parsing JSON reports as "invalid content"), and the
// connection is closed so a flood is shed cheaply.
func waitingRefuse(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Connection", "close")
	waitingJSON(w, code, map[string]string{"error": msg})
}
