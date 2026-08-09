// Package web serves Ken's human-facing UI: the first-run setup wizard, login
// (Argon2id + server-side session + CSRF + per-IP login guard), search/browse,
// and the proposal review queue (diff + promote/reject). The human curates; the
// UI never creates brand-new entries — that is the AI's job over MCP.
package web

import (
	"context"
	"log"
	"net/http"

	"github.com/Quest-ICT/ken/internal/comm"
	"github.com/Quest-ICT/ken/internal/i18n"
	"github.com/Quest-ICT/ken/internal/settings"
	"github.com/Quest-ICT/ken/internal/store"
)

// Deps are the collaborators the web UI needs.
type Deps struct {
	Store *store.Store
	// SecureCookies marks session cookies Secure and uses the __Host- prefix
	// (required behind TLS; disable for plain-HTTP dev).
	SecureCookies bool
	// SetupToken pins the one-time /setup token (env KEN_SETUP_TOKEN); if empty a
	// random one is generated and logged at startup while an admin is absent.
	SetupToken string
	// TrustedProxies is a comma-separated CIDR list (env KEN_TRUSTED_PROXIES);
	// X-Forwarded-For is honored for the login guard only when the direct peer
	// matches one of these. Used only when Settings is nil (tests); otherwise the
	// live value from Settings wins.
	TrustedProxies string
	// Settings, when set, makes the client-IP resolver, login lockout and session
	// TTL read live values, and enables the /settings admin page.
	Settings *settings.Live
	// OAuthEnabled mounts the interactive OAuth consent flow (/oauth/authorize)
	// and the Connectors management section. Mirrors KEN_OAUTH_ENABLED; the
	// stateless discovery/token endpoints are mounted separately in main.go.
	OAuthEnabled bool
	// I18n provides the reloadable UI translations. If nil, a Manager with the
	// embedded English, Spanish + French defaults (no external override dir) is used.
	I18n *i18n.Manager
	// Comm, when set, mounts the inter-session communication console (/comm) —
	// where a human mints the pairing codes that are the ONLY way a channel comes
	// into existence, and revokes channels or endpoints. Nil hides the page and its
	// nav entry entirely.
	//
	// COMM is core and on by default, so main.go passes a live store unless the
	// operator opted out with KEN_COMM_ENABLED=0 — or unless comm.db could not be
	// opened, which degrades to nil on purpose so an expendable database cannot take
	// the durable knowledge base down. Nil therefore means BOTH "switched off" and
	// "failed to open", and the console cannot tell them apart; the server log says
	// which. The comm MCP endpoint is mounted separately in main.go.
	Comm *comm.Store
	// StationsEnabled, when true, mounts the stations console (/stations) — where a
	// human approves a session's request for a working identity and TYPES its name,
	// which is the capability every station tool is denied. The station MCP endpoint
	// is mounted separately in main.go.
	//
	// Stations are core and on by default; main.go passes true unless the operator set
	// KEN_STATION_ENABLED=0. This field stays an explicit bool rather than defaulting
	// on, because the zero value belongs to whoever CONSTRUCTS Deps — tests build one
	// deliberately without stations — and a library that turns a surface on because a
	// caller forgot a field is worse than one that waits to be told.
	//
	// Independent of Comm on purpose, and MORE easily forgotten now that both default
	// on: the notebook and task list need no peers, so KEN_COMM_ENABLED=0 must leave
	// stations entirely working (S2).
	StationsEnabled bool
}

// Handler returns the web UI mux. When no admin exists yet it logs the one-time
// setup token so the operator (who can read the log) can complete first-run setup.
func Handler(d Deps) http.Handler {
	a := newApp(d)
	if n, err := d.Store.CountHumanUsers(context.Background()); err == nil && n == 0 {
		log.Printf("FIRST-RUN SETUP REQUIRED — no admin yet. Open  /setup?token=%s  (SETUP TOKEN: %s)", a.setupToken, a.setupToken)
	}
	return a.routes()
}
