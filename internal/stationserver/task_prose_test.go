package stationserver

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Quest-ICT/ken/internal/tooldoc"
)

// THE LAYER MATTERS HERE. Both strings under test are sent to a client in `tools/list`
// — one as station_task_add's description, one as the description of `near_matches` in
// its OUTPUT schema — and both are frozen for the life of a session. A test over the Go
// constants would not notice if either stopped being advertised, so this asserts what a
// connecting session is actually handed.
func advertisedStationTools(t *testing.T) map[string]*mcp.Tool {
	t.Helper()
	_, srv, key, _ := harness(t)
	ctx := context.Background()
	cli := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	sess, err := cli.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             srv.URL,
		HTTPClient:           &http.Client{Transport: stnRT{token: key, base: http.DefaultTransport}},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	res, err := sess.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	out := map[string]*mcp.Tool{}
	for _, tl := range res.Tools {
		out[tl.Name] = tl
	}
	return out
}

func advertisedSchema(t *testing.T, tl *mcp.Tool) (map[string]json.RawMessage, []string) {
	t.Helper()
	b, err := json.Marshal(tl)
	if err != nil {
		t.Fatal(err)
	}
	var adv struct {
		InputSchema struct {
			Properties map[string]json.RawMessage `json:"properties"`
			Required   []string                   `json:"required"`
		} `json:"inputSchema"`
	}
	if err := json.Unmarshal(b, &adv); err != nil {
		t.Fatal(err)
	}
	return adv.InputSchema.Properties, adv.InputSchema.Required
}

// documentedParams reads one §11.9 signature out of STATIONS.md.
func documentedParams(t *testing.T, doc, tool string) []string {
	t.Helper()
	re := regexp.MustCompile("`" + regexp.QuoteMeta(tool) + "\\(([^)]*)\\)`")
	m := re.FindAllStringSubmatch(doc, -1)
	if len(m) != 1 {
		t.Fatalf("docs/STATIONS.md: found %d documented signatures for %s, want exactly 1 — the comparison below would be vacuous", len(m), tool)
	}
	var out []string
	for _, p := range strings.Split(m[0][1], ",") {
		p = strings.TrimSuffix(strings.TrimSpace(p), "?")
		if p == "" {
			t.Fatalf("%s: empty parameter in the documented signature %q", tool, m[0][1])
		}
		out = append(out, p)
	}
	if len(out) < 3 {
		t.Fatalf("%s: parsed only %v out of the doc row — the parse is broken, not the row", tool, out)
	}
	return out
}

// station_task_add offered to "merge" in two places a session cannot refresh, and there
// is no merge verb.
func TestTaskAddAdvertisesNoMergeVerb(t *testing.T) {
	tl, ok := advertisedStationTools(t)["station_task_add"]
	if !ok {
		t.Fatal("station_task_add is not in tools/list")
	}
	b, err := json.Marshal(tl)
	if err != nil {
		t.Fatal(err)
	}
	blob := string(b)
	// PLUS THE FULL RULES, because that is where per-tool detail lives now: a description freezes
	// at conversation start and a result does not, so the rules moved to
	// ken_instructions{tool:"…"} and the description kept a sentence and a pointer. Both halves are
	// in the corpus deliberately — the absence check below must hold across EVERYTHING a session
	// can read, not only the half that happens to be short.
	if full, ok := tooldoc.Full("station_task_add"); ok {
		blob += "\n" + full
	}

	// POSITIVE CONTROLS. Without these, "merge does not appear" would pass just as
	// happily if the description were empty or the output schema were not advertised
	// at all — which is where one of the two strings lives.
	for _, want := range []string{
		"blocked_on is required", // the description reached the wire
		"near_matches",           // the OUTPUT schema is advertised
		"close the duplicate",    // the replacement wording, in both strings
	} {
		if !strings.Contains(blob, want) {
			t.Fatalf("station_task_add's advertised surface is missing %q — the absence check below would pass for the wrong reason:\n%s", want, blob)
		}
	}

	if strings.Contains(strings.ToLower(blob), "merge") {
		t.Errorf("station_task_add still offers to \"merge\" and there is no merge verb; this text is frozen at connect, so it is pinned into every session that holds it:\n%s", blob)
	}
}

// The §11.9 rows this item owns must name arguments the wire actually accepts: the SDK
// hard-errors on an unknown one, so a session working from the table gets a failure.
func TestDocumentedTaskSignaturesMatchTheAdvertisedSchema(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "STATIONS.md"))
	if err != nil {
		t.Fatalf("read STATIONS.md: %v", err)
	}
	tools := advertisedStationTools(t)
	for _, name := range []string{"station_task_add", "station_task_defer"} {
		tl, ok := tools[name]
		if !ok {
			t.Fatalf("%s is not in tools/list", name)
		}
		props, required := advertisedSchema(t, tl)
		absent := func(params []string) []string {
			var out []string
			for _, p := range params {
				if _, ok := props[p]; !ok {
					out = append(out, p)
				}
			}
			return out
		}
		params := documentedParams(t, string(doc), name)
		if bad := absent(params); len(bad) > 0 {
			t.Errorf("docs/STATIONS.md §11.9 documents %s%v, which the wire does not accept (it takes %v)", name, bad, slices.Sorted(maps.Keys(props)))
		}
		// POSITIVE CONTROL: the same comparison must REJECT a name the wire does not
		// have. An empty properties map would otherwise make the check above pass.
		if bad := absent([]string{"merge_into"}); len(bad) != 1 {
			t.Fatalf("%s: the comparison accepted `merge_into`, which is not on the wire — the check above proves nothing", name)
		}
		for _, r := range required {
			if !slices.Contains(params, r) {
				t.Errorf("%s requires %q on the wire and docs/STATIONS.md §11.9 does not document it", name, r)
			}
		}
	}
}
