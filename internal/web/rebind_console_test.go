package web

import (
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/Quest-ICT/ken/internal/comm"
)

// credentialsBlock and endpointsBlock cut /comm into the two regions these tests reason about.
//
// WHY EVERY ASSERTION BELOW IS SCOPED. The first version of this file asserted against the whole
// page, and an adversarial review of the shipping commit showed what that bought: the check that
// the two BULK pickers render options was satisfied by the per-endpoint pickers in the table
// underneath, so both bulk selects could render with zero options and the suite stayed green;
// and the check that the new "Bound by" column renders was satisfied by the same key id
// appearing in the credentials block above it. Both were verified by deleting the markup.
//
// This is the same mistake as matching a flash against the page's own help text, one layer out:
// an assertion satisfied by a DIFFERENT part of the page measures the page, not the thing.
func credentialsBlock(t *testing.T, body string) string {
	t.Helper()
	return regionBetween(t, body, "Credentials these endpoints depend on", "Registered sessions")
}

func endpointsBlock(t *testing.T, body string) string {
	t.Helper()
	i := strings.Index(body, "Registered sessions")
	if i < 0 {
		t.Fatal("no endpoints section on /comm — the page did not render what these tests read")
	}
	return body[i:]
}

func regionBetween(t *testing.T, body, from, to string) string {
	t.Helper()
	i := strings.Index(body, from)
	if i < 0 {
		t.Fatalf("%q is not on the page — the section these assertions scope to did not render", from)
	}
	j := strings.Index(body[i:], to)
	if j < 0 {
		t.Fatalf("%q does not follow %q — the page layout changed and this helper now returns the wrong region", to, from)
	}
	return body[i : i+j]
}

// keyIDOf turns a minted station-key secret into the token id the binding column stores.
func keyIDOf(secret string) string {
	return strings.Split(strings.TrimPrefix(secret, "kens_"), "_")[0]
}

