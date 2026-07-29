package store

import (
	"errors"
	"strings"
	"testing"
)

// Approving a station request is the moment the curation gate is exercised: an agent
// asked, a human decides, and the human types the name. The properties that matter
// are that the agent's suggested name has NO authority, and that the station and the
// resolution land together.
func TestApproveStationRequestTakesTheHumansNameAndResolvesAtomically(t *testing.T) {
	st, ctx, actorID := stationFixture(t)

	reqID, err := st.CreateStationRequest(ctx, 1, "tok_abc", "", "please-call-me-this", "run the deploys")
	if err != nil {
		t.Fatal(err)
	}

	station, err := st.ApproveStationRequest(ctx, reqID, "prod-ops", actorID)
	if err != nil {
		t.Fatal(err)
	}
	// The name_hint is advisory and must not leak into the created station: the whole
	// point of S3 is that naming is the human's act.
	if station.Name != "prod-ops" {
		t.Fatalf("station took the name %q; the human typed prod-ops and the agent's hint must carry no weight", station.Name)
	}
	// The request must be gone from the queue in the same breath. If it were not, the
	// operator would see a pending request whose station already exists and approve twice.
	pending, err := st.PendingStationRequests(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("request still pending after approval (%d) — creation and resolution must be one transaction", len(pending))
	}
	// Approving twice must be refused distinguishably, so the console can say "already
	// handled" rather than showing a broken link — the two-tabs-open case.
	if _, err := st.ApproveStationRequest(ctx, reqID, "prod-ops-2", actorID); !errors.Is(err, ErrRequestNotPending) {
		t.Fatalf("second approval returned %v, want ErrRequestNotPending", err)
	}
}

// A nameless approval must be refused rather than defaulted. A default would be the
// agent's hint or a generated string, and both would quietly undo the human-names-it rule.
func TestApproveRefusesAnEmptyName(t *testing.T) {
	st, ctx, actorID := stationFixture(t)
	reqID, err := st.CreateStationRequest(ctx, 1, "tok_abc", "", "hint", "purpose")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ApproveStationRequest(ctx, reqID, "   ", actorID); err == nil {
		t.Fatal("approved a station with a blank name — there must be no default, the human types it")
	}
}

// A denial without a reason is refused for the same reason a task cannot be closed
// without a resolution: the next request arrives to a human who would otherwise have
// to re-decide blind.
func TestDenyRequiresAReasonAndClearsTheQueue(t *testing.T) {
	st, ctx, actorID := stationFixture(t)
	reqID, err := st.CreateStationRequest(ctx, 1, "tok_abc", "", "hint", "purpose")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DenyStationRequest(ctx, reqID, "", actorID); err == nil {
		t.Fatal("denied with no reason — the reason is what stops the next human re-deciding blind")
	}
	if err := st.DenyStationRequest(ctx, reqID, "not needed, use prod-ops", actorID); err != nil {
		t.Fatal(err)
	}
	pending, err := st.PendingStationRequests(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("denied request still in the queue (%d)", len(pending))
	}
}

