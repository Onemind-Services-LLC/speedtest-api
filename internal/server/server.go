package server

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const ProtocolVersion = 1

type Server struct {
	logger        *slog.Logger
	packetLoss    *packetLossServer
	config        Config
	origins       map[string]bool
	payload       []byte
	slots         chan struct{}
	ready         atomic.Bool
	downloadBytes atomic.Int64
	uploadBytes   atomic.Int64
	transfers     atomic.Int64
	rejected      atomic.Int64
}

func New(c Config) (*Server, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	s := &Server{config: c, origins: make(map[string]bool), payload: make([]byte, 1<<20), slots: make(chan struct{}, c.MaxConcurrent)}
	s.logger = slog.Default().With("region", c.RegionID)
	if _, err := rand.Read(s.payload); err != nil {
		return nil, err
	}
	for _, origin := range c.AllowedOrigins {
		s.origins[strings.TrimSpace(origin)] = true
	}
	s.ready.Store(true)
	return s, nil
}

func (s *Server) SetReady(ready bool) { s.ready.Store(ready) }

func (s *Server) HTTPServer() *http.Server {
	return &http.Server{
		Addr: s.config.Address, Handler: s,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: s.config.RequestTimeout,
		WriteTimeout: s.config.RequestTimeout, IdleTimeout: 60 * time.Second,
		MaxHeaderBytes: 16 << 10,
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	recorded := &loggedResponse{ResponseWriter: w}
	w = recorded
	body := &loggedBody{ReadCloser: r.Body}
	r.Body = body
	requestID := rand.Text()
	w.Header().Set("X-Request-ID", requestID)
	defer s.logRequest(r, recorded, body, requestID, started)
	h := w.Header()
	h.Set("Cache-Control", "no-store, no-transform")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Speedtest-Region", s.config.RegionID)
	h.Set("X-Accel-Buffering", "no")
	h.Set("Vary", "Origin")
	if origin := r.Header.Get("Origin"); origin != "" {
		if !s.origins[origin] {
			fail(w, http.StatusForbidden, "origin is not allowed")
			return
		}
		h.Set("Access-Control-Allow-Origin", origin)
		h.Set("Timing-Allow-Origin", origin)
		h.Set("Access-Control-Expose-Headers", "Content-Length, Content-Encoding, Server-Timing, X-Speedtest-Region, X-Request-ID")
	}
	method := http.MethodGet
	switch r.URL.Path {
	case "/__up", "/v1/packet-loss":
		method = http.MethodPost
	case "/__down", "/v1/info", "/healthz", "/readyz", "/metrics":
	default:
		fail(w, http.StatusNotFound, "endpoint not found")
		return
	}
	if r.Method == http.MethodOptions {
		if r.Header.Get("Access-Control-Request-Method") != method {
			fail(w, http.StatusMethodNotAllowed, "method is not allowed")
			return
		}
		for _, header := range strings.Split(r.Header.Get("Access-Control-Request-Headers"), ",") {
			if header = strings.TrimSpace(header); header != "" && !strings.EqualFold(header, "Content-Type") {
				fail(w, http.StatusForbidden, "request header is not allowed")
				return
			}
		}
		h.Set("Access-Control-Allow-Methods", method)
		h.Set("Access-Control-Allow-Headers", "Content-Type")
		h.Set("Access-Control-Max-Age", "600")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != method {
		h.Set("Allow", method)
		fail(w, http.StatusMethodNotAllowed, "method is not allowed")
		return
	}
	switch r.URL.Path {
	case "/healthz":
		reply(w, http.StatusOK, map[string]string{"status": "ok"})
	case "/readyz":
		if !s.ready.Load() {
			fail(w, http.StatusServiceUnavailable, "server is draining")
			return
		}
		reply(w, http.StatusOK, map[string]string{"status": "ready"})
	case "/metrics":
		s.metrics(w)
	default:
		if !s.ready.Load() {
			fail(w, http.StatusServiceUnavailable, "server is draining")
			return
		}
		switch r.URL.Path {
		case "/v1/info":
			if len(s.slots) == cap(s.slots) {
				h.Set("Retry-After", "5")
				fail(w, http.StatusServiceUnavailable, "server is busy")
				return
			}
			reply(w, http.StatusOK, map[string]any{
				"capabilities":    map[string]bool{"packetLoss": s.packetLoss != nil},
				"protocolVersion": ProtocolVersion, "region": map[string]string{"id": s.config.RegionID, "name": s.config.RegionName},
				"limits": map[string]any{"maxDownloadBytes": s.config.MaxDownload, "maxUploadBytes": s.config.MaxUpload, "maxConcurrentTransfers": s.config.MaxConcurrent},
			})
		case "/v1/packet-loss":
			s.packetLossOffer(w, r)
		case "/__down":
			s.download(w, r, started)
		case "/__up":
			s.upload(w, r)
		}
	}
}

func (s *Server) acquire(w http.ResponseWriter) bool {
	select {
	case s.slots <- struct{}{}:
		s.transfers.Add(1)
		return true
	default:
		s.rejected.Add(1)
		w.Header().Set("Retry-After", "5")
		fail(w, http.StatusServiceUnavailable, "server is busy")
		return false
	}
}

func (s *Server) download(w http.ResponseWriter, r *http.Request, started time.Time) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		fail(w, http.StatusBadRequest, "invalid query string")
		return
	}
	values := query["bytes"]
	n := int64(0)
	if len(values) > 1 {
		fail(w, http.StatusBadRequest, "bytes must be a single integer")
		return
	}
	if len(values) == 1 {
		n, err = strconv.ParseInt(values[0], 10, 64)
	}
	if err != nil || n < 0 {
		fail(w, http.StatusBadRequest, "bytes must be a nonnegative integer")
		return
	}
	if n > s.config.MaxDownload {
		fail(w, http.StatusRequestEntityTooLarge, "download exceeds payload limit")
		return
	}
	// Zero-byte latency probes stay responsive when transfer slots are occupied.
	if n > 0 {
		if !s.acquire(w) {
			return
		}
		defer func() { <-s.slots }()
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(n, 10))
	w.Header().Set("Server-Timing", fmt.Sprintf("processing;dur=%.3f", float64(time.Since(started).Microseconds())/1000))
	w.WriteHeader(http.StatusOK)
	for remaining := n; remaining > 0; {
		if r.Context().Err() != nil {
			return
		}
		size := min(remaining, int64(len(s.payload)))
		written, err := w.Write(s.payload[:size])
		s.downloadBytes.Add(int64(written))
		remaining -= int64(written)
		if err != nil || written == 0 {
			return
		}
	}
}

