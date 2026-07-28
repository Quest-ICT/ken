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
	// into existence, and revokes channels or endpoints. Nil (the default) hides
	// the page and its nav entry entirely. Mirrors KEN_COMM_ENABLED; the comm MCP
	// endpoint is mounted separately in main.go.
	Comm *comm.Store
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
