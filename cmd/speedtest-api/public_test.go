package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
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
	"strings"
	"testing"
	"time"

	"github.com/Onemind-Services-LLC/speedtest-api/internal/server"
	proxyproto "github.com/pires/go-proxyproto"
)

func writeTestCertificate(t *testing.T, certFile, keyFile string, serial int64) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "api.example.com"}, DNSNames: []string{"api.example.com"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: private}), 0600); err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func tlsTestConfig(t *testing.T) (server.Config, *x509.Certificate) {
	t.Helper()
	dir := t.TempDir()
	c := server.DefaultConfig()
	c.TLSAddress = "127.0.0.1:0"
	c.RedirectAddress = "127.0.0.1:0"
	c.TLSCertFile = filepath.Join(dir, "tls.crt")
	c.TLSKeyFile = filepath.Join(dir, "tls.key")
	c.PublicOrigin = "https://api.example.com"
	c.BrowserRedirectURL = "https://ui.example.com/"
	return c, writeTestCertificate(t, c.TLSCertFile, c.TLSKeyFile, 1)
}

func TestCertificateRotationRetainsLastGoodPair(t *testing.T) {
	c, _ := tlsTestConfig(t)
	s := &certificateStore{certFile: c.TLSCertFile, keyFile: c.TLSKeyFile, hostname: "api.example.com"}
	if err := s.reload(); err != nil {
		t.Fatal(err)
	}
	if s.current.Load().Leaf.SerialNumber.Int64() != 1 {
		t.Fatal("initial certificate missing")
	}
	writeTestCertificate(t, c.TLSCertFile, c.TLSKeyFile, 2)
	if err := s.reload(); err != nil {
		t.Fatal(err)
	}
	if s.current.Load().Leaf.SerialNumber.Int64() != 2 {
		t.Fatal("rotated certificate missing")
	}
	if err := os.WriteFile(c.TLSKeyFile, []byte("incomplete secret update"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.reload(); err == nil {
		t.Fatal("invalid replacement accepted")
	}
	if s.current.Load().Leaf.SerialNumber.Int64() != 2 {
		t.Fatal("lost last good certificate")
	}
}

func TestPublicListenerTLSProxyAndRedirects(t *testing.T) {
	c, cert := tlsTestConfig(t)
	c.RedirectAddress = ""
	c.ProxyProtocolCIDRs = []string{"127.0.0.1/32"}
	c.AllowedOrigins = []string{"https://ui.example.com"}
	app, err := server.New(c)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listeners, err := newPublicServers(ctx, app, c)
	if err != nil {
		t.Fatal(err)
	}
	item := listeners[0]
	defer item.server.Close()
	defer item.listener.Close()
	go func() { _ = item.server.ServeTLS(item.listener, "", "") }()
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	dial := func(sendProxy bool, host string) (*tls.Conn, error) {
		conn, err := net.DialTimeout("tcp", item.listener.Addr().String(), time.Second)
		if err != nil {
			return nil, err
		}
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		if sendProxy {
			h := proxyproto.HeaderProxyFromAddrs(2, &net.TCPAddr{IP: net.ParseIP("203.0.113.9"), Port: 12345}, conn.RemoteAddr())
			if _, err := h.WriteTo(conn); err != nil {
				conn.Close()
				return nil, err
			}
		}
		secure := tls.Client(conn, &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: host, NextProtos: []string{"h2", "http/1.1"}})
		if err := secure.Handshake(); err != nil {
			conn.Close()
			return nil, err
		}
		return secure, nil
	}
	transport := &http.Transport{DialTLSContext: func(context.Context, string, string) (net.Conn, error) { return dial(true, "api.example.com") }}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, path := range []string{"/v1/network", "/__down?bytes=1048576", "/metrics"} {
		req, _ := http.NewRequest(http.MethodGet, c.PublicOrigin+path, nil)
		req.Header.Set("X-Forwarded-For", "198.51.100.10")
		req.Header.Set("Origin", "https://ui.example.com")
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(res.Body)
		res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if res.TLS.NegotiatedProtocol != "http/1.1" {
			t.Fatal("TLS must select independent HTTP/1.1 connections")
		}
		if path == "/v1/network" && (!strings.Contains(string(body), "203.0.113.9") || strings.Contains(string(body), "198.51.100.10")) {
			t.Fatalf("client address spoofed: %s", body)
		}
		if strings.HasPrefix(path, "/__down") && (len(body) != 1048576 || res.StatusCode != 200) {
			t.Fatal("TLS transfer did not complete")
		}
		if path == "/metrics" && res.StatusCode != 404 {
			t.Fatal("private metrics exposed")
		}
		if res.Header.Get("Strict-Transport-Security") == "" {
			t.Fatal("HSTS missing")
		}
	}
	if conn, err := dial(false, "api.example.com"); err == nil {
		conn.Close()
		t.Fatal("missing PROXY header accepted")
	}
	if conn, err := dial(true, "wrong.example.com"); err == nil {
		conn.Close()
		t.Fatal("unknown SNI accepted")
	}
}

func TestNavigationDoesNotRedirectMeasurements(t *testing.T) {
	c, _ := tlsTestConfig(t)
	c.AllowedOrigins = []string{"https://ui.example.com"}
	app, err := server.New(c)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	for _, tc := range []struct {
		name, method, path, mode, dest string
		secure                         bool
		status                         int
		location                       string
	}{
		{"navigation", "GET", "/v1/info", "navigate", "document", true, 302, c.BrowserRedirectURL},
		{"fetch", "GET", "/v1/info", "cors", "empty", true, 200, ""},
		{"plain client", "GET", "/v1/info", "", "", true, 200, ""},
		{"upload", "POST", "/__up", "navigate", "document", true, 200, ""},
		{"preflight", "OPTIONS", "/__up", "cors", "empty", true, 204, ""},
		{"http navigation", "GET", "/", "navigate", "document", false, 302, c.BrowserRedirectURL},
		{"http API", "GET", "/v1/info?x=1", "", "", false, 308, c.PublicOrigin + "/v1/info?x=1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, c.PublicOrigin+tc.path, strings.NewReader("abc"))
			r.RemoteAddr = "203.0.113.9:1234"
			r.Header.Set("Sec-Fetch-Mode", tc.mode)
			r.Header.Set("Sec-Fetch-Dest", tc.dest)
			r.Header.Set("Origin", "https://ui.example.com")
			r.Header.Set("Access-Control-Request-Method", "POST")
			w := httptest.NewRecorder()
			publicHandler(app, c, tc.secure).ServeHTTP(w, r)
			if w.Code != tc.status || w.Header().Get("Location") != tc.location {
				t.Fatalf("status=%d location=%q body=%s", w.Code, w.Header().Get("Location"), w.Body.String())
			}
			if !strings.Contains(strings.Join(w.Header().Values("Vary"), ","), "Sec-Fetch-Mode") {
				t.Fatal("navigation cache variation missing")
			}
		})
	}
}

