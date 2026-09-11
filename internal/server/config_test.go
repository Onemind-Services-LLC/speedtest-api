package server

import "testing"

func TestInvalidConfig(t *testing.T) {
	for name, change := range map[string]func(*Config){
		"invalid public IP":     func(c *Config) { c.WebRTCPublicIP = "not-an-ip" },
		"unspecified public IP": func(c *Config) { c.WebRTCPublicIP = "0.0.0.0" },
		"webRTC capacity":       func(c *Config) { c.MaxWebRTC = 0 },
		"webRTC address":        func(c *Config) { c.WebRTCAddress = "missing-port" },
		"region":                func(c *Config) { c.RegionID = "BAD\nHEADER" },
		"origin":                func(c *Config) { c.AllowedOrigins = []string{"https://example.com/"} },
		"wildcard":              func(c *Config) { c.AllowedOrigins = []string{"*"} },
		"wildcard host":         func(c *Config) { c.AllowedOrigins = []string{"https://*.example.com"} },
		"empty query":           func(c *Config) { c.AllowedOrigins = []string{"https://example.com?"} },
		"limit":                 func(c *Config) { c.MaxUpload = 0 },
		"concurrency":           func(c *Config) { c.MaxConcurrent = 0 },
		"timeout":               func(c *Config) { c.RequestTimeout = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			c := DefaultConfig()
			change(&c)
			if _, err := New(c); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
}

func TestEnvironmentConfig(t *testing.T) {
	t.Setenv("REGION_ID", "blr-1")
	t.Setenv("REGION_NAME", "Bengaluru")
	t.Setenv("ALLOWED_ORIGINS", "https://speed.example.com, https://other.example.com")
	t.Setenv("MAX_UPLOAD_BYTES", "1024")
	t.Setenv("REQUEST_TIMEOUT", "10s")
	c, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if c.RegionID != "blr-1" || c.MaxUpload != 1024 {
		t.Fatal("environment not applied")
	}
	t.Setenv("MAX_CONCURRENT_TRANSFERS", "NaN")
	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("invalid number accepted")
	}
}

func TestPublicTLSConfigValidation(t *testing.T) {
	valid := DefaultConfig()
	valid.TLSAddress = ":8443"
	valid.RedirectAddress = ":8082"
	valid.TLSCertFile = "/tls/tls.crt"
	valid.TLSKeyFile = "/tls/tls.key"
	valid.PublicOrigin = "https://api.example.com"
	valid.BrowserRedirectURL = "https://ui.example.com/"
	valid.ProxyProtocolCIDRs = []string{"172.16.3.0/24"}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Config){
		"missing TLS listener":  func(c *Config) { c.TLSAddress = "" },
		"missing key":           func(c *Config) { c.TLSKeyFile = "" },
		"missing cert":          func(c *Config) { c.TLSCertFile = "" },
		"missing origin":        func(c *Config) { c.PublicOrigin = "" },
		"wildcard proxy trust":  func(c *Config) { c.ProxyProtocolCIDRs = []string{"0.0.0.0/0"} },
		"invalid proxy range":   func(c *Config) { c.ProxyProtocolCIDRs = []string{"bad"} },
		"plaintext origin":      func(c *Config) { c.PublicOrigin = "http://api.example.com" },
		"redirect credentials":  func(c *Config) { c.BrowserRedirectURL = "https://user@ui.example.com" },
		"redirect path":         func(c *Config) { c.BrowserRedirectURL = "https://ui.example.com/path" },
		"origin query":          func(c *Config) { c.PublicOrigin = "https://api.example.com?" },
		"listener missing port": func(c *Config) { c.TLSAddress = "localhost" },
	} {
		t.Run(name, func(t *testing.T) {
			c := valid
			change(&c)
			if c.Validate() == nil {
				t.Fatal("invalid public listener config accepted")
			}
		})
	}
	t.Setenv("TLS_LISTEN_ADDR", ":8443")
	t.Setenv("TLS_CERT_FILE", "/tls/tls.crt")
	t.Setenv("TLS_KEY_FILE", "/tls/tls.key")
	t.Setenv("PUBLIC_ORIGIN", "https://api.example.com")
	t.Setenv("PROXY_PROTOCOL_TRUSTED_CIDRS", "172.16.3.0/24, 10.0.0.0/24")
	c, err := ConfigFromEnv()
	if err != nil || len(c.ProxyProtocolCIDRs) != 2 || c.ProxyProtocolCIDRs[1] != "10.0.0.0/24" {
		t.Fatalf("TLS env not parsed: %v", err)
	}
}