func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Content-Encoding") != "" {
		fail(w, http.StatusUnsupportedMediaType, "encoded uploads are not supported")
		return
	}
	if r.ContentLength > s.config.MaxUpload {
		fail(w, http.StatusRequestEntityTooLarge, "upload exceeds payload limit")
		return
	}
	if !s.acquire(w) {
		return
	}
	defer func() { <-s.slots }()
	r.Body = http.MaxBytesReader(w, r.Body, s.config.MaxUpload)
	defer r.Body.Close()
	// Stream to discard: success means every byte was received, never just the headers.
	n, err := io.Copy(io.Discard, r.Body)
	s.uploadBytes.Add(n)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			fail(w, http.StatusRequestEntityTooLarge, "upload exceeds payload limit")
		} else {
			fail(w, http.StatusBadRequest, "upload body was incomplete")
		}
		return
	}
	reply(w, http.StatusOK, map[string]any{"bytes": n, "regionId": s.config.RegionID})
}

func (s *Server) metrics(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	fmt.Fprintf(w, "# TYPE speedtest_active_transfers gauge\nspeedtest_active_transfers %d\n# TYPE speedtest_transfers_total counter\nspeedtest_transfers_total %d\n# TYPE speedtest_rejected_transfers_total counter\nspeedtest_rejected_transfers_total %d\n# TYPE speedtest_download_bytes_total counter\nspeedtest_download_bytes_total %d\n# TYPE speedtest_upload_bytes_total counter\nspeedtest_upload_bytes_total %d\n", len(s.slots), s.transfers.Load(), s.rejected.Load(), s.downloadBytes.Load(), s.uploadBytes.Load())
}

func reply(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func fail(w http.ResponseWriter, status int, message string) {
	reply(w, status, map[string]string{"error": message})
}
