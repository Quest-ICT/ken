// Package webtls resolves and runs Ken's TLS posture. Three modes, chosen by
// KEN_TLS:
//
//   - off  — plain HTTP. Valid ONLY behind a reverse proxy that terminates TLS.
//   - acme — in-process Let's Encrypt via autocert: automatic issuance AND
//     renewal, certs cached on disk. Needs KEN_TLS_DOMAINS (+ KEN_TLS_EMAIL).
//   - file — an operator-supplied PEM cert/key (KEN_TLS_CERT/KEN_TLS_KEY),
//     hot-reloaded when the files change on disk (so a renewal needs no restart).
//
// In the two TLS modes it also runs a plain-HTTP listener (default :80) that
// serves ACME HTTP-01 challenges and 301-redirects everything else to HTTPS, so
// a user who types http:// still lands on the encrypted site.
package webtls

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/acme/autocert"
	"golang.org/x/net/idna"
)

// Mode is the TLS termination strategy.
type Mode string

const (
	ModeOff  Mode = "off"
	ModeACME Mode = "acme"
	ModeFile Mode = "file"
)

// Config is the resolved TLS configuration.
type Config struct {
	Mode     Mode
	Addr     string   // main app listener: plain HTTP in off mode, HTTPS otherwise
	HTTPAddr string   // plain-HTTP redirect + ACME HTTP-01 listener (TLS modes; "" disables)
	Domains  []string // cert host allowlist (acme mode; also gates the HTTPS redirect)
	Email    string   // ACME account email (acme mode; recommended, optional)
	CacheDir string   // ACME certificate cache directory (acme mode)
	CertFile string   // PEM certificate (file mode)
	KeyFile  string   // PEM private key (file mode)

	QuietHandshake bool // suppress benign "TLS handshake error" scanner noise in the log
}

// Enabled reports whether Ken terminates TLS itself (acme or file mode).
func (c Config) Enabled() bool { return c.Mode == ModeACME || c.Mode == ModeFile }

// FromEnv resolves the TLS configuration from KEN_TLS* environment variables.
// addr is the main listen address already resolved from --addr / KEN_ADDR (empty
// accepts the mode default: :8080 plain, :443 with TLS). dataDir seeds the default
// ACME cache directory (<dataDir>/acme). It fails fast on an invalid combination.
func FromEnv(addr, dataDir string) (Config, error) {
	c := Config{
		Mode:           Mode(strings.ToLower(strings.TrimSpace(envOr("KEN_TLS", "off")))),
		Addr:           strings.TrimSpace(addr),
		Domains:        splitList(os.Getenv("KEN_TLS_DOMAINS")),
		Email:          strings.TrimSpace(os.Getenv("KEN_TLS_EMAIL")),
		CertFile:       strings.TrimSpace(os.Getenv("KEN_TLS_CERT")),
		KeyFile:        strings.TrimSpace(os.Getenv("KEN_TLS_KEY")),
		QuietHandshake: envTruthy("KEN_TLS_QUIET_HANDSHAKE"),
	}
	switch c.Mode {
	case ModeOff, ModeACME, ModeFile:
	default:
		return Config{}, fmt.Errorf("KEN_TLS must be off, acme, or file (got %q)", c.Mode)
	}

	if c.Addr == "" {
		if c.Enabled() {
			c.Addr = ":443"
		} else {
			c.Addr = ":8080"
		}
	}
	// Port-80 redirect/challenge listener: on by default in TLS modes; an explicit
	// KEN_HTTP_ADDR="" disables it (e.g. when the operator handles :80 elsewhere —
	// ACME still works over TLS-ALPN-01 on the HTTPS port).
	if v, ok := os.LookupEnv("KEN_HTTP_ADDR"); ok {
		c.HTTPAddr = strings.TrimSpace(v)
	} else if c.Enabled() {
		c.HTTPAddr = ":80"
	}
	c.CacheDir = envOr("KEN_TLS_CACHE", filepath.Join(dataDir, "acme"))

	switch c.Mode {
	case ModeACME:
		if len(c.Domains) == 0 {
			return Config{}, errors.New("KEN_TLS=acme requires KEN_TLS_DOMAINS (comma-separated hostnames the cert is issued for)")
		}
	case ModeFile:
		if c.CertFile == "" || c.KeyFile == "" {
			return Config{}, errors.New("KEN_TLS=file requires KEN_TLS_CERT and KEN_TLS_KEY (PEM file paths)")
		}
	}
	return c, nil
}

