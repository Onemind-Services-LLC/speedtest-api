package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Onemind-Services-LLC/speedtest-api/internal/server"
	proxyproto "github.com/pires/go-proxyproto"
)

type publicServer struct {
	server   *http.Server
	listener net.Listener
	secure   bool
}

// Certificates are reloaded from the mounted Secret without restarting active
// transfers. A temporarily incomplete rotation leaves the last good pair active.
type certificateStore struct {
	certFile, keyFile, hostname string
	current                     atomic.Pointer[tls.Certificate]
}

func (s *certificateStore) reload() error {
	cert, err := tls.LoadX509KeyPair(s.certFile, s.keyFile)
	if err != nil {
		return err
	}
	if err := cert.Leaf.VerifyHostname(s.hostname); err != nil {
		return err
	}
	now := time.Now()
	if now.Before(cert.Leaf.NotBefore) || !now.Before(cert.Leaf.NotAfter) {
		return fmt.Errorf("TLS certificate is outside its validity period")
	}
	s.current.Store(&cert)
	return nil
}

func (s *certificateStore) watch(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.reload(); err != nil {
				slog.Error("TLS certificate reload failed; retaining previous certificate", "error", err)
			}
		}
	}
}

func newPublicServers(ctx context.Context, app *server.Server, c server.Config) (result []publicServer, err error) {
	if c.TLSAddress == "" {
		return nil, nil
	}
	origin, _ := url.Parse(c.PublicOrigin) // Config has already been validated.
	store := &certificateStore{certFile: c.TLSCertFile, keyFile: c.TLSKeyFile, hostname: origin.Hostname()}
	if err := store.reload(); err != nil {
		return nil, fmt.Errorf("load TLS certificate: %w", err)
	}
	var policy proxyproto.ConnPolicyFunc
	if len(c.ProxyProtocolCIDRs) > 0 {
		policy, err = proxyproto.TrustProxyHeaderFromRanges(c.ProxyProtocolCIDRs)
		if err != nil {
			return nil, err
		}
	}
	defer func() {
		if err != nil {
			for _, item := range result {
				item.listener.Close()
			}
			result = nil
		}
	}()
	limits := newPublicLimits()
	for _, address := range []string{c.TLSAddress, c.RedirectAddress} {
		if address == "" {
			continue
		}
		listener, listenErr := net.Listen("tcp", address)
		if listenErr != nil {
			return result, listenErr
		}
		if policy != nil {
			listener = &proxyproto.Listener{Listener: listener, ConnPolicy: policy, ReadHeaderTimeout: 3 * time.Second}
		}
		srv := app.HTTPServer()
		srv.Addr = address
		secure := address == c.TLSAddress
		srv.Handler = limits.wrap(publicHandler(app, c, secure))
		if secure {
			srv.Protocols = new(http.Protocols)
			srv.Protocols.SetHTTP1(true)
			srv.TLSConfig = &tls.Config{
				MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"},
				GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
					if hello.ServerName != origin.Hostname() {
						return nil, fmt.Errorf("unrecognized TLS server name")
					}
					return store.current.Load(), nil
				},
			}
		}
		result = append(result, publicServer{server: srv, listener: listener, secure: secure})
	}
	go store.watch(ctx)
	return result, nil
}

func publicHandler(app http.Handler, c server.Config, secure bool) http.Handler {
	origin, _ := url.Parse(c.PublicOrigin)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store, no-transform")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if secure {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		}
		if r.Host != origin.Host {
			http.Error(w, "unrecognized host", http.StatusMisdirectedRequest)
			return
		}
		if !secure && c.ACMEChallengeDir != "" && strings.HasPrefix(r.URL.Path, "/.well-known/acme-challenge/") {
			serveACMEChallenge(w, r, c.ACMEChallengeDir)
			return
		}
		// Fetch/XHR, preflight, and uploads must never take the navigation redirect.
		w.Header().Add("Vary", "Sec-Fetch-Mode, Sec-Fetch-Dest")
		if c.BrowserRedirectURL != "" && r.Method == http.MethodGet && r.Header.Get("Sec-Fetch-Mode") == "navigate" && r.Header.Get("Sec-Fetch-Dest") == "document" {
			http.Redirect(w, r, c.BrowserRedirectURL, http.StatusFound)
			return
		}
		if !secure {
			http.Redirect(w, r, c.PublicOrigin+r.URL.RequestURI(), http.StatusPermanentRedirect)
			return
		}
		if r.URL.Path == "/metrics" {
			http.NotFound(w, r)
			return
		}
		app.ServeHTTP(w, r)
	})
}

var acmeToken = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)

// Only a single public ACME token may be read. OpenRoot prevents symlink and
// path traversal outside the webroot, including when a file changes mid-read.
func serveACMEChallenge(w http.ResponseWriter, r *http.Request, directory string) {
	token := strings.TrimPrefix(r.URL.Path, "/.well-known/acme-challenge/")
	if (r.Method != http.MethodGet && r.Method != http.MethodHead) || !acmeToken.MatchString(token) {
		http.NotFound(w, r)
		return
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer root.Close()
	file, err := root.Open(token)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 8192 {
		http.NotFound(w, r)
		return
	}
	body, err := io.ReadAll(io.LimitReader(file, 8193))
	if err != nil || len(body) > 8192 {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	if r.Method == http.MethodGet {
		_, _ = w.Write(body)
	}
}

type publicBucket struct {
	tokens  float64
	updated time.Time
}
type publicLimits struct {
	mu      sync.Mutex
	buckets map[string]publicBucket
	cleaned time.Time
	active  chan struct{}
}

func newPublicLimits() *publicLimits {
	return &publicLimits{buckets: make(map[string]publicBucket), cleaned: time.Now(), active: make(chan struct{}, 128)}
}
func (l *publicLimits) allow(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.cleaned) >= time.Minute {
		for key, bucket := range l.buckets {
			if now.Sub(bucket.updated) >= time.Minute {
				delete(l.buckets, key)
			}
		}
		l.cleaned = now
	}
	b, exists := l.buckets[ip]
	if !exists {
		if len(l.buckets) >= 10000 {
			return false
		}
		b = publicBucket{tokens: 400, updated: now}
	}
	b.tokens = min(400, b.tokens+now.Sub(b.updated).Seconds()*200)
	b.updated = now
	allowed := b.tokens >= 1
	if allowed {
		b.tokens--
	}
	l.buckets[ip] = b
	return allowed
}
func (l *publicLimits) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.TLS != nil {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		}
		// RemoteAddr has already been authenticated by the PROXY listener. Never
		// use client-supplied HTTP forwarding headers for public request limits.
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil || !l.allow(ip, time.Now()) {
			w.Header().Set("Retry-After", "1")
			w.Header().Set("Cache-Control", "no-store")
			http.Error(w, "request rate limit reached", http.StatusTooManyRequests)
			return
		}
		select {
		case l.active <- struct{}{}:
			defer func() { <-l.active }()
		default:
			w.Header().Set("Retry-After", "1")
			w.Header().Set("Cache-Control", "no-store")
			http.Error(w, "server is busy", http.StatusServiceUnavailable)
			return
		}
		next.ServeHTTP(w, r)
	})
}
