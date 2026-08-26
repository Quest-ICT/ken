package commserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Quest-ICT/ken/internal/comm"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// *** BINDING NEEDS NO VOUCHER — docs/IDENTITY.md §10 step 3. ***
//
// §9.2 names the voucher chain as "the single largest safe deletion available" and states its one
// condition: **"The voucher exists SOLELY so a station key never crosses to the comm surface as a
// tool argument."** Step 2 made one identity span both surfaces; step 4 replaced the per-folder
// station key with a header that authorises nothing. There is no key to keep off this surface, so
// there is nothing left for a voucher to carry.
//
// THE ENDPOINT IS BOUND WITH NO AUTHORISING KEY, WHICH IS THE POINT AND NOT A GAP.
// `bound_by_station_key_id` is the second weld: checked at USE on every call, with a MISSING row
// treated as revoked. An endpoint bound this way names no key, so that check skips it — correct,
// because no key authorised it and none can sever it. Revocation moves to the credential that OWNS
// the endpoint, which is re-pointable since 3.19.0. One credential, one revocation, instead of two
// welds on one row.
//
// Asserted at the description level here plus over the transport in the store tests, because the
// failure this guards against is the one prod's session hit: being told to fetch a voucher from a
// tool it does not have and cannot get.
func TestCommBindNoLongerDemandsAVoucherItCannotGet(t *testing.T) {
	desc := toolDescription(t, "comm_bind")

	// IT MUST NOT LEAD WITH THE VOUCHER. A session reading this holds an endpoint and a
	// workspace header; the tool it is being sent to lives on a surface it may not have.
	if !strings.Contains(desc, "X-Ken-Workspace") {
		t.Error("comm_bind's description never mentions the workspace header, so the voucher-free " +
			"path is unreachable by anyone who reads only the tool list")
	}
	if !strings.Contains(desc, "NO VOUCHER") {
		t.Error("comm_bind still presents a voucher as the way in — the session that reported this " +
			"was told to fetch one from station_binding_voucher, a tool it demonstrably did not have")
	}
	// AND IT MUST NOT SEND ANYONE TO THE DELETED TOOL. The chain is gone; a description that
	// still names station_binding_voucher routes a session to something that cannot exist, which
	// is strictly worse than the original defect — that tool at least existed on some surface.
	if strings.Contains(desc, "station_binding_voucher") {
		t.Error("comm_bind still names station_binding_voucher, which no longer exists on any surface")
	}
}