// Describe returns a one-line human summary of the TLS posture for the startup log.
func (c Config) Describe() string {
	switch c.Mode {
	case ModeACME:
		redir := "no HTTP listener"
		if c.HTTPAddr != "" {
			redir = "HTTP->HTTPS redirect + ACME HTTP-01 on " + c.HTTPAddr
		}
		return fmt.Sprintf("TLS: in-process ACME (Let's Encrypt) for [%s], HTTPS on %s; %s; certs cached in %s",
			strings.Join(c.Domains, ", "), c.Addr, redir, c.CacheDir)
	case ModeFile:
		redir := "no HTTP listener"
		if c.HTTPAddr != "" {
			redir = "HTTP->HTTPS redirect on " + c.HTTPAddr
		}
		return fmt.Sprintf("TLS: in-process (cert=%s) on %s; %s", c.CertFile, c.Addr, redir)
	default:
		return fmt.Sprintf("TLS: OFF — plain HTTP on %s. This is safe ONLY behind a TLS-terminating reverse proxy; "+
			"otherwise set KEN_TLS=acme (Let's Encrypt) or KEN_TLS=file.", c.Addr)
	}
}

// httpsPort extracts the port of the HTTPS listener (":443" -> "443"); "" if none.
func (c Config) httpsPort() string {
	_, port, err := net.SplitHostPort(c.Addr)
	if err != nil {
		return ""
	}
	return port
}

// Servers holds the running listeners so the caller keeps control of shutdown.
type Servers struct {
	main     *http.Server
	redirect *http.Server // nil in off mode (or when the :80 listener is disabled)
	tls      bool
}

// Build constructs the server(s) for c. appHandler is the application mux; tune
// (if non-nil) is applied to the main server (timeouts, etc.). Build performs the
// fail-fast checks: it loads the cert/key in file mode and creates the ACME cache
// directory in acme mode, so a misconfiguration is caught at startup.
// acmeDomains, when non-nil, supplies the live ACME host allowlist (from settings)
// so domains can change without a restart; nil uses the static c.Domains.
func (c Config) Build(appHandler http.Handler, tune func(*http.Server), acmeDomains func() []string) (*Servers, error) {
	main := &http.Server{Addr: c.Addr, Handler: appHandler}
	if tune != nil {
		tune(main)
	}
	if c.QuietHandshake {
		main.ErrorLog = quietErrorLog()
	}
	s := &Servers{main: main, tls: c.Enabled()}

	switch c.Mode {
	case ModeOff:
		return s, nil

	case ModeACME:
		if err := os.MkdirAll(c.CacheDir, 0o700); err != nil {
			return nil, fmt.Errorf("acme cache dir %s: %w", c.CacheDir, err)
		}
		m := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,            // choosing KEN_TLS=acme accepts the Let's Encrypt TOS
			Cache:      autocert.DirCache(c.CacheDir), // certs persisted under the data dir
			HostPolicy: hostPolicy(c.Domains, acmeDomains),
			Email:      c.Email,
		}
		tc := m.TLSConfig() // sets GetCertificate + NextProtos (incl. TLS-ALPN-01 + h2)
		tc.MinVersion = tls.VersionTLS12
		main.TLSConfig = tc
		if c.HTTPAddr != "" {
			// autocert.HTTPHandler serves /.well-known/acme-challenge/*; everything
			// else falls through to our host-checked HTTPS redirect.
			s.redirect = redirectServer(c.HTTPAddr, m.HTTPHandler(c.redirectHandler()))
		}
		return s, nil

	case ModeFile:
		k := &certKeeper{certFile: c.CertFile, keyFile: c.KeyFile}
		if err := k.load(); err != nil { // fail-fast: bad/missing cert or key
			return nil, fmt.Errorf("load TLS cert/key: %w", err)
		}
		main.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: k.get}
		if c.HTTPAddr != "" {
			s.redirect = redirectServer(c.HTTPAddr, c.redirectHandler())
		}
		return s, nil
	}
	return nil, fmt.Errorf("unhandled TLS mode %q", c.Mode)
}

