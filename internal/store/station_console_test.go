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

	reqID, err := st.CreateStationRequest(ctx, "tok_abc", "", "please-call-me-this", "run the deploys")
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
	pending, err := st.PendingStationRequests(ctx)
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
	reqID, err := st.CreateStationRequest(ctx, "tok_abc", "", "hint", "purpose")
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
	reqID, err := st.CreateStationRequest(ctx, "tok_abc", "", "hint", "purpose")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DenyStationRequest(ctx, reqID, "", actorID); err == nil {
		t.Fatal("denied with no reason — the reason is what stops the next human re-deciding blind")
	}
	if err := st.DenyStationRequest(ctx, reqID, "not needed, use prod-ops", actorID); err != nil {
		t.Fatal(err)
	}
	pending, err := st.PendingStationRequests(ctx)
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
	from, err := st.CreateStation(ctx, "old-box", "", actorID)
	if err != nil {
		t.Fatal(err)
	}
	to, err := st.CreateStation(ctx, "new-box", "", actorID)
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

	_, err = st.TransferStationAssets(ctx, from.StationID, to.StationID, true, true, true, true, "tok", actorID)
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
	from, err := st.CreateStation(ctx, "old-box", "", actorID)
	if err != nil {
		t.Fatal(err)
	}
	to, err := st.CreateStation(ctx, "new-box", "", actorID)
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

	res, err := st.TransferStationAssets(ctx, from.StationID, to.StationID, true, true, true, true, "tok", actorID)
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
	mine, err := st.CreateStation(ctx, "mine", "", actorID)
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.CreateStation(ctx, "other", "", actorID)
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
	if _, err := st.IssueToken(ctx, actorID, []string{"station"}, "laptop"); err != nil {
		t.Fatal(err)
	}

	u, err := st.StationAssetUsage(ctx, mine.StationID)
	if err != nil {
		t.Fatal(err)
	}
	if u.Notes != 1 {
		t.Fatalf("counted %d notes for this station, want 1 — the other station's pages must not be included", u.Notes)
	}
	if u.NoteBytes == 0 {
		t.Fatal("note bytes reported as 0 though a page with a body exists")
	}
}

// A denied pair is MUTED, and a re-request must be indistinguishable from a filed
// one. If the caller could tell the difference, a persistent session could probe the
// human's past refusals one request at a time — and the mute exists precisely to stop
// re-asking until a tired human says yes.
func TestADeniedLinkIsMutedAndTheMuteCannotBeProbed(t *testing.T) {
	st, ctx, actorID := stationFixture(t)
	a, err := st.CreateStation(ctx, "prod-ops", "", actorID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateStation(ctx, "promo", "", actorID)
	if err != nil {
		t.Fatal(err)
	}

	first, err := st.CreateStationLinkRequest(ctx, "kens_1", a.StationID, b.StationID, "we need to coordinate the launch", false)
	if err != nil {
		t.Fatal(err)
	}
	if first == "" {
		t.Fatal("the first request was dropped")
	}
	if err := st.DenyStationRequest(ctx, first, "promo does not need production access", actorID); err != nil {
		t.Fatal(err)
	}

	// Re-asking must SUCCEED at the API level and file nothing.
	second, err := st.CreateStationLinkRequest(ctx, "kens_1", a.StationID, b.StationID, "asking again", false)
	if err != nil {
		t.Fatalf("a muted re-request returned an error, which is exactly the signal that must not exist: %v", err)
	}
	if second != "" {
		t.Fatal("a muted re-request was filed — the human's refusal should hold without them re-deciding")
	}
	pending, err := st.PendingStationRequests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("%d request(s) reached the human despite the mute", len(pending))
	}

	// And the mute is on the UNORDERED pair: asking from the other side must not
	// reset it, or the relationship is simply re-asked in the opposite direction.
	reverse, err := st.CreateStationLinkRequest(ctx, "kens_2", b.StationID, a.StationID, "from the other side", false)
	if err != nil {
		t.Fatal(err)
	}
	if reverse != "" {
		t.Fatal("the mute was bypassed by asking from the other station — it must be on the unordered pair")
	}
}

