// SPDX-License-Identifier: AGPL-3.0-only

// Command ken runs the Ken knowledge-base service and its admin CLI.
//
//	ken [serve] [flags]        run the MCP + web server (default)
//	ken token add|list|revoke  manage API tokens (MCP access)
//	ken user  add|list         manage human users (web login)
package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Quest-ICT/ken/internal/clientip"
	"github.com/Quest-ICT/ken/internal/comm"
	"github.com/Quest-ICT/ken/internal/commserver"
	"github.com/Quest-ICT/ken/internal/embed"
	"github.com/Quest-ICT/ken/internal/health"
	"github.com/Quest-ICT/ken/internal/i18n"
	"github.com/Quest-ICT/ken/internal/mcpserver"
	"github.com/Quest-ICT/ken/internal/metrics"
	"github.com/Quest-ICT/ken/internal/oauth"
	"github.com/Quest-ICT/ken/internal/ratelimit"
	"github.com/Quest-ICT/ken/internal/settings"
	"github.com/Quest-ICT/ken/internal/stationserver"
	"github.com/Quest-ICT/ken/internal/store"
	"github.com/Quest-ICT/ken/internal/version"
	"github.com/Quest-ICT/ken/internal/web"
	"github.com/Quest-ICT/ken/internal/webtls"
)

func main() {
	args := os.Args[1:]
	if len(args) > 0 {
		switch args[0] {
		case "token":
			runToken(args[1:])
			return
		case "user":
			runUser(args[1:])
			return
		case "backup":
			runBackup(args[1:])
			return
		case "import":
			runImport(args[1:])
			return
		case "embed":
			runEmbed(args[1:])
			return
		case "station":
			runStation(args[1:])
			return
		case "serve":
			runServe(args[1:])
			return
		case "version", "--version", "-v":
			runVersion()
			return
		case "help", "-h", "--help":
			usage()
			return
		}
	}
	runServe(args) // no subcommand: treat args as serve flags
}

// runVersion prints the build identity on stdout. Kept trivial and dependency-free
// so it is the one command that always works: it is the first thing an operator or
// bug reporter runs, and it is also the only surface that reveals a SILENTLY failed
// -ldflags injection (`go build` does not error on an unknown -X symbol, so a stale
// symbol path ships a binary quietly reporting the compiled-in dev version).
func runVersion() {
	fmt.Println(version.Line())
	fmt.Println("source: " + version.SourceURL())
}

func usage() {
	fmt.Fprint(os.Stderr, `Ken — AI-first knowledge base

Usage:
  ken [serve] [flags]        run the MCP + web server (default)
  ken token add|list|revoke  manage API tokens (MCP access)
  ken user  add|list         manage human users (web login)
  ken backup snapshot|verify make/verify a consistent DB snapshot
  ken import --dir DIR       import flat memory .md files as curated entries
  ken embed backfill|status  compute embeddings for semantic search
  ken station add|list|key   create and name stations, mint their keys (docs/STATIONS.md)
  ken version                print the build version and source location

Serve flags:
  --db PATH         SQLite path (env KEN_DB, default ./data/ken.db)
  --addr ADDR       listen address (env KEN_ADDR; default :8080 plain, :443 with TLS)
  --secure-cookies  force Secure/__Host- session cookies (implied when TLS is on)
  --demo-seed       insert a demo curated entry (dev)

TLS (env) — prefer HTTPS; plain HTTP (off) is safe ONLY behind a TLS-terminating proxy:
  KEN_TLS           off (default) | acme | file
  KEN_TLS_DOMAINS   acme: comma-separated hostnames the certificate is issued for
  KEN_TLS_EMAIL     acme: Let's Encrypt account email (recommended)
  KEN_TLS_CACHE     acme: certificate cache dir (default <db dir>/acme)
  KEN_TLS_CERT      file: PEM certificate path
  KEN_TLS_KEY       file: PEM private key path
  KEN_HTTP_ADDR     TLS modes: plain-HTTP redirect + ACME HTTP-01 listener (default :80; "" disables)
  KEN_TLS_QUIET_HANDSHAKE  drop benign "TLS handshake error" scanner noise from the log (on|off, default off)

Env:
  KEN_DEV_TOKEN     if set, a static dev bearer token (read,write-draft,propose) — DEV ONLY (refused with TLS)
  KEN_DEDUP_SECRET  HMAC key for the search->save dedup token (random if unset)
  KEN_SETUP_TOKEN   pin the one-time /setup token (random + logged if unset)
  KEN_TRUSTED_PROXIES  CIDR allowlist whose X-Forwarded-For is trusted (proxy deployments only)
  KEN_SECURE_COOKIES   force Secure/__Host- cookies behind a TLS-terminating proxy (same as --secure-cookies)
  KEN_SOURCE_URL    where THIS instance's source can be obtained — shown in the UI footer.
                    Set it if you run a MODIFIED Ken: AGPL-3.0 §13 requires a network
                    service to offer its own Corresponding Source, not upstream's.

Semantic search (optional; embeddings are OFF unless KEN_EMBED_PROVIDER is set):
  KEN_EMBED_PROVIDER  http (hosted, OpenAI-compatible) | hash (offline; tests/air-gapped) | unset = off
  KEN_EMBED_URL       http: the embeddings endpoint, e.g. https://api.openai.com/v1/embeddings  [required for http]
  KEN_EMBED_MODEL     http: model name, e.g. text-embedding-3-small                              [required for http]
  KEN_EMBED_DIM       vector dimension — http: required; hash: default 256
  KEN_EMBED_KEY       http: bearer/API key (optional)
                      after configuring, backfill existing entries with:  ken embed backfill   (check: ken embed status)

Rate limiting (env; on by default — loopback + KEN_RATELIMIT_ALLOW_CIDRS + /healthz are exempt):
  KEN_RATELIMIT              on (default) | off
  KEN_RATELIMIT_IP_RPM       per-IP requests/min (default 120); KEN_RATELIMIT_IP_BURST (default 120)
  KEN_RATELIMIT_TOKEN_RPM    per-token requests/min (default 120); KEN_RATELIMIT_TOKEN_BURST (default 60)
  KEN_RATELIMIT_BLOCK_AFTER  over-limit rejections before an IP is auto-blocked (default 100)
  KEN_RATELIMIT_LOCKOUT_SEC  auto-block duration, seconds (default 900)
  KEN_RATELIMIT_ALLOW_CIDRS  extra always-allowed CIDRs (comma-separated)

Inter-session communication (OFF unless enabled — see docs/COMM.md):
  KEN_COMM_ENABLED  1 = expose the comm MCP endpoint at /comm/mcp + console at /comm (default off)
  KEN_COMM_DB       message database path (default <db dir>/comm/comm.db; NOT backed up — it is expendable)
                    needs a DEDICATED token:  ken token add --actor comm-dev --scopes comm
                    (add comm-file to that token for file exchange: --scopes comm,comm-file)
                    a token may hold comm scopes or knowledge-base scopes, never both
                    file exchange is a separate live setting (Settings -> Inter-session comms)

Stations — durable AI working identities (OFF unless enabled — see docs/STATIONS.md):
  KEN_STATION_ENABLED  1 = expose the station MCP endpoint at /station/mcp + console at /stations (default off)
                    a station is created and NAMED by a human; a session staffs it
                    create it, then mint its key:  ken station add --name prod-ops
                                                   ken station key --station prod-ops --label laptop
                    (add --locker to that key for the file locker)
                    NOT ken token add: /station needs a kens_ key BOUND to a station,
                    which only ken station key mints
                    a token holds knowledge-base, comm, or station scopes — station and
                    comm may combine, neither may mix with knowledge-base scopes
                    works with COMM off: the notebook and task list need no peers

Observability (health always on; metrics on by default, loopback-only):
  /healthz          liveness (public, plain "ok")
  /health           readiness JSON (public; component details only to loopback/token/CIDR)
  /metrics          Prometheus text (loopback + KEN_METRICS_CIDRS, or KEN_METRICS_TOKEN)
  KEN_METRICS       on (default) | off — expose /metrics at all
  KEN_METRICS_TOKEN bearer token a remote Prometheus presents to scrape /metrics
  KEN_METRICS_CIDRS extra CIDRs allowed to scrape /metrics + see /health details
`)
}

func runServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	dbPath := fs.String("db", envOr("KEN_DB", "./data/ken.db"), "SQLite database path")
	addr := fs.String("addr", os.Getenv("KEN_ADDR"), "listen address (default :8080 plain, :443 with TLS; env KEN_ADDR)")
	demoSeed := fs.Bool("demo-seed", false, "insert a demo curated entry on startup (dev)")
	secureCookies := fs.Bool("secure-cookies", false, "force Secure + __Host- session cookies (implied when TLS is on)")
	_ = fs.Parse(args)

	st := mustOpenStore(*dbPath)
	defer st.Close()

	// Resolve the TLS posture (KEN_TLS=off|acme|file). The ACME cert cache defaults
	// to <db dir>/acme. Terminating TLS in-process implies Secure session cookies.
	tlsCfg, err := webtls.FromEnv(*addr, filepath.Dir(*dbPath))
	if err != nil {
		log.Fatalf("tls config: %v", err)
	}
	// Secure/__Host- cookies when TLS terminates in-process, or the operator opts in
	// with --secure-cookies / KEN_SECURE_COOKIES (behind a TLS-terminating proxy).
	// Deliberately NOT inferred from KEN_TRUSTED_PROXIES: trusting a proxy's XFF does
	// not mean that proxy terminates TLS, and Secure cookies over a plain-HTTP hop
	// would silently break login.
	secure := *secureCookies || tlsCfg.Enabled() || envTrue("KEN_SECURE_COOKIES")

	if *demoSeed {
		slug, err := st.SeedDemo(context.Background())
		if err != nil {
			log.Fatalf("demo seed: %v", err)
		}
		log.Printf("seeded demo entry: %s", slug)
	}
	if os.Getenv("KEN_DEV_TOKEN") != "" {
		if secure {
			log.Fatal("refusing to start: KEN_DEV_TOKEN is set together with a TLS/production posture " +
				"(--secure-cookies or KEN_TLS=acme|file). The dev token is a static, unrevocable credential — " +
				"unset KEN_DEV_TOKEN for any TLS deployment.")
		}
		log.Print("WARNING: KEN_DEV_TOKEN is set — the MCP endpoint accepts a static, unrevocable dev token. DEV ONLY.")
	}

	dedupSecret := loadOrCreateDedupSecret(*dbPath)

	emb, err := embed.FromEnv()
	if err != nil {
		log.Fatalf("embeddings: %v", err)
	}
	if emb != nil {
		log.Printf("embeddings enabled: %s (dim %d)", emb.ID(), emb.Dimension())
	}

	// Live runtime settings: env/compiled defaults + persisted overrides (app_setting),
	// atomically swapped so the web UI can retune limits/proxies/domains without a restart.
	live := settings.New(st, settings.DefaultsFromEnv())
	if err := live.Load(context.Background()); err != nil {
		log.Fatalf("load settings: %v", err)
	}

	// Observability: a tiny, dependency-free metrics registry + a health checker.
	reg := metrics.New(version.Version)
	registerCollectors(reg, st)
	checker := health.New(filepath.Dir(*dbPath))
	checker.AddPing("db", func(ctx context.Context) error { return st.R.PingContext(ctx) })

	// The rate limiter is (re)built from the live snapshot, but ONLY when a
	// rate-limit-relevant field changed — so an unrelated settings edit doesn't reset
	// in-progress auto-block / throttle state.
	rlGuard := &ratelimit.ReloadableGuard{}
	rlToken := &ratelimit.ReloadableBucket{}
	rebuildRL := func(s *settings.Snapshot) {
		rlGuard.Store(guardFromSettings(s, reg))
		rlToken.Store(tokenBucketFromSettings(s))
	}
	rebuildRL(live.Current())
	lastRL := rlRelevant(live.Current().Values)
	live.OnChange(func(s *settings.Snapshot) {
		if k := rlRelevant(s.Values); k != lastRL {
			lastRL = k
			rebuildRL(s)
		}
	})

	// Metrics exposure: loopback + KEN_METRICS_CIDRS are always allowed; a remote
	// scraper needs KEN_METRICS_TOKEN. /metrics leaks internal counts, so it is
	// NOT public. /healthz (liveness) and /health (readiness JSON) stay public,
	// but /health reveals per-component details only to an operator (loopback/token).
	metricsOn := envBoolDefault("KEN_METRICS", true)
	metricsToken := os.Getenv("KEN_METRICS_TOKEN")
	metricsAllow := clientip.ParseCIDRs(os.Getenv("KEN_METRICS_CIDRS"))

	// Optional OAuth 2.1 authorization server (off unless KEN_OAUTH_ENABLED),
	// so claude.ai can add ken as a remote-MCP "custom connector" (OAuth-only on
	// personal accounts). publicBaseURL is the canonical https origin used verbatim
	// as the issuer + in every discovery URL: the configured ACME domain when set
	// (stable, request-independent), else derived from the request.
	oauthEnabled := envBoolDefault("KEN_OAUTH_ENABLED", false)
	publicBaseURL := func(r *http.Request) string {
		host := r.Host
		scheme := "http"
		if r.TLS != nil || secure {
			scheme = "https"
		}
		snap := live.Current()
		// Honor X-Forwarded-* only from a declared trusted proxy (anti-spoofing).
		if res := snap.Resolver; res != nil && res.TrustedPeer(r) {
			if xh := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Host"), ",")[0]); xh != "" {
				host = xh
			}
			if strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
				scheme = "https"
			}
		}
		// When ACME domains are configured, the issuer must be a canonical https
		// origin: use the contacted host if it IS one of them (multi-domain byte-match),
		// else fall back to the primary — a client can't dictate our issuer via Host.
		if doms := snap.Domains; len(doms) > 0 {
			for _, d := range doms {
				if strings.EqualFold(host, d) {
					return "https://" + host
				}
			}
			return "https://" + doms[0]
		}
		return scheme + "://" + host
	}
	var oauthSrv *oauth.Server
	if oauthEnabled {
		oauthSrv = oauth.New(st, publicBaseURL, oauth.Config{})
		log.Print("OAuth: authorization server ENABLED — discovery, dynamic client registration, and token endpoints are live (claude.ai custom connectors can authenticate).")
	}

	// Inter-session communication (docs/COMM.md) — OFF unless KEN_COMM_ENABLED.
	// A default install stays exactly the curated knowledge base it advertises:
	// with COMM off, no second database is created, no tools are registered, and
	// no instruction section reaches any agent.
	//
	// Opened BEFORE the MCP deps are built because the knowledge base's write path
	// needs the hearsay check (docs/COMM.md §7), which reads this store.
	var commStore *comm.Store
	if os.Getenv("KEN_COMM_ENABLED") == "1" {
		// Every failure here DEGRADES rather than aborting: an unwritable directory
		// or a corrupt comm.db must not take the durable knowledge base down with an
		// expendable one. That is the whole point of "COMM may fail; the KB stays UP"
		// (docs/COMM.md §5) — a log.Fatal here would make the isolation claim false
		// at the very first failure an operator is likely to hit.
		commPath := envOr("KEN_COMM_DB", filepath.Join(filepath.Dir(*dbPath), "comm", "comm.db"))
		if cs, err := openComm(commPath, commLimits(live.Current())); err != nil {
			log.Printf("COMM: DISABLED — %v. The knowledge base is unaffected; fix the comm database or unset KEN_COMM_ENABLED.", err)
		} else {
			defer cs.Close()
			commStore = cs
		}
	}

	mux := http.NewServeMux()
	mcpDeps := mcpserver.Deps{
		Store: st, DedupSecret: dedupSecret, Embedder: emb, TokenLimiter: rlToken, Metrics: reg,
		CurationLangs: live.Current().CurationLangSet,
	}
	if commStore != nil {
		// Mark versions authored by an actor that recently RECEIVED an inter-session
		// message, so the curator can ask for a first-hand citation (docs/COMM.md §7).
		// Actor-keyed, not token-keyed: a COMM token is dedicated, so the receiving
		// token is never the authoring one.
		// A closure keeps internal/mcpserver free of any dependency on the optional
		// subsystem. An error yields false — the marker is advisory and must never
		// block a save.
		mcpDeps.CommProvenance = func(ctx context.Context, actorID int64) bool {
			win := live.Current().CommProvenanceWindowSec
			if win <= 0 {
				return false
			}
			got, err := commStore.ReceivedSince(ctx, actorID, win)
			if err != nil {
				log.Printf("comm: provenance check: %v", err)
				return false
			}
			return got
		}
	}
	if oauthSrv != nil {
		mcpDeps.ResourceMetadataURL = oauthSrv.ResourceMetadataURL // 401 → discovery challenge
	}
	mcpHandler := mcpserver.NewHTTPHandler(mcpDeps)
	mux.Handle("/mcp", mcpHandler)
	// Rebuild the AI-facing instructions live when the curation language(s) change,
	// but only then — an unrelated settings edit shouldn't churn the MCP server.
	lastCurLangs := strings.Join(live.Current().CurationLangSet, ",")
	live.OnChange(func(s *settings.Snapshot) {
		if k := strings.Join(s.CurationLangSet, ","); k != lastCurLangs {
			lastCurLangs = k
			mcpHandler.SetCurationLangs(s.CurationLangSet)
		}
	})
	// Inter-session communication (docs/COMM.md) — OFF unless KEN_COMM_ENABLED.
	// A default install stays exactly the curated knowledge base it advertises:
	// with COMM off, no second database is created, no tools are registered, and
	// no instruction section reaches any agent.
	//
	// Deliberately NOT registered with the health checker: that marks the whole
	// service DOWN on any component failure and /health then returns 503, so a
	// wedged COMM sweeper would pull a healthy knowledge base out of rotation.
	// COMM may fail; the KB stays UP.
	// Mount the COMM endpoint and apply its limits live.
	//
	// Deliberately NOT registered with the health checker: that marks the whole
	// service DOWN on any component failure and /health then returns 503, so a
	// wedged COMM sweeper would pull a healthy knowledge base out of rotation.
	// COMM may fail; the KB stays UP.
	var commHandler *commserver.Handler
	if commStore != nil {
		commDeps := commserver.Deps{
			Comm: commStore, Store: st, TokenLimiter: rlToken, Metrics: reg,
			MaxPollWaitSeconds: live.Current().CommPollWaitMaxSec,
		}
		commHandler = commserver.NewHTTPHandler(commDeps)
		// The byte relay: one-time-grant PUT/GET, gated on the comm-file scope and
		// the live comm_files_enabled switch. Mounted as a prefix because the grant
		// travels in the path.
		mux.Handle("/comm/files/", commserver.NewFileHandler(commDeps, commHandler))
		// The MCP endpoint is /comm/mcp, not /comm: the human console lives at /comm
		// (served by the web handler mounted on "/"), and a top-level exact "/comm"
		// route here would shadow it. It also reads better — /mcp and /comm/mcp are
		// the two machine surfaces, everything else is human.
		mux.Handle("/comm/mcp", commHandler)

		// Apply COMM limit edits live, and only when a COMM field actually changed —
		// same guard as the rate limiter, so an unrelated settings edit does not churn
		// the subsystem. Enabling COMM itself is NOT live: it opens a second database,
		// which is a restart-level act.
		lastComm := commRelevant(live.Current().Values)
		live.OnChange(func(sn *settings.Snapshot) {
			if k := commRelevant(sn.Values); k != lastComm {
				lastComm = k
				commStore.SetLimits(commLimits(sn))
				commHandler.SetMaxPollWait(sn.CommPollWaitMaxSec)
			}
		})
		registerCommCollectors(reg, commStore, commHandler)
		log.Printf("COMM: inter-session communication ENABLED at /comm/mcp + console at /comm (db=%s) — requires a dedicated token with the 'comm' scope", commStore.Path())
	}

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		rep := checker.Check(r.Context())
		code := http.StatusOK
		if rep.Status != "UP" {
			code = http.StatusServiceUnavailable
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(code)
		rep.WriteJSON(w, metricsAllowed(r, metricsToken, metricsAllow, live.Current().Resolver))
	})
	if metricsOn {
		mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
			if !metricsAllowed(r, metricsToken, metricsAllow, live.Current().Resolver) {
				http.NotFound(w, r) // 404 rather than 403 — don't advertise the endpoint
				return
			}
			w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
			reg.WriteText(r.Context(), w)
		})
	}
	// OAuth authorization-server endpoints (stateless, machine-to-machine JSON).
	// Mounted on the TOP-LEVEL mux so they sit at the true root, bypass the web
	// wizard-gate + CSP, and get their own CORS. The interactive /oauth/authorize
	// consent page lives on the web mux (it needs the human session).
	if oauthSrv != nil {
		mux.HandleFunc("/.well-known/oauth-authorization-server", oauthSrv.HandleASMetadata)
		mux.HandleFunc("/.well-known/oauth-protected-resource", oauthSrv.HandlePRMetadata)
		mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", oauthSrv.HandlePRMetadata)
		mux.HandleFunc("/oauth/register", oauthSrv.HandleRegister)
		mux.HandleFunc("/oauth/token", oauthSrv.HandleToken)
	}

	// The web UI (its own mux, incl. /healthz) is counted as the "web" surface;
	// the streaming MCP surface is tracked by per-tool counters instead.
	// UI translations: built-in English, Spanish + French, overridable/extendable by
	// dropping messages_<lang>.properties into KEN_I18N_DIR (default <data>/i18n).
	i18nDir := os.Getenv("KEN_I18N_DIR")
	if i18nDir == "" {
		i18nDir = filepath.Join(filepath.Dir(*dbPath), "i18n")
	}
	// Stations: durable, human-named working identities (docs/STATIONS.md). Gated on
	// its OWN flag alone — never on KEN_COMM_ENABLED, because the notebook and the task
	// list are valuable to a solo session with no peers (S2), and gating them behind a
	// messaging feature they have nothing to do with would be the wrong dependency.
	stationsEnabled := os.Getenv("KEN_STATION_ENABLED") == "1"
	if stationsEnabled {
		sd := stationserver.Deps{Store: st, TokenLimiter: rlToken, Metrics: reg}
		// The hearsay marker is keyed on the ACTOR, so it only works when COMM is on and
		// the station key was minted under the same actor as that machine's comm token.
		// With COMM off it is nil and the marking is absent — "no signal", never
		// "known clean" (§7).
		if commStore != nil {
			sd.Hearsay = func(ctx context.Context, actorID int64) bool {
				ok, err := commStore.ReceivedSince(ctx, actorID, live.Current().CommProvenanceWindowSec)
				return err == nil && ok
			}
			// Reachability for station_directory. This is the ONE place both handles
			// exist, so the adaptation lives here and the dependency stays one-way:
			// stationserver never imports comm. Absent when COMM is off, which the
			// directory reports as unknown rather than as everyone being idle.
			sd.Staffing = func(ctx context.Context) (map[string]stationserver.StationStaffing, error) {
				raw, err := commStore.StaffingByStation(ctx)
				if err != nil {
					return nil, err
				}
				out := make(map[string]stationserver.StationStaffing, len(raw))
				for id, s := range raw {
					out[id] = stationserver.StationStaffing{Endpoints: s.Endpoints, LastSeenAt: s.LastSeenAt}
				}
				return out, nil
			}
		}
		sd.TaskLimits, sd.NoteLimits, sd.LockerLimits = stationLimits(live.Current())
		stationHandler := stationserver.NewHTTPHandler(sd)
		// Live: an operator lowering a station cap is usually reacting to something
		// already growing, which is the worst case for "applies at the next restart".
		lastStation := stationRelevant(live.Current().Values)
		live.OnChange(func(sn *settings.Snapshot) {
			if k := stationRelevant(sn.Values); k != lastStation {
				lastStation = k
				stationHandler.SetLimits(stationLimits(sn))
			}
		})
		mux.Handle("/station/mcp", stationHandler)
		log.Printf("STATIONS: enabled at /station/mcp — requires a kens_ key bound to a station (ken station add --name <n>, then ken station key --station <n> --label <machine>)")
	}

	mux.Handle("/", reg.Counting("web", web.Handler(web.Deps{
		Store: st, SecureCookies: secure, SetupToken: os.Getenv("KEN_SETUP_TOKEN"),
		TrustedProxies: os.Getenv("KEN_TRUSTED_PROXIES"), Settings: live, OAuthEnabled: oauthEnabled,
		I18n: i18n.New(i18nDir), Comm: commStore, StationsEnabled: stationsEnabled,
	})))

	// The per-IP abuse guard is the outermost handler — ahead of web routing and MCP auth.
	handler := rlGuard.Wrap(mux)

	servers, err := tlsCfg.Build(handler, func(s *http.Server) {
		s.ReadHeaderTimeout = 10 * time.Second
		s.ReadTimeout = 60 * time.Second
		s.IdleTimeout = 120 * time.Second
		// No WriteTimeout: the MCP streamable transport holds long-lived SSE responses.
	}, func() []string { return live.Current().Domains })
	if err != nil {
		log.Fatalf("tls setup: %v", err)
	}

	// Housekeeping: periodically purge expired web sessions.
	janitorCtx, stopJanitor := context.WithCancel(context.Background())
	defer stopJanitor()
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-janitorCtx.Done():
				return
			case <-t.C:
				if n, err := st.DeleteExpiredSessions(janitorCtx); err == nil && n > 0 {
					log.Printf("housekeeping: purged %d expired sessions", n)
				}
				if oauthEnabled {
					_ = st.PurgeExpiredOAuth(janitorCtx) // spent codes + long-expired tokens
				}
				if stationsEnabled {
					// Expired UNREDEEMED binding vouchers. Redeemed ones are kept
					// deliberately: they answer "which key bound this endpoint",
					// which is the first question asked when a station key leaks.
					// Hourly is right — a voucher lives five minutes, so this is
					// about unbounded growth in a BACKED-UP database, not latency.
					if n, err := st.SweepBindingVouchers(janitorCtx); err == nil && n > 0 {
						log.Printf("housekeeping: swept %d expired binding voucher(s)", n)
					}
				}
			}
		}
	}()

	// COMM's sweeper runs on its own short cadence rather than joining the hourly
	// janitor above: at a sustained send rate a single sender writes a great deal
	// before an hourly sweep would first run, so a TTL is not a quota.
	//
	// The recover is load-bearing. COMM is the newest, highest-churn code in the
	// process, and an unrecovered panic here would take the mature knowledge base
	// down with it. It degrades instead — log and keep ticking — which is what
	// makes "COMM may fail; the KB stays UP" true rather than aspirational.
	if commStore != nil {
		go func() {
			t := time.NewTicker(time.Minute)
			defer t.Stop()
			for {
				select {
				case <-janitorCtx.Done():
					return
				case <-t.C:
					func() {
						defer func() {
							if r := recover(); r != nil {
								log.Printf("comm: sweeper panic recovered (comm degraded, knowledge base unaffected): %v", r)
							}
						}()
						if exp, purged, err := commStore.Sweep(janitorCtx); err != nil {
							log.Printf("comm: sweep: %v", err)
						} else if exp > 0 || purged > 0 {
							log.Printf("comm: swept %d expired, purged %d settled", exp, purged)
						}
					}()
				}
			}
		}()
	}

	// Report a serve failure through a channel so the normal return path runs
	// (and `defer st.Close()` fires) — a log.Fatal in the goroutine would skip it.
	serveErr := make(chan error, 1)
	log.Printf("Ken %s starting (db=%s)", version.Version, *dbPath)
	log.Print(tlsCfg.Describe())
	if tlsCfg.QuietHandshake {
		log.Print("TLS: benign handshake-error log noise is quieted (KEN_TLS_QUIET_HANDSHAKE)")
	}
	if s := live.Current(); s.RLEnabled {
		log.Printf("rate limit: per-IP %d/min (burst %d), per-token %d/min (burst %d); auto-block after %d, lockout %ds (loopback + allow-CIDRs + /healthz exempt)",
			s.IPPerMin, s.IPBurst, s.TokenPerMin, s.TokenBurst, s.BlockAfter, s.LockoutSec)
	} else {
		log.Print("rate limit: OFF")
	}
	if metricsOn {
		scope := "loopback only"
		if metricsToken != "" {
			scope = "loopback + token"
		}
		if len(metricsAllow) > 0 {
			scope += " + CIDRs"
		}
		log.Printf("metrics: /metrics exposed (%s); /health + /healthz public", scope)
		if metricsToken == "" && os.Getenv("KEN_TRUSTED_PROXIES") == "" {
			log.Print("note: /metrics is loopback-gated; if a reverse proxy is co-located on this host, every client appears as loopback — set KEN_METRICS_TOKEN or declare the proxy in KEN_TRUSTED_PROXIES so the gate sees real client IPs.")
		}
	} else {
		log.Print("metrics: /metrics disabled (KEN_METRICS=off); /health + /healthz still public")
	}
	if tlsCfg.Enabled() && os.Getenv("KEN_TRUSTED_PROXIES") != "" {
		log.Print("note: KEN_TRUSTED_PROXIES is set while terminating TLS in-process — X-Forwarded-For will be trusted; unset it unless a proxy still fronts Ken.")
	}
	servers.Start(serveErr)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	select {
	case err := <-serveErr:
		log.Printf("serve error: %v", err)
	case <-sig:
		log.Print("shutting down…")
	}
	// Wake every parked COMM long poll BEFORE shutting the servers down. The
	// shutdown budget is shorter than a long poll, so without this every deploy
	// would sever parked connections mid-response and surface a burst of transport
	// errors in each connected agent. Woken pollers return a normal empty result.
	if commHandler != nil {
		commHandler.Drain()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	servers.Shutdown(ctx)
}

