package lansharing

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"infinite-canvas/backend/internal/model"
)

const (
	collaborationStreamTicketTTL = 30 * time.Second
	maxCollaborationPresenceSize = 32 << 10
)

type collaborationHub struct {
	mu      sync.Mutex
	rooms   map[string]map[*collaborationStreamClient]struct{}
	tickets map[string]collaborationStreamTicket
}

type peerCollaborationStreams struct {
	mu       sync.Mutex
	outbound map[string]*peerCollaborationConnection
	incoming map[*websocket.Conn]string
}

type peerCollaborationConnection struct {
	mu       sync.Mutex
	conn     *websocket.Conn
	endpoint string
}

type collaborationPeerFrame struct {
	Type         string          `json:"type"`
	CanvasID     string          `json:"canvasId"`
	OriginNodeID string          `json:"originNodeId,omitempty"`
	UpdateBase64 string          `json:"updateBase64,omitempty"`
	State        json.RawMessage `json:"state,omitempty"`
}

type collaborationStreamTicket struct {
	canvasID string
	expires  time.Time
}

type collaborationStreamClient struct {
	canvasID string
	conn     *websocket.Conn
	send     chan []byte
	done     chan struct{}
	once     sync.Once
}

type collaborationClientFrame struct {
	Type         string          `json:"type"`
	UpdateBase64 string          `json:"updateBase64,omitempty"`
	State        json.RawMessage `json:"state,omitempty"`
}

type collaborationPresenceFrame struct {
	Type      string          `json:"type"`
	CanvasID  string          `json:"canvasId"`
	PeerID    string          `json:"peerId"`
	State     json.RawMessage `json:"state"`
	ExpiresAt time.Time       `json:"expiresAt"`
}

var collaborationStreamUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(*http.Request) bool { return true },
}

func newCollaborationHub() *collaborationHub {
	return &collaborationHub{rooms: make(map[string]map[*collaborationStreamClient]struct{}), tickets: make(map[string]collaborationStreamTicket)}
}

func newPeerCollaborationStreams() *peerCollaborationStreams {
	return &peerCollaborationStreams{outbound: make(map[string]*peerCollaborationConnection), incoming: make(map[*websocket.Conn]string)}
}

func (s *peerCollaborationStreams) outboundFor(peerID string) *peerCollaborationConnection {
	s.mu.Lock()
	defer s.mu.Unlock()
	connection := s.outbound[peerID]
	if connection == nil {
		connection = &peerCollaborationConnection{}
		s.outbound[peerID] = connection
	}
	return connection
}

func (s *peerCollaborationStreams) addIncoming(peerID string, conn *websocket.Conn) {
	s.mu.Lock()
	s.incoming[conn] = peerID
	s.mu.Unlock()
}

func (s *peerCollaborationStreams) removeIncoming(conn *websocket.Conn) {
	s.mu.Lock()
	delete(s.incoming, conn)
	s.mu.Unlock()
}

func (s *peerCollaborationStreams) closePeer(peerID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	outbound := s.outbound[peerID]
	delete(s.outbound, peerID)
	var incoming []*websocket.Conn
	for conn, id := range s.incoming {
		if id == peerID {
			incoming = append(incoming, conn)
			delete(s.incoming, conn)
		}
	}
	s.mu.Unlock()
	if outbound != nil {
		outbound.mu.Lock()
		if outbound.conn != nil {
			_ = outbound.conn.Close()
			outbound.conn = nil
		}
		outbound.mu.Unlock()
	}
	for _, conn := range incoming {
		_ = conn.Close()
	}
}

func (s *peerCollaborationStreams) closeAll() {
	if s == nil {
		return
	}
	s.mu.Lock()
	peers := make([]string, 0, len(s.outbound))
	for peerID := range s.outbound {
		peers = append(peers, peerID)
	}
	incoming := make([]*websocket.Conn, 0, len(s.incoming))
	for conn := range s.incoming {
		incoming = append(incoming, conn)
	}
	s.mu.Unlock()
	for _, peerID := range peers {
		s.closePeer(peerID)
	}
	for _, conn := range incoming {
		_ = conn.Close()
	}
}

