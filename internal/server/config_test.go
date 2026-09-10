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
