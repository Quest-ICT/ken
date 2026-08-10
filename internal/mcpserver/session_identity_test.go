package mcpserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Quest-ICT/ken/internal/store"
)

// A tool handler must act as the principal that made THIS call, not the one that
// happened to open the MCP session.
//
// The go-sdk binds a session to the INITIALIZE request's context —
// `server.Connect(req.Context(), transport, connectOpts)` in mcp/streamable.go — so
// anything a handler reads from its context is frozen at connect. Ken's auth
// middleware runs per HTTP request and puts a principal in the request context, but
// the handler never sees that one: it sees the connect-time principal, forever.
//
// WHY THIS MATTERS MORE THAN A STALE SCOPE. Ken records author_actor_id on every
// entry_version, and its whole model is "who said this, and is it second-hand". A
// handler that authors under the wrong actor writes false provenance into the durable
// record — the one thing the curation gate cannot repair later, because a human
// promoting the entry sees the wrong author and no signal that it is wrong.
//
// It is latent today, because a client uses one token for the life of a connection.
// It stops being latent the moment one credential carries several capability families
// that a human can toggle after consent, which is the direction Ken is going: a
// family switched off mid-session would keep working until the client reconnected.
func TestHandlerActsAsTheCallerNotTheConnectionOpener(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	actorA, err := st.FindOrCreateActor(ctx, "ai", "opener")
	if err != nil {
		t.Fatal(err)
	}
	actorB, err := st.FindOrCreateActor(ctx, "ai", "caller")
	if err != nil {
		t.Fatal(err)
	}
	if actorA == actorB {
		t.Fatal("setup: both actors resolved to one id, so authorship cannot discriminate")
	}
	tokA, err := st.IssueToken(ctx, actorA, []string{"read", "write-draft", "propose"}, "opener")
	if err != nil {
		t.Fatal(err)
	}
	tokB, err := st.IssueToken(ctx, actorB, []string{"read", "write-draft", "propose"}, "caller")
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(NewHTTPHandler(Deps{Store: st, DedupSecret: []byte("test-dedup-secret")}))
	defer srv.Close()

	var sessID string
	call := func(tok, body string) string {
		t.Helper()
		req, _ := http.NewRequest("POST", srv.URL+"/mcp", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if sessID != "" {
			req.Header.Set("Mcp-Session-Id", sessID)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if s := resp.Header.Get("Mcp-Session-Id"); s != "" {
			sessID = s
		}
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}

	// Session opened by A.
	call(tokA, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`)
	call(tokA, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)

	// B searches to obtain a dedup token, then saves. Both calls present B's bearer
	// and both are authenticated as B by the middleware.
	dct := dedupTokenFrom(t, call(tokB, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"kb_search","arguments":{"query":"anything"}}}`))
	saved := call(tokB, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"kb_save","arguments":{`+
		`"title":"who wrote this","summary":"provenance probe","problem":"p","solution":"s",`+
		`"triggers":["t"],"applies_to":["a"],"confidence":0.6,"kind":"reference",`+
		`"dedup_check_token":"`+dct+`"}}}`)
	if strings.Contains(saved, `"isError":true`) {
		t.Fatalf("setup: the save failed, so authorship cannot be read: %s", saved)
	}

	var author int64
	if err := st.R.QueryRow(
		`SELECT author_actor_id FROM entry_version ORDER BY id DESC LIMIT 1`).Scan(&author); err != nil {
		t.Fatalf("read authorship: %v", err)
	}

	if author == actorA {
		t.Fatalf("the entry was authored by actor %d, who merely OPENED the session — "+
			"the caller was actor %d and presented their own bearer on the very request that wrote it.\n"+
			"Every entry_version carries this field, a human reads it when deciding whether to promote, "+
			"and nothing on the page says it is wrong.", actorA, actorB)
	}
	if author != actorB {
		t.Fatalf("authored by actor %d, which is neither the opener (%d) nor the caller (%d)", author, actorA, actorB)
	}
}

// dedupTokenFrom pulls the dedup_check_token out of an SSE tool result. kb_save
// requires one, so without it the test above cannot reach the write path at all.
func dedupTokenFrom(t *testing.T, sse string) string {
	t.Helper()
	sc := bufio.NewScanner(bytes.NewReader([]byte(sse)))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimPrefix(sc.Text(), "data: ")
		var env struct {
			Result struct {
				StructuredContent struct {
					DedupCheckToken string `json:"dedup_check_token"`
				} `json:"structuredContent"`
			} `json:"result"`
		}
		if json.Unmarshal([]byte(line), &env) == nil && env.Result.StructuredContent.DedupCheckToken != "" {
			return env.Result.StructuredContent.DedupCheckToken
		}
	}
	t.Fatalf("no dedup_check_token in tool result: %s", sse)
	return ""
}

// A session must not outlive its credential indefinitely. The go-sdk expires idle
// sessions only when the handler sets SessionTimeout; every Ken handler passes nil
// options today, so a connection opened once is authorized forever from the
// handler's point of view.
//
// Asserted on the value the handler is BUILT with rather than by waiting out a
// timeout: a test that sleeps long enough to observe expiry would be slow enough that
// someone eventually skips it, and this is the kind of setting that only ever
// regresses by being quietly dropped.
func TestMCPSessionsExpire(t *testing.T) {
	if sessionTimeout <= 0 {
		t.Fatal("MCP sessions have no timeout, so one opened with a credential stays authorized " +
			"for as long as the client holds the connection, whatever happens to that credential afterwards")
	}
	if sessionTimeout > maxReasonableSessionTimeout {
		t.Fatalf("sessionTimeout is %v, which is long enough that it is a timeout in name only", sessionTimeout)
	}
}