func (h *collaborationHub) issueTicket(canvasID string) (string, error) {
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(token[:])
	now := time.Now()
	h.mu.Lock()
	for key, ticket := range h.tickets {
		if !now.Before(ticket.expires) {
			delete(h.tickets, key)
		}
	}
	h.tickets[encoded] = collaborationStreamTicket{canvasID: canvasID, expires: now.Add(collaborationStreamTicketTTL)}
	h.mu.Unlock()
	return encoded, nil
}

func (h *collaborationHub) consumeTicket(canvasID, token string) bool {
	h.mu.Lock()
	ticket, ok := h.tickets[token]
	delete(h.tickets, token)
	h.mu.Unlock()
	return ok && ticket.canvasID == canvasID && time.Now().Before(ticket.expires)
}

func (m *Manager) CreateCollaborationCanvasStreamTicket(ctx context.Context, canvasID string) (string, error) {
	canvasID = strings.TrimSpace(canvasID)
	if !validCollaborationCanvasID(canvasID) {
		return "", ErrInvalid
	}
	var row collaborationCanvasExistence
	if err := m.db.WithContext(ctx).Table("lan_collaboration_canvases").Select("id").Where("id = ? AND deleted_at IS NULL", canvasID).Take(&row).Error; err != nil {
		return "", collaborationRecordError(err)
	}
	return m.collaborationHub.issueTicket(canvasID)
}

type collaborationCanvasExistence struct {
	ID string `gorm:"column:id"`
}

func (m *Manager) HandleCollaborationCanvasStream(w http.ResponseWriter, r *http.Request, canvasID string) {
	canvasID = strings.TrimSpace(canvasID)
	ticket := strings.TrimSpace(r.URL.Query().Get("ticket"))
	if !validCollaborationCanvasID(canvasID) || !m.collaborationHub.consumeTicket(canvasID, ticket) {
		http.Error(w, "collaboration stream ticket is invalid or expired", http.StatusForbidden)
		return
	}
	var row collaborationCanvasExistence
	if err := m.db.WithContext(r.Context()).Table("lan_collaboration_canvases").Select("id").Where("id = ? AND deleted_at IS NULL", canvasID).Take(&row).Error; err != nil {
		http.NotFound(w, r)
		return
	}
	conn, err := collaborationStreamUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	client := &collaborationStreamClient{canvasID: canvasID, conn: conn, send: make(chan []byte, 64), done: make(chan struct{})}
	roomClients := m.collaborationHub.add(client)
	go client.writePump()
	if roomClients == 1 {
		state, _ := json.Marshal(map[string]any{"online": true})
		m.collaborationHub.broadcastPresence(canvasID, m.NodeID(), state)
		m.broadcastCollaborationPresence(r.Context(), canvasID, m.NodeID(), state, "")
	}
	defer func() {
		remaining := m.collaborationHub.remove(client)
		_ = conn.Close()
		if remaining == 0 {
			state, _ := json.Marshal(map[string]any{"online": false})
			m.collaborationHub.broadcastPresence(canvasID, m.NodeID(), state)
			m.broadcastCollaborationPresence(r.Context(), canvasID, m.NodeID(), state, "")
		}
	}()

	conn.SetReadLimit(maxCollaborationCanvasUpdate*2 + maxCollaborationPresenceSize)
	_ = conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(90 * time.Second)) })
	for {
		_, data, readErr := conn.ReadMessage()
		if readErr != nil {
			return
		}
		var frame collaborationClientFrame
		decoder := json.NewDecoder(strings.NewReader(string(data)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&frame); err != nil || !errors.Is(decoder.Decode(new(any)), io.EOF) {
			return
		}
		if frame.Type == "update" {
			update, err := base64.StdEncoding.DecodeString(frame.UpdateBase64)
			if err != nil || len(update) == 0 || len(update) > maxCollaborationCanvasUpdate {
				return
			}
			if _, err := m.AppendCollaborationCanvasUpdate(r.Context(), canvasID, update); err != nil {
				return
			}
			continue
		}
		if frame.Type != "presence" || len(frame.State) == 0 || len(frame.State) > maxCollaborationPresenceSize || !json.Valid(frame.State) {
			return
		}
		m.collaborationHub.broadcastPresence(canvasID, m.NodeID(), frame.State)
		m.broadcastCollaborationPresence(r.Context(), canvasID, m.NodeID(), frame.State, "")
	}
}

