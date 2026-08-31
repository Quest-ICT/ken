package stationserver

import (
	"net/http"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// testHandler builds an HTTP surface carrying this package's tools, FOR TESTS ONLY.
//
// *** IT LIVES IN A _test.go FILE ON PURPOSE, AND THAT IS THE WHOLE POINT. ***
//
// Production used to export NewHTTPHandler for /station/mcp. That endpoint was deleted in 4.0.0
// and the constructor was not: main.go stopped calling it, so a complete MCP server, its
// instruction text and its middleware were compiled and wired on every boot and reachable by
// nothing. 5.0.0 deleted it.
//
// The tests below still want a transport — auth refusals, the directory, room requests and
// session identity are all worth asserting over HTTP rather than by calling a handler func. So the
// harness is built HERE, where it is unmistakably a harness. A constructor in production code that
// only tests call is the shape that produced the dead server in the first place, and the shape
// that lets a test certify a path no deployment serves.
//
// WHAT IT DOES NOT COVER, stated so nobody reads more into a green run than it earns: the SERVED
// assembly is internal/allserver, which chains all three surfaces' middleware onto one server.
// This harness chains only this package's. Tests that care about the chain belong in allserver,
// and several already live there.
func testHandler(t *testing.T, d Deps) http.Handler {
	t.Helper()
	h := &Handler{}
	h.SetLimits(d.taskLim(), d.noteLim(), d.lockerLim(), d.vaultLim())
	d.limits = func() *limits { return h.lim.Load() }

	s := mcp.NewServer(&mcp.Implementation{Name: "ken-station-test", Version: "test"},
		&mcp.ServerOptions{KeepAlive: mcpKeepAlive})
	RegisterTools(s, d)
	inner := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s },
		&mcp.StreamableHTTPOptions{SessionTimeout: sessionTimeout})
	return authMiddleware(d.Store, d.TokenLimiter, d.Metrics, d.SkipTokenTouch, inner)
}
