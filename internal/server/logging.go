package server

import (
	"io"
	"log/slog"
	"net/http"
	"time"
)

// Count streaming IO without retaining request bodies or response payloads.
type loggedResponse struct {
	http.ResponseWriter
	status   int
	bytes    int64
	writeErr bool
}

func (w *loggedResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *loggedResponse) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *loggedResponse) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytes += int64(n)
	w.writeErr = w.writeErr || err != nil || n != len(p)
	return n, err
}

type loggedBody struct {
	io.ReadCloser
	bytes int64
}

func (b *loggedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.bytes += int64(n)
	return n, err
}

func (s *Server) logRequest(r *http.Request, w *loggedResponse, body *loggedBody, id string, started time.Time) {
	path := r.URL.Path
	switch path {
	case "/v1/info", "/v1/packet-loss", "/__down", "/__up", "/healthz", "/readyz", "/metrics":
	default:
		// Arbitrary paths, queries, headers and network addresses can contain
		// personal data. Log the route classification instead of user input.
		path = "unmatched"
	}
	status := w.status
	if status == 0 {
		status = http.StatusOK
	}
	level, outcome := slog.LevelInfo, "completed"
	if path == "/healthz" || path == "/readyz" || path == "/metrics" || r.Method == http.MethodOptions {
		level = slog.LevelDebug
	}
	if status >= 400 {
		level, outcome = slog.LevelWarn, "rejected"
	}
	if status >= 500 {
		level, outcome = slog.LevelError, "unavailable"
	}
	if w.writeErr || r.Context().Err() != nil {
		level, outcome = slog.LevelWarn, "interrupted"
	}
	s.logger.Log(r.Context(), level, "http request",
		"request_id", id, "method", r.Method, "path", path,
		"status", status, "outcome", outcome,
		"duration_ms", float64(time.Since(started).Microseconds())/1000,
		"bytes_received", body.bytes, "bytes_sent", w.bytes,
	)
}
