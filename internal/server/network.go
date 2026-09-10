package server

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

type networkASN struct {
	Number       uint   `json:"number" maxminddb:"autonomous_system_number"`
	Organization string `json:"organization" maxminddb:"autonomous_system_organization"`
}

// Close releases the optional local database after HTTP requests have drained.
func (s *Server) Close() error {
	if s.asnDB != nil {
		return s.asnDB.Close()
	}
	return nil
}

func (s *Server) trusted(ip netip.Addr) bool {
	for _, prefix := range s.trustedProxies {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}

// Forwarded addresses are trusted only through an explicitly configured proxy
// chain. Walk from the transport peer toward the client, stopping at the first
// untrusted address. Never accept client-supplied prefixes of the chain.
func (s *Server) clientIP(r *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	peer = peer.Unmap()
	if !s.trusted(peer) {
		return peer
	}
	parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	if len(parts) > 32 {
		return peer
	}
	current := peer
	for i := len(parts) - 1; i >= 0 && s.trusted(current); i-- {
		address, err := netip.ParseAddr(strings.TrimSpace(parts[i]))
		if err != nil || address.Zone() != "" {
			return peer
		}
		current = address.Unmap()
	}
	return current
}

func (s *Server) network(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if !ip.IsValid() {
		fail(w, http.StatusServiceUnavailable, "client address is unavailable")
		return
	}
	family := 6
	if ip.Is4() {
		family = 4
	}
	status := "not-configured"
	var asn *networkASN
	if ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || !ip.IsGlobalUnicast() {
		status = "private-address"
	} else if s.asnDB != nil {
		status = "not-found"
		var found networkASN
		if err := s.asnDB.Lookup(ip).Decode(&found); err != nil {
			status = "lookup-failed"
		} else if found.Number > 0 && found.Organization != "" {
			asn = &found
			status = "available"
		}
	}
	reply(w, http.StatusOK, map[string]any{
		"protocolVersion": ProtocolVersion, "regionId": s.config.RegionID,
		"ip": ip.String(), "family": family, "asn": asn, "asnStatus": status,
		// This protocol describes the API hop, which may be behind an ingress.
		"serverProtocol": r.Proto,
	})
}
