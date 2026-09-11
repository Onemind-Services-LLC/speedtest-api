package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestRequestLogsCorrelateTestsWithinClientSession(t *testing.T) {
	s := testServer(t)
	logs := captureLogs(s, slog.LevelInfo)
	clientID := "873f962f-70c1-478e-9c67-5cfe3cd72cd5"
	testID := "b66380e6-9474-461e-8224-e54688b97c7e"
	seen := map[string]bool{}
	for _, path := range []string{"/v1/info?", "/__down?bytes=0&", "/__down?bytes=128&", "/__up?"} {
		logs.Reset()
		method := http.MethodGet
		if strings.HasPrefix(path, "/__up") {
			method = http.MethodPost
		}
		w := call(s, method, path+"client_id="+clientID+"&test_id="+testID, strings.NewReader("payload"), nil)
		entry := decodeLog(t, logs)
		id := w.Header().Get("X-Request-ID")
		if id == "" || seen[id] || entry["request_id"] != id {
			t.Fatalf("request ID missing, reused, or mismatched: %v", entry)
		}
		seen[id] = true
		if entry["client_id"] != clientID || entry["test_id"] != testID {
			t.Fatalf("missing session/test context: %v", entry)
		}
	}
}

func TestRequestLogsExcludeMalformedAndDuplicateCorrelationIDs(t *testing.T) {
	valid := "873f962f-70c1-478e-9c67-5cfe3cd72cd5"
	for _, name := range []string{"client_id", "test_id"} {
		for _, value := range []string{"", "private-token", "private\nlevel=ERROR", "192.0.2.1", strings.Repeat("a", 2000), valid + "\n", "873f962f-70c1-178e-9c67-5cfe3cd72cd5", "873f962f-70c1-478e-0c67-5cfe3cd72cd5"} {
			s := testServer(t)
			logs := captureLogs(s, slog.LevelInfo)
			w := call(s, "GET", "/v1/info?"+name+"="+url.QueryEscape(value), nil, nil)
			entry := decodeLog(t, logs)
			if _, exists := entry[name]; exists || w.Code != http.StatusOK {
				t.Fatalf("invalid correlation field retained or request rejected: %v", entry)
			}
			if strings.Contains(logs.String(), "private") || strings.Contains(logs.String(), "192.0.2.1") {
				t.Fatal("private data leaked into logs")
			}
		}
		s := testServer(t)
		logs := captureLogs(s, slog.LevelInfo)
		call(s, "GET", "/v1/info?"+name+"="+valid+"&"+name+"="+valid, nil, nil)
		if _, exists := decodeLog(t, logs)[name]; exists {
			t.Fatal("ambiguous duplicate ID retained")
		}
	}
}

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
			if entry["method"] != method || entry["path"] != strings.SplitN(path, "?", 2)[0] {
				t.Fatalf("missing request classification: %v", entry)
			}
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

func TestRequestLogsExcludeUnrecognizedMethodsAndPaths(t *testing.T) {
	for _, tc := range []struct {
		name, method, target, loggedMethod, loggedPath string
		status                                         int
	}{
		{"path line breaks", "GET", "/v1/info%0d%0alevel=ERROR%20msg=forged", "GET", "unmatched", 404},
		{"path markup", "GET", "/%3Cscript%3Eforged%3C/script%3E", "GET", "unmatched", 404},
		{"path unicode separator", "GET", "/v1/info%E2%80%A8forged", "GET", "unmatched", 404},
		{"custom method", "forged-private-token", "/v1/info", "OTHER", "/v1/info", 405},
		{"method line breaks", "GET\r\nlevel=ERROR msg=forged", "/v1/info", "OTHER", "/v1/info", 405},
		{"method markup", "<script>forged</script>", "/v1/info", "OTHER", "/v1/info", 405},
		{"standard rejected method", "DELETE", "/v1/info", "DELETE", "/v1/info", 405},
		{"query on known path", "GET", "/v1/info?token=forged", "GET", "/v1/info", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testServer(t)
			logs := captureLogs(s, slog.LevelInfo)
			r := httptest.NewRequest(http.MethodGet, tc.target, nil)
			// Exercise the handler directly as well as values accepted by the
			// HTTP parser; logging must not depend on transport validation.
			r.Method = tc.method
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			entry := decodeLog(t, logs)
			if w.Code != tc.status || entry["status"] != float64(tc.status) {
				t.Fatalf("unexpected response or logged status: %d, %v", w.Code, entry)
			}
			if entry["method"] != tc.loggedMethod || entry["path"] != tc.loggedPath {
				t.Fatalf("unexpected request classification: %v", entry)
			}
			if strings.Contains(logs.String(), "forged") || bytes.Count(logs.Bytes(), []byte("\n")) != 1 {
				t.Fatal("untrusted request content leaked into logs")
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
