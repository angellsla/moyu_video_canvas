package lansharing

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/grandcat/zeroconf"
)

type PeerServer struct {
	identity  *Identity
	isTrusted func(string) bool
	handler   http.Handler
	mu        sync.Mutex
	listeners []net.Listener
	server    *http.Server
	mdns      map[string]*zeroconf.Server
	endpoints []PrivateEndpoint
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

func (s *PeerServer) Start() ([]PrivateEndpoint, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.listeners) > 0 {
		return append([]PrivateEndpoint(nil), s.endpoints...), s.port, nil
	}
	endpoints, err := FindPrivateEndpoints()
	if err != nil {
		return nil, 0, err
	}
	tlsConfig, err := s.identity.ServerTLSConfig(s.isTrusted)
	if err != nil {
		return nil, 0, err
	}
	boundEndpoints, rawListeners, port, err := listenPrivateEndpoints(endpoints, net.ListenTCP)
	if err != nil {
		return nil, 0, err
	}
	listeners := make([]net.Listener, 0, len(rawListeners))
	for _, listener := range rawListeners {
		listeners = append(listeners, tls.NewListener(listener, tlsConfig))
	}
	server := &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
	advertisements := make(map[string]*zeroconf.Server)
	for _, endpoint := range boundEndpoints {
		if endpoint.Interface.Flags&net.FlagMulticast == 0 {
			continue
		}
		advertisement, advertiseErr := Advertise(s.identity, endpoint, port, s.nickname)
		if advertiseErr == nil {
			advertisements[endpoint.Address.String()] = advertisement
		}
	}
	s.listeners = listeners
	s.server = server
	s.mdns = advertisements
	s.endpoints = boundEndpoints
	s.port = port
	for _, listener := range listeners {
		go func(listener net.Listener) {
			err := server.Serve(listener)
			if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
				err = nil
			}
			select {
			case s.serveErr <- err:
			default:
			}
		}(listener)
	}
	return append([]PrivateEndpoint(nil), boundEndpoints...), port, nil
}

func listenPrivateEndpoints(endpoints []PrivateEndpoint, listen func(string, *net.TCPAddr) (*net.TCPListener, error)) ([]PrivateEndpoint, []net.Listener, int, error) {
	var bound []PrivateEndpoint
	var listeners []net.Listener
	var failures []error
	port := 0
	for _, endpoint := range endpoints {
		listener, err := listen("tcp4", &net.TCPAddr{IP: endpoint.Address.To4(), Port: port})
		if err != nil {
			failures = append(failures, fmt.Errorf("listen on %s: %w", endpoint.Address, err))
			continue
		}
		port = listener.Addr().(*net.TCPAddr).Port
		bound = append(bound, endpoint)
		listeners = append(listeners, listener)
	}
	if len(listeners) == 0 {
		if len(failures) == 0 {
			return nil, nil, 0, errors.New("no private LAN endpoints to listen on")
		}
		return nil, nil, 0, errors.Join(failures...)
	}
	return bound, listeners, port, nil
}

// SetNickname updates the discovery name without restarting the TLS listener.
func (s *PeerServer) SetNickname(nickname string) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nickname = normalizeNickname(nickname)
	if len(s.listeners) == 0 {
		return nil
	}
	for _, advertisement := range s.mdns {
		advertisement.Shutdown()
	}
	s.mdns = make(map[string]*zeroconf.Server)
	var failures []error
	for _, endpoint := range s.endpoints {
		if endpoint.Interface.Flags&net.FlagMulticast == 0 {
			continue
		}
		advertisement, err := Advertise(s.identity, endpoint, s.port, s.nickname)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		s.mdns[endpoint.Address.String()] = advertisement
	}
	return errors.Join(failures...)
}

func (s *PeerServer) EndpointStatus() ([]string, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	endpoints := make([]string, 0, len(s.endpoints))
	advertised := make([]string, 0, len(s.mdns))
	for _, endpoint := range s.endpoints {
		address := net.JoinHostPort(endpoint.Address.String(), strconv.Itoa(s.port))
		endpoints = append(endpoints, address)
		if s.mdns[endpoint.Address.String()] != nil {
			advertised = append(advertised, address)
		}
	}
	return endpoints, advertised
}

func (s *PeerServer) Stop(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	server := s.server
	mdnsServers := s.mdns
	s.server = nil
	s.listeners = nil
	s.mdns = nil
	s.endpoints = nil
	s.port = 0
	s.mu.Unlock()
	for _, advertisement := range mdnsServers {
		advertisement.Shutdown()
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