// toolDescription reads one tool's shipped description out of the source, so the test reads what a
// caller receives rather than a copy that can drift from it.
func toolDescription(t *testing.T, tool string) string {
	t.Helper()
	b, err := os.ReadFile("commserver.go")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`Name:\s*"` + regexp.QuoteMeta(tool) + `"[^}]*?Description:\s*((?:"(?:[^"\\]|\\.)*"\s*\+?\s*)+)`).FindSubmatch(b)
	if m == nil {
		t.Fatalf("%s is not registered with a parseable description", tool)
	}
	var sb strings.Builder
	for _, p := range regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`).FindAllSubmatch(m[1], -1) {
		sb.Write(p[1])
	}
	if sb.Len() == 0 {
		t.Fatalf("%s's description parsed as empty; the scanner is broken, not the text", tool)
	}
	return sb.String()
}

// wsRT sends the bearer plus a workspace header, the way a folder's MCP entry does.
type wsRT struct {
	token, workspace string
	base             http.RoundTripper
}

func (w wsRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Header.Set("Authorization", "Bearer "+w.token)
	if w.workspace != "" {
		r.Header.Set(WorkspaceHeader, w.workspace)
	}
	return w.base.RoundTrip(r)
}

// *** BIND OVER THE WIRE, WITH A HEADER AND NO VOUCHER. ***
//
// The property step 3 is for: an endpoint joins a workspace with nothing handed across, because
// there is no longer a station key that must be kept off this surface.
//
// AND IT MUST BIND WITH NO AUTHORISING KEY. `bound_by_station_key_id` empty is what makes the
// at-use severing check skip this endpoint — correct, since no key authorised it. If a future edit
// were to stuff something in that column, `IsStationKeyRevoked` would look it up in `api_token`,
// find nothing, and treat a MISSING row as revoked: the endpoint would be cut off on its very next
// call, for a credential that never existed. That is the trap this asserts against.
func TestBindWithAWorkspaceHeaderAndNoVoucher(t *testing.T) {
	ctx := context.Background()
	st := newKB(t)
	cs, err := comm.Open(filepath.Join(t.TempDir(), "comm.db"), comm.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	if err := cs.Migrate(); err != nil {
		t.Fatal(err)
	}

	actor, err := st.FindOrCreateActor(ctx, "human", "curator")
	if err != nil {
		t.Fatal(err)
	}
	station, err := st.CreateStation(ctx, "ken-public", "", actor)
	if err != nil {
		t.Fatal(err)
	}

	tok := mintToken(t, st, "binding-agent", "comm")
	prin, err := authenticate(ctx, st, tok, ScopeComm)
	if err != nil {
		t.Fatal(err)
	}
	ep, secret, err := cs.RegisterEndpoint(ctx, comm.Owner{
		TokenID: prin.TokenID, ActorID: prin.ActorID}, "session", "")
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(NewHTTPHandler(Deps{Comm: cs, Store: st}))
	t.Cleanup(srv.Close)

	bind := func(workspace string) (*mcp.CallToolResult, error) {
		cli := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
		sess, err := cli.Connect(ctx, &mcp.StreamableClientTransport{
			Endpoint:             srv.URL,
			HTTPClient:           &http.Client{Transport: wsRT{token: tok, workspace: workspace, base: http.DefaultTransport}},
			DisableStandaloneSSE: true,
		}, nil)
		if err != nil {
			return nil, err
		}
		defer sess.Close()
		return sess.CallTool(ctx, &mcp.CallToolParams{Name: "comm_bind", Arguments: map[string]any{
			"endpoint_id": ep.EndpointID, "endpoint_secret": secret}})
	}

	// WITHOUT a header and without a voucher it is refused — and told what to do.
	res, err := bind("")
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("bound with neither a workspace nor a voucher")
	}
	if msg := errString(res).Error(); !strings.Contains(msg, "X-Ken-Workspace") {
		t.Errorf("the refusal does not name the header that fixes it: %v", msg)
	}

	// WITH the header it binds, no voucher anywhere.
	res, err = bind(station.StationID)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("bind with a declared workspace failed: %v", errString(res))
	}

	got := endpointOf(t, cs, ep.EndpointID)
	if got.StationID != station.StationID {
		t.Fatalf("endpoint bound to %q, want %q", got.StationID, station.StationID)
	}
	if got.BoundByStationKeyID != "" {
		t.Errorf("bound_by_station_key_id = %q, want empty. Any value here is looked up in api_token "+
			"by IsStationKeyRevoked, which treats a MISSING row as REVOKED — this endpoint would be "+
			"severed on its very next call, for a key that never existed", got.BoundByStationKeyID)
	}

	// AND AN UNKNOWN WORKSPACE IS REFUSED — ON A FRESH ENDPOINT, WHICH IS THE WHOLE POINT.
	//
	// Asserted first against the endpoint bound above, and it "passed" for the wrong reason:
	// that endpoint was already bound, so the refusal came from the already-bound guard and the
	// workspace was never validated at all. Mutation caught it — deleting the existence check
	// left the assertion green. A second endpoint is the only way to reach the branch.
	ep2, secret2, err := cs.RegisterEndpoint(ctx, comm.Owner{
		TokenID: prin.TokenID, ActorID: prin.ActorID}, "second", "")
	if err != nil {
		t.Fatal(err)
	}
	cli := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	sess, err := cli.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             srv.URL,
		HTTPClient:           &http.Client{Transport: wsRT{token: tok, workspace: "NoSuchWorkspace12", base: http.DefaultTransport}},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	res, err = sess.CallTool(ctx, &mcp.CallToolParams{Name: "comm_bind", Arguments: map[string]any{
		"endpoint_id": ep2.EndpointID, "endpoint_secret": secret2}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("an UNBOUND endpoint bound itself to a workspace that does not exist")
	}
	if got := endpointOf(t, cs, ep2.EndpointID); got.StationID != "" {
		t.Errorf("it is now bound to %q — a station id nothing on this server knows", got.StationID)
	}
}

// endpointOf reads one endpoint back through the list the console renders.
func endpointOf(t *testing.T, cs *comm.Store, id string) comm.Endpoint {
	t.Helper()
	eps, err := cs.ListEndpoints(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range eps {
		if e.EndpointID == id {
			return e
		}
	}
	t.Fatalf("endpoint %s not found", id)
	return comm.Endpoint{}
}
