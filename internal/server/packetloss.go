package server

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/webrtc/v4"
)

// A regional, ICE-lite UDP endpoint. ICE-lite never initiates connectivity checks
// to client-supplied candidates. Sessions are bounded in time, count and payload.
type packetLossServer struct {
	api    *webrtc.API
	mux    ice.UDPMux
	mu     sync.Mutex
	closed bool
	peers  map[*webrtc.PeerConnection]func()
	perIP  map[string]int
	limit  int
}

// StartPacketLoss must be called before serving HTTP. An empty address disables it.
func (s *Server) StartPacketLoss() error {
	if s.config.WebRTCAddress == "" {
		return nil
	}
	addr, err := net.ResolveUDPAddr("udp4", s.config.WebRTCAddress)
	if err != nil {
		return fmt.Errorf("WebRTC address: %w", err)
	}
	conn, err := net.ListenUDP("udp4", addr)
	if err != nil {
		return fmt.Errorf("WebRTC listen: %w", err)
	}
	mux := ice.NewUDPMuxDefault(ice.UDPMuxParams{UDPConn: conn})
	settings := webrtc.SettingEngine{}
	settings.SetLite(true)
	settings.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	settings.SetIncludeLoopbackCandidate(true)
	settings.SetICEUDPMux(mux)
	settings.SetSCTPMaxReceiveBufferSize(64 << 10)
	settings.SetSCTPMaxMessageSize(64)
	if s.config.WebRTCPublicIP != "" {
		err = settings.SetICEAddressRewriteRules(webrtc.ICEAddressRewriteRule{
			External: []string{s.config.WebRTCPublicIP}, AsCandidateType: webrtc.ICECandidateTypeHost,
			Mode: webrtc.ICEAddressRewriteReplace,
		})
		if err != nil {
			_ = mux.Close()
			return err
		}
	}
	s.packetLoss = &packetLossServer{api: webrtc.NewAPI(webrtc.WithSettingEngine(settings)), mux: mux,
		peers: make(map[*webrtc.PeerConnection]func()), perIP: make(map[string]int), limit: s.config.MaxWebRTC}
	return nil
}

func (s *Server) ClosePacketLoss() {
	if s.packetLoss == nil {
		return
	}
	p := s.packetLoss
	p.mu.Lock()
	p.closed = true
	closers := make([]func(), 0, len(p.peers))
	for _, closePeer := range p.peers {
		closers = append(closers, closePeer)
	}
	p.mu.Unlock()
	for _, closePeer := range closers {
		closePeer()
	}
	_ = p.mux.Close()
}

func (s *Server) packetLossOffer(w http.ResponseWriter, r *http.Request) {
	p := s.packetLoss
	if p == nil {
		fail(w, http.StatusNotImplemented, "packet loss is disabled in this region")
		return
	}
	if r.Header.Get("Origin") == "" {
		fail(w, http.StatusForbidden, "browser origin required")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	var offer webrtc.SessionDescription
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&offer); err != nil || offer.Type != webrtc.SDPTypeOffer {
		fail(w, http.StatusBadRequest, "a valid WebRTC offer is required")
		return
	}
	// Accept a single data-channel media section; this endpoint does not serve audio/video.
	media := 0
	for _, line := range strings.Split(offer.SDP, "\n") {
		if strings.HasPrefix(line, "m=") {
			media++
			if !strings.HasPrefix(line, "m=application ") {
				fail(w, http.StatusBadRequest, "only data-channel offers are supported")
				return
			}
		}
	}
	if media != 1 {
		fail(w, http.StatusBadRequest, "one data-channel media section is required")
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		fail(w, http.StatusBadRequest, "invalid offer body")
		return
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		fail(w, http.StatusBadRequest, "invalid client address")
		return
	}
	p.mu.Lock()
	if p.closed || len(p.peers) >= p.limit || p.perIP[ip] >= 4 {
		p.mu.Unlock()
		w.Header().Set("Retry-After", "5")
		fail(w, http.StatusServiceUnavailable, "packet loss service is busy")
		return
	}
	peer, err := p.api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		p.mu.Unlock()
		fail(w, http.StatusServiceUnavailable, "packet loss session could not start")
		return
	}
	var once sync.Once
	expiry := time.NewTimer(20 * time.Second)
	closed := make(chan struct{})
	closePeer := func() {
		once.Do(func() {
			expiry.Stop()
			close(closed)
			// Close before releasing the slot, so capacity includes closing connections.
			_ = peer.Close()
			p.mu.Lock()
			delete(p.peers, peer)
			p.perIP[ip]--
			if p.perIP[ip] == 0 {
				delete(p.perIP, ip)
			}
			p.mu.Unlock()
		})
	}
	go func() {
		select {
		case <-expiry.C:
			closePeer()
		case <-closed:
		}
	}()
	p.peers[peer] = closePeer
	p.perIP[ip]++
	p.mu.Unlock()
	success := false
	defer func() {
		if !success {
			closePeer()
		}
	}()
	peer.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed || state == webrtc.PeerConnectionStateDisconnected {
			go closePeer()
		}
	})
	var channels atomic.Int32
	peer.OnDataChannel(func(channel *webrtc.DataChannel) {
		retries := channel.MaxRetransmits()
		if channels.Add(1) != 1 || channel.Label() != "packet-loss-v1" || channel.Ordered() || retries == nil || *retries != 0 || channel.MaxPacketLifeTime() != nil {
			go closePeer()
			return
		}
		var messages atomic.Int32
		channel.OnMessage(func(message webrtc.DataChannelMessage) {
			if message.IsString || len(message.Data) != 64 || binary.BigEndian.Uint32(message.Data[:4]) >= 1000 || messages.Add(1) > 1000 {
				go closePeer()
				return
			}
			if err := channel.Send(message.Data); err != nil {
				go closePeer()
			}
		})
		channel.OnClose(func() { go closePeer() })
	})
	if err := peer.SetRemoteDescription(offer); err != nil {
		fail(w, http.StatusBadRequest, "invalid WebRTC offer")
		return
	}
	gathered := webrtc.GatheringCompletePromise(peer)
	answer, err := peer.CreateAnswer(nil)
	if err != nil {
		fail(w, http.StatusBadRequest, "unsupported WebRTC offer")
		return
	}
	if err := peer.SetLocalDescription(answer); err != nil {
		fail(w, http.StatusInternalServerError, "could not create answer")
		return
	}
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	select {
	case <-gathered:
	case <-r.Context().Done():
		return
	case <-timer.C:
		fail(w, http.StatusServiceUnavailable, "UDP candidates unavailable")
		return
	}
	reply(w, http.StatusOK, peer.LocalDescription())
	success = true
}
