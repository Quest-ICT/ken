package web

import (
	"net/url"
	"strings"
	"testing"

	"github.com/Quest-ICT/ken/internal/comm"
)

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

	ep, secret, err := cs.RegisterEndpoint(ctx, comm.Owner{TokenID: oldID, ActorID: actor, SpaceID: spaceForSession}, "session", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := cs.BindEndpointToStation(ctx, ep.EndpointID, s.StationID, oldID); err != nil {
		t.Fatal(err)
	}

	// THE BINDING KEY IS ON THE PAGE. It never was, so the credential that severs a session
	// on its next call was invisible to the only person who can act on it.
	body := get(t, cli, base+"/comm")
	if !strings.Contains(body, oldID) {
		t.Fatal("the endpoint's binding station key is not rendered — the second weld is invisible")
	}
	if !strings.Contains(body, "/rebind") {
		t.Fatal("no re-bind control on the page")
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
	toTok, _ := st.IssueToken(ctx, actor, []string{"comm"}, "new-machine")
	toTokID := strings.SplitN(strings.TrimPrefix(toTok, "ken_"), "_", 2)[0]

	var eps []string
	for i := 0; i < 3; i++ {
		ep, _, err := cs.RegisterEndpoint(ctx, comm.Owner{TokenID: oldID, ActorID: actor, SpaceID: spaceForSession}, "session", "")
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
	for _, want := range []string{"/comm/tokens/" + oldID + "/repoint", "/comm/keys/" + oldID + "/rebind"} {
		if !strings.Contains(body, want) {
			t.Fatalf("the credentials block does not offer %s — the bulk verb has no console surface", want)
		}
	}
	// AND THE PICKER HAS SOMETHING IN IT. A form whose select renders empty is a button that
	// posts no target and is refused — reachable, and useless. The two lists are assembled as
	// []any so the one template loop can render either kind, which is exactly the shape that
	// renders blank if the field names ever stop matching.
	for _, want := range []string{`value="` + toTokID + `"`, `value="` + newID + `"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("no option %s in the credentials block — the picker renders empty", want)
		}
	}
	n, err := cs.CountEndpointsBoundBy(ctx, oldID)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(eps) {
		t.Fatalf("fixture: %d bound, store counts %d", len(eps), n)
	}

	csrf := extract(t, cli, base+"/comm", `name="csrf" value="([^"]+)"`)
	postForm(t, cli, base+"/comm/keys/"+oldID+"/rebind", url.Values{"csrf": {csrf}, "to_key": {newID}})
	for _, id := range eps {
		if got := endpointOf(t, cs, id); got.BoundByStationKeyID != newID {
			t.Fatalf("endpoint %s still bound by %q after the bulk re-bind", id, got.BoundByStationKeyID)
		}
	}

	csrf = extract(t, cli, base+"/comm", `name="csrf" value="([^"]+)"`)
	postForm(t, cli, base+"/comm/tokens/"+oldID+"/repoint", url.Values{"csrf": {csrf}, "to_token": {toTokID}})
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
