package web

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/Quest-ICT/ken/internal/comm"
)

// THE CONSOLE IS THE ONLY SURFACE, and this asserts it end to end: the page offers the control,
// the POST moves the owner, and the endpoint keeps everything else.
func TestRepointingAnEndpointFromTheConsole(t *testing.T) {
	st, ctx, cli, base, actor := stationsHarnessWithComm(t)
	cs := commOf(t)

	from, err := st.IssueToken(ctx, actor, []string{"comm"}, "old-machine")
	if err != nil {
		t.Fatal(err)
	}
	to, err := st.IssueToken(ctx, actor, []string{"comm"}, "new-machine")
	if err != nil {
		t.Fatal(err)
	}
	fromID := strings.SplitN(strings.TrimPrefix(from, "ken_"), "_", 2)[0]
	toID := strings.SplitN(strings.TrimPrefix(to, "ken_"), "_", 2)[0]

	ep, _, err := cs.RegisterEndpoint(ctx, comm.Owner{TokenID: fromID, ActorID: actor, SpaceID: spaceForSession}, "laptop", "")
	if err != nil {
		t.Fatal(err)
	}

	// THE OWNING TOKEN IS ON THE PAGE. It never was — which is why nobody could see that
	// eleven endpoints hung off one credential without querying the database by hand.
	body := get(t, cli, base+"/comm")
	if !strings.Contains(body, fromID) {
		t.Fatal("the endpoint's owning token is not rendered — the concentration is invisible")
	}
	if !strings.Contains(body, "/repoint") {
		t.Fatal("no re-point control on the page")
	}

	csrf := extract(t, cli, base+"/comm", `name="csrf" value="([^"]+)"`)
	postForm(t, cli, base+"/comm/endpoints/"+ep.EndpointID+"/repoint",
		url.Values{"csrf": {csrf}, "from_token": {fromID}, "to_token": {toID}})

	got := endpointOf(t, cs, ep.EndpointID)
	if got.Owner.TokenID != toID {
		t.Fatalf("owner token = %q, want %q", got.Owner.TokenID, toID)
	}
	if got.EndpointID != ep.EndpointID {
		t.Fatalf("the endpoint id changed: %q -> %q", ep.EndpointID, got.EndpointID)
	}
}

// *** A TARGET THAT CANNOT OWN AN ENDPOINT IS REFUSED, AND NOTHING MOVES. ***
//
// Re-pointing onto a revoked or non-comm token produces an endpoint that authenticates NOWHERE
// and fails indistinguishably from one whose secret leaked — a missing scope is a 401 at the
// transport, a revoked target is the bare ownership string, and neither says "you re-pointed
// onto a dead token". That is the hunted defect class, manufactured by the control built to
// cure it, so the refusal is part of the operation rather than a nicety.
func TestRepointRefusesATargetThatCannotOwnAnEndpoint(t *testing.T) {
	st, ctx, cli, base, actor := stationsHarnessWithComm(t)
	cs := commOf(t)

	from, _ := st.IssueToken(ctx, actor, []string{"comm"}, "old")
	fromID := strings.SplitN(strings.TrimPrefix(from, "ken_"), "_", 2)[0]
	ep, _, err := cs.RegisterEndpoint(ctx, comm.Owner{TokenID: fromID, ActorID: actor, SpaceID: spaceForSession}, "laptop", "")
	if err != nil {
		t.Fatal(err)
	}

	// A knowledge-base token: perfectly valid, and cannot carry a comm endpoint.
	kb, _ := st.IssueToken(ctx, actor, []string{"read", "write-draft", "propose"}, "kb")
	kbID := strings.SplitN(strings.TrimPrefix(kb, "ken_"), "_", 2)[0]
	// A revoked comm token.
	rev, _ := st.IssueToken(ctx, actor, []string{"comm"}, "revoked")
	revID := strings.SplitN(strings.TrimPrefix(rev, "ken_"), "_", 2)[0]
	if err := st.RevokeToken(ctx, revID); err != nil {
		t.Fatal(err)
	}

	for _, target := range []struct{ name, id string }{
		{"knowledge-base token", kbID},
		{"revoked comm token", revID},
		{"nonexistent token", "no-such-token"},
	} {
		csrf := extract(t, cli, base+"/comm", `name="csrf" value="([^"]+)"`)
		postForm(t, cli, base+"/comm/endpoints/"+ep.EndpointID+"/repoint",
			url.Values{"csrf": {csrf}, "from_token": {fromID}, "to_token": {target.id}})
		got := endpointOf(t, cs, ep.EndpointID)
		if got.Owner.TokenID != fromID {
			t.Fatalf("%s was accepted as a target: owner is now %q", target.name, got.Owner.TokenID)
		}
	}

	// CONTROL: a legitimate target still works, or the three refusals above prove only that
	// the handler refuses everything.
	ok, _ := st.IssueToken(ctx, actor, []string{"comm"}, "good")
	okID := strings.SplitN(strings.TrimPrefix(ok, "ken_"), "_", 2)[0]
	csrf := extract(t, cli, base+"/comm", `name="csrf" value="([^"]+)"`)
	postForm(t, cli, base+"/comm/endpoints/"+ep.EndpointID+"/repoint",
		url.Values{"csrf": {csrf}, "from_token": {fromID}, "to_token": {okID}})
	got := endpointOf(t, cs, ep.EndpointID)
	if got.Owner.TokenID != okID {
		t.Fatalf("the control target was refused too — this test cannot tell validation from a broken handler")
	}
}