func mustOpenStore(dbPath string) *store.Store {
	if dir := filepath.Dir(dbPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			// Name the CAUSE, not just the symptom. The overwhelmingly common way to
			// reach this is running a subcommand without KEN_DB, so dbPath is the
			// RELATIVE default and the failure is about the current directory — an
			// error reading "mkdir data: permission denied" sends the reader to check
			// permissions on a directory they never meant to use, instead of setting
			// the variable. Only say so when the path really is the relative default.
			if !filepath.IsAbs(dbPath) && os.Getenv("KEN_DB") == "" {
				log.Fatalf("create data dir %q: %v\n"+
					"KEN_DB is not set, so this fell back to the relative default %q — "+
					"set KEN_DB to your database path, e.g. KEN_DB=/opt/ken/data/ken.db", dir, err, dbPath)
			}
			log.Fatalf("create data dir %q: %v", dir, err)
		}
	}
	st, err := store.Open(dbPath)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	if err := st.Migrate(); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	return st
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envTrue(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "on", "true", "yes":
		return true
	}
	return false
}

// guardFromSettings builds the per-IP abuse guard from a live snapshot (nil when
// rate limiting is disabled). The metrics registry, when present, receives the
// 429/403 counts via the guard's reject/block hooks.
func guardFromSettings(s *settings.Snapshot, reg *metrics.Registry) *ratelimit.IPGuard {
	if !s.RLEnabled {
		return nil
	}
	cfg := ratelimit.Config{
		Enabled:    true,
		IPPerMin:   s.IPPerMin,
		IPBurst:    s.IPBurst,
		BlockAfter: s.BlockAfter,
		Lockout:    time.Duration(s.LockoutSec) * time.Second,
		Allow:      s.AllowNets,
	}
	if reg != nil {
		cfg.OnReject = reg.RateLimitRejected
		cfg.OnBlock = reg.RateLimitBlocked
	}
	return ratelimit.NewIPGuard(cfg, s.Resolver)
}

