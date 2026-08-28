package store

import (
	"errors"
	"strings"
	"testing"
)

// *** THE ABANDONED-STATION DEAD END, AND THE ONLY DOOR OUT OF IT. ***
//
// A session adopts a station only while `session_key IS NULL` — deliberately, because a key that
// could take a claimed post would be a credential, and it is documented as selecting rather than
// authorising. The consequence nobody had answered: when a conversation dies, the station it
// claimed is SEALED. Its notes, tasks, locker and vault are intact and unreachable forever.
//
// This asserts the recovery as a human would experience it, end to end: point the station at a
// new conversation from the console, and that conversation's ordinary claim call lands in it —
// with the contents still there. A test that only checked the column would pass while the claim
// path still refused, which is the whole failure.
func TestReassignLetsANewConversationTakeOverAnAbandonedStation(t *testing.T) {
	st, ctx, actorID := stationFixture(t)

	// The dead conversation's station, with work in it.
	original, _, err := st.ClaimStationForSession(ctx, "conv-dead", "old laptop", actorID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.WriteStationNote(ctx, DefaultStationNoteLimits(), original.StationID,
		"handoff", "Handoff", "where I left off", nil, "replace", -1, "tok", actorID, false); err != nil {
		t.Fatal(err)
	}

	// CONTROL FIRST: without a reassign, the new conversation gets a DIFFERENT station. This is
	// the dead end being demonstrated, and without it the assertion below proves nothing.
	stranded, created, err := st.ClaimStationForSession(ctx, "conv-new", "chat", actorID, "")
	if err != nil {
		t.Fatal(err)
	}
	if !created || stranded.StationID == original.StationID {
		t.Fatalf("a fresh conversation reached the abandoned station with no human action "+
			"(created=%v, id=%q) — the claim path is not refusing what it is documented to refuse",
			created, stranded.StationID)
	}

	// THE HUMAN ACTS. One console form, one pasted string, nothing secret.
	if _, err := st.ReassignStationToSession(ctx, original.StationID, "conv-new"); err != nil {
		t.Fatal(err)
	}

	got, created, err := st.ClaimStationForSession(ctx, "conv-new", "chat", actorID, "")
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Error("the takeover minted a NEW station instead of returning the recovered one")
	}
	if got.StationID != original.StationID {
		t.Fatalf("the session landed in %q, want the recovered station %q", got.StationID, original.StationID)
	}
	// And the work is what it came for.
	notes, err := st.ListStationNotes(ctx, got.StationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 || notes[0].Key != "handoff" {
		t.Errorf("the recovered station holds %v, want the handoff page that made it worth "+
			"recovering", notes)
	}
}

// AN EMPTY KEY RELEASES, and that is not a convenience. Without it a station pointed at the
// wrong conversation is stuck to it permanently — the same dead end, re-created by the fix for it.
func TestReassignWithAnEmptyKeyReturnsTheStationToThePool(t *testing.T) {
	st, ctx, actorID := stationFixture(t)
	original, _, err := st.ClaimStationForSession(ctx, "conv-dead", "old laptop", actorID, "")
	if err != nil {
		t.Fatal(err)
	}

	released, err := st.ReassignStationToSession(ctx, original.StationID, "")
	if err != nil {
		t.Fatal(err)
	}
	if released.Station.SessionKey != "" {
		t.Errorf("the station still answers to %q after being released", released.Station.SessionKey)
	}
	// UNCLAIMED MEANS ADOPTABLE: the adopt path is the thing that was blocked, so assert it works.
	got, created, err := st.ClaimStationForSession(ctx, "conv-next", "chat", actorID, original.StationID)
	if err != nil {
		t.Fatal(err)
	}
	if created || got.StationID != original.StationID {
		t.Errorf("a released station could not be adopted (created=%v, id=%q, want %q)",
			created, got.StationID, original.StationID)
	}
	// And the old key no longer reaches it — a release that left the key working would be a fork,
	// with two conversations writing to one post and neither told.
	if _, err := st.StationBySessionKey(ctx, "conv-dead"); !errors.Is(err, ErrNotFound) {
		t.Errorf("the released key still resolves to a station: %v", err)
	}
}

// *** THE KEY IS TAKEN FROM WHOEVER HOLDS IT, AND THE DISPLACEMENT IS REPORTED. ***
//
// This exact case broke the first implementation, which refused it — and it is not an edge case,
// it is the MAIN PATH. The human asks a chat session for its conversation key; that session has
// already called station_me, so a fresh empty station already answers to the key it just
// invented. Refusing meant the recovery flow failed on its own happy path with "that key is in
// use". The test above hit it on the first run.
//
// Nothing is destroyed by taking it: the displaced station keeps everything and stays listed.
// So the safety is DISCLOSURE — the result must name what it displaced, or this is a silent steal.
func TestReassignTakesTheKeyFromTheStationHoldingItAndSaysSo(t *testing.T) {
	st, ctx, actorID := stationFixture(t)
	abandoned, _, err := st.ClaimStationForSession(ctx, "conv-dead", "old laptop", actorID, "")
	if err != nil {
		t.Fatal(err)
	}
	// The fresh station the taking-over session already minted for itself.
	fresh, _, err := st.ClaimStationForSession(ctx, "conv-new", "chat", actorID, "")
	if err != nil {
		t.Fatal(err)
	}

	res, err := st.ReassignStationToSession(ctx, abandoned.StationID, "conv-new")
	if err != nil {
		t.Fatalf("the main recovery path was refused: %v", err)
	}
	if res.TakenFromID != fresh.StationID {
		t.Errorf("the result says the key came from %q, want the station that held it, %q — "+
			"an unreported displacement is a silent steal", res.TakenFromID, fresh.StationID)
	}
	if !strings.Contains(res.TakenFromName, "chat") {
		t.Errorf("the result names %q, which the operator cannot find in the list", res.TakenFromName)
	}

	// THE DISPLACED STATION IS UNCLAIMED, NOT DAMAGED: nothing else may reach it by the old key,
	// and it is still there to be reassigned back.
	if got, err := st.StationBySessionKey(ctx, "conv-new"); err != nil {
		t.Fatal(err)
	} else if got.StationID != abandoned.StationID {
		t.Errorf("the key still resolves to %q, want the station it was moved to", got.StationID)
	}
	if still, err := st.StationByID(ctx, fresh.StationID); err != nil {
		t.Fatalf("the displaced station is gone: %v", err)
	} else if still.SessionKey != "" {
		t.Errorf("two stations answer to one key (%q still holds %q)", still.StationID, still.SessionKey)
	}
}

// RE-POINTING A STATION AT THE KEY IT ALREADY HAS IS NOT A COLLISION WITH ITSELF. An operator
// who opens the form, reads the prefilled key and clicks Reassign must not be told the key is
// taken by the very station they are editing.
func TestReassignToItsOwnKeyIsANoOp(t *testing.T) {
	st, ctx, actorID := stationFixture(t)
	mine, _, err := st.ClaimStationForSession(ctx, "conv-a", "mine", actorID, "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.ReassignStationToSession(ctx, mine.StationID, "conv-a")
	if err != nil {
		t.Fatalf("a station could not be reassigned to the key it already holds: %v", err)
	}
	if got.Station.SessionKey != "conv-a" {
		t.Errorf("the station answers to %q afterwards, want conv-a", got.Station.SessionKey)
	}
}

// ARCHIVE IS NOT A SUGGESTION. Archiving severs live endpoints and StationExists refuses archived
// ids; re-staffing one from this form would leave an operator watching a session work inside a
// station they had shut, with nothing to explain it.
func TestReassignRefusesAnArchivedStation(t *testing.T) {
	st, ctx, actorID := stationFixture(t)
	dead, _, err := st.ClaimStationForSession(ctx, "conv-dead", "old laptop", actorID, "")
	if err != nil {
		t.Fatal(err)
	}
	// CONTROL: it is reassignable BEFORE archiving, so the refusal below is about the archive.
	if _, err := st.ReassignStationToSession(ctx, dead.StationID, "conv-x"); err != nil {
		t.Fatalf("an active station refused a reassign: %v", err)
	}
	if err := st.ArchiveStation(ctx, dead.StationID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReassignStationToSession(ctx, dead.StationID, "conv-y"); err == nil {
		t.Error("an ARCHIVED station accepted a reassign — archive would be advisory")
	}
}
