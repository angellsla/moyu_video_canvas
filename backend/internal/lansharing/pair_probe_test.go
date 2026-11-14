package lansharing

import (
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPairProbeResponseRequiresActiveCodeAndPrivateSource(t *testing.T) {
	peerID := strings.Repeat("a", 64)
	nonce := []byte("unique-probe-123")
	request, err := json.Marshal(pairProbeRequest{
		Kind: pairProbeRequestKind, Service: serviceType, Version: protocol,
		Nonce: base64.RawURLEncoding.EncodeToString(nonce),
	})
	if err != nil {
		t.Fatal(err)
	}
	source := &net.UDPAddr{IP: net.ParseIP("192.168.1.20"), Port: 41000}
	packet := pairProbeResponse(request, source, peerID, 42123, "ONE-TIME")
	var reply pairProbeReply
	if err := json.Unmarshal(packet, &reply); err != nil {
		t.Fatalf("decode probe response: %v", err)
	}
	proof, err := base64.RawURLEncoding.DecodeString(reply.Proof)
	if err != nil || reply.PeerID != peerID || reply.Port != 42123 || reply.Nonce != base64.RawURLEncoding.EncodeToString(nonce) || !hmac.Equal(proof, pairProbeProof("ONE-TIME", nonce, peerID, reply.Port, "")) {
		t.Fatalf("invalid authenticated probe response: %#v, err=%v", reply, err)
	}
	if hmac.Equal(proof, pairProbeProof("ONE-TIME", nonce, peerID, 42124, "")) {
		t.Fatal("proof must bind the TLS port")
	}
	if hmac.Equal(proof, pairProbeProof("WRONG", nonce, peerID, reply.Port, "")) {
		t.Fatal("proof must bind the pairing code")
	}
	for _, test := range []struct {
		name   string
		packet []byte
		source *net.UDPAddr
		code   string
	}{
		{name: "no active code", packet: request, source: source},
		{name: "public source", packet: request, source: &net.UDPAddr{IP: net.ParseIP("8.8.8.8")}, code: "ONE-TIME"},
		{name: "malformed nonce", packet: []byte(`{"kind":"pair-probe","service":"_beeftv-share._tcp","version":"1","nonce":"bad"}`), source: source, code: "ONE-TIME"},
		{name: "oversize", packet: append(request, make([]byte, broadcastPacketLimit)...), source: source, code: "ONE-TIME"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if response := pairProbeResponse(test.packet, test.source, peerID, 42123, test.code); response != nil {
				t.Fatalf("unexpected probe response: %s", response)
			}
		})
	}
}

func TestDirectPairProbeRejectsProofForDifferentTLSIdentity(t *testing.T) {
	_, serverKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, clientKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, otherKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serverIdentity := &Identity{privateKey: serverKey}
	clientIdentity := &Identity{privateKey: clientKey}
	otherIdentity := &Identity{privateKey: otherKey}
	certificate, err := serverIdentity.TLSCertificate()
	if err != nil {
		t.Fatal(err)
	}
	port := 0
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nonce, ok := decodePairProbeNonce(r.URL.Query().Get("nonce"))
		if !ok {
			http.Error(w, "invalid nonce", http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, newPairProbeReply(nonce, otherIdentity.ID(), port, "ONE-TIME", Fingerprint(otherIdentity.PublicKey())))
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, ClientAuth: tls.RequireAnyClientCert}
	server.StartTLS()
	defer server.Close()
	port = server.Listener.Addr().(*net.TCPAddr).Port
	if _, _, err := queryPairEndpointTLS(context.Background(), clientIdentity, net.IPv4(127, 0, 0, 1), port, "ONE-TIME"); !errors.Is(err, ErrConflict) {
		t.Fatalf("proof for another TLS identity error = %v", err)
	}
}

func TestQueryPairEndpointVerifiesCodeAndNonce(t *testing.T) {
	server, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	peerID := strings.Repeat("b", 64)
	done := make(chan struct{})
	go func() {
		defer close(done)
		packet := make([]byte, broadcastPacketLimit+1)
		for {
			length, source, err := server.ReadFromUDP(packet)
			if err != nil {
				return
			}
			response := pairProbeResponse(packet[:length], &net.UDPAddr{IP: net.ParseIP("192.168.1.20")}, peerID, 42123, "ONE-TIME")
			if response != nil {
				_, _ = server.WriteToUDP(response, source)
			}
		}
	}()
	defer func() { _ = server.Close(); <-done }()
	port := server.LocalAddr().(*net.UDPAddr).Port
	resolvedID, resolvedPort, err := queryPairEndpoint(context.Background(), net.IPv4(127, 0, 0, 1), port, "ONE-TIME")
	if err != nil || resolvedID != peerID || resolvedPort != 42123 {
		t.Fatalf("resolved peer = %s:%d, err=%v", resolvedID, resolvedPort, err)
	}
	if _, _, err := queryPairEndpoint(context.Background(), net.IPv4(127, 0, 0, 1), port, "WRONG"); !errors.Is(err, ErrPairingCode) {
		t.Fatalf("incorrect pairing code error = %v", err)
	}
}

func TestQueryPairEndpointRejectsReplayAndStopsOnCancellation(t *testing.T) {
	server, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	peerID := strings.Repeat("c", 64)
	staleRequest, err := json.Marshal(pairProbeRequest{
		Kind: pairProbeRequestKind, Service: serviceType, Version: protocol,
		Nonce: base64.RawURLEncoding.EncodeToString(make([]byte, 16)),
	})
	if err != nil {
		t.Fatal(err)
	}
	staleResponse := pairProbeResponse(staleRequest, &net.UDPAddr{IP: net.ParseIP("192.168.1.20")}, peerID, 42123, "ONE-TIME")
	done := make(chan struct{})
	replied := make(chan struct{}, 1)
	go func() {
		defer close(done)
		packet := make([]byte, broadcastPacketLimit+1)
		for {
			_, source, err := server.ReadFromUDP(packet)
			if err != nil {
				return
			}
			_, _ = server.WriteToUDP(staleResponse, source)
			select {
			case replied <- struct{}{}:
			default:
			}
		}
	}()
	defer func() { _ = server.Close(); <-done }()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if peer, port, err := queryPairEndpoint(ctx, net.IPv4(127, 0, 0, 1), server.LocalAddr().(*net.UDPAddr).Port, "ONE-TIME"); !errors.Is(err, context.DeadlineExceeded) || peer != "" || port != 0 {
		t.Fatalf("replayed proof accepted: peer=%s port=%d err=%v", peer, port, err)
	}
	select {
	case <-replied:
	default:
		t.Fatal("test did not deliver a replayed proof")
	}
}