// registerCollectors wires the on-scrape gauge sources: knowledge-base row counts
// and database connection-pool stats. All are cheap reads evaluated per scrape.
func registerCollectors(reg *metrics.Registry, st *store.Store) {
	reg.AddCollector(func(ctx context.Context) []metrics.Family {
		var fam []metrics.Family
		add := func(name, help string, v int) { fam = append(fam, metrics.Gauge(name, help, float64(v))) }
		if n, err := st.CountEntries(ctx); err == nil {
			add("ken_kb_entries", "Knowledge-base entries (curated or draft).", n)
		}
		if n, err := st.CountVersions(ctx); err == nil {
			add("ken_kb_versions", "Entry versions (append-only history).", n)
		}
		if rows, err := st.ListProposals(ctx); err == nil {
			add("ken_kb_proposals_pending", "Proposals awaiting human promotion.", len(rows))
		}
		if done, total, err := st.EmbeddingStats(ctx); err == nil {
			add("ken_kb_embeddings", "Versions that have an embedding.", done)
			add("ken_kb_embeddable_versions", "Total versions (embedding denominator).", total)
		}
		if n, err := st.CountHumanUsers(ctx); err == nil {
			add("ken_users", "Human users.", n)
		}
		if n, err := st.CountActiveTokens(ctx); err == nil {
			add("ken_tokens_active", "Active (non-revoked) agent tokens.", n)
		}
		return fam
	})
	reg.AddCollector(func(ctx context.Context) []metrics.Family {
		rs, ws := st.R.Stats(), st.W.Stats()
		pool := func(name string) []metrics.Label { return []metrics.Label{{Name: "pool", Value: name}} }
		return []metrics.Family{
			{Name: "ken_db_connections_open", Type: "gauge", Help: "Open database connections.", Series: []metrics.Series{
				{Labels: pool("reader"), Value: float64(rs.OpenConnections)},
				{Labels: pool("writer"), Value: float64(ws.OpenConnections)},
			}},
			{Name: "ken_db_connections_in_use", Type: "gauge", Help: "In-use database connections.", Series: []metrics.Series{
				{Labels: pool("reader"), Value: float64(rs.InUse)},
				{Labels: pool("writer"), Value: float64(ws.InUse)},
			}},
			{Name: "ken_db_wait_total", Type: "counter", Help: "Total connection-pool waits.", Series: []metrics.Series{
				{Labels: pool("reader"), Value: float64(rs.WaitCount)},
				{Labels: pool("writer"), Value: float64(ws.WaitCount)},
			}},
		}
	})
}