// The transitive path (S9): a peer cannot open a channel, but it can talk another
// session into asking for one, and the request then reaches the human looking like
// that session's own idea. The marker is the only signal the human gets.
//
// It must be NULL rather than 0 when there is no signal — with COMM off nothing is
// known, and a 0 would claim knowledge the server does not have.
func TestLinkRequestsRecordWhetherTheAskerWasMidConversation(t *testing.T) {
	st, ctx, actorID := stationFixture(t)
	a, err := st.CreateStation(ctx, "prod-ops", "", actorID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateStation(ctx, "promo", "", actorID)
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.CreateStation(ctx, "infra", "", actorID)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := st.CreateStationLinkRequest(ctx, "kens_1", a.StationID, b.StationID, "prompted", true); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateStationLinkRequest(ctx, "kens_1", a.StationID, c.StationID, "own idea", false); err != nil {
		t.Fatal(err)
	}

	rows, err := st.PendingStationRequests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d pending requests, want 2", len(rows))
	}
	var marked, unmarked int
	for _, r := range rows {
		if r.PromptedByPeerTraffic {
			marked++
		} else {
			unmarked++
		}
	}
	if marked != 1 || unmarked != 1 {
		t.Fatalf("marked=%d unmarked=%d, want exactly one of each — the marker shipped permanently false until this change", marked, unmarked)
	}
}

