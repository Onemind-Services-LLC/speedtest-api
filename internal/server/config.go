package server

import (
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Config describes one regional endpoint. There is no shared database or registry.
type Config struct {
	Environment        string
	MetricsAddress     string
	MaxPerClient       int
	MaxWebRTCPerClient int
	ASNDatabasePath    string
	TrustedProxies     []string
	WebRTCAddress      string
	WebRTCPublicIP     string
	MaxWebRTC          int
	Address            string
	RegionID           string
	RegionName         string
	AllowedOrigins     []string
	MaxDownload        int64
	MaxUpload          int64
	MaxConcurrent      int
	RequestTimeout     time.Duration
	LogLevel           slog.Level
}

func DefaultConfig() Config {
	return Config{
		Environment: "development", MaxPerClient: 16, MaxWebRTCPerClient: 16,
		WebRTCAddress: ":8081", MaxWebRTC: 64,
		Address: ":8080", RegionID: "local", RegionName: "Local development",
		AllowedOrigins: []string{"http://localhost:3000", "http://127.0.0.1:3000"},
		MaxDownload:    64 << 20, MaxUpload: 64 << 20, MaxConcurrent: 64,
		RequestTimeout: 30 * time.Second,
	}
}

func ConfigFromEnv() (Config, error) {
	c := DefaultConfig()
	if value, ok := os.LookupEnv("LOG_LEVEL"); ok {
		switch strings.ToLower(value) {
		case "debug":
			c.LogLevel = slog.LevelDebug
		case "info":
			c.LogLevel = slog.LevelInfo
		case "warn":
			c.LogLevel = slog.LevelWarn
		case "error":
			c.LogLevel = slog.LevelError
		default:
			return c, fmt.Errorf("LOG_LEVEL must be debug, info, warn or error")
		}
	}
	for name, target := range map[string]*string{
		"SPEEDTEST_ENV": &c.Environment, "METRICS_LISTEN_ADDR": &c.MetricsAddress,
		"WEBRTC_LISTEN_ADDR": &c.WebRTCAddress, "WEBRTC_PUBLIC_IP": &c.WebRTCPublicIP,
		"ASN_DATABASE_PATH": &c.ASNDatabasePath,
		"LISTEN_ADDR":       &c.Address, "REGION_ID": &c.RegionID, "REGION_NAME": &c.RegionName,
	} {
		if value, ok := os.LookupEnv(name); ok {
			*target = value
		}
	}
	if value := os.Getenv("TRUSTED_PROXY_CIDRS"); value != "" {
		c.TrustedProxies = strings.Split(value, ",")
	}
	if value, ok := os.LookupEnv("ALLOWED_ORIGINS"); ok {
		c.AllowedOrigins = strings.Split(value, ",")
	}
	for name, target := range map[string]*int64{"MAX_DOWNLOAD_BYTES": &c.MaxDownload, "MAX_UPLOAD_BYTES": &c.MaxUpload} {
		if value, ok := os.LookupEnv(name); ok {
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return c, fmt.Errorf("%s must be an integer", name)
			}
			*target = n
		}
	}
	for name, target := range map[string]*int{"MAX_CONCURRENT_TRANSFERS": &c.MaxConcurrent, "MAX_TRANSFERS_PER_CLIENT": &c.MaxPerClient, "MAX_WEBRTC_PER_CLIENT": &c.MaxWebRTCPerClient} {
		if value, ok := os.LookupEnv(name); ok {
			n, err := strconv.Atoi(value)
			if err != nil {
				return c, fmt.Errorf("%s must be an integer", name)
			}
			*target = n
		}
	}
	if value, ok := os.LookupEnv("MAX_WEBRTC_SESSIONS"); ok {
		n, err := strconv.Atoi(value)
		if err != nil {
			return c, fmt.Errorf("MAX_WEBRTC_SESSIONS must be an integer")
		}
		c.MaxWebRTC = n
	}
	if value, ok := os.LookupEnv("REQUEST_TIMEOUT"); ok {
		d, err := time.ParseDuration(value)
		if err != nil {
			return c, fmt.Errorf("REQUEST_TIMEOUT must be a duration such as 30s")
		}
		c.RequestTimeout = d
	}
	return c, c.Validate()
}