// metricsAllowed reports whether a request may read /metrics (and see /health
// details): the client IP is loopback or an allowlisted CIDR, or it carries the
// matching KEN_METRICS_TOKEN bearer. The client IP is resolved through the same
// trusted-proxy machinery as the rate limiter (resolver.IP) — so it uses the real,
// validated client address behind a declared proxy and is never fooled by a raw
// X-Forwarded-For. (Without a trusted proxy configured this is just RemoteAddr.)
func metricsAllowed(r *http.Request, token string, allow []*net.IPNet, resolver *clientip.Resolver) bool {
	ipStr := r.RemoteAddr
	if resolver != nil {
		ipStr = resolver.IP(r)
	} else if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		ipStr = host
	}
	if ip := net.ParseIP(ipStr); ip != nil {
		if ip.IsLoopback() {
			return true
		}
		for _, n := range allow {
			if n.Contains(ip) {
				return true
			}
		}
	}
	if token != "" {
		if bt := bearerTok(r); bt != "" && subtle.ConstantTimeCompare([]byte(bt), []byte(token)) == 1 {
			return true
		}
	}
	return false
}

func bearerTok(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const p = "Bearer "
	if len(h) > len(p) && strings.EqualFold(h[:len(p)], p) {
		return strings.TrimSpace(h[len(p):])
	}
	return ""
}