// Start begins serving on every listener. A failure of the MAIN listener (other
// than a clean shutdown) is sent to errc. A failure of the optional :80
// redirect/challenge listener is only logged — it must never tear down an
// otherwise-healthy HTTPS service.
func (s *Servers) Start(errc chan<- error) {
	go func() {
		var err error
		if s.tls {
			err = s.main.ListenAndServeTLS("", "") // cert comes from main.TLSConfig
		} else {
			err = s.main.ListenAndServe()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()
	if s.redirect != nil {
		go func() {
			if err := s.redirect.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Printf("webtls: HTTP redirect/ACME listener on %s failed: %v (HTTPS unaffected)", s.redirect.Addr, err)
			}
		}()
	}
}

// Shutdown gracefully stops every listener.
func (s *Servers) Shutdown(ctx context.Context) {
	_ = s.main.Shutdown(ctx)
	if s.redirect != nil {
		_ = s.redirect.Shutdown(ctx)
	}
}

// hostPolicy allows the configured ACME domains, preferring the live list (settings)
// when supplied. autocert normalizes the incoming SNI (IDNA + lowercase + trailing
// dot) before this is called, so we IDNA-normalize each configured domain and
// compare case-insensitively — issuing only for allow-listed hosts.
func hostPolicy(static []string, live func() []string) autocert.HostPolicy {
	return func(_ context.Context, host string) error {
		// Union of the startup domains and the live (settings) list: live edits can
		// ADD domains without a restart, but can never remove a startup domain — so an
		// accidental settings edit cannot lock the operator out of the host they booted
		// on (removing a startup domain needs a KEN_TLS_DOMAINS change + restart).
		if hostIn(static, host) || (live != nil && hostIn(live(), host)) {
			return nil
		}
		return fmt.Errorf("acme/autocert: host %q is not in the configured TLS domains", host)
	}
}

func hostIn(domains []string, host string) bool {
	for _, d := range domains {
		if a, err := idna.Lookup.ToASCII(strings.TrimSpace(d)); err == nil && strings.EqualFold(a, host) {
			return true
		}
	}
	return false
}

func redirectServer(addr string, h http.Handler) *http.Server {
	return &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 10 * time.Second}
}

// redirectHandler builds the plain-HTTP handler that 301-redirects to HTTPS. It
// reflects a request Host into the Location only when that host is in the
// configured domain allowlist; an unknown/forged Host is redirected to the primary
// configured domain instead, so the internet-facing :80 listener is not an
// open-redirect gadget. With no domains configured (file mode without
// KEN_TLS_DOMAINS) it falls back to echoing the request Host.
func (c Config) redirectHandler() http.Handler {
	port := c.httpsPort()
	allow := make(map[string]bool, len(c.Domains))
	for _, d := range c.Domains {
		if d = strings.ToLower(strings.TrimSpace(d)); d != "" {
			allow[d] = true
		}
	}
	var primary string
	if len(c.Domains) > 0 {
		primary = strings.TrimSpace(c.Domains[0])
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, redirectTarget(r.Host, r.URL.RequestURI(), port, primary, allow), http.StatusMovedPermanently)
	})
}

