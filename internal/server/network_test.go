package server

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestNetworkIdentityTrustBoundary(t *testing.T) {
	for _, test := range []struct {
		name, peer, forwarded, want string
		trusted                     []string
	}{
		{"direct", "203.0.113.2:1234", "1.1.1.1", "203.0.113.2", nil},
		{"trusted ingress", "10.0.0.2:1234", "203.0.113.2", "203.0.113.2", []string{"10.0.0.0/24"}},
		{"spoofed prefix", "10.0.0.2:1234", "1.1.1.1, 203.0.113.2", "203.0.113.2", []string{"10.0.0.0/24"}},
		{"proxy chain", "10.0.0.2:1234", "2001:db8::1, 10.0.0.3", "2001:db8::1", []string{"10.0.0.0/24"}},
		{"malformed chain", "10.0.0.2:1234", "invalid", "10.0.0.2", []string{"10.0.0.0/24"}},
		{"mapped ipv4", "[::ffff:127.0.0.1]:1234", "", "127.0.0.1", nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := DefaultConfig()
			c.TrustedProxies = test.trusted
			app, err := New(c)
			if err != nil {
				t.Fatal(err)
			}
			defer app.Close()
			r := httptest.NewRequest("GET", "/v1/network", nil)
			r.RemoteAddr = test.peer
			r.Header.Set("X-Forwarded-For", test.forwarded)
			r.Header.Set("Origin", "http://localhost:3000")
			w := httptest.NewRecorder()
			app.ServeHTTP(w, r)
			var result struct {
				IP     string      `json:"ip"`
				Family int         `json:"family"`
				ASN    *networkASN `json:"asn"`
				Region string      `json:"regionId"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if w.Code != 200 || result.IP != test.want || result.Region != c.RegionID || result.ASN != nil {
				t.Fatalf("unexpected response: %d %s", w.Code, w.Body.String())
			}
			expectedFamily := 4
			if test.name == "proxy chain" {
				expectedFamily = 6
			}
			if result.Family != expectedFamily {
				t.Fatalf("family = %d", result.Family)
			}
			if w.Header().Get("Timing-Allow-Origin") != "http://localhost:3000" || w.Header().Get("Cache-Control") != "no-store, no-transform" {
				t.Fatal("missing privacy/timing headers")
			}
		})
	}
}

func TestNetworkConfigurationFailsClosed(t *testing.T) {
	for _, cidr := range []string{"*", "0.0.0.0/0", "::/0", "localhost"} {
		c := DefaultConfig()
		c.TrustedProxies = []string{cidr}
		if _, err := New(c); err == nil {
			t.Fatalf("accepted %s", cidr)
		}
	}
	c := DefaultConfig()
	c.ASNDatabasePath = t.TempDir() + "/missing.mmdb"
	if _, err := New(c); err == nil {
		t.Fatal("accepted missing database")
	}
}