// THE SECOND WELD, END TO END FROM THE CONSOLE.
//
// A bound endpoint answers to TWO credentials. `token_id` says which token may drive it;
// `bound_by_station_key_id` says which station key authorised the binding, and that column is
// checked at USE on every call — a revoked key, or a missing one, severs the session. Retiring
// a station key therefore kills its bound endpoints exactly as retiring their comm token does,
// through a column the console never rendered and docs/IDENTITY.md §9.3 did not name.
//
// Moving it must change nothing else: same id, same secret, same station, same mail.
func TestRebindingAnEndpointFromTheConsole(t *testing.T) {
	st, ctx, cli, base, actor := stationsHarnessWithComm(t)
	cs := commOf(t)

	s, err := st.CreateStation(ctx, spaceForSession, "prod-ops", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	oldKey, err := st.IssueStationKey(ctx, actor, s.StationID, "laptop", []string{"station"})
	if err != nil {
		t.Fatal(err)
	}
	newKey, err := st.IssueStationKey(ctx, actor, s.StationID, "replacement", []string{"station"})
	if err != nil {
		t.Fatal(err)
	}
	oldID, newID := keyIDOf(oldKey), keyIDOf(newKey)

	// THE OWNING TOKEN AND THE BINDING KEY ARE DIFFERENT CREDENTIALS, and this fixture has to
	// make them different VALUES or the assertions below cannot tell the two columns apart. An
	// earlier version registered the endpoint under the station key itself, so "Bound by shows
	// oldID" was satisfied by the Owned by cell — deleting the whole Bound by column left this
	// test green. Production's shape is the honest one: ken-prod-ops runs tok=jMl4ZNH4q73E with
	// key=86rzqnM35CCU.
	ownerTok, err := st.IssueToken(ctx, actor, []string{"comm"}, "machine")
	if err != nil {
		t.Fatal(err)
	}
	ownerID := strings.SplitN(strings.TrimPrefix(ownerTok, "ken_"), "_", 2)[0]

	ep, secret, err := cs.RegisterEndpoint(ctx, comm.Owner{TokenID: ownerID, ActorID: actor, SpaceID: spaceForSession}, "session", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := cs.BindEndpointToStation(ctx, ep.EndpointID, s.StationID, oldID); err != nil {
		t.Fatal(err)
	}

	// THE BINDING KEY IS ON THE PAGE. It never was, so the credential that severs a session
	// on its next call was invisible to the only person who can act on it.
	body := get(t, cli, base+"/comm")
	rows := endpointsBlock(t, body)
	// THE DATA CELLS ONLY — everything from the endpoint id to the row's first <form>. The key
	// also travels in the re-bind form's `from_key` hidden input, so an assertion over the whole
	// row is satisfied by the control rather than by the column: deleting the Bound by cell
	// outright left this green until the region was narrowed. Third instance in this file of one
	// mistake — an assertion answered by a neighbour — which is why the helpers exist.
	cells := regionBetween(t, rows, ep.EndpointID, "<form")
	if !strings.Contains(cells, oldID) {
		t.Fatal("the endpoint's binding station key is not in the Bound by column — the second weld is " +
			"invisible on the row it belongs to")
	}
	if !strings.Contains(rows, `action="/comm/endpoints/`+ep.EndpointID+`/rebind"`) {
		t.Fatal("no re-bind control on this endpoint's row")
	}
	// AND THE PICKER DOES NOT OFFER THE KEY IT IS ALREADY BOUND BY. That is the no-op-success
	// defect this release fixed on the Re-point control; nothing stopped it being re-created
	// on the new one, because the filter had no test.
	pick := regionBetween(t, rows, `action="/comm/endpoints/`+ep.EndpointID+`/rebind"`, "</form>")
	if strings.Contains(pick, `<option value="`+oldID+`"`) {
		t.Error("the re-bind picker offers the key this endpoint is already bound by — submitting it " +
			"moves nothing and flashes success")
	}
	if !strings.Contains(pick, `<option value="`+newID+`"`) {
		t.Fatal("the re-bind picker has no option at all — this test cannot tell a filter from an empty picker")
	}

	csrf := extract(t, cli, base+"/comm", `name="csrf" value="([^"]+)"`)
	postForm(t, cli, base+"/comm/endpoints/"+ep.EndpointID+"/rebind",
		url.Values{"csrf": {csrf}, "from_key": {oldID}, "to_key": {newID}})

	got := endpointOf(t, cs, ep.EndpointID)
	if got.BoundByStationKeyID != newID {
		t.Fatalf("binding key = %q, want %q", got.BoundByStationKeyID, newID)
	}
	if got.StationID != s.StationID {
		t.Fatalf("the station moved: %q -> %q — a re-bind must not change which station this reads for", s.StationID, got.StationID)
	}
	if got.EndpointID != ep.EndpointID {
		t.Fatalf("the endpoint id changed: %q -> %q", ep.EndpointID, got.EndpointID)
	}
	// THE SECRET IS UNTOUCHED, checked by USING it rather than by comparing a hash. The whole
	// value of this control is that the running session needs no ceremony afterwards: if the
	// secret had to be reissued, this would be the unbind-and-rebind it exists to replace.
	if _, err := cs.AuthenticateEndpoint(ctx, ep.EndpointID, secret); err != nil {
		t.Fatalf("the session cannot authenticate after a re-bind: %v", err)
	}

	// AND THE OLD KEY IS NOW HARMLESS TO IT. This is the property the operator is buying:
	// retiring the key they were about to retire no longer severs this session.
	if err := st.RevokeToken(ctx, oldID); err != nil {
		t.Fatal(err)
	}
	if n, err := cs.SeverEndpointsBoundBy(ctx, oldID); err != nil || n != 0 {
		t.Fatalf("revoking the old key severed %d endpoint(s) (err %v) — the re-bind did not take", n, err)
	}
	if revoked, err := st.IsStationKeyRevoked(ctx, newID); err != nil || revoked {
		t.Fatalf("the new key reads as revoked (%v, err %v) — the session would be refused on its next call", revoked, err)
	}
}

// *** A BINDING NEVER MOVES TO ANOTHER STATION'S KEY. ***
//
// `bound_by_station_key_id` is a sever lever. Pointing this endpoint at station B's key would
// hand B's operator the power to disconnect A's session, and would take that power away from
// A — an authority laundered, with every count on both pages still reconciling. The rule lives
// in the UPDATE's WHERE rather than in a check above it, so there is no window between them.
func TestRebindRefusesAKeyFromAnotherStation(t *testing.T) {
	st, ctx, cli, base, actor := stationsHarnessWithComm(t)
	cs := commOf(t)

	a, err := st.CreateStation(ctx, spaceForSession, "station-a", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateStation(ctx, spaceForSession, "station-b", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	aKey, _ := st.IssueStationKey(ctx, actor, a.StationID, "a-key", []string{"station"})
	bKey, _ := st.IssueStationKey(ctx, actor, b.StationID, "b-key", []string{"station"})
	aID, bID := keyIDOf(aKey), keyIDOf(bKey)

	ep, _, err := cs.RegisterEndpoint(ctx, comm.Owner{TokenID: aID, ActorID: actor, SpaceID: spaceForSession}, "session", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := cs.BindEndpointToStation(ctx, ep.EndpointID, a.StationID, aID); err != nil {
		t.Fatal(err)
	}

	// A revoked key of the RIGHT station, and a knowledge-base token, are refused for the
	// reason the owner re-point refuses theirs: the result would be a session severed on its
	// next call, failing identically to one whose credential leaked.
	deadKey, _ := st.IssueStationKey(ctx, actor, a.StationID, "dead", []string{"station"})
	deadID := keyIDOf(deadKey)
	if err := st.RevokeToken(ctx, deadID); err != nil {
		t.Fatal(err)
	}
	kb, _ := st.IssueToken(ctx, actor, []string{"read"}, "kb")
	kbID := strings.SplitN(strings.TrimPrefix(kb, "ken_"), "_", 2)[0]

	// EACH REFUSAL IS CHECKED TWICE — nothing moved, AND the operator was told which thing
	// refused. The two are not the same assertion and mutation showed why: a handler that
	// ignores the validator entirely still moves nothing, because the re-point statement's
	// `station_id=?` guard rejects the empty station a bad target resolves to. The row is
	// safe and the operator is told "not found" about an endpoint that is right there. A
	// refusal that names the wrong subject is the failure this project keeps finding, so the
	// message is part of the contract rather than presentation.
	for _, target := range []struct{ name, id, says string }{
		// A key of another station is a perfectly VALID station key, so the validator
		// accepts it and the statement refuses it. The operator must still be told which
		// rule stopped them rather than "not found" about an endpoint on the page.
		{"another station's key", bID, "Nothing moved"},
		{"a revoked key of the right station", deadID, "cannot have bound"},
		{"a knowledge-base token", kbID, "cannot have bound"},
		{"a nonexistent token", "no-such-token", "cannot have bound"},
	} {
		csrf := extract(t, cli, base+"/comm", `name="csrf" value="([^"]+)"`)
		body := postForm(t, cli, base+"/comm/endpoints/"+ep.EndpointID+"/rebind",
			url.Values{"csrf": {csrf}, "from_key": {aID}, "to_key": {target.id}})
		got := endpointOf(t, cs, ep.EndpointID)
		if got.BoundByStationKeyID != aID {
			t.Fatalf("%s was accepted: the binding is now %q", target.name, got.BoundByStationKeyID)
		}
		if !strings.Contains(body, target.says) {
			t.Errorf("%s: the refusal does not say the target could not have bound an endpoint — "+
				"the operator is told the wrong thing refused", target.name)
		}
	}

	// CONTROL: a second key of station A is accepted, or the four refusals above prove only
	// that the handler refuses everything.
	okKey, _ := st.IssueStationKey(ctx, actor, a.StationID, "a-second", []string{"station"})
	okID := keyIDOf(okKey)
	csrf := extract(t, cli, base+"/comm", `name="csrf" value="([^"]+)"`)
	postForm(t, cli, base+"/comm/endpoints/"+ep.EndpointID+"/rebind",
		url.Values{"csrf": {csrf}, "from_key": {aID}, "to_key": {okID}})
	if got := endpointOf(t, cs, ep.EndpointID); got.BoundByStationKeyID != okID {
		t.Fatal("the control target was refused too — this test cannot tell the same-station rule from a broken handler")
	}

	// AND THE PICKER NEVER OFFERS WHAT THE ACTION WILL REFUSE. The statement is the gate, so
	// an over-broad picker is not a security hole — it is worse in a different way: an
	// operator who picks an option and is told no learns the control is unreliable, and stops
	// using the one thing that saves their sessions. Station B owns no endpoint here, so its
	// key has no other reason to appear anywhere on this page.
	page := get(t, cli, base+"/comm")
	if strings.Contains(page, bID) {
		t.Error("another station's key is offered as a re-bind target — the picker promises a move the statement refuses")
	}
}

// *** BOTH BULK VERBS ARE REACHABLE FROM THE PAGE AND MOVE EVERY LIVE ROW. ***
//
// The bulk owner re-point shipped in 3.19.0 with a route, a handler, a store primitive, a test
// through the mux and flash strings in three locales — and no form posted to it. Eleven live
// endpoints hang off one token on the live estate, so the verb that exists precisely for
// moving them at once was the half that could only be reached with curl. This asserts the
// CONTROL renders alongside the count, for both welds.
func TestTheCredentialBlockOffersBothBulkMoves(t *testing.T) {
	st, ctx, cli, base, actor := stationsHarnessWithComm(t)
	cs := commOf(t)

	s, err := st.CreateStation(ctx, spaceForSession, "prod-ops", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	oldKey, _ := st.IssueStationKey(ctx, actor, s.StationID, "laptop", []string{"station"})
	newKey, _ := st.IssueStationKey(ctx, actor, s.StationID, "replacement", []string{"station"})
	oldID, newID := keyIDOf(oldKey), keyIDOf(newKey)
	// Distinct owning token, for the reason the sibling test states: with one id in both roles
	// the two rows of the block are indistinguishable, and a binder row that never rendered at
	// all was matched by the owner row above it.
	fromTok, _ := st.IssueToken(ctx, actor, []string{"comm"}, "old-machine")
	toTok, _ := st.IssueToken(ctx, actor, []string{"comm"}, "new-machine")
	fromTokID := strings.SplitN(strings.TrimPrefix(fromTok, "ken_"), "_", 2)[0]
	toTokID := strings.SplitN(strings.TrimPrefix(toTok, "ken_"), "_", 2)[0]

	var eps []string
	for i := 0; i < 3; i++ {
		ep, _, err := cs.RegisterEndpoint(ctx, comm.Owner{TokenID: fromTokID, ActorID: actor, SpaceID: spaceForSession}, "session", "")
		if err != nil {
			t.Fatal(err)
		}
		if err := cs.BindEndpointToStation(ctx, ep.EndpointID, s.StationID, oldID); err != nil {
			t.Fatal(err)
		}
		eps = append(eps, ep.EndpointID)
	}

	// THE COUNT AND THE CONTROL ARE ON THE PAGE TOGETHER. The oracle is the store, not the
	// literal 3: a block whose number disagrees with what its own button moves is the failure
	// this is watching for, and asserting a constant would pass through it.
	body := get(t, cli, base+"/comm")
	block := credentialsBlock(t, body)
	for _, want := range []string{
		`action="/comm/tokens/` + fromTokID + `/repoint"`,
		`action="/comm/keys/` + oldID + `/rebind"`,
	} {
		if !strings.Contains(block, want) {
			t.Fatalf("the credentials block does not offer %s — the bulk verb has no console surface", want)
		}
	}
	// AND EACH PICKER HAS SOMETHING IN IT, checked inside the block. A form whose select renders
	// empty is a button that posts no target and is refused — reachable, and useless. The two
	// lists are assembled as []any so one template loop can render either kind, which is exactly
	// the shape that renders blank if the field names ever stop matching.
	for _, want := range []string{`<option value="` + toTokID + `"`, `<option value="` + newID + `"`} {
		if !strings.Contains(block, want) {
			t.Fatalf("no option %s in the credentials block — the bulk picker renders empty", want)
		}
	}

	// *** THE NUMBER BESIDE THE BUTTON IS READ OFF THE PAGE, AND ITS ORACLE IS THE STORE. ***
	//
	// This is the assertion the block exists for. The count is what an operator weighs before
	// clicking a move they cannot undo, and comm.go takes it from the store precisely because
	// the rendered row list is space-scoped while the verb is not. Asserting the store against
	// the fixture — which is what this test did — checks the fixture, not the page: the block
	// could render 0 beside every credential with the suite green.
	n, err := cs.CountEndpointsBoundBy(ctx, oldID)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(eps) {
		t.Fatalf("fixture: %d bound, store counts %d", len(eps), n)
	}
	binderRow := regionBetween(t, block, oldID, "</tr>")
	if !strings.Contains(binderRow, `>`+strconv.Itoa(n)+`<`) {
		t.Errorf("the binding key's row does not render the live count %d that its own button would move:\n%s", n, binderRow)
	}
	m, err := cs.CountEndpointsByToken(ctx, fromTokID)
	if err != nil {
		t.Fatal(err)
	}
	if m != len(eps) {
		t.Fatalf("fixture: %d owned, store counts %d", len(eps), m)
	}
	ownerRow := regionBetween(t, block, fromTokID+`</td>`, `action="/comm/tokens/`+fromTokID+`/repoint"`)
	if !strings.Contains(ownerRow, `>`+strconv.Itoa(m)+`<`) {
		t.Errorf("the owning token's row does not render the live count %d that its own button would move:\n%s", m, ownerRow)
	}

	csrf := extract(t, cli, base+"/comm", `name="csrf" value="([^"]+)"`)
	postForm(t, cli, base+"/comm/keys/"+oldID+"/rebind", url.Values{"csrf": {csrf}, "to_key": {newID}})
	for _, id := range eps {
		if got := endpointOf(t, cs, id); got.BoundByStationKeyID != newID {
			t.Fatalf("endpoint %s still bound by %q after the bulk re-bind", id, got.BoundByStationKeyID)
		}
	}

	csrf = extract(t, cli, base+"/comm", `name="csrf" value="([^"]+)"`)
	postForm(t, cli, base+"/comm/tokens/"+fromTokID+"/repoint", url.Values{"csrf": {csrf}, "to_token": {toTokID}})
	for _, id := range eps {
		if got := endpointOf(t, cs, id); got.Owner.TokenID != toTokID {
			t.Fatalf("endpoint %s still owned by %q after the bulk re-point", id, got.Owner.TokenID)
		}
	}
}

// *** THE CLI REVOKE PATH LEAVES SESSIONS ALIVE-LOOKING AND DEAD, AND THE CONSOLE CAN REPAIR
// THEM. ***
//
// `ken token revoke` runs in a separate process with no comm.db handle, so it cannot sever the
// endpoints its key bound. They stay unrevoked in `endpoint` — listed, counted, apparently
// healthy — and are refused one call at a time by store.IsStationKeyRevoked, with nothing on
// any page saying why. That is the project's recurring defect class exactly: a session that has
// stopped working looks identical to one that has not.
//
// So the row must RENDER, with the move offered, because re-pointing it onto a live key is the
// only repair that does not cost a re-registration. This is the one case where the control is
// curative rather than preventive, and the distinction is invisible from the console — which is
// why it is asserted rather than described.
func TestRebindRepairsASessionSeveredByACliRevoke(t *testing.T) {
	st, ctx, cli, base, actor := stationsHarnessWithComm(t)
	cs := commOf(t)

	s, err := st.CreateStation(ctx, spaceForSession, "prod-ops", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	oldKey, _ := st.IssueStationKey(ctx, actor, s.StationID, "laptop", []string{"station"})
	newKey, _ := st.IssueStationKey(ctx, actor, s.StationID, "replacement", []string{"station"})
	oldID, newID := keyIDOf(oldKey), keyIDOf(newKey)

	ep, _, err := cs.RegisterEndpoint(ctx, comm.Owner{TokenID: oldID, ActorID: actor, SpaceID: spaceForSession}, "session", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := cs.BindEndpointToStation(ctx, ep.EndpointID, s.StationID, oldID); err != nil {
		t.Fatal(err)
	}

	// The CLI path: the token row is revoked and comm.db is never touched.
	if err := st.RevokeToken(ctx, oldID); err != nil {
		t.Fatal(err)
	}
	if revoked, err := st.IsStationKeyRevoked(ctx, oldID); err != nil || !revoked {
		t.Fatalf("fixture: the key does not read as revoked (%v, err %v)", revoked, err)
	}
	if got := endpointOf(t, cs, ep.EndpointID); got.EndpointID == "" {
		t.Fatal("fixture: the endpoint was severed, so this is not the CLI path")
	}

	// THE ROW IS ON THE PAGE despite its key being revoked — a block built from the tokens a
	// move may legally NAME would have omitted the one credential the operator has to see.
	body := get(t, cli, base+"/comm")
	if !strings.Contains(body, "/comm/keys/"+oldID+"/rebind") {
		t.Fatal("a revoked binding key with live endpoints is not offered a move — the repair is unreachable")
	}

	csrf := extract(t, cli, base+"/comm", `name="csrf" value="([^"]+)"`)
	postForm(t, cli, base+"/comm/endpoints/"+ep.EndpointID+"/rebind",
		url.Values{"csrf": {csrf}, "from_key": {oldID}, "to_key": {newID}})

	got := endpointOf(t, cs, ep.EndpointID)
	if got.BoundByStationKeyID != newID {
		t.Fatalf("binding key = %q, want %q — the repair did not take", got.BoundByStationKeyID, newID)
	}
	if revoked, err := st.IsStationKeyRevoked(ctx, got.BoundByStationKeyID); err != nil || revoked {
		t.Fatal("the session still answers to a revoked key after the repair")
	}
}

// *** THE BULK VERB HAS THE SAME-STATION RULE TOO, AND A HAND-POSTED FORM IS THE WHOLE
// REASON IT NEEDS ONE. ***
//
// The picker offers only keys of the endpoint's own station, so nothing on the page can reach
// this — and the picker is not the gate. A POST naming another station's key would, without
// the guard in the UPDATE, move EVERY endpoint this key bound onto a credential belonging to
// someone else: the other station's operator gains a lever that disconnects these sessions,
// and this station's operator loses it. Every count on both pages would still reconcile.
//
// Caught by mutation: dropping `AND station_id=?` from the bulk statement survived the whole
// suite while the identical mutation on the single-endpoint statement was killed. The guard
// was there and nothing was holding it.
func TestBulkRebindRefusesAKeyFromAnotherStation(t *testing.T) {
	st, ctx, cli, base, actor := stationsHarnessWithComm(t)
	cs := commOf(t)

	a, err := st.CreateStation(ctx, spaceForSession, "station-a", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateStation(ctx, spaceForSession, "station-b", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	aKey, _ := st.IssueStationKey(ctx, actor, a.StationID, "a-key", []string{"station"})
	bKey, _ := st.IssueStationKey(ctx, actor, b.StationID, "b-key", []string{"station"})
	aID, bID := keyIDOf(aKey), keyIDOf(bKey)

	var eps []string
	for i := 0; i < 2; i++ {
		ep, _, err := cs.RegisterEndpoint(ctx, comm.Owner{TokenID: aID, ActorID: actor, SpaceID: spaceForSession}, "session", "")
		if err != nil {
			t.Fatal(err)
		}
		if err := cs.BindEndpointToStation(ctx, ep.EndpointID, a.StationID, aID); err != nil {
			t.Fatal(err)
		}
		eps = append(eps, ep.EndpointID)
	}

	csrf := extract(t, cli, base+"/comm", `name="csrf" value="([^"]+)"`)
	body := postForm(t, cli, base+"/comm/keys/"+aID+"/rebind", url.Values{"csrf": {csrf}, "to_key": {bID}})
	// MATCH A PHRASE ONLY THE FLASH CAN PRODUCE. The first version of both assertions here
	// looked for "same station", which is also the wording of this page's own help text — so
	// they passed against a handler that reported plain success on zero rows, and against one
	// that never explained anything. An assertion satisfied by the surrounding page measures
	// the page, not the answer.
	if !strings.Contains(body, "Nothing moved") {
		t.Error("the bulk refusal does not name the same-station rule — zero is this verb's refusal, " +
			"and an unexplained zero reads as a no-op")
	}
	for _, id := range eps {
		if got := endpointOf(t, cs, id); got.BoundByStationKeyID != aID {
			t.Fatalf("endpoint %s was moved onto another station's key (%q)", id, got.BoundByStationKeyID)
		}
	}

	// CONTROL: a second key of station A moves both, or this proves only that the bulk verb
	// never moves anything.
	okKey, _ := st.IssueStationKey(ctx, actor, a.StationID, "a-second", []string{"station"})
	okID := keyIDOf(okKey)
	csrf = extract(t, cli, base+"/comm", `name="csrf" value="([^"]+)"`)
	postForm(t, cli, base+"/comm/keys/"+aID+"/rebind", url.Values{"csrf": {csrf}, "to_key": {okID}})
	for _, id := range eps {
		if got := endpointOf(t, cs, id); got.BoundByStationKeyID != okID {
			t.Fatalf("the control move was refused too — this test cannot tell the same-station rule from a broken bulk verb")
		}
	}
}

// *** THE from-key GUARD MAKES A STALE PAGE FAIL INSTEAD OF OVERWRITING SOMEBODY ELSE'S MOVE. ***
//
// `bound_by_station_key_id=?` in the WHERE is what makes the operation conditional rather than
// check-then-act — §9.5 of docs/IDENTITY.md states that rule and says it outlives the mechanism
// it came from. Nothing held it: dropping the clause AND its bound argument left the whole
// repository suite green.
//
// It read as held because the first mutation that tried it removed the clause and LEFT the
// argument, so the statement failed on placeholder arity and the test died of a SQL error. A
// mutant that dies of its own malformation is indistinguishable from one the tests killed — this
// project's defect class, committed inside the harness built to find it.
func TestRebindRefusesAStaleFromKey(t *testing.T) {
	st, ctx, cli, base, actor := stationsHarnessWithComm(t)
	cs := commOf(t)

	s, err := st.CreateStation(ctx, spaceForSession, "prod-ops", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	k1, _ := st.IssueStationKey(ctx, actor, s.StationID, "one", []string{"station"})
	k2, _ := st.IssueStationKey(ctx, actor, s.StationID, "two", []string{"station"})
	k3, _ := st.IssueStationKey(ctx, actor, s.StationID, "three", []string{"station"})
	id1, id2, id3 := keyIDOf(k1), keyIDOf(k2), keyIDOf(k3)

	ep, _, err := cs.RegisterEndpoint(ctx, comm.Owner{TokenID: id1, ActorID: actor, SpaceID: spaceForSession}, "session", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := cs.BindEndpointToStation(ctx, ep.EndpointID, s.StationID, id1); err != nil {
		t.Fatal(err)
	}

	// Operator B moves it while operator A's page sits open.
	if err := cs.RepointEndpointBinder(ctx, ep.EndpointID, id1, id2, s.StationID); err != nil {
		t.Fatal(err)
	}

	// Operator A now submits their stale form, which still names the ORIGINAL key.
	csrf := extract(t, cli, base+"/comm", `name="csrf" value="([^"]+)"`)
	body := postForm(t, cli, base+"/comm/endpoints/"+ep.EndpointID+"/rebind",
		url.Values{"csrf": {csrf}, "from_key": {id1}, "to_key": {id3}})

	if got := endpointOf(t, cs, ep.EndpointID); got.BoundByStationKeyID != id2 {
		t.Fatalf("a stale form overwrote a move that had already happened: binding is %q, want %q", got.BoundByStationKeyID, id2)
	}
	if !strings.Contains(body, "Nothing moved") {
		t.Error("the stale submit reported success — an operator is told they moved something they did not")
	}
}

// *** NEITHER VERB RESURRECTS A REVOKED ENDPOINT, INCLUDING THE BULK ONE. ***
//
// `revoked_at IS NULL` is stated in both statements and in the CHANGELOG — "a revoked endpoint is
// refused here exactly as rotation and owner re-pointing refuse one" — and was held by nothing on
// the bulk path: dropping the clause left the suite green. The consequence is not merely a wrong
// row count. The bulk flash says *"The sessions keep running and need no restart"*, which would
// then be said about sessions that are dead and, with no un-revoke path anywhere in the tree,
// cannot be revived.
func TestBulkRebindDoesNotMoveRevokedEndpoints(t *testing.T) {
	st, ctx, cli, base, actor := stationsHarnessWithComm(t)
	cs := commOf(t)

	s, err := st.CreateStation(ctx, spaceForSession, "prod-ops", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	oldKey, _ := st.IssueStationKey(ctx, actor, s.StationID, "laptop", []string{"station"})
	newKey, _ := st.IssueStationKey(ctx, actor, s.StationID, "replacement", []string{"station"})
	oldID, newID := keyIDOf(oldKey), keyIDOf(newKey)

	var live, dead string
	for i := 0; i < 2; i++ {
		ep, _, err := cs.RegisterEndpoint(ctx, comm.Owner{TokenID: oldID, ActorID: actor, SpaceID: spaceForSession}, "session", "")
		if err != nil {
			t.Fatal(err)
		}
		if err := cs.BindEndpointToStation(ctx, ep.EndpointID, s.StationID, oldID); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			live = ep.EndpointID
		} else {
			dead = ep.EndpointID
		}
	}
	if err := cs.RevokeEndpoint(ctx, dead); err != nil {
		t.Fatal(err)
	}

	csrf := extract(t, cli, base+"/comm", `name="csrf" value="([^"]+)"`)
	body := postForm(t, cli, base+"/comm/keys/"+oldID+"/rebind", url.Values{"csrf": {csrf}, "to_key": {newID}})

	if got := endpointOf(t, cs, live); got.BoundByStationKeyID != newID {
		t.Fatalf("the live endpoint did not move: binding is %q", got.BoundByStationKeyID)
	}
	// THE ORACLE IS THE COUNT THE OPERATOR IS SHOWN, not the row state alone: the flash asserts
	// how many sessions kept running, and a revoked one inflating that number is the lie.
	if !strings.Contains(body, "1 endpoint binding") {
		t.Errorf("the bulk move did not report exactly one moved binding — a revoked endpoint was counted "+
			"as a session that keeps running:\n%s", flashOf(body))
	}
	if raw := rawBinderOf(t, cs, dead); raw != oldID {
		t.Errorf("a revoked endpoint's binding was moved (%q -> %q); it can never be un-revoked, so the move "+
			"only makes the dead row look live", oldID, raw)
	}
}

// rawBinderOf reads a REVOKED endpoint's binding column, which ListEndpoints deliberately hides.
func rawBinderOf(t *testing.T, cs *comm.Store, endpointID string) string {
	t.Helper()
	var got string
	if err := cs.R.QueryRow(
		`SELECT COALESCE(bound_by_station_key_id,'') FROM endpoint WHERE endpoint_id=?`, endpointID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	return got
}

// flashOf trims a page down to the flash region for a readable failure message.
func flashOf(body string) string {
	i := strings.Index(body, "flash")
	if i < 0 {
		return "(no flash on the page)"
	}
	j := i + 400
	if j > len(body) {
		j = len(body)
	}
	return body[i:j]
}

// *** THE CONFIRM NAMES EVERY SESSION THE BULK VERB WILL MOVE, NOT JUST HOW MANY. ***
//
// ken-prod-ops put the objection to a bare number on 2026-08-25, against a page that had just
// passed its own count check: "an operator reads 11, inspects 6, clicks a bulk verb that moves 11
// — and the five they never saw include `runway-prod-admin` and `rb5009-config`, both in use this
// week." A number is a claim about a population. A confirm that names the population is a claim
// the operator can check, which is the standard S6 already sets for revocation one step earlier.
//
// The list comes from the verb's own predicate, so it cannot be shorter than what the button
// moves — see TestTheBlastRadiusListAndCountCannotDisagree for the other half of that.
func TestTheBulkConfirmNamesEverySessionItWillMove(t *testing.T) {
	st, ctx, cli, base, actor := stationsHarnessWithComm(t)
	cs := commOf(t)

	s, err := st.CreateStation(ctx, spaceForSession, "prod-ops", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := st.IssueStationKey(ctx, actor, s.StationID, "laptop", []string{"station"})
	keyID := keyIDOf(key)
	tok, _ := st.IssueToken(ctx, actor, []string{"comm"}, "old-machine")
	tokID := strings.SplitN(strings.TrimPrefix(tok, "ken_"), "_", 2)[0]
	if _, err := st.IssueToken(ctx, actor, []string{"comm"}, "new-machine"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.IssueStationKey(ctx, actor, s.StationID, "replacement", []string{"station"}); err != nil {
		t.Fatal(err)
	}

	// Two bound and one UNBOUND, all on the same token. The unbound one is the case prod named:
	// it hangs off the comm token exactly as the bound ones do, and revoking that token ends it.
	labels := []string{"runway-prod-admin", "rb5009-config", "collector-proxy-dev"}
	for i, label := range labels {
		ep, _, err := cs.RegisterEndpoint(ctx, comm.Owner{TokenID: tokID, ActorID: actor, SpaceID: spaceForSession}, label, "")
		if err != nil {
			t.Fatal(err)
		}
		if i < 2 {
			if err := cs.BindEndpointToStation(ctx, ep.EndpointID, s.StationID, keyID); err != nil {
				t.Fatal(err)
			}
		}
	}

	block := credentialsBlock(t, get(t, cli, base+"/comm"))

	// THE OWNER CONFIRM NAMES ALL THREE, the unbound one included.
	ownerConfirm := regionBetween(t, block, `action="/comm/tokens/`+tokID+`/repoint"`, "</form>")
	for _, label := range labels {
		if !strings.Contains(ownerConfirm, label) {
			t.Errorf("the bulk re-point confirm does not name %q — an operator approving it cannot see "+
				"what they are moving:\n%s", label, ownerConfirm)
		}
	}

	// THE BINDER CONFIRM NAMES THE TWO BOUND ONES AND NOT THE THIRD, because that is what its
	// own verb moves. A confirm that over-states is as wrong as one that under-states.
	binderConfirm := regionBetween(t, block, `action="/comm/keys/`+keyID+`/rebind"`, "</form>")
	for _, label := range labels[:2] {
		if !strings.Contains(binderConfirm, label) {
			t.Errorf("the bulk re-bind confirm does not name %q", label)
		}
	}
	if strings.Contains(binderConfirm, "collector-proxy-dev") {
		t.Error("the bulk re-bind confirm names an UNBOUND endpoint, which its verb cannot move — " +
			"an over-stated blast radius is a false alarm the operator learns to ignore")
	}
}

// *** THE NUMBER AND THE LIST COME FROM ONE QUERY, AND STAY EQUAL TO WHAT /tokens STATES. ***
//
// The console now renders a count that is `len(list)`, so those two cannot drift by construction.
// What CAN drift is this pair against `CountEndpointsByToken` / `CountEndpointsBoundBy`, which
// `/tokens` still uses to state a revoke's blast radius — two pages describing one credential.
// Their WHERE clauses are character-identical today; this fails the moment they are not.
func TestTheBlastRadiusListAndCountCannotDisagree(t *testing.T) {
	st, ctx, _, _, actor := stationsHarnessWithComm(t)
	cs := commOf(t)

	s, err := st.CreateStation(ctx, spaceForSession, "prod-ops", "", actor)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := st.IssueStationKey(ctx, actor, s.StationID, "laptop", []string{"station"})
	keyID := keyIDOf(key)
	tok, _ := st.IssueToken(ctx, actor, []string{"comm"}, "machine")
	tokID := strings.SplitN(strings.TrimPrefix(tok, "ken_"), "_", 2)[0]

	var revoked string
	for i := 0; i < 4; i++ {
		ep, _, err := cs.RegisterEndpoint(ctx, comm.Owner{TokenID: tokID, ActorID: actor, SpaceID: spaceForSession}, "s", "")
		if err != nil {
			t.Fatal(err)
		}
		if i < 3 {
			if err := cs.BindEndpointToStation(ctx, ep.EndpointID, s.StationID, keyID); err != nil {
				t.Fatal(err)
			}
		}
		if i == 0 {
			revoked = ep.EndpointID
		}
	}
	// A revoked row, because that is the discriminator: a count including it reports a leftover
	// that does not exist, on every attempt, forever — which trains an operator to click through
	// the one number meant to stop them. prod tested /tokens for exactly this on 2026-08-24.
	if err := cs.RevokeEndpoint(ctx, revoked); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name  string
		list  func() ([]comm.EndpointRef, error)
		count func() (int, error)
		want  int
	}{
		{"owned by token", func() ([]comm.EndpointRef, error) { return cs.EndpointsOwnedBy(ctx, tokID) },
			func() (int, error) { return cs.CountEndpointsByToken(ctx, tokID) }, 3},
		{"bound by key", func() ([]comm.EndpointRef, error) { return cs.EndpointsBoundBy(ctx, keyID) },
			func() (int, error) { return cs.CountEndpointsBoundBy(ctx, keyID) }, 2},
	} {
		refs, err := tc.list()
		if err != nil {
			t.Fatal(err)
		}
		n, err := tc.count()
		if err != nil {
			t.Fatal(err)
		}
		if len(refs) != n {
			t.Errorf("%s: the list names %d and the count says %d — /comm and /tokens would describe "+
				"the same credential differently", tc.name, len(refs), n)
		}
		// AND BOTH MUST BE RIGHT. Equal-and-wrong is the failure mode a pure agreement check
		// cannot see, so the expected value is stated independently of either query.
		if len(refs) != tc.want {
			t.Errorf("%s: %d live endpoints, want %d — the pair agrees with itself and not with the data",
				tc.name, len(refs), tc.want)
		}
		for _, r := range refs {
			if r.EndpointID == revoked {
				t.Errorf("%s: the revoked endpoint is named in the blast radius", tc.name)
			}
		}
	}
}
