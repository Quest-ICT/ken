package webtls

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	crand "crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRedirectTarget(t *testing.T) {
	allow := map[string]bool{"example.com": true, "kb.local": true}
	cases := []struct {
		host, uri, port, primary, want string
		allow                          map[string]bool
	}{
		{"example.com", "/a?b=1", "443", "example.com", "https://example.com/a?b=1", allow},
		{"example.com:80", "/", "443", "example.com", "https://example.com/", allow},
		{"example.com", "/x", "8443", "example.com", "https://example.com:8443/x", allow},
		{"example.com", "", "443", "example.com", "https://example.com/", allow},
		{"kb.local:80", "/p?q=2", "8443", "example.com", "https://kb.local:8443/p?q=2", allow},
		// a forged/unknown Host is replaced by the primary domain (anti open-redirect)
		{"evil.example", "/x", "443", "example.com", "https://example.com/x", allow},
		// no allowlist configured (file mode without KEN_TLS_DOMAINS) → echo the host
		{"anything.host", "/y", "443", "", "https://anything.host/y", nil},
		// an IPv6 literal Host is re-bracketed in the URL authority
		{"[::1]:80", "/z", "8443", "", "https://[::1]:8443/z", nil},
	}
	for _, c := range cases {
		if got := redirectTarget(c.host, c.uri, c.port, c.primary, c.allow); got != c.want {
			t.Errorf("redirectTarget(%q,%q,%q,%q) = %q, want %q", c.host, c.uri, c.port, c.primary, got, c.want)
		}
	}
}

func TestRedirectHandler301(t *testing.T) {
	h := Config{Domains: []string{"example.com"}}.redirectHandler()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://example.com/foo?x=1", nil)
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusMovedPermanently {
		t.Fatalf("status = %d, want 301", rr.Code)
	}
	if loc := rr.Header().Get("Location"); loc != "https://example.com/foo?x=1" {
		t.Fatalf("Location = %q", loc)
	}

	// A forged Host is redirected to the primary domain, never reflected.
	rr2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "http://example.com/p", nil)
	req2.Host = "evil.example"
	h.ServeHTTP(rr2, req2)
	if loc := rr2.Header().Get("Location"); loc != "https://example.com/p" {
		t.Fatalf("forged-host Location = %q, want the primary domain", loc)
	}
}

func TestFromEnv(t *testing.T) {
	t.Run("off default", func(t *testing.T) {
		t.Setenv("KEN_TLS", "")
		c, err := FromEnv("", "/data")
		if err != nil {
			t.Fatal(err)
		}
		if c.Mode != ModeOff || c.Addr != ":8080" || c.Enabled() {
			t.Fatalf("off default wrong: %+v", c)
		}
		if c.HTTPAddr != "" {
			t.Fatalf("off mode should have no HTTP redirect listener, got %q", c.HTTPAddr)
		}
	})

	t.Run("acme needs domains", func(t *testing.T) {
		t.Setenv("KEN_TLS", "acme")
		t.Setenv("KEN_TLS_DOMAINS", "")
		if _, err := FromEnv("", "/data"); err == nil {
			t.Fatal("expected error when KEN_TLS=acme without domains")
		}
	})

	t.Run("acme ok", func(t *testing.T) {
		t.Setenv("KEN_TLS", "acme")
		t.Setenv("KEN_TLS_DOMAINS", "kb.example.com, alt.example.com")
		t.Setenv("KEN_TLS_EMAIL", "ops@example.com")
		c, err := FromEnv("", "/srv/data")
		if err != nil {
			t.Fatal(err)
		}
		if c.Addr != ":443" || c.HTTPAddr != ":80" {
			t.Fatalf("acme defaults wrong: addr=%q http=%q", c.Addr, c.HTTPAddr)
		}
		if len(c.Domains) != 2 || c.Domains[0] != "kb.example.com" {
			t.Fatalf("domains parsed wrong: %v", c.Domains)
		}
		if c.CacheDir != filepath.Join("/srv/data", "acme") {
			t.Fatalf("cache dir wrong: %q", c.CacheDir)
		}
		if c.httpsPort() != "443" {
			t.Fatalf("httpsPort = %q", c.httpsPort())
		}
	})

	t.Run("http listener can be disabled", func(t *testing.T) {
		t.Setenv("KEN_TLS", "acme")
		t.Setenv("KEN_TLS_DOMAINS", "kb.example.com")
		t.Setenv("KEN_HTTP_ADDR", "")
		c, err := FromEnv("", "/data")
		if err != nil {
			t.Fatal(err)
		}
		if c.HTTPAddr != "" {
			t.Fatalf("explicit empty KEN_HTTP_ADDR should disable the listener, got %q", c.HTTPAddr)
		}
	})

	t.Run("file needs cert and key", func(t *testing.T) {
		t.Setenv("KEN_TLS", "file")
		t.Setenv("KEN_TLS_CERT", "/x/cert.pem")
		t.Setenv("KEN_TLS_KEY", "")
		if _, err := FromEnv("", "/data"); err == nil {
			t.Fatal("expected error when KEN_TLS=file without key")
		}
	})

	t.Run("bogus mode", func(t *testing.T) {
		t.Setenv("KEN_TLS", "sslv3")
		if _, err := FromEnv("", "/data"); err == nil {
			t.Fatal("expected error for unknown KEN_TLS mode")
		}
	})

	t.Run("explicit addr override", func(t *testing.T) {
		t.Setenv("KEN_TLS", "acme")
		t.Setenv("KEN_TLS_DOMAINS", "kb.example.com")
		c, err := FromEnv(":8443", "/data")
		if err != nil {
			t.Fatal(err)
		}
		if c.Addr != ":8443" || c.httpsPort() != "8443" {
			t.Fatalf("explicit addr not honored: %+v", c)
		}
	})
}

