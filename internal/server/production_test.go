package server

import (
	"io"
	"net/http/httptest"
	"testing"
)

func TestProductionConfigurationAndMetricsIsolation(t *testing.T) {
	c := DefaultConfig()
	c.Environment = "production"
	if c.Validate() == nil {
		t.Fatal("development defaults accepted in production")
	}
	c.RegionID = "blr-1"
	c.AllowedOrigins = []string{"https://speed.company.test"}
	c.MetricsAddress = "127.0.0.1:9090"
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, origin := range []string{"http://speed.company.test", "https://localhost", "https://127.0.0.2", "https://[::1]"} {
		bad := c
		bad.AllowedOrigins = []string{origin}
		if bad.Validate() == nil {
			t.Fatalf("unsafe origin accepted: %s", origin)
		}
	}
	for _, address := range []string{":9090", "0.0.0.0:9090", "[::]:9090", "8.8.8.8:9090"} {
		bad := c
		bad.MetricsAddress = address
		if bad.Validate() == nil {
			t.Fatalf("public metrics accepted: %s", address)
		}
	}
	app, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if call(app, "GET", "/metrics", nil, nil).Code != 404 {
		t.Fatal("metrics exposed on public listener")
	}
	w := httptest.NewRecorder()
	app.MetricsServer().Handler.ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	if w.Code != 200 {
		t.Fatal("private metrics unavailable")
	}
}

func TestConcurrentClientIsolationAndRecovery(t *testing.T) {
	c := DefaultConfig()
	c.MaxConcurrent = 2
	c.MaxPerClient = 1
	c.TrustedProxies = []string{"127.0.0.1/32"}
	app, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	request := func(client string, body io.Reader) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/__up", body)
		r.RemoteAddr = "127.0.0.1:4321"
		r.Header.Set("X-Forwarded-For", client)
		w := httptest.NewRecorder()
		app.ServeHTTP(w, r)
		return w
	}
	done := make(chan *httptest.ResponseRecorder, 2)
	writers := []*io.PipeWriter{}
	for _, ip := range []string{"192.0.2.1", "192.0.2.2"} {
		reader, writer := io.Pipe()
		defer reader.Close()
		defer writer.Close()
		writers = append(writers, writer)
		go func() { done <- request(ip, reader) }()
		if _, err := writer.Write([]byte("held")); err != nil {
			t.Fatal(err)
		}
	}
	if w := request("192.0.2.1", nil); w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatalf("client quota: %d", w.Code)
	}
	if w := request("192.0.2.3", nil); w.Code != 503 {
		t.Fatalf("global quota: %d", w.Code)
	}
	for _, route := range []string{"/healthz", "/readyz", "/__down?bytes=0"} {
		if w := call(app, "GET", route, nil, nil); w.Code != 200 {
			t.Fatalf("probe blocked: %s", route)
		}
	}
	// A spoofed header from an untrusted socket must remain in that socket's quota.
	r := httptest.NewRequest("POST", "/__up", nil)
	r.RemoteAddr = "192.0.2.1:4321"
	r.Header.Set("X-Forwarded-For", "192.0.2.99")
	w := httptest.NewRecorder()
	app.ServeHTTP(w, r)
	if w.Code != 429 {
		t.Fatal("spoofed forwarding header bypassed quota")
	}
	for _, writer := range writers {
		_ = writer.Close()
	}
	for range writers {
		if w := <-done; w.Code != 200 {
			t.Fatalf("held transfer failed: %d", w.Code)
		}
	}
	if len(app.clients) != 0 || len(app.slots) != 0 {
		t.Fatal("client state or transfer slot leaked")
	}
	if w := request("192.0.2.1", nil); w.Code != 200 {
		t.Fatal("capacity did not recover")
	}
}