func (c Config) Validate() error {
	if c.Environment != "development" && c.Environment != "production" {
		return fmt.Errorf("SPEEDTEST_ENV must be development or production")
	}
	if c.MaxPerClient < 1 || c.MaxPerClient > 1024 || c.MaxWebRTCPerClient < 1 || c.MaxWebRTCPerClient > 1024 {
		return fmt.Errorf("per-client limits must be between 1 and 1024")
	}
	if c.MetricsAddress != "" {
		host, _, err := net.SplitHostPort(c.MetricsAddress)
		ip, ipErr := netip.ParseAddr(host)
		if err != nil || ipErr != nil || !(ip.IsLoopback() || ip.IsPrivate()) {
			return fmt.Errorf("METRICS_LISTEN_ADDR must bind an explicit loopback or private IP and port")
		}
	}
	if c.Environment == "production" && (c.RegionID == "local" || c.MetricsAddress == "") {
		return fmt.Errorf("production requires a non-local REGION_ID and private METRICS_LISTEN_ADDR")
	}
	for _, value := range c.TrustedProxies {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(value))
		if err != nil || prefix.Bits() == 0 {
			return fmt.Errorf("TRUSTED_PROXY_CIDRS must contain explicit non-default CIDR ranges")
		}
	}
	if c.MaxWebRTC < 1 || c.MaxWebRTC > 1024 {
		return fmt.Errorf("MAX_WEBRTC_SESSIONS must be between 1 and 1024")
	}
	if c.WebRTCPublicIP != "" {
		ip := net.ParseIP(c.WebRTCPublicIP)
		if ip == nil || ip.To4() == nil || ip.IsUnspecified() || ip.IsMulticast() {
			return fmt.Errorf("WEBRTC_PUBLIC_IP must be a unicast IPv4 address")
		}
	}
	if c.WebRTCAddress != "" {
		if _, err := net.ResolveUDPAddr("udp4", c.WebRTCAddress); err != nil {
			return fmt.Errorf("WEBRTC_LISTEN_ADDR must be a UDP address: %w", err)
		}
	}
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`).MatchString(c.RegionID) {
		return fmt.Errorf("REGION_ID must be 1-63 lowercase letters, digits or hyphens")
	}
	if strings.TrimSpace(c.RegionName) == "" || len(c.RegionName) > 120 {
		return fmt.Errorf("REGION_NAME must be 1-120 characters")
	}
	if c.Address == "" {
		return fmt.Errorf("LISTEN_ADDR is required")
	}
	if c.MaxDownload < 1 || c.MaxDownload > 1<<30 || c.MaxUpload < 1 || c.MaxUpload > 1<<30 {
		return fmt.Errorf("payload limits must be between 1 and 1073741824 bytes")
	}
	if c.MaxConcurrent < 1 || c.MaxConcurrent > 1024 {
		return fmt.Errorf("MAX_CONCURRENT_TRANSFERS must be between 1 and 1024")
	}
	if c.RequestTimeout < time.Second || c.RequestTimeout > 2*time.Minute {
		return fmt.Errorf("REQUEST_TIMEOUT must be between 1s and 2m")
	}
	if len(c.AllowedOrigins) == 0 {
		return fmt.Errorf("ALLOWED_ORIGINS is required")
	}
	for _, raw := range c.AllowedOrigins {
		origin := strings.TrimSpace(raw)
		u, err := url.Parse(origin)
		if err != nil || u.Hostname() == "" || strings.Contains(u.Host, "*") || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || origin != u.Scheme+"://"+u.Host {
			return fmt.Errorf("ALLOWED_ORIGINS must contain exact http(s) origins without paths or wildcards")
		}
		host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
		ip := net.ParseIP(host)
		if c.Environment == "production" && (u.Scheme != "https" || host == "localhost" || strings.HasSuffix(host, ".localhost") || (ip != nil && (ip.IsLoopback() || ip.IsUnspecified()))) {
			return fmt.Errorf("production ALLOWED_ORIGINS must use HTTPS without loopback hosts")
		}
	}
	return nil
}
