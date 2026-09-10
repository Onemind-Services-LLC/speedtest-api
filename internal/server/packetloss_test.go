package server

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

func packetTestServer(t *testing.T) *Server {
	t.Helper()
	config := DefaultConfig()
	config.WebRTCAddress = "127.0.0.1:0"
	config.MaxWebRTC = 1
	app, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.StartPacketLoss(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.ClosePacketLoss)
	return app
}

func packetOffer(t *testing.T, app *Server, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/v1/packet-loss", bytes.NewReader(body))
	request.Header.Set("Origin", "http://localhost:3000")
	recorder := httptest.NewRecorder()
	app.ServeHTTP(recorder, request)
	return recorder
}

func TestPacketLossOfferValidation(t *testing.T) {
	app := packetTestServer(t)
	for _, body := range []string{`{}`, `{"type":"answer","sdp":"bad"}`, `{"type":"offer","sdp":"bad"}`, `{"type":"offer","sdp":"bad"} {}`, strings.Repeat("x", 33000)} {
		if got := packetOffer(t, app, []byte(body)).Code; got != 400 {
			t.Fatalf("invalid offer status %d", got)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/packet-loss", strings.NewReader(`{}`))
	recorder := httptest.NewRecorder()
	app.ServeHTTP(recorder, req)
	if recorder.Code != 403 {
		t.Fatalf("origin-free offer accepted: %d", recorder.Code)
	}
	app.ClosePacketLoss()
	app.packetLoss.mu.Lock()
	defer app.packetLoss.mu.Unlock()
	if len(app.packetLoss.peers) != 0 || len(app.packetLoss.perIP) != 0 {
		t.Fatal("invalid offers leaked sessions")
	}
}

func TestPacketLossEchoAndCapacity(t *testing.T) {
	app := packetTestServer(t)
	settings := webrtc.SettingEngine{}
	settings.SetIncludeLoopbackCandidate(true)
	client, err := webrtc.NewAPI(webrtc.WithSettingEngine(settings)).NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	retries := uint16(0)
	ordered := false
	channel, err := client.CreateDataChannel("packet-loss-v1", &webrtc.DataChannelInit{Ordered: &ordered, MaxRetransmits: &retries})
	if err != nil {
		t.Fatal(err)
	}
	messages := make(chan []byte, 1)
	channel.OnMessage(func(message webrtc.DataChannelMessage) { messages <- message.Data })
	payload := make([]byte, 64)
	binary.BigEndian.PutUint32(payload, 17)
	channel.OnOpen(func() {
		if err := channel.Send(payload); err != nil {
			t.Error(err)
		}
	})
	gather := webrtc.GatheringCompletePromise(client)
	offer, err := client.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	select {
	case <-gather:
	case <-time.After(5 * time.Second):
		t.Fatal("client gather timed out")
	}
	body, _ := json.Marshal(client.LocalDescription())
	response := packetOffer(t, app, body)
	if response.Code != 200 {
		t.Fatalf("offer: %d %s", response.Code, response.Body.String())
	}
	busy := packetOffer(t, app, body)
	if busy.Code != 503 || busy.Header().Get("Retry-After") == "" {
		t.Fatal("capacity limit not enforced")
	}
	var answer webrtc.SessionDescription
	if err := json.Unmarshal(response.Body.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}
	if err := client.SetRemoteDescription(answer); err != nil {
		t.Fatal(err)
	}
	select {
	case echo := <-messages:
		if !bytes.Equal(payload, echo) {
			t.Fatal("echo changed payload")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("UDP echo timed out")
	}
	invalid := make([]byte, 64)
	binary.BigEndian.PutUint32(invalid, 1000)
	if err := channel.Send(invalid); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		app.packetLoss.mu.Lock()
		active := len(app.packetLoss.peers)
		app.packetLoss.mu.Unlock()
		if active == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("out-of-range sequence did not close the session")
		}
		time.Sleep(10 * time.Millisecond)
	}
	app.ClosePacketLoss()
	app.packetLoss.mu.Lock()
	defer app.packetLoss.mu.Unlock()
	if len(app.packetLoss.peers) != 0 || len(app.packetLoss.perIP) != 0 {
		t.Fatal("shutdown leaked sessions")
	}
}

func TestPacketLossDisabled(t *testing.T) {
	config := DefaultConfig()
	config.WebRTCAddress = ""
	app, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.StartPacketLoss(); err != nil {
		t.Fatal(err)
	}
	if got := packetOffer(t, app, []byte(`{}`)).Code; got != 501 {
		t.Fatalf("disabled status %d", got)
	}
	app.ClosePacketLoss()
}
