package mcpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Quest-ICT/ken/internal/store"
)

// oauthMCPServer builds an MCP handler with the OAuth 401-challenge wired, plus a
// live OAuth access token for a connector actor.
func oauthMCPServer(t *testing.T) (*httptest.Server, *store.Store, string, int64) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	human, _ := st.FindOrCreateActor(ctx, "human", "curator")
	conn, _ := st.FindOrCreateActor(ctx, "ai", "Claude")
	const redir = "https://claude.ai/api/mcp/auth_callback"
	clientID, _ := st.RegisterOAuthClient(ctx, "Claude", []string{redir})
	code, _ := st.CreateOAuthGrantAndCode(ctx, store.NewAuthCode{
		ClientID: clientID, ConnectorActorID: conn, HumanActorID: human,
		RedirectURI: redir, CodeChallenge: "c", CodeChallengeMethod: "S256",
		Scope: "read write offline_access", Resource: "https://host/mcp",
	}, time.Minute)
	cd, _ := st.PeekOAuthCode(ctx, code)
	access, _, err := st.ExchangeOAuthCode(ctx, code, cd.GrantID, time.Hour, 90*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	h := NewHTTPHandler(Deps{
		Store: st, DedupSecret: []byte("test-dedup-secret"),
		ResourceMetadataURL: func(*http.Request) string {
			return "https://host/.well-known/oauth-protected-resource/mcp"
		},
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, st, access, conn
}

// TestMCPOAuthTokenReadAndWrite: an OAuth access token authenticates for both a
// read (kb_search) and a write (kb_save), and the created draft is authored by
// the connector actor.
func TestMCPOAuthTokenReadAndWrite(t *testing.T) {
	srv, st, access, conn := oauthMCPServer(t)
	ctx := context.Background()

	cli := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	sess, err := cli.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: srv.URL, HTTPClient: clientWithToken(access), DisableStandaloneSSE: true, MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatalf("connect with OAuth access token: %v", err)
	}
	defer sess.Close()

	// read scope: kb_search returns a dedup token.
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "kb_search", Arguments: map[string]any{"query": "nonexistent zebra"}})
	if err != nil || res.IsError {
		t.Fatalf("kb_search over OAuth failed: err=%v res=%+v", err, res)
	}
	var so struct {
		DedupCheckToken string `json:"dedup_check_token"`
	}
	decodeResult(t, res, &so)
	if so.DedupCheckToken == "" {
		t.Fatal("expected a dedup_check_token from kb_search")
	}

	// write-draft scope: kb_save creates a draft.
	res, err = sess.CallTool(ctx, &mcp.CallToolParams{Name: "kb_save", Arguments: map[string]any{
		"dedup_check_token": so.DedupCheckToken,
		"kind":              "reference", "title": "OAuth draft", "summary": "created by a connector",
	}})
	if err != nil || res.IsError {
		t.Fatalf("kb_save over OAuth failed: err=%v res=%+v", err, res)
	}
	var out struct {
		Slug string `json:"slug"`
	}
	decodeResult(t, res, &out)

	// The draft is authored by the connector actor (attribution flows from the token).
	var author int64
	if err := st.R.QueryRowContext(ctx,
		`SELECT ev.author_actor_id FROM entry_version ev JOIN entry e ON e.id=ev.entry_id WHERE e.slug=?`, out.Slug).
		Scan(&author); err != nil {
		t.Fatalf("read author: %v", err)
	}
	if author != conn {
		t.Fatalf("draft author = %d, want connector actor %d", author, conn)
	}
}

// TestMCPServerInstructions: a connecting client receives Ken's usage
// instructions (the search-first loop) via the initialize response, so an AI
// learns how to use Ken without a human pasting a prompt.
func TestMCPServerInstructions(t *testing.T) {
	srv, _, access, _ := oauthMCPServer(t)
	ctx := context.Background()
	cli := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	sess, err := cli.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: srv.URL, HTTPClient: clientWithToken(access), DisableStandaloneSSE: true, MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	ins := sess.InitializeResult().Instructions
	if !strings.Contains(ins, "Ken") || !strings.Contains(ins, "kb_search") || !strings.Contains(ins, "kb_record_outcome") {
		t.Fatalf("server instructions missing the usage loop, got: %q", ins)
	}
}

// TestMCP401Challenge: an unauthenticated /mcp request returns 401 with the RFC
// 9728 WWW-Authenticate discovery pointer, and CORS is present.
func TestMCP401Challenge(t *testing.T) {
	srv, _, _, _ := oauthMCPServer(t)

	resp, err := http.Post(srv.URL, "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", resp.StatusCode)
	}
	wa := resp.Header.Get("WWW-Authenticate")
	if !strings.Contains(wa, `resource_metadata="https://host/.well-known/oauth-protected-resource/mcp"`) {
		t.Fatalf("401 must carry the resource_metadata pointer, got %q", wa)
	}
	if !strings.Contains(resp.Header.Get("Access-Control-Expose-Headers"), "WWW-Authenticate") {
		t.Fatalf("must expose WWW-Authenticate for CORS, got %q", resp.Header.Get("Access-Control-Expose-Headers"))
	}

	// CORS preflight is answered.
	req, _ := http.NewRequest("OPTIONS", srv.URL, nil)
	pre, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	pre.Body.Close()
	if pre.StatusCode != http.StatusNoContent {
		t.Fatalf("preflight want 204, got %d", pre.StatusCode)
	}
}
