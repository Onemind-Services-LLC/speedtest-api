package server

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testServer(t *testing.T) *Server {
	t.Helper()
	c := DefaultConfig()
	c.MaxDownload = 2 << 20
	c.MaxUpload = 1024
	c.MaxConcurrent = 1
	s, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func call(s *Server, method, path string, body io.Reader, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, body)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func TestDownloads(t *testing.T) {
	s := testServer(t)
	for _, tc := range []struct {
		query        string
		status, size int
	}{
		{"", 200, 0}, {"?bytes=0", 200, 0}, {"?bytes=1048699", 200, 1048699},
		{"?bytes=-1", 400, 0}, {"?bytes=NaN", 400, 0}, {"?bytes=1.5", 400, 0}, {"?bytes=", 400, 0},
		{"?bytes=1&bytes=2", 400, 0}, {"?bytes=%zz", 400, 0}, {"?bytes=2097153", 413, 0},
	} {
		t.Run(tc.query, func(t *testing.T) {
			w := call(s, "GET", "/__down"+tc.query, nil, nil)
			if w.Code != tc.status {
				t.Fatalf("status %d, want %d", w.Code, tc.status)
			}
			if tc.status == 200 && w.Body.Len() != tc.size {
				t.Fatalf("size %d, want %d", w.Body.Len(), tc.size)
			}
			if !strings.Contains(w.Header().Get("Cache-Control"), "no-transform") {
				t.Fatal("missing cache protection")
			}
		})
	}
	w := call(s, "GET", "/__down?bytes=1048576", nil, nil)
	var compressed bytes.Buffer
	z := gzip.NewWriter(&compressed)
	_, _ = z.Write(w.Body.Bytes())
	_ = z.Close()
	if compressed.Len() < w.Body.Len()*99/100 {
		t.Fatal("download payload is too compressible")
	}
}

func TestUploadLimitsAndReceipt(t *testing.T) {
	s := testServer(t)
	for _, tc := range []struct {
		size, status int
		chunked      bool
	}{
		{0, 200, false}, {1024, 200, false}, {1025, 413, false}, {1025, 413, true},
	} {
		r := httptest.NewRequest("POST", "/__up", strings.NewReader(strings.Repeat("x", tc.size)))
		if tc.chunked {
			r.ContentLength = -1
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("size %d chunked %v: status %d", tc.size, tc.chunked, w.Code)
		}
		if tc.status == 200 {
			var receipt struct {
				Bytes    int
				RegionID string
			}
			if err := json.Unmarshal(w.Body.Bytes(), &receipt); err != nil {
				t.Fatal(err)
			}
			if receipt.Bytes != tc.size || receipt.RegionID != "local" {
				t.Fatalf("bad receipt: %+v", receipt)
			}
		}
	}
	if w := call(s, "POST", "/__up", strings.NewReader("x"), map[string]string{"Content-Encoding": "gzip"}); w.Code != 415 {
		t.Fatal("encoded upload was accepted")
	}
}

func TestUploadWaitsForEOFAndKeepsPingAvailable(t *testing.T) {
	s := testServer(t)
	reader, writer := io.Pipe()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- call(s, "POST", "/__up", reader, nil) }()
	if _, err := writer.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
		t.Fatal("upload acknowledged before EOF")
	default:
	}
	if w := call(s, "GET", "/__down?bytes=1", nil, nil); w.Code != 503 {
		t.Fatalf("capacity limit not enforced: %d", w.Code)
	}
	if w := call(s, "GET", "/v1/info", nil, nil); w.Code != 503 {
		t.Fatal("discovery advertised a full server")
	}
	if w := call(s, "GET", "/__down?bytes=0", nil, nil); w.Code != 200 {
		t.Fatal("ping was blocked by transfer capacity")
	}
	_ = writer.Close()
	select {
	case w := <-done:
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"bytes":5`) {
			t.Fatalf("bad receipt: %s", w.Body.String())
		}
	case <-time.After(time.Second):
		t.Fatal("upload did not finish")
	}
	if len(s.slots) != 0 {
		t.Fatal("transfer slot leaked")
	}
}

func TestBrokenUploadReleasesCapacity(t *testing.T) {
	s := testServer(t)
	reader, writer := io.Pipe()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- call(s, "POST", "/__up", reader, nil) }()
	_, _ = writer.Write([]byte("part"))
	_ = writer.CloseWithError(io.ErrUnexpectedEOF)
	w := <-done
	if w.Code != 400 || len(s.slots) != 0 {
		t.Fatalf("broken upload: status %d, active %d", w.Code, len(s.slots))
	}
}

func TestOriginAndPreflight(t *testing.T) {
	s := testServer(t)
	w := call(s, "GET", "/__down", nil, map[string]string{"Origin": "http://localhost:3000"})
	if w.Header().Get("Access-Control-Allow-Origin") != "http://localhost:3000" || w.Header().Get("Timing-Allow-Origin") == "" {
		t.Fatal("CORS/timing headers missing")
	}
	for _, origin := range []string{"https://attacker.example", "null", "http://localhost:3000.attacker.example"} {
		w = call(s, "GET", "/__down", nil, map[string]string{"Origin": origin})
		if w.Code != 403 || w.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatal("untrusted origin was allowed")
		}
	}
	h := map[string]string{"Origin": "http://localhost:3000", "Access-Control-Request-Method": "POST", "Access-Control-Request-Headers": "content-type"}
	if w = call(s, "OPTIONS", "/__up", nil, h); w.Code != 204 {
		t.Fatal("upload preflight failed")
	}
	h["Access-Control-Request-Headers"] = "authorization"
	if w = call(s, "OPTIONS", "/__up", nil, h); w.Code != 403 {
		t.Fatal("unsupported header allowed")
	}
}

func TestRoutesAndDraining(t *testing.T) {
	s := testServer(t)
	if w := call(s, "HEAD", "/__down", nil, nil); w.Code != 405 {
		t.Fatal("HEAD unexpectedly accepted")
	}
	if w := call(s, "GET", "/unknown", nil, nil); w.Code != 404 {
		t.Fatal("unknown route accepted")
	}
	s.SetReady(false)
	for _, path := range []string{"/readyz", "/v1/info", "/__down", "/__up"} {
		method := "GET"
		if path == "/__up" {
			method = "POST"
		}
		if w := call(s, method, path, nil, nil); w.Code != 503 {
			t.Fatalf("%s available while draining", path)
		}
	}
	if w := call(s, "GET", "/healthz", nil, nil); w.Code != 200 {
		t.Fatal("liveness failed during drain")
	}
}

func TestHTTPServerReadsRealUpload(t *testing.T) {
	s := testServer(t)
	ts := httptest.NewServer(s)
	defer ts.Close()
	response, err := http.Post(ts.URL+"/__up", "application/octet-stream", strings.NewReader(strings.Repeat("x", 1024)))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var receipt map[string]any
	if err := json.NewDecoder(response.Body).Decode(&receipt); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 || receipt["bytes"] != float64(1024) {
		t.Fatalf("invalid network receipt: %+v", receipt)
	}
}
