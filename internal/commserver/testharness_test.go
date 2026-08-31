package commserver

import (
	"net/http"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// testHandler builds an HTTP surface carrying this package's tools, FOR TESTS ONLY.
//
// See the twin in internal/stationserver for the full reasoning. In short: production stopped
// serving /comm/mcp in 4.0.0 and kept building its server anyway until 5.0.0 deleted it, so a
// constructor that only tests call is exactly the shape that produced a server nothing reached.
// The harness lives here, where it cannot be mistaken for the served assembly.
//
// NewHTTPHandler still exists and is still production code — it owns the long-poll waiters, the
// live max-poll-wait and Drain — but it no longer builds a server, so tests that want a transport
// build one here.
func testHandler(t *testing.T, d Deps) http.Handler {
	t.Helper()
	h := NewHTTPHandler(d)
	s := mcp.NewServer(&mcp.Implementation{Name: "ken-comm-test", Version: "test"},
		&mcp.ServerOptions{KeepAlive: mcpKeepAlive})
	RegisterTools(s, d, h)
	inner := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s },
		&mcp.StreamableHTTPOptions{SessionTimeout: sessionTimeout})
	return authMiddleware(d.Store, d.TokenLimiter, d.Metrics, d.SkipTokenTouch, inner)
}
