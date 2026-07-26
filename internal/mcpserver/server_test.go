package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Quest-ICT/ken/internal/store"
)

// testServer builds a store (migrated + seeded) behind the MCP HTTP handler.
func testServer(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := st.SeedDemo(context.Background()); err != nil {
		t.Fatalf("seed: %v", err)
	}
	srv := httptest.NewServer(NewHTTPHandler(Deps{Store: st, DedupSecret: []byte("test-dedup-secret")}))
	t.Cleanup(srv.Close)
	return srv, st
}

type authRoundTripper struct {
	token string
	base  http.RoundTripper
}

func (a authRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	if a.token != "" {
		r.Header.Set("Authorization", "Bearer "+a.token)
	}
	return a.base.RoundTrip(r)
}

func clientWithToken(token string) *http.Client {
	return &http.Client{Transport: authRoundTripper{token: token, base: http.DefaultTransport}}
}

func decodeResult(t *testing.T, res *mcp.CallToolResult, dst any) {
	t.Helper()
	if res.StructuredContent != nil {
		if b, err := json.Marshal(res.StructuredContent); err == nil {
			if json.Unmarshal(b, dst) == nil {
				return
			}
		}
	}
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			if err := json.Unmarshal([]byte(tc.Text), dst); err == nil {
				return
			}
		}
	}
	t.Fatalf("could not decode tool result: %+v", res)
}

func TestMCPEndToEnd(t *testing.T) {
	t.Setenv("KEN_DEV_TOKEN", "dev-secret")
	srv, _ := testServer(t)
	ctx := context.Background()

	cli := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	tr := &mcp.StreamableClientTransport{
		Endpoint:             srv.URL,
		HTTPClient:           clientWithToken("dev-secret"),
		DisableStandaloneSSE: true,
	}
	sess, err := cli.Connect(ctx, tr, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer sess.Close()

	// tools/list exposes all five tools.
	tools, err := sess.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	names := map[string]bool{}
	for _, tl := range tools.Tools {
		names[tl.Name] = true
	}
	for _, want := range []string{"kb_search", "kb_get", "kb_propose_enhancement", "kb_save", "kb_flag_stale"} {
		if !names[want] {
			t.Errorf("tool %q missing from tools/list (%v)", want, names)
		}
	}

	// kb_search returns the seeded entry.
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name:      "kb_search",
		Arguments: map[string]any{"query": "docker build reinstalls dependencies layer cache"},
	})
	if err != nil {
		t.Fatalf("kb_search call: %v", err)
	}
	if res.IsError {
		t.Fatalf("kb_search returned error: %+v", res.Content)
	}
	var so searchOut
	decodeResult(t, res, &so)
	if len(so.Results) == 0 || so.Results[0].Slug != "docker-copy-manifests-before-source" {
		t.Fatalf("unexpected kb_search results: %+v", so)
	}

	// kb_get returns the full entry with a curated head body.
	gres, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name:      "kb_get",
		Arguments: map[string]any{"slugs": []string{"docker-copy-manifests-before-source"}, "response_format": "detailed"},
	})
	if err != nil {
		t.Fatalf("kb_get call: %v", err)
	}
	var go_ getOut
	decodeResult(t, gres, &go_)
	if len(go_.Entries) != 1 || go_.Entries[0].Head == nil || go_.Entries[0].Head.Solution == "" {
		t.Fatalf("unexpected kb_get result: %+v", go_)
	}

	// kb_save without a dedup_check_token is rejected (search-before-save gate).
	bad, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "kb_save", Arguments: map[string]any{
		"kind": "project", "title": "x", "summary": "y",
	}})
	if err != nil {
		t.Fatalf("kb_save(no token) call: %v", err)
	}
	if !bad.IsError {
		t.Fatal("kb_save without a dedup_check_token should error")
	}

	// kb_save with the token returned by the earlier kb_search succeeds.
	if so.DedupCheckToken == "" {
		t.Fatal("kb_search did not return a dedup_check_token")
	}
	good, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "kb_save", Arguments: map[string]any{
		"dedup_check_token": so.DedupCheckToken,
		"kind":              "reference",
		"title":             "New note about widgets",
		"summary":           "A distinct entry to prove the write path.",
		"triggers":          []string{"widget frobnication"},
	}})
	if err != nil {
		t.Fatalf("kb_save call: %v", err)
	}
	if good.IsError {
		t.Fatalf("kb_save should succeed: %+v", good.Content)
	}
	var sv saveOut
	decodeResult(t, good, &sv)
	if sv.Slug == "" || sv.State != "proposed" || sv.Lifecycle != "draft" {
		t.Fatalf("unexpected kb_save result: %+v", sv)
	}
}

func TestMCPUnauthorized(t *testing.T) {
	t.Setenv("KEN_DEV_TOKEN", "dev-secret")
	srv, _ := testServer(t)
	ctx := context.Background()

	cli := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	tr := &mcp.StreamableClientTransport{
		Endpoint:             srv.URL,
		HTTPClient:           clientWithToken("WRONG-TOKEN"),
		DisableStandaloneSSE: true,
		MaxRetries:           -1,
	}
	if sess, err := cli.Connect(ctx, tr, nil); err == nil {
		sess.Close()
		t.Fatal("expected connect to fail with an invalid bearer token")
	}
}

// TestMCPRealToken exercises the real (DB-backed) token path: an issued token
// authenticates; once revoked it no longer connects. No KEN_DEV_TOKEN here.
func TestMCPRealToken(t *testing.T) {
	srv, st := testServer(t)
	ctx := context.Background()

	actorID, err := st.FindOrCreateActor(ctx, "ai", "review-agent")
	if err != nil {
		t.Fatalf("actor: %v", err)
	}
	tok, err := st.IssueToken(ctx, actorID, []string{"read"}, "test")
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	connect := func(bearer string) (*mcp.ClientSession, error) {
		cli := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
		return cli.Connect(ctx, &mcp.StreamableClientTransport{
			Endpoint: srv.URL, HTTPClient: clientWithToken(bearer), DisableStandaloneSSE: true, MaxRetries: -1,
		}, nil)
	}

	sess, err := connect(tok)
	if err != nil {
		t.Fatalf("connect with real token: %v", err)
	}
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "kb_search", Arguments: map[string]any{"query": "docker build reinstalls dependencies layer cache"}})
	if err != nil || res.IsError {
		t.Fatalf("kb_search with real token failed: err=%v res=%+v", err, res)
	}
	sess.Close()

	// Revoke -> the same token no longer connects.
	parts := strings.SplitN(tok, "_", 3)
	if err := st.RevokeToken(ctx, parts[1]); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if s2, err := connect(tok); err == nil {
		s2.Close()
		t.Fatal("a revoked token must not connect")
	}
}
