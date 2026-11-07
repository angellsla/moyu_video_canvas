package lansharing

import (
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

const (
	pairProbeRequestKind = "pair-probe"
	pairProbeReplyKind   = "pair-proof"
	pairProbeAttempts    = 3
	pairProbeWait        = 650 * time.Millisecond
)

type pairProbeRequest struct {
	Kind    string `json:"kind"`
	Service string `json:"service"`
	Version string `json:"version"`
	Nonce   string `json:"nonce"`
}

type pairProbeReply struct {
	Kind    string `json:"kind"`
	Service string `json:"service"`
	Version string `json:"version"`
	Nonce   string `json:"nonce"`
	PeerID  string `json:"peerId"`
	Port    int    `json:"port"`
	Proof   string `json:"proof"`
}

func pairProbeProof(code string, nonce []byte, peerID string, port int) []byte {
	mac := hmac.New(sha256.New, []byte(code))
	_, _ = mac.Write([]byte("beeftv-lan-pair-v1\x00"))
	_, _ = mac.Write(nonce)
	_, _ = mac.Write([]byte(peerID))
	var encodedPort [2]byte
	binary.BigEndian.PutUint16(encodedPort[:], uint16(port))
	_, _ = mac.Write(encodedPort[:])
	return mac.Sum(nil)
}

func decodePairProbeNonce(encoded string) ([]byte, bool) {
	nonce, err := base64.RawURLEncoding.DecodeString(encoded)
	return nonce, err == nil && len(nonce) == 16 && base64.RawURLEncoding.EncodeToString(nonce) == encoded
}

func newPairProbeReply(nonce []byte, peerID string, port int, code string) pairProbeReply {
	return pairProbeReply{
		Kind: pairProbeReplyKind, Service: serviceType, Version: protocol,
		Nonce: base64.RawURLEncoding.EncodeToString(nonce), PeerID: peerID, Port: port,
		Proof: base64.RawURLEncoding.EncodeToString(pairProbeProof(code, nonce, peerID, port)),
	}
}

func verifyPairProbeReply(packet []byte, nonce []byte, code string) (string, int, error) {
	if len(packet) == 0 || len(packet) > broadcastPacketLimit {
		return "", 0, ErrInvalid
	}
	var reply pairProbeReply
	if err := json.Unmarshal(packet, &reply); err != nil || reply.Kind != pairProbeReplyKind || reply.Service != serviceType || reply.Version != protocol || reply.Nonce != base64.RawURLEncoding.EncodeToString(nonce) || !validNodeID(reply.PeerID) || reply.Port < 1 || reply.Port > 65535 {
		return "", 0, ErrInvalid
	}
	proof, err := base64.RawURLEncoding.DecodeString(reply.Proof)
	if err != nil || !hmac.Equal(proof, pairProbeProof(code, nonce, reply.PeerID, reply.Port)) {
		return "", 0, ErrPairingCode
	}
	return reply.PeerID, reply.Port, nil
}

func pairProbeResponse(packet []byte, source *net.UDPAddr, peerID string, port int, code string) []byte {
	if len(packet) == 0 || len(packet) > broadcastPacketLimit || source == nil || !isPrivateLANAddress(source.IP) || code == "" || !validNodeID(peerID) || port < 1 || port > 65535 {
		return nil
	}
	var request pairProbeRequest
	if err := json.Unmarshal(packet, &request); err != nil || request.Kind != pairProbeRequestKind || request.Service != serviceType || request.Version != protocol {
		return nil
	}
	nonce, ok := decodePairProbeNonce(request.Nonce)
	if !ok {
		return nil
	}
	reply, err := json.Marshal(newPairProbeReply(nonce, peerID, port, code))
	if err != nil || len(reply) > broadcastPacketLimit {
		return nil
	}
	return reply
}

func queryPairEndpoint(ctx context.Context, address net.IP, discoveryPort int, code string) (string, int, error) {
	if ctx == nil || address == nil || address.To4() == nil || discoveryPort < 1 || discoveryPort > 65535 || code == "" {
		return "", 0, ErrInvalid
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", 0, err
	}
	request, err := json.Marshal(pairProbeRequest{
		Kind: pairProbeRequestKind, Service: serviceType, Version: protocol,
		Nonce: base64.RawURLEncoding.EncodeToString(nonce[:]),
	})
	if err != nil {
		return "", 0, err
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "udp4", net.JoinHostPort(address.String(), fmt.Sprint(discoveryPort)))
	if err != nil {
		return "", 0, fmt.Errorf("%w: %v", ErrPeerUnreachable, err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.SetReadDeadline(time.Now()) })
	defer stop()
	invalidCode := false
	packet := make([]byte, broadcastPacketLimit+1)
	for attempt := 0; attempt < pairProbeAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", 0, err
		}
		if _, err := conn.Write(request); err != nil {
			return "", 0, fmt.Errorf("%w: %v", ErrPeerUnreachable, err)
		}
		deadline := time.Now().Add(pairProbeWait)
		if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
			deadline = ctxDeadline
		}
		for time.Now().Before(deadline) {
			if err := ctx.Err(); err != nil {
				return "", 0, err
			}
			_ = conn.SetReadDeadline(deadline)
			length, err := conn.Read(packet)
			if err != nil {
				var netErr net.Error
				if errors.As(err, &netErr) && netErr.Timeout() {
					break
				}
				if ctx.Err() != nil {
					return "", 0, ctx.Err()
				}
				return "", 0, fmt.Errorf("%w: %v", ErrPeerUnreachable, err)
			}
			if length > broadcastPacketLimit {
				continue
			}
			peerID, port, err := verifyPairProbeReply(packet[:length], nonce[:], code)
			if errors.Is(err, ErrInvalid) {
				continue
			}
			if errors.Is(err, ErrPairingCode) {
				invalidCode = true
				continue
			}
			return peerID, port, nil
		}
	}
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	if invalidCode {
		return "", 0, ErrPairingCode
	}
	return "", 0, ErrPeerUnreachable
}