// redirectTarget builds the HTTPS URL for a plain-HTTP request. It strips any port
// from host, substitutes the primary domain when host is not allow-listed (and an
// allowlist exists), re-brackets an IPv6 literal, and appends the public HTTPS port
// unless it is 443.
func redirectTarget(host, reqURI, httpsPort, primary string, allow map[string]bool) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if len(allow) > 0 && !allow[strings.ToLower(host)] && primary != "" {
		host = primary
	}
	if strings.Contains(host, ":") { // IPv6 literal — re-bracket for the URL authority
		host = "[" + host + "]"
	}
	target := "https://" + host
	if httpsPort != "" && httpsPort != "443" {
		target += ":" + httpsPort
	}
	if reqURI == "" {
		reqURI = "/"
	}
	return target + reqURI
}

// certSig is a cheap fingerprint of the cert+key files used to detect a change
// (a renewal) without hashing on every handshake. Comparable by ==.
type certSig struct {
	certMod, keyMod   time.Time
	certSize, keySize int64
}

func statSig(certFile, keyFile string) (certSig, error) {
	cs, err := os.Stat(certFile)
	if err != nil {
		return certSig{}, err
	}
	ks, err := os.Stat(keyFile)
	if err != nil {
		return certSig{}, err
	}
	return certSig{cs.ModTime(), ks.ModTime(), cs.Size(), ks.Size()}, nil
}

// certKeeper serves a file-based cert/key and reloads it whenever the files change
// on disk (any mtime OR size change — not just a strictly-newer mtime — so an
// atomic-rename renewal with a preserved or rewound timestamp is still picked up),
// so a certificate renewal is honored without restarting the server.
type certKeeper struct {
	certFile, keyFile string
	mu                sync.RWMutex
	cert              *tls.Certificate
	sig               certSig
	lastErrLogged     string
}

func (k *certKeeper) load() error {
	sig, err := statSig(k.certFile, k.keyFile)
	if err != nil {
		return err
	}
	pair, err := tls.LoadX509KeyPair(k.certFile, k.keyFile)
	if err != nil {
		return err
	}
	k.mu.Lock()
	k.cert, k.sig, k.lastErrLogged = &pair, sig, ""
	k.mu.Unlock()
	return nil
}

// get is a tls.Config.GetCertificate callback. It reloads when the files' signature
// changed; on a reload error it logs once (rate-limited) and keeps serving the last
// good certificate rather than failing the handshake.
func (k *certKeeper) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	k.mu.RLock()
	cur, cursig := k.cert, k.sig
	k.mu.RUnlock()

	if sig, err := statSig(k.certFile, k.keyFile); err == nil && sig != cursig {
		if err := k.load(); err != nil {
			k.logReloadErr(err) // broken renewal — keep the previous cert, but surface it
		} else {
			k.mu.RLock()
			cur = k.cert
			k.mu.RUnlock()
		}
	}
	if cur == nil {
		return nil, errors.New("no TLS certificate loaded")
	}
	return cur, nil
}

func (k *certKeeper) logReloadErr(err error) {
	msg := err.Error()
	k.mu.Lock()
	dup := msg == k.lastErrLogged
	k.lastErrLogged = msg
	k.mu.Unlock()
	if !dup {
		log.Printf("webtls: TLS cert reload failed, keeping previous certificate: %v", err)
	}
}

// quietErrorLog returns an http.Server ErrorLog that swallows the benign
// "http: TLS handshake error" lines (no-SNI probes, EOF/reset from TCP scanners,
// odd ALPN/cipher fingerprinting) — the constant background noise on a public :443
// — while still logging every other server error.
func quietErrorLog() *log.Logger { return log.New(quietWriter{os.Stderr}, "", log.LstdFlags) }

type quietWriter struct{ w io.Writer }

func (q quietWriter) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("http: TLS handshake error")) {
		return len(p), nil // drop it, report success so the logger doesn't error
	}
	return q.w.Write(p)
}

func envTruthy(k string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(k))) {
	case "1", "on", "true", "yes":
		return true
	}
	return false
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
