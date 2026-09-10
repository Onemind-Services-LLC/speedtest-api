package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func captureLogs(s *Server, level slog.Level) *bytes.Buffer {
	buffer := new(bytes.Buffer)
	s.logger = slog.New(slog.NewJSONHandler(buffer, &slog.HandlerOptions{Level: level})).With("region", s.config.RegionID)
	return buffer
}

func decodeLog(t *testing.T, data *bytes.Buffer) map[string]any {
	t.Helper()
	var entry map[string]any
	if err := json.Unmarshal(data.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	return entry
}

func TestRequestLogsCountStreamingBytesAndMatchRequestID(t *testing.T) {
	s := testServer(t)
	for _, direction := range []string{"download", "upload"} {
		t.Run(direction, func(t *testing.T) {
			logs := captureLogs(s, slog.LevelInfo)
			method, path, payload := "GET", "/__down?bytes=128&nonce=private-value", ""
			if direction == "upload" {
				method, path, payload = "POST", "/__up?secret=private-value", strings.Repeat("private-body", 4)
			}
			w := call(s, method, path, strings.NewReader(payload), map[string]string{"Authorization": "private-token", "Origin": "http://localhost:3000"})
			entry := decodeLog(t, logs)
			if entry["status"] != float64(200) || entry["outcome"] != "completed" || entry["region"] != "local" {
				t.Fatalf("unexpected entry: %v", entry)
			}
			if entry["request_id"] != w.Header().Get("X-Request-ID") || w.Header().Get("X-Request-ID") == "" {
				t.Fatal("missing correlation ID")
			}
			if entry["bytes_received"] != float64(len(payload)) || entry["bytes_sent"] != float64(w.Body.Len()) {
				t.Fatalf("incorrect byte counts: %v", entry)
			}
			if entry["duration_ms"].(float64) < 0 {
				t.Fatal("negative duration")
			}
			if strings.Contains(logs.String(), "private-") || strings.Contains(logs.String(), "192.0.2.1") {
				t.Fatal("request data leaked into logs")
			}
		})
	}
}

func TestRequestLogsReportRejectionsAndSuppressRoutineHealthChecks(t *testing.T) {
	s := testServer(t)
	logs := captureLogs(s, slog.LevelInfo)
	call(s, "GET", "/healthz", nil, nil)
	if logs.Len() != 0 {
		t.Fatal("health check logged at info")
	}
	call(s, "GET", "/v1/info", nil, map[string]string{"Origin": "https://untrusted.example"})
	entry := decodeLog(t, logs)
	if entry["status"] != float64(403) || entry["level"] != "WARN" {
		t.Fatalf("missing CORS rejection: %v", entry)
	}
	logs.Reset()
	s.SetReady(false)
	call(s, "GET", "/v1/info", nil, nil)
	entry = decodeLog(t, logs)
	if entry["status"] != float64(503) || entry["outcome"] != "unavailable" {
		t.Fatalf("missing unavailable status: %v", entry)
	}
	logs = captureLogs(s, slog.LevelDebug)
	call(s, "GET", "/healthz", nil, nil)
	if decodeLog(t, logs)["level"] != "DEBUG" {
		t.Fatal("missing debug health check")
	}
	logs.Reset()
	call(s, "GET", "/private-path?token=private-token", nil, nil)
	if decodeLog(t, logs)["path"] != "unmatched" || strings.Contains(logs.String(), "private-") {
		t.Fatal("unmatched path leaked into logs")
	}
}

type failedWriter struct{ *httptest.ResponseRecorder }

func (w failedWriter) Write([]byte) (int, error) { return 3, errors.New("broken connection") }

func TestRequestLogsDetectPartialDownloadsAndPreserveResponseController(t *testing.T) {
	s := testServer(t)
	logs := captureLogs(s, slog.LevelInfo)
	s.ServeHTTP(failedWriter{httptest.NewRecorder()}, httptest.NewRequest("GET", "/__down?bytes=128", nil))
	entry := decodeLog(t, logs)
	if entry["bytes_sent"] != float64(3) || entry["outcome"] != "interrupted" {
		t.Fatalf("incomplete response not logged: %v", entry)
	}
	recorder := httptest.NewRecorder()
	if err := http.NewResponseController(&loggedResponse{ResponseWriter: recorder}).Flush(); err != nil || !recorder.Flushed {
		t.Fatal("streaming flush is unavailable")
	}
}

func TestLogLevelConfiguration(t *testing.T) {
	for _, level := range []string{"debug", "info", "warn", "error"} {
		t.Setenv("LOG_LEVEL", level)
		c, err := ConfigFromEnv()
		if err != nil || !strings.EqualFold(c.LogLevel.String(), level) {
			t.Fatalf("invalid parsed level: %v %v", c.LogLevel, err)
		}
	}
	t.Setenv("LOG_LEVEL", "invalid")
	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("invalid log level accepted")
	}
}