// envBoolDefault reads a boolean env var, returning def when unset/unrecognized.
func envBoolDefault(key string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "":
		return def
	case "1", "on", "true", "yes":
		return true
	case "0", "off", "false", "no":
		return false
	}
	return def
}

// tokenBucketFromSettings builds the per-token limiter from a live snapshot.
func tokenBucketFromSettings(s *settings.Snapshot) *ratelimit.Bucket {
	if !s.RLEnabled {
		return nil
	}
	return ratelimit.NewBucket(s.TokenPerMin, s.TokenBurst)
}

// rlKey is the comparable subset of settings that the rate limiter is built from;
// a change in any of these (and only these) triggers a rebuild.
type rlKey struct {
	Enabled                                                            bool
	IPPerMin, IPBurst, TokenPerMin, TokenBurst, BlockAfter, LockoutSec int
	Allow, Trusted                                                     string
}

func rlRelevant(v settings.Values) rlKey {
	return rlKey{v.RLEnabled, v.IPPerMin, v.IPBurst, v.TokenPerMin, v.TokenBurst, v.BlockAfter, v.LockoutSec, v.AllowCIDRs, v.TrustedProxies}
}

// loadOrCreateDedupSecret returns the HMAC key for the search->save dedup token.
// Precedence: KEN_DEDUP_SECRET env > persisted dedup.key next to the DB > a fresh
// random key persisted to dedup.key (0600). Persisting keeps in-flight tokens
// valid across a restart; multi-instance deployments should set KEN_DEDUP_SECRET.
func loadOrCreateDedupSecret(dbPath string) []byte {
	if s := os.Getenv("KEN_DEDUP_SECRET"); s != "" {
		return []byte(s)
	}
	path := filepath.Join(filepath.Dir(dbPath), "dedup.key")
	if b, err := os.ReadFile(path); err == nil && len(b) >= 32 {
		return b
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		log.Fatalf("dedup secret: %v", err)
	}
	if err := os.WriteFile(path, secret, 0o600); err != nil {
		log.Printf("warning: could not persist dedup secret to %s: %v (save tokens won't survive a restart)", path, err)
	}
	return secret
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func die(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(2)
}

// commLimits maps the live settings snapshot onto the COMM store's bounds.
//
// The settings registry is the source of truth at runtime; comm.DefaultLimits()
// only seeds a store constructed directly (tests, or a future CLI verb).
func commLimits(s *settings.Snapshot) comm.Limits {
	return comm.Limits{
		MaxBodyBytes:          s.CommMaxBodyBytes,
		MaxUnackedPerChannel:  s.CommMaxUnacked,
		MessageTTLSeconds:     s.CommMessageTTLSec,
		MetadataTTLSeconds:    s.CommMetadataTTLSec,
		ReplyDeadlineSeconds:  s.CommReplyDeadlineS,
		PairingCodeTTLSeconds: s.CommPairingCodeTTLS,

		FilesEnabled:     s.CommFilesEnabled,
		FileMaxBytes:     int64(s.CommFileMaxMB) << 20,
		FileBudgetBytes:  int64(s.CommFileBudgetMB) << 20,
		FileMinFreeBytes: int64(s.CommFileMinFreeMB) << 20,
		FileTTLSeconds:   s.CommFileTTLSec,
		GrantTTLSeconds:  s.CommGrantTTLSec,

		EndpointIdleTTLSeconds: s.CommEndpointIdleTTLSec,
		ClaimLeaseSeconds:      s.CommClaimLeaseSec,
	}
}

// commRelevant is the changed-subset key for COMM settings: a settings edit
// rebuilds the subsystem's limits only when one of these actually moved.
func commRelevant(v settings.Values) string {
	return fmt.Sprintf("%d|%d|%d|%d|%d|%d|%d|%d|%t|%d|%d|%d|%d|%d|%d|%d",
		v.CommMaxBodyBytes, v.CommMaxUnacked, v.CommMessageTTLSec, v.CommMetadataTTLSec,
		v.CommReplyDeadlineS, v.CommPairingCodeTTLS, v.CommPollWaitMaxSec, v.CommProvenanceWindowSec,
		v.CommFilesEnabled, v.CommFileMaxMB, v.CommFileBudgetMB, v.CommFileMinFreeMB,
		v.CommFileTTLSec, v.CommGrantTTLSec, v.CommEndpointIdleTTLSec, v.CommClaimLeaseSec)
}

// registerCommCollectors exposes COMM gauges on the existing /metrics endpoint.
//
// Wrapped in recover for the same reason the sweeper is: collectors run inline in
// the scrape handler with no panic recovery of their own, so a COMM bug would take
// down /metrics — and with it the operator's view of a perfectly healthy knowledge
// base. On any failure it emits nothing, which reads as a gap in the series rather
// than a false zero.
//
// COMM stays out of /healthz for the related reason (a component failure there
// marks the WHOLE service DOWN); metrics are where an ephemeral subsystem belongs.
func registerCommCollectors(reg *metrics.Registry, cs *comm.Store, h *commserver.Handler) {
	reg.AddCollector(func(ctx context.Context) (fam []metrics.Family) {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("comm: metrics collector panic recovered: %v", r)
				fam = nil
			}
		}()
		add := func(name, help string, v float64) { fam = append(fam, metrics.Gauge(name, help, v)) }
		if s, err := cs.StatsFor(ctx, 1); err == nil {
			add("ken_comm_endpoints", "Registered inter-session endpoints (sessions).", float64(s.Endpoints))
			add("ken_comm_channels_open", "Open inter-session channels.", float64(s.OpenChannels))
			add("ken_comm_messages_unacked", "Messages delivered or queued but not yet acknowledged.", float64(s.Unacked))
			add("ken_comm_message_bytes", "Bytes of retained message bodies (deleted at acknowledgement).", float64(s.BodyBytes))
			add("ken_comm_files", "Live file attachments (offered or awaiting delivery).", float64(s.Files))
			add("ken_comm_file_bytes", "Relay bytes currently held on disk.", float64(s.FileBytes))
		}
		if h != nil {
			add("ken_comm_poll_waiters", "Long-poll receive calls currently parked.", float64(h.ParkedWaiters()))
		}
		return fam
	})
}

