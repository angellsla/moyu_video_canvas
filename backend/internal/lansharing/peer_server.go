package lansharing

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/grandcat/zeroconf"
)

type PeerServer struct {
	identity  *Identity
	isTrusted func(string) bool
	handler   http.Handler
	mu        sync.Mutex
	listener  net.Listener
	server    *http.Server
	mdns      *zeroconf.Server
	endpoint  PrivateEndpoint
	port      int
	nickname  string
	serveErr  chan error
}

func NewPeerServer(identity *Identity, isTrusted func(string) bool, handler http.Handler) (*PeerServer, error) {
	if identity == nil || identity.ID() == "" || isTrusted == nil || handler == nil {
		return nil, errors.New("LAN peer server dependencies are incomplete")
	}
	return &PeerServer{identity: identity, isTrusted: isTrusted, handler: handler, serveErr: make(chan error, 1)}, nil
}

func (s *PeerServer) Start() (PrivateEndpoint, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener != nil {
		addr := s.listener.Addr().(*net.TCPAddr)
		endpoint, err := endpointFromTCPAddr(addr)
		return endpoint, addr.Port, err
	}
	endpoint, err := FindPrivateEndpoint()
	if err != nil {
		return PrivateEndpoint{}, 0, err
	}
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: endpoint.Address.To4(), Port: 0})
	if err != nil {
		return PrivateEndpoint{}, 0, err
	}
	tlsConfig, err := s.identity.ServerTLSConfig(s.isTrusted)
	if err != nil {
		_ = listener.Close()
		return PrivateEndpoint{}, 0, err
	}
	secureListener := tls.NewListener(listener, tlsConfig)
	server := &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
	port := listener.Addr().(*net.TCPAddr).Port
	mdnsServer, err := Advertise(s.identity, endpoint, port, s.nickname)
	if err != nil {
		mdnsServer = nil
	}
	s.listener = secureListener
	s.server = server
	s.mdns = mdnsServer
	s.endpoint = endpoint
	s.port = port
	go func() {
		err := server.Serve(secureListener)
		if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
			err = nil
		}
		select {
		case s.serveErr <- err:
		default:
		}
	}()
	return endpoint, port, nil
}

// SetNickname updates the discovery name without restarting the TLS listener.
func (s *PeerServer) SetNickname(nickname string) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nickname = normalizeNickname(nickname)
	if s.listener == nil {
		return nil
	}
	if s.mdns != nil {
		s.mdns.Shutdown()
		s.mdns = nil
	}
	mdnsServer, err := Advertise(s.identity, s.endpoint, s.port, s.nickname)
	if err != nil {
		return err
	}
	s.mdns = mdnsServer
	return nil
}

func endpointFromTCPAddr(address *net.TCPAddr) (PrivateEndpoint, error) {
	if address == nil || !isPrivateLANAddress(address.IP) {
		return PrivateEndpoint{}, errors.New("LAN peer listener is not bound to a private interface")
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		return PrivateEndpoint{}, err
	}
	for _, iface := range interfaces {
		addresses, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, candidate := range addresses {
			ip, _, err := net.ParseCIDR(candidate.String())
			if err == nil && ip.Equal(address.IP) {
				return PrivateEndpoint{Interface: iface, Address: ip}, nil
			}
		}
	}
	return PrivateEndpoint{}, errors.New("LAN peer listener interface could not be resolved")
}

func (s *PeerServer) Stop(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	server := s.server
	mdnsServer := s.mdns
	s.server = nil
	s.listener = nil
	s.mdns = nil
	s.endpoint = PrivateEndpoint{}
	s.port = 0
	s.mu.Unlock()
	if mdnsServer != nil {
		mdnsServer.Shutdown()
	}
	if server == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return server.Shutdown(ctx)
}

func (s *PeerServer) Errors() <-chan error {
	if s == nil {
		closed := make(chan error)
		close(closed)
		return closed
	}
	return s.serveErr
}
