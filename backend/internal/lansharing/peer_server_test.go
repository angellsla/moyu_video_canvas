package lansharing

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"
)

func TestPeerServerListensOnAllPrivateLANInterfaces(t *testing.T) {
	available, err := FindPrivateEndpoints()
	interfaceIndexes := make(map[int]bool)
	for _, endpoint := range available {
		interfaceIndexes[endpoint.Interface.Index] = true
	}
	if err != nil || len(interfaceIndexes) < 2 {
		t.Skipf("multi-interface private LAN test needs at least two eligible adapters: %v", err)
	}
	identity, err := LoadIdentity(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	clientTLS, err := identity.ClientTLSConfig(identity.ID(), Fingerprint(identity.PublicKey()))
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewPeerServer(identity, func(peerID string, key ed25519.PublicKey) bool {
		return peerID == identity.ID() && key.Equal(identity.PublicKey())
	}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Stop(context.Background()) })
	endpoints, port, err := server.Start()
	if err != nil {
		t.Fatal(err)
	}
	if port < 1 || len(endpoints) != len(available) {
		t.Fatalf("listener port = %d, endpoints = %#v, want %d private addresses", port, endpoints, len(available))
	}
	statusEndpoints, advertised := server.EndpointStatus()
	if len(statusEndpoints) != len(endpoints) {
		t.Fatalf("reported endpoints = %v, want all %d listeners", statusEndpoints, len(endpoints))
	}
	t.Logf("LAN listeners: %v; mDNS advertisements: %v", statusEndpoints, advertised)
	checkConnections := func() {
		t.Helper()
		for _, endpoint := range endpoints {
			address := net.JoinHostPort(endpoint.Address.String(), strconv.Itoa(port))
			conn, dialErr := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp4", address, clientTLS)
			if dialErr != nil {
				t.Errorf("connect to %s: %v", address, dialErr)
				continue
			}
			_ = conn.Close()
		}
	}
	checkConnections()
	if err := server.SetNickname("Multi-NIC Studio"); err != nil {
		t.Logf("mDNS re-advertisement unavailable on one or more adapters: %v", err)
	}
	checkConnections()
	if err := server.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	statusEndpoints, advertised = server.EndpointStatus()
	if len(statusEndpoints) != 0 || len(advertised) != 0 {
		t.Fatalf("stopped server still reports endpoints=%v advertised=%v", statusEndpoints, advertised)
	}
	for _, endpoint := range endpoints {
		address := net.JoinHostPort(endpoint.Address.String(), strconv.Itoa(port))
		conn, dialErr := net.DialTimeout("tcp4", address, 250*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			t.Errorf("listener on %s remained reachable after stop", address)
		}
	}
}

func TestListenPrivateEndpointsKeepsAvailableAdapters(t *testing.T) {
	endpoints := []PrivateEndpoint{
		{Address: net.ParseIP("127.0.0.1")},
		{Address: net.ParseIP("127.0.0.2")},
	}
	bound, listeners, port, err := listenPrivateEndpoints(endpoints, func(network string, address *net.TCPAddr) (*net.TCPListener, error) {
		if address.IP.Equal(endpoints[1].Address) {
			return nil, errors.New("adapter unavailable")
		}
		return net.ListenTCP(network, address)
	})
	if err != nil || len(bound) != 1 || len(listeners) != 1 || port < 1 || !bound[0].Address.Equal(endpoints[0].Address) {
		t.Fatalf("bound=%v, listener count=%d, port=%d, err=%v", bound, len(listeners), port, err)
	}
	for _, listener := range listeners {
		_ = listener.Close()
	}
	if _, _, _, err := listenPrivateEndpoints(endpoints, func(string, *net.TCPAddr) (*net.TCPListener, error) {
		return nil, errors.New("adapter unavailable")
	}); err == nil {
		t.Fatal("all failed adapters should prevent startup")
	}
}