// endpointOf reads one endpoint back through the list the console itself renders, so the test
// observes what an operator would rather than a private lookup.
func endpointOf(t *testing.T, cs *comm.Store, id string) comm.Endpoint {
	t.Helper()
	eps, err := cs.ListEndpoints(context.Background(), spaceForSession)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range eps {
		if e.EndpointID == id {
			return e
		}
	}
	t.Fatalf("endpoint %s not in ListEndpoints", id)
	return comm.Endpoint{}
}

// *** THE PICKER NEVER DEFAULTS TO A NO-OP. ***
//
// The control shipped in 3.19.0 listing every comm token, the endpoint's own included, and its
// own sorts first — so the default selection was "move it to the token it is already on".
// Clicking Re-point then flashed *"Endpoint X re-pointed. Its channels, binding and queued mail
// are unchanged; the session needs the new token in its config and a restart"* over a row
// nothing had touched: a success message for a no-op, with instructions to restart a session
// that did not need it.
//
// The store is right to accept it — `token_id=? WHERE token_id=?` affects one row, which is
// idempotent and correct. The console is what must not offer it.
func TestThePickerDoesNotOfferTheTokenTheEndpointIsAlreadyOn(t *testing.T) {
	st, ctx, cli, base, actor := stationsHarnessWithComm(t)
	cs := commOf(t)

	own, _ := st.IssueToken(ctx, actor, []string{"comm"}, "current-machine")
	other, _ := st.IssueToken(ctx, actor, []string{"comm"}, "other-machine")
	ownID := strings.SplitN(strings.TrimPrefix(own, "ken_"), "_", 2)[0]
	otherID := strings.SplitN(strings.TrimPrefix(other, "ken_"), "_", 2)[0]

	if _, _, err := cs.RegisterEndpoint(ctx, comm.Owner{TokenID: ownID, ActorID: actor, SpaceID: spaceForSession}, "laptop", ""); err != nil {
		t.Fatal(err)
	}

	body := get(t, cli, base+"/comm")
	if strings.Contains(body, `<option value="`+ownID+`"`) {
		t.Error("the re-point picker offers the endpoint's own token — the default selection is a no-op that flashes success")
	}
	// CONTROL: the other token IS offered, so the assertion above is about the filter and not
	// about a picker that renders nothing at all.
	if !strings.Contains(body, `<option value="`+otherID+`"`) {
		t.Fatal("no re-point option renders at all — this test cannot tell a filter from an empty picker")
	}
}