// The optional TCP port is probed without sending the code. A code-bound proof
// must match the certificate seen on that TLS connection before /pair is sent.
func queryPairEndpointTLS(ctx context.Context, identity *Identity, address net.IP, port int, code string) (string, error) {
	if ctx == nil || identity == nil || address == nil || address.To4() == nil || port < 1 || port > 65535 || code == "" {
		return "", ErrInvalid
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	certificate, err := identity.TLSCertificate()
	if err != nil {
		return "", err
	}
	var certificateID string
	transport := &http.Transport{TLSClientConfig: &tls.Config{
		MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{certificate}, InsecureSkipVerify: true,
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) != 1 {
				return ErrConflict
			}
			publicKey, ok := state.PeerCertificates[0].PublicKey.(ed25519.PublicKey)
			if !ok {
				return ErrConflict
			}
			certificateID = Fingerprint(publicKey)
			return nil
		},
	}, ForceAttemptHTTP2: false}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	endpoint := "https://" + net.JoinHostPort(address.String(), fmt.Sprint(port)) + "/pair/probe?nonce=" + url.QueryEscape(base64.RawURLEncoding.EncodeToString(nonce[:]))
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("%w: %v", ErrPeerUnreachable, err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusForbidden {
		return "", ErrPairingCode
	}
	if response.StatusCode != http.StatusOK {
		return "", ErrPeerUnreachable
	}
	packet, err := io.ReadAll(io.LimitReader(response.Body, broadcastPacketLimit+1))
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrPeerUnreachable, err)
	}
	peerID, proofPort, err := verifyPairProbeReply(packet, nonce[:], code)
	if errors.Is(err, ErrInvalid) {
		return "", ErrPeerUnreachable
	}
	if err != nil {
		return "", err
	}
	if peerID != certificateID || proofPort != port {
		return "", ErrConflict
	}
	return peerID, nil
}