// openComm opens and migrates the COMM database, returning an error rather than
// exiting so the caller can run without it. Closes the store on a migration
// failure so a half-opened database does not leak its pools.
func openComm(path string, limits comm.Limits) (*comm.Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	cs, err := comm.Open(path, limits)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	if err := cs.Migrate(); err != nil {
		_ = cs.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return cs, nil
}

// stationLimits maps the live settings onto the station bounds (docs/STATIONS.md §9).
//
// KiB rather than bytes in the settings, because these are the numbers an operator
// reasons about in a backup conversation — "four megabytes of notebook per station"
// is a sentence, "4194304" is not.
func stationLimits(s *settings.Snapshot) (store.StationTaskLimits, store.StationNoteLimits, store.StationLockerLimits) {
	return store.StationTaskLimits{
			MaxOpen:               s.StationMaxOpenTasks,
			MaxTextBytes:          s.StationTaskTextBytes,
			MaxDetailBytes:        s.StationTaskDetailBytes,
			ListLimit:             s.StationTaskListLimit,
			BriefStampThrottleSec: store.DefaultStationTaskLimits().BriefStampThrottleSec,
		},
		store.StationNoteLimits{
			MaxPageBytes:     s.StationNotePageKiB << 10,
			MaxRevisionBytes: s.StationNoteRevisionKiB << 10,
			MaxNotebookBytes: s.StationNotebookKiB << 10,
		},
		store.StationLockerLimits{
			MaxBlobBytes:  s.StationLockerBlobKiB << 10,
			MaxTotalBytes: s.StationLockerTotalKiB << 10,
		}
}

// stationRelevant is the changed-subset key for station settings, so an unrelated
// edit (a rate limit, a TLS field) does not churn the live bounds.
func stationRelevant(v settings.Values) string {
	return fmt.Sprintf("%d|%d|%d|%d|%d|%d|%d|%d|%d",
		v.StationNotePageKiB, v.StationNoteRevisionKiB, v.StationNotebookKiB,
		v.StationLockerBlobKiB, v.StationLockerTotalKiB,
		v.StationMaxOpenTasks, v.StationTaskTextBytes, v.StationTaskDetailBytes,
		v.StationTaskListLimit)
}