func TestPublicRateLimitsArePerClientAndBounded(t *testing.T) {
	l := newPublicLimits()
	now := time.Now()
	for i := 0; i < 400; i++ {
		if !l.allow("a", now) {
			t.Fatal("burst rejected early")
		}
	}
	if l.allow("a", now) {
		t.Fatal("burst exceeded")
	}
	if !l.allow("b", now) {
		t.Fatal("unrelated client blocked")
	}
	if !l.allow("a", now.Add(time.Second)) {
		t.Fatal("rate did not recover")
	}
	for i := len(l.buckets); i < 10000; i++ {
		l.buckets[big.NewInt(int64(i)).String()] = publicBucket{updated: now}
	}
	if l.allow("new", now) {
		t.Fatal("client table unbounded")
	}
	if !l.allow("new", now.Add(2*time.Minute)) {
		t.Fatal("stale clients not evicted")
	}
	// Occupied request slots cannot grow past the public concurrency limit.
	for i := 0; i < 128; i++ {
		l.active <- struct{}{}
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "https://api.example.com/v1/info", nil)
	l.wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("overload reached handler") })).ServeHTTP(w, r)
	if w.Code != 503 {
		t.Fatalf("overload status=%d", w.Code)
	}
}

func TestACMEChallengeConfinedToWebroot(t *testing.T) {
	c, _ := tlsTestConfig(t)
	c.ACMEChallengeDir = t.TempDir()
	if err := os.WriteFile(filepath.Join(c.ACMEChallengeDir, "valid-token_123"), []byte("token.thumbprint"), 0644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "private-key")
	if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(c.ACMEChallengeDir, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.ACMEChallengeDir, "large"), make([]byte, 8193), 0644); err != nil {
		t.Fatal(err)
	}
	handler := publicHandler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("ACME reached measurement handler") }), c, false)
	for _, tc := range []struct {
		path   string
		status int
	}{
		{"/.well-known/acme-challenge/valid-token_123", 200},
		{"/.well-known/acme-challenge/missing", 404},
		{"/.well-known/acme-challenge/escape", 404},
		{"/.well-known/acme-challenge/large", 404},
		{"/.well-known/acme-challenge/../private-key", 404},
		{"/.well-known/acme-challenge/", 404},
	} {
		r := httptest.NewRequest("GET", c.PublicOrigin+tc.path, nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s: status=%d", tc.path, w.Code)
		}
		if tc.status == 200 && w.Body.String() != "token.thumbprint" {
			t.Fatal("challenge corrupted")
		}
		if strings.Contains(w.Body.String(), "secret") {
			t.Fatal("webroot escaped")
		}
	}
}