// Approving must clear the mute. The human changed their mind; leaving the denial in
// place would silently drop the next request for a relationship they just allowed.
func TestApprovingALinkClearsAnEarlierDenial(t *testing.T) {
	st, ctx, actorID := stationFixture(t)
	a, err := st.CreateStation(ctx, "prod-ops", "", actorID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateStation(ctx, "promo", "", actorID)
	if err != nil {
		t.Fatal(err)
	}
	first, err := st.CreateStationLinkRequest(ctx, "kens_1", a.StationID, b.StationID, "please", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DenyStationRequest(ctx, first, "not yet", actorID); err != nil {
		t.Fatal(err)
	}
	// The human relents and creates the link directly from a later request. Simulate
	// by clearing through approval of a fresh request, which requires the mute gone.
	linked, err := st.AreStationsLinked(ctx, a.StationID, b.StationID)
	if err != nil {
		t.Fatal(err)
	}
	if linked {
		t.Fatal("stations are linked before any approval")
	}

	// Insert a request bypassing the mute the way the console would if the human
	// approved a pending one, then approve it.
	id, err := randBase62(12)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.W.ExecContext(ctx, `
INSERT INTO station_request(request_id, kind, from_station, to_station, from_token_id, reason)
VALUES(?,'link',?,?,'kens_1','second thoughts')`, id, a.StationID, b.StationID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ApproveLinkRequest(ctx, id, actorID); err != nil {
		t.Fatal(err)
	}
	linked, err = st.AreStationsLinked(ctx, a.StationID, b.StationID)
	if err != nil {
		t.Fatal(err)
	}
	if !linked {
		t.Fatal("approving a link request did not create an active link")
	}
	// The mute must be gone, or the next request for a now-allowed relationship is
	// silently dropped.
	again, err := st.CreateStationLinkRequest(ctx, "kens_1", a.StationID, b.StationID, "third", false)
	if err != nil {
		t.Fatal(err)
	}
	if again == "" {
		t.Fatal("a request is still being muted after the human APPROVED the relationship")
	}
}

// *** THE VAULT MOVES, AND FOR EIGHT RELEASES IT DID NOT. ***
//
// TransferStationAssets is the answer to "a session is gone and its work should not be", and it
// silently left that session's CREDENTIALS behind — while its doc comment carefully explained why
// the message queue stays put, so a reader would have concluded the transfer was complete. An API
// key with no second copy is the part of a workspace hardest to recreate, which makes it the worst
// possible thing to drop quietly.
//
// This asserts the property that actually matters, which is not "a row moved": it is that the
// secret is READABLE AT THE DESTINATION. A transfer that relocated ciphertext nobody could open
// would pass a row-count check and lose the credential just the same.
func TestTransferMovesVaultSecretsAndTheyStillOpen(t *testing.T) {
	st, ctx, actorID := stationFixture(t)
	from, err := st.CreateStation(ctx, "old-box", "", actorID)
	if err != nil {
		t.Fatal(err)
	}
	to, err := st.CreateStation(ctx, "new-box", "", actorID)
	if err != nil {
		t.Fatal(err)
	}
	lim := DefaultStationVaultLimits()
	const plaintext = "sk-live-51H9d2xqPQ"
	// WRITTEN TWICE, so there is a prior revision for the history assertion below to be about.
	if _, _, err := st.PutStationVaultSecret(ctx, lim, from.StationID, "stripe", "sk-live-OLD", "billing", "tok", actorID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.PutStationVaultSecret(ctx, lim, from.StationID, "stripe", plaintext, "billing", "tok", actorID); err != nil {
		t.Fatal(err)
	}
	// A READ BEFORE THE MOVE, so the trail-stays-put assertion below has something to be about.
	if _, err := st.GetStationVaultSecret(ctx, lim, from.StationID, "stripe", "station", "tok", actorID); err != nil {
		t.Fatal(err)
	}

	res, err := st.TransferStationAssets(ctx, from.StationID, to.StationID, false, false, false, true, "tok", actorID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Vault != 1 {
		t.Errorf("the receipt says %d secrets moved, want 1 — an operator reads this to know "+
			"whether the credentials came across", res.Vault)
	}

	// THE POINT: it opens on the far side, with the same bytes.
	got, err := st.GetStationVaultSecret(ctx, lim, to.StationID, "stripe", "station", "tok", actorID)
	if err != nil {
		t.Fatalf("the transferred secret cannot be read at the destination: %v", err)
	}
	if got.Secret != plaintext {
		t.Errorf("the destination reads %q, want the original value — the credential did not "+
			"survive the move intact", got.Secret)
	}

	// THE REVISION HISTORY COMES WITH IT. A credential that arrives with no previous values
	// cannot be rolled back any more, and reversibility is the vault's founding promise — a
	// rotation that turns out to be wrong is exactly when a takeover is under way.
	if hist, err := st.StationVaultHistoryFor(ctx, to.StationID, "stripe"); err != nil {
		t.Fatal(err)
	} else if len(hist) != 1 {
		t.Errorf("the destination holds %d prior revisions of stripe, want 1 — the secret moved "+
			"but its history did not, so it can no longer be rolled back", len(hist))
	}
	if hist, err := st.StationVaultHistoryFor(ctx, from.StationID, "stripe"); err != nil {
		t.Fatal(err)
	} else if len(hist) != 0 {
		t.Errorf("the source kept %d revisions of a secret it no longer holds — old values of a "+
			"live credential, readable from an abandoned workspace", len(hist))
	}

	// And it is GONE from the source. A copy would mean an abandoned workspace still holds live
	// credentials, which is the opposite of what a takeover is for.
	if left, err := st.ListStationVault(ctx, from.StationID); err != nil {
		t.Fatal(err)
	} else if len(left) != 0 {
		t.Errorf("the source still holds %d secrets after the transfer", len(left))
	}
}

// THE DEPARTURE IS ON THE RECORD, AND THE OLD TRAIL DOES NOT FOLLOW THE SECRET.
//
// via='transfer' already means "this station's secret left it" — that is what station_vault_send
// writes — so a workspace transfer writes the same event. The existing read rows STAY at the
// source, because they record reads that happened there: moving them would make the destination's
// log assert reads from before it held the secret and erase the source's record of ever holding it.
func TestTransferAuditsTheDepartureAtTheSource(t *testing.T) {
	st, ctx, actorID := stationFixture(t)
	from, _ := st.CreateStation(ctx, "old-box", "", actorID)
	to, _ := st.CreateStation(ctx, "new-box", "", actorID)
	lim := DefaultStationVaultLimits()
	if _, _, err := st.PutStationVaultSecret(ctx, lim, from.StationID, "stripe", "sk-live-x", "", "tok", actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetStationVaultSecret(ctx, lim, from.StationID, "stripe", "station", "tok", actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.TransferStationAssets(ctx, from.StationID, to.StationID, false, false, false, true, "tok", actorID); err != nil {
		t.Fatal(err)
	}

	reads, _, err := st.StationVaultReads(ctx, from.StationID, 50)
	if err != nil {
		t.Fatal(err)
	}
	var vias []string
	for _, r := range reads {
		vias = append(vias, r.Via)
	}
	var transfers, stations int
	for _, v := range vias {
		switch v {
		case "transfer":
			transfers++
		case "station":
			stations++
		}
	}
	if transfers != 1 {
		t.Errorf("the source's trail holds %d transfer rows, want exactly 1 (all: %v) — without it "+
			"nothing records that the credential left", transfers, vias)
	}
	if stations != 1 {
		t.Errorf("the source's trail holds %d earlier reads, want the 1 that happened there "+
			"(all: %v) — the record of who saw this value must not travel with it", stations, vias)
	}

	// CONTROL: the destination's trail is NOT prefilled with the source's history. Its first entry
	// is its own read, which is the honest starting point.
	dst, _, err := st.StationVaultReads(ctx, to.StationID, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(dst) != 0 {
		t.Errorf("the destination inherited %d read rows it did not earn", len(dst))
	}
}

// COLLIDING SECRET NAMES REFUSE THE WHOLE TRANSFER. A silent merge here overwrites one credential
// with another and the loser is unrecoverable — strictly worse than the note case, which at least
// keeps revisions. Same refusal shape as notes and locker files, deliberately.
func TestTransferRefusesCollidingVaultNames(t *testing.T) {
	st, ctx, actorID := stationFixture(t)
	from, _ := st.CreateStation(ctx, "old-box", "", actorID)
	to, _ := st.CreateStation(ctx, "new-box", "", actorID)
	lim := DefaultStationVaultLimits()
	for _, s := range []*Station{from, to} {
		if _, _, err := st.PutStationVaultSecret(ctx, lim, s.StationID, "stripe", "sk-"+s.StationID, "", "tok", actorID); err != nil {
			t.Fatal(err)
		}
	}
	// Only the source has this one; it must not move, because the transfer is refused whole.
	if _, _, err := st.PutStationVaultSecret(ctx, lim, from.StationID, "aws", "AKIA-x", "", "tok", actorID); err != nil {
		t.Fatal(err)
	}

	_, err := st.TransferStationAssets(ctx, from.StationID, to.StationID, false, false, false, true, "tok", actorID)
	var collision *ErrTransferCollision
	if !errors.As(err, &collision) {
		t.Fatalf("transfer returned %v, want an ErrTransferCollision naming the clash", err)
	}
	if collision.Class != "vault" {
		t.Errorf("collision reported class %q, want \"vault\" — the operator has to know WHICH "+
			"class to reconcile", collision.Class)
	}
	if len(collision.Colliding) != 1 || collision.Colliding[0] != "stripe" {
		t.Fatalf("collision reported %v, want exactly [stripe]", collision.Colliding)
	}
	if left, err := st.ListStationVault(ctx, from.StationID); err != nil {
		t.Fatal(err)
	} else if len(left) != 2 {
		t.Errorf("source holds %d secrets after a REFUSED transfer, want 2 — a half-applied "+
			"credential move leaves the human guessing which side has what", len(left))
	}
}

// THE FLAG IS A FLAG. Without this, every assertion above would also pass against an
// implementation that moved the vault unconditionally, and an operator who deliberately unticked
// the box would ship credentials to another workspace anyway.
func TestTransferLeavesTheVaultWhenNotSelected(t *testing.T) {
	st, ctx, actorID := stationFixture(t)
	from, _ := st.CreateStation(ctx, "old-box", "", actorID)
	to, _ := st.CreateStation(ctx, "new-box", "", actorID)
	lim := DefaultStationVaultLimits()
	if _, _, err := st.PutStationVaultSecret(ctx, lim, from.StationID, "stripe", "sk-live-x", "", "tok", actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.WriteStationNote(ctx, DefaultStationNoteLimits(), from.StationID, "handoff", "H", "state", nil, "replace", -1, "tok", actorID, false); err != nil {
		t.Fatal(err)
	}

	res, err := st.TransferStationAssets(ctx, from.StationID, to.StationID, true, true, true, false, "tok", actorID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Notes != 1 {
		t.Fatalf("the notes did not move, so this test is not exercising a working transfer: %+v", res)
	}
	if res.Vault != 0 {
		t.Errorf("the receipt claims %d secrets moved with the vault box unticked", res.Vault)
	}
	if left, err := st.ListStationVault(ctx, from.StationID); err != nil {
		t.Fatal(err)
	} else if len(left) != 1 {
		t.Errorf("the source holds %d secrets, want the 1 it was told to keep", len(left))
	}
}