// The transfer is the answer to "a session is gone and its work should not be". The
// property under test is the refusal: every station is expected to keep a `handoff`
// page, so a handoff-on-handoff collision is the COMMON case, and silently merging
// would destroy exactly the page a human reaches for when reconstructing a session.
func TestTransferRefusesNameCollisionsAndMovesNothing(t *testing.T) {
	st, ctx, actorID := stationFixture(t)
	from, err := st.CreateStation(ctx, 1, "old-box", "", actorID)
	if err != nil {
		t.Fatal(err)
	}
	to, err := st.CreateStation(ctx, 1, "new-box", "", actorID)
	if err != nil {
		t.Fatal(err)
	}
	lim := DefaultStationNoteLimits()

	for _, s := range []*Station{from, to} {
		if _, err := st.WriteStationNote(ctx, lim, s.StationID, "handoff", "Handoff", "state of play", nil, "replace", -1, "tok", actorID, false); err != nil {
			t.Fatal(err)
		}
	}
	// Only the source has this one; it must NOT move, because the whole transfer is refused.
	if _, err := st.WriteStationNote(ctx, lim, from.StationID, "runbook", "Runbook", "how to deploy", nil, "replace", -1, "tok", actorID, false); err != nil {
		t.Fatal(err)
	}

	_, err = st.TransferStationAssets(ctx, from.StationID, to.StationID, true, true, true)
	var collision *ErrTransferCollision
	if !errors.As(err, &collision) {
		t.Fatalf("transfer returned %v, want an ErrTransferCollision naming the clash", err)
	}
	// The names must come back: a bare refusal leaves the human with nothing to act on.
	if len(collision.Colliding) != 1 || collision.Colliding[0] != "handoff" {
		t.Fatalf("collision reported %v, want exactly [handoff]", collision.Colliding)
	}
	if !strings.Contains(collision.Error(), "handoff") {
		t.Fatalf("error text does not name the colliding page: %s", collision.Error())
	}

	// Nothing moved. A half-applied transfer is worse than none: the human would have
	// to work out which classes went across.
	notes, err := st.ListStationNotes(ctx, from.StationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 2 {
		t.Fatalf("source lost pages to a REFUSED transfer: has %d, want 2", len(notes))
	}
}

// The clean path: distinct names move, and tasks — keyed by opaque id rather than a
// name — always move because they cannot collide.
func TestTransferMovesAssetsWhenNamesAreDistinct(t *testing.T) {
	st, ctx, actorID := stationFixture(t)
	from, err := st.CreateStation(ctx, 1, "old-box", "", actorID)
	if err != nil {
		t.Fatal(err)
	}
	to, err := st.CreateStation(ctx, 1, "new-box", "", actorID)
	if err != nil {
		t.Fatal(err)
	}
	notesLim := DefaultStationNoteLimits()
	if _, err := st.WriteStationNote(ctx, notesLim, from.StationID, "runbook", "Runbook", "how to deploy", nil, "replace", -1, "tok", actorID, false); err != nil {
		t.Fatal(err)
	}
	taskLim := DefaultStationTaskLimits()
	if _, _, err := st.AddStationTask(ctx, taskLim,
		StationTask{StationID: from.StationID, Text: "rotate the key", BlockedOn: "human"}, "tok", actorID, false); err != nil {
		t.Fatal(err)
	}

	res, err := st.TransferStationAssets(ctx, from.StationID, to.StationID, true, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Notes != 1 || res.Tasks != 1 {
		t.Fatalf("moved notes=%d tasks=%d, want 1 and 1", res.Notes, res.Tasks)
	}

	got, err := st.ListStationNotes(ctx, to.StationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Key != "runbook" {
		t.Fatalf("destination notes = %+v, want the runbook page", got)
	}
	left, err := st.ListStationNotes(ctx, from.StationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("source still holds %d page(s) after a move", len(left))
	}
	// A move, not a copy: the task must be on the destination and gone from the source.
	open, _, err := st.ListStationTasks(ctx, taskLim, to.StationID, "open", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 1 {
		t.Fatalf("destination has %d open task(s), want 1", len(open))
	}
}

// Usage is what the console shows against the caps, so it must count the station's own
// assets and no one else's — the failure that would make a full station look empty.
func TestAssetUsageCountsOnlyThisStation(t *testing.T) {
	st, ctx, actorID := stationFixture(t)
	mine, err := st.CreateStation(ctx, 1, "mine", "", actorID)
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.CreateStation(ctx, 1, "other", "", actorID)
	if err != nil {
		t.Fatal(err)
	}
	notesLim := DefaultStationNoteLimits()
	if _, err := st.WriteStationNote(ctx, notesLim, mine.StationID, "a", "A", "body", nil, "replace", -1, "tok", actorID, false); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"x", "y"} {
		if _, err := st.WriteStationNote(ctx, notesLim, other.StationID, k, k, "body", nil, "replace", -1, "tok", actorID, false); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.IssueStationKey(ctx, actorID, mine.StationID, "laptop", []string{"station"}); err != nil {
		t.Fatal(err)
	}

	u, err := st.StationAssetUsage(ctx, mine.StationID)
	if err != nil {
		t.Fatal(err)
	}
	if u.Notes != 1 {
		t.Fatalf("counted %d notes for this station, want 1 — the other station's pages must not be included", u.Notes)
	}
	if u.Keys != 1 {
		t.Fatalf("counted %d live keys, want 1", u.Keys)
	}
	if u.NoteBytes == 0 {
		t.Fatal("note bytes reported as 0 though a page with a body exists")
	}
}