func (m *Manager) HandlePeerCollaborationPresence(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxCollaborationPresenceSize+4096)
	var payload struct {
		CanvasID     string          `json:"canvasId"`
		OriginNodeID string          `json:"originNodeId"`
		State        json.RawMessage `json:"state"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil || !errors.Is(decoder.Decode(new(any)), io.EOF) {
		http.Error(w, "invalid collaboration presence", http.StatusBadRequest)
		return
	}
	peerID := requestPeerID(r)
	if !validCollaborationCanvasID(payload.CanvasID) || !validNodeID(peerID) || len(payload.State) == 0 || len(payload.State) > maxCollaborationPresenceSize || !json.Valid(payload.State) {
		http.Error(w, "invalid collaboration presence", http.StatusBadRequest)
		return
	}
	room, err := m.requireCollaborationPeerAccess(r.Context(), payload.CanvasID, peerID)
	if err != nil {
		writePeerError(w, err)
		return
	}
	origin := strings.ToLower(strings.TrimSpace(payload.OriginNodeID))
	if !validNodeID(origin) {
		origin = peerID
	}
	if room.CreatedByNodeID == m.NodeID() && origin != peerID {
		writePeerError(w, ErrPeerNotTrusted)
		return
	}
	m.collaborationHub.broadcastPresence(payload.CanvasID, origin, payload.State)
	if room.CreatedByNodeID == m.NodeID() {
		m.broadcastCollaborationPresence(r.Context(), payload.CanvasID, origin, payload.State, peerID)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"accepted": true})
}

func (m *Manager) handlePeerCollaborationStream(w http.ResponseWriter, r *http.Request) {
	peerID := requestPeerID(r)
	if !m.isTrusted(peerID) {
		http.Error(w, "peer is not trusted", http.StatusForbidden)
		return
	}
	conn, err := collaborationStreamUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	m.peerStreams.addIncoming(peerID, conn)
	defer func() {
		m.peerStreams.removeIncoming(conn)
		_ = conn.Close()
	}()
	conn.SetReadLimit(maxCollaborationCanvasUpdate*2 + maxCollaborationPresenceSize)
	for {
		if !m.isTrusted(peerID) {
			return
		}
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var frame collaborationPeerFrame
		decoder := json.NewDecoder(strings.NewReader(string(data)))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&frame) != nil || !errors.Is(decoder.Decode(new(any)), io.EOF) || !validCollaborationCanvasID(frame.CanvasID) || !m.isTrusted(peerID) {
			return
		}
		switch frame.Type {
		case "update":
			room, err := m.requireCollaborationPeerAccess(r.Context(), frame.CanvasID, peerID)
			if err != nil {
				return
			}
			update, err := base64.StdEncoding.DecodeString(frame.UpdateBase64)
			if err != nil || len(update) == 0 || len(update) > maxCollaborationCanvasUpdate {
				return
			}
			origin := strings.ToLower(strings.TrimSpace(frame.OriginNodeID))
			if !validNodeID(origin) {
				origin = peerID
			}
			if room.CreatedByNodeID == m.NodeID() && origin != peerID {
				return
			}
			accepted, err := m.appendCollaborationCanvasUpdate(r.Context(), frame.CanvasID, update, origin, false)
			if err != nil {
				return
			}
			if accepted {
				m.broadcastCollaborationUpdate(r.Context(), frame.CanvasID, update, origin, peerID)
			}
		case "presence":
			room, err := m.requireCollaborationPeerAccess(r.Context(), frame.CanvasID, peerID)
			if err != nil {
				return
			}
			if len(frame.State) == 0 || len(frame.State) > maxCollaborationPresenceSize || !json.Valid(frame.State) {
				return
			}
			origin := strings.ToLower(strings.TrimSpace(frame.OriginNodeID))
			if !validNodeID(origin) {
				origin = peerID
			}
			if room.CreatedByNodeID == m.NodeID() && origin != peerID {
				return
			}
			m.collaborationHub.broadcastPresence(frame.CanvasID, origin, frame.State)
			if room.CreatedByNodeID == m.NodeID() {
				m.broadcastCollaborationPresence(r.Context(), frame.CanvasID, origin, frame.State, peerID)
			}
		default:
			return
		}
	}
}

func (m *Manager) sendPeerCollaborationFrame(peerID, address string, port int, frame collaborationPeerFrame) error {
	if m == nil || m.peerStreams == nil || !m.Status().Enabled || !validNodeID(peerID) || !m.isTrusted(peerID) {
		return ErrPeerNotTrusted
	}
	endpoint := "wss://" + net.JoinHostPort(address, fmt.Sprint(port)) + "/collaboration/stream"
	encoded, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	connection := m.peerStreams.outboundFor(peerID)
	connection.mu.Lock()
	defer connection.mu.Unlock()
	if !m.Status().Enabled || !m.isTrusted(peerID) {
		return ErrPeerNotTrusted
	}
	if connection.conn != nil && connection.endpoint != endpoint {
		_ = connection.conn.Close()
		connection.conn = nil
	}
	if connection.conn == nil {
		config, err := m.identity.ClientTLSConfig(peerID)
		if err != nil {
			return err
		}
		dialer := websocket.Dialer{TLSClientConfig: config, HandshakeTimeout: 3 * time.Second}
		conn, response, err := dialer.DialContext(m.syncContext, endpoint, nil)
		if err != nil {
			if response != nil {
				_ = response.Body.Close()
			}
			return err
		}
		connection.conn = conn
		connection.endpoint = endpoint
	}
	_ = connection.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := connection.conn.WriteMessage(websocket.TextMessage, encoded); err != nil {
		_ = connection.conn.Close()
		connection.conn = nil
		return err
	}
	return nil
}

func (m *Manager) runCollaborationPresenceHeartbeat(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			state := json.RawMessage(`{"online":true}`)
			for _, canvasID := range m.collaborationHub.activeRooms() {
				m.broadcastCollaborationPresence(ctx, canvasID, m.NodeID(), state, "")
			}
		}
	}
}

func (m *Manager) collaborationTargets(ctx context.Context, canvasID, excludePeerID string) []PeerSummary {
	if !m.Status().Enabled {
		return nil
	}
	var room model.LANCollaborationCanvas
	if err := m.db.WithContext(ctx).First(&room, "id = ?", canvasID).Error; err != nil || room.DeletedAt != nil {
		return nil
	}
	peers, err := m.TrustedPeers(ctx)
	if err != nil {
		return nil
	}
	targets := make([]PeerSummary, 0, len(peers))
	for _, peer := range peers {
		if peer.PeerID == excludePeerID || (room.CreatedByNodeID != m.NodeID() && peer.PeerID != room.CreatedByNodeID) {
			continue
		}
		targets = append(targets, peer)
	}
	return targets
}

type collaborationEndpoint struct {
	address string
	port    int
}

func (m *Manager) collaborationPeerEndpoints(peer PeerSummary) []collaborationEndpoint {
	var discoveredAddresses []string
	port := peer.Port
	for _, discovered := range m.DiscoveredPeers() {
		if discovered.ID != peer.PeerID || len(discovered.Addresses) == 0 {
			continue
		}
		discoveredAddresses = discovered.Addresses
		port = discovered.Port
		break
	}
	var endpoints []collaborationEndpoint
	seen := make(map[string]bool)
	add := func(address string, port int) {
		ip := net.ParseIP(strings.TrimSpace(address))
		if !isPrivateLANAddress(ip) || ip.To4() == nil || port < 1 || port > 65535 {
			return
		}
		key := net.JoinHostPort(ip.String(), fmt.Sprint(port))
		if !seen[key] {
			seen[key] = true
			endpoints = append(endpoints, collaborationEndpoint{ip.String(), port})
		}
	}
	for _, address := range discoveredAddresses {
		if address == peer.Address {
			add(address, port)
			break
		}
	}
	for _, address := range discoveredAddresses {
		add(address, port)
	}
	add(peer.Address, peer.Port)
	if m.peerStreams != nil {
		connection := m.peerStreams.outboundFor(peer.PeerID)
		connection.mu.Lock()
		active := connection.endpoint
		connected := connection.conn != nil
		connection.mu.Unlock()
		if connected {
			for index, endpoint := range endpoints {
				if active == "wss://"+net.JoinHostPort(endpoint.address, fmt.Sprint(endpoint.port))+"/collaboration/stream" {
					endpoints[0], endpoints[index] = endpoints[index], endpoints[0]
					break
				}
			}
		}
	}
	return endpoints
}

func (m *Manager) collaborationPeerEndpoint(peer PeerSummary) (string, int) {
	endpoints := m.collaborationPeerEndpoints(peer)
	if len(endpoints) == 0 {
		return "", 0
	}
	return endpoints[0].address, endpoints[0].port
}

func (m *Manager) broadcastCollaborationPresence(ctx context.Context, canvasID, originNodeID string, state json.RawMessage, excludePeerID string) {
	peers := m.collaborationTargets(ctx, canvasID, excludePeerID)
	payload, err := json.Marshal(map[string]any{"canvasId": canvasID, "originNodeId": originNodeID, "state": state})
	if err != nil {
		return
	}
	for _, peer := range peers {
		endpoints := m.collaborationPeerEndpoints(peer)
		if len(endpoints) == 0 {
			continue
		}
		go func(peerID string, endpoints []collaborationEndpoint) {
			frame := collaborationPeerFrame{Type: "presence", CanvasID: canvasID, OriginNodeID: originNodeID, State: state}
			for _, endpoint := range endpoints {
				if m.sendPeerCollaborationFrame(peerID, endpoint.address, endpoint.port, frame) == nil {
					return
				}
			}
			for _, endpoint := range endpoints {
				if m.postCollaborationPresence(peerID, endpoint.address, endpoint.port, payload) {
					return
				}
			}
		}(peer.PeerID, endpoints)
	}
}

func (m *Manager) broadcastCollaborationUpdate(ctx context.Context, canvasID string, update []byte, originNodeID, excludePeerID string) {
	peers := m.collaborationTargets(ctx, canvasID, excludePeerID)
	payload, err := json.Marshal(map[string]string{"updateBase64": base64.StdEncoding.EncodeToString(update), "originNodeId": originNodeID})
	if err != nil {
		return
	}
	for _, peer := range peers {
		endpoints := m.collaborationPeerEndpoints(peer)
		if len(endpoints) == 0 {
			continue
		}
		go func(peerID string, endpoints []collaborationEndpoint) {
			frame := collaborationPeerFrame{Type: "update", CanvasID: canvasID, OriginNodeID: originNodeID, UpdateBase64: base64.StdEncoding.EncodeToString(update)}
			for _, endpoint := range endpoints {
				if m.sendPeerCollaborationFrame(peerID, endpoint.address, endpoint.port, frame) == nil {
					return
				}
			}
			for _, endpoint := range endpoints {
				if m.postCollaborationUpdate(peerID, endpoint.address, endpoint.port, canvasID, payload) {
					return
				}
			}
		}(peer.PeerID, endpoints)
	}
}

func (m *Manager) postCollaborationUpdate(peerID, address string, port int, canvasID string, payload []byte) bool {
	if !m.Status().Enabled || !m.isTrusted(peerID) {
		return false
	}
	config, err := m.identity.ClientTLSConfig(peerID)
	if err != nil {
		return false
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: config, ForceAttemptHTTP2: false}, Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	endpoint := "https://" + net.JoinHostPort(address, fmt.Sprint(port)) + "/collaboration/canvases/" + url.PathEscape(canvasID) + "/updates"
	req, err := http.NewRequestWithContext(m.syncContext, http.MethodPost, endpoint, strings.NewReader(string(payload)))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return false
	}
	_ = response.Body.Close()
	return response.StatusCode >= 200 && response.StatusCode < 300
}

func (m *Manager) postCollaborationPresence(peerID, address string, port int, payload []byte) bool {
	if !m.Status().Enabled || !m.isTrusted(peerID) {
		return false
	}
	config, err := m.identity.ClientTLSConfig(peerID)
	if err != nil {
		return false
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: config, ForceAttemptHTTP2: false}, Timeout: 2 * time.Second}
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(m.syncContext, http.MethodPost, "https://"+net.JoinHostPort(address, fmt.Sprint(port))+"/collaboration/presence", strings.NewReader(string(payload)))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return false
	}
	_ = response.Body.Close()
	return response.StatusCode >= 200 && response.StatusCode < 300
}

func (h *collaborationHub) add(client *collaborationStreamClient) int {
	h.mu.Lock()
	room := h.rooms[client.canvasID]
	if room == nil {
		room = make(map[*collaborationStreamClient]struct{})
		h.rooms[client.canvasID] = room
	}
	room[client] = struct{}{}
	count := len(room)
	h.mu.Unlock()
	return count
}

func (h *collaborationHub) activeRooms() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	rooms := make([]string, 0, len(h.rooms))
	for canvasID, clients := range h.rooms {
		if len(clients) > 0 {
			rooms = append(rooms, canvasID)
		}
	}
	return rooms
}

func (h *collaborationHub) remove(client *collaborationStreamClient) int {
	h.mu.Lock()
	remaining := 0
	if room := h.rooms[client.canvasID]; room != nil {
		delete(room, client)
		remaining = len(room)
		if len(room) == 0 {
			delete(h.rooms, client.canvasID)
		}
	}
	h.mu.Unlock()
	client.close()
	return remaining
}

func (h *collaborationHub) broadcastUpdate(canvasID, originNodeID, update string) {
	h.broadcast(canvasID, map[string]any{"type": "update", "canvasId": canvasID, "originNodeId": originNodeID, "updateBase64": update})
}

// A graph update may arrive before its asset catalog. Wake only local editors
// of matching rooms after the catalog is durable; polling remains the fallback.
func (m *Manager) notifyCollaborationCatalogUpdate(ctx context.Context, catalogID string) {
	active := m.collaborationHub.activeRooms()
	if len(active) == 0 {
		return
	}
	var rooms []string
	if err := m.db.WithContext(ctx).Model(&model.LANCollaborationCanvas{}).
		Where("asset_catalog_id = ? AND id IN ? AND deleted_at IS NULL", catalogID, active).
		Pluck("id", &rooms).Error; err != nil {
		return
	}
	for _, canvasID := range rooms {
		m.collaborationHub.broadcast(canvasID, map[string]string{"type": "asset-catalog", "canvasId": canvasID, "catalogId": catalogID})
	}
}

func (h *collaborationHub) broadcastPresence(canvasID, peerID string, state json.RawMessage) {
	encoded, err := json.Marshal(collaborationPresenceFrame{
		Type: "presence", CanvasID: canvasID, PeerID: peerID, State: append(json.RawMessage(nil), state...), ExpiresAt: time.Now().UTC().Add(30 * time.Second),
	})
	if err != nil {
		return
	}
	h.broadcastRaw(canvasID, encoded)
}

func (h *collaborationHub) broadcast(canvasID string, message any) {
	encoded, err := json.Marshal(message)
	if err != nil {
		return
	}
	h.broadcastRaw(canvasID, encoded)
}

func (h *collaborationHub) broadcastRaw(canvasID string, encoded []byte) {
	h.mu.Lock()
	clients := make([]*collaborationStreamClient, 0, len(h.rooms[canvasID]))
	for client := range h.rooms[canvasID] {
		clients = append(clients, client)
	}
	h.mu.Unlock()
	for _, client := range clients {
		select {
		case client.send <- append([]byte(nil), encoded...):
		default:
			_ = h.remove(client)
			_ = client.conn.Close()
		}
	}
}

func (h *collaborationHub) close() {
	h.mu.Lock()
	clients := make([]*collaborationStreamClient, 0)
	for _, room := range h.rooms {
		for client := range room {
			clients = append(clients, client)
		}
	}
	h.rooms = make(map[string]map[*collaborationStreamClient]struct{})
	h.tickets = make(map[string]collaborationStreamTicket)
	h.mu.Unlock()
	for _, client := range clients {
		client.close()
		_ = client.conn.Close()
	}
}

func (c *collaborationStreamClient) close() {
	c.once.Do(func() { close(c.done) })
}

func (c *collaborationStreamClient) writePump() {
	ticker := time.NewTicker(30 * time.Second)
	defer func() {
		ticker.Stop()
		_ = c.conn.Close()
	}()
	for {
		select {
		case message := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				c.close()
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				c.close()
				return
			}
		case <-c.done:
			return
		}
	}
}