// writeSelfSigned writes a self-signed cert+key valid for localhost/127.0.0.1 and
// returns their paths plus the parsed leaf (for the client trust pool). serial
// lets a test tell two generated certs apart after a reload.
func writeSelfSigned(t *testing.T, dir string, serial int64) (certPath, keyPath string, leaf *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), crand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(serial),
		Subject:               pkix.Name{CommonName: "ken-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(crand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err = x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath, leaf
}

func TestCertKeeperReload(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath, _ := writeSelfSigned(t, dir, 1)
	k := &certKeeper{certFile: certPath, keyFile: keyPath}
	if err := k.load(); err != nil {
		t.Fatal(err)
	}
	c1, err := k.get(nil)
	if err != nil {
		t.Fatal(err)
	}

	// Replace with a different cert and advance the mtime so get() reloads.
	writeSelfSigned(t, dir, 2)
	future := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(certPath, future, future)
	_ = os.Chtimes(keyPath, future, future)

	c2, err := k.get(nil)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(c1.Certificate[0], c2.Certificate[0]) {
		t.Fatal("expected certKeeper to reload the changed certificate")
	}
}

func TestFileModeHandshake(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath, leaf := writeSelfSigned(t, dir, 1)

	cfg := Config{Mode: ModeFile, Addr: "127.0.0.1:0", CertFile: certPath, KeyFile: keyPath}
	servers, err := cfg.Build(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}), nil, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if servers.redirect != nil {
		t.Fatal("no redirect server expected when HTTPAddr is empty")
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = servers.main.ServeTLS(ln, "", "") }()
	defer servers.main.Close()

	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}},
	}
	resp, err := client.Get("https://" + ln.Addr().String() + "/")
	if err != nil {
		t.Fatalf("https get: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(body) != "ok" {
		t.Fatalf("unexpected response: %d %q", resp.StatusCode, body)
	}
}

func TestQuietWriterDropsHandshakeNoise(t *testing.T) {
	var buf bytes.Buffer
	q := quietWriter{&buf}
	if _, err := q.Write([]byte("2026/07/19 http: TLS handshake error from 1.2.3.4:5: EOF\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Write([]byte("2026/07/19 http: some genuine server error\n")); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "2026/07/19 http: some genuine server error\n" {
		t.Fatalf("quietWriter should drop only handshake noise, got %q", got)
	}
}
