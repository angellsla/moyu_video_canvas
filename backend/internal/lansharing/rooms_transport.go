package lansharing

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"infinite-canvas/backend/internal/kernel"
	"infinite-canvas/backend/internal/model"
)

type RoomLocation struct {
	ID      string `json:"id"`
	Code    string `json:"code"`
	Name    string `json:"name"`
	PeerID  string `json:"peerId"`
	Address string `json:"address"`
	Port    int    `json:"port"`
}
type RoomJoinInput struct {
	RoomID  string `json:"roomId"`
	PeerID  string `json:"peerId"`
	Address string `json:"address"`
	Port    int    `json:"port"`
}
type roomJoinWire struct {
	ID       string `json:"id"`
	RoomID   string `json:"roomId"`
	Nickname string `json:"nickname"`
	Port     int    `json:"port"`
	Proof    string `json:"proof"`
}

func joinProofBytes(join roomJoinWire) []byte {
	join.Proof = ""
	b, _ := json.Marshal(join)
	return b
}
func (m *Manager) roomClient(peerID, address string, port int, key string) (*http.Client, string, error) {
	if !validNodeID(peerID) || !validPeerEndpoint(address, port) {
		return nil, "", ErrInvalid
	}
	certificate, err := m.identity.TLSCertificate()
	if err != nil {
		return nil, "", err
	}
	config := &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{certificate}, InsecureSkipVerify: true}
	config.VerifyConnection = func(state tls.ConnectionState) error {
		if len(state.PeerCertificates) != 1 || certificateNodeID(state.PeerCertificates[0]) != peerID {
			return ErrPeerNotTrusted
		}
		actual, ok := state.PeerCertificates[0].PublicKey.(ed25519.PublicKey)
		if !ok || (key != "" && base64.RawStdEncoding.EncodeToString(actual) != key) {
			return ErrPeerNotTrusted
		}
		return nil
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: config, TLSHandshakeTimeout: 3 * time.Second}, Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return client, "https://" + net.JoinHostPort(address, strconv.Itoa(port)), nil
}
func (m *Manager) DiscoverRooms(ctx context.Context, code string) ([]RoomLocation, error) {
	code = strings.TrimSpace(code)
	if code != "" && !validRoomCode(code) {
		return nil, ErrInvalid
	}
	result := []RoomLocation{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	slots := make(chan struct{}, 4)
	peers := m.DiscoveredPeers()
	if len(peers) > 64 {
		peers = peers[:64]
	}
	for _, peer := range peers {
		peer := peer
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-slots }()
			for _, address := range peer.Addresses {
				client, base, err := m.roomClient(peer.ID, address, peer.Port, "")
				if err != nil {
					continue
				}
				var directory struct {
					Rooms []RoomLocation `json:"rooms"`
				}
				err = requestPeerJSON(ctx, client, http.MethodGet, base+"/rooms/directory", nil, &directory)
				client.CloseIdleConnections()
				if err != nil {
					continue
				}
				mu.Lock()
				for _, room := range directory.Rooms {
					if validNodeID(room.ID) && validRoomCode(room.Code) && (code == "" || code == room.Code) {
						room.PeerID, room.Address, room.Port = peer.ID, address, peer.Port
						result = append(result, room)
					}
				}
				mu.Unlock()
				break
			}
		}()
	}
	wg.Wait()
	result = sortRoomLocations(result)
	return result, ctx.Err()
}
func sortRoomLocations(rooms []RoomLocation) []RoomLocation {
	// Stable display even when adapters reply in different orders.
	for i := 1; i < len(rooms); i++ {
		for j := i; j > 0 && (rooms[j].ID+rooms[j].PeerID) < (rooms[j-1].ID+rooms[j-1].PeerID); j-- {
			rooms[j], rooms[j-1] = rooms[j-1], rooms[j]
		}
	}
	seen := make(map[string]struct{}, len(rooms))
	unique := rooms[:0]
	for _, room := range rooms {
		if _, exists := seen[room.ID]; exists {
			continue
		}
		seen[room.ID] = struct{}{}
		unique = append(unique, room)
	}
	clear(rooms[len(unique):])
	return unique
}
func (m *Manager) RequestRoomJoin(ctx context.Context, input RoomJoinInput) (model.LANRoomJoin, error) {
	if !validNodeID(input.RoomID) {
		return model.LANRoomJoin{}, ErrInvalid
	}
	if !m.Status().Enabled {
		return model.LANRoomJoin{}, ErrManagerClosed
	}
	client, base, err := m.roomClient(input.PeerID, input.Address, input.Port, "")
	if err != nil {
		return model.LANRoomJoin{}, err
	}
	defer client.CloseIdleConnections()
	_, rawPort, err := net.SplitHostPort(m.Status().Endpoint)
	if err != nil {
		return model.LANRoomJoin{}, err
	}
	port, _ := strconv.Atoi(rawPort)
	wire := roomJoinWire{ID: kernel.NewID(), RoomID: input.RoomID, Nickname: m.LocalNickname(), Port: port}
	wire.Proof = base64.RawStdEncoding.EncodeToString(ed25519.Sign(m.identity.privateKey, joinProofBytes(wire)))
	body, _ := json.Marshal(wire)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/rooms/join", strings.NewReader(string(body)))
	if err != nil {
		return model.LANRoomJoin{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return model.LANRoomJoin{}, ErrPeerUnreachable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		switch response.StatusCode {
		case http.StatusBadRequest:
			return model.LANRoomJoin{}, ErrInvalid
		case http.StatusForbidden:
			return model.LANRoomJoin{}, ErrPeerNotTrusted
		case http.StatusConflict:
			return model.LANRoomJoin{}, ErrConflict
		default:
			return model.LANRoomJoin{}, fmt.Errorf("room join peer returned HTTP %d", response.StatusCode)
		}
	}
	if response.TLS == nil || len(response.TLS.PeerCertificates) != 1 {
		return model.LANRoomJoin{}, ErrPeerNotTrusted
	}
	key := response.TLS.PeerCertificates[0].PublicKey.(ed25519.PublicKey)
	row := model.LANRoomJoin{ID: wire.ID, RoomID: input.RoomID, NodeID: m.NodeID(), PublicKey: base64.RawStdEncoding.EncodeToString(m.identity.PublicKey()), Nickname: wire.Nickname,
		Address: input.Address, Port: input.Port, PeerID: input.PeerID, PeerKey: base64.RawStdEncoding.EncodeToString(key), Proof: wire.Proof, Outgoing: true, Status: "pending", ExpiresAt: time.Now().UTC().Add(10 * time.Minute)}
	err = m.db.WithContext(ctx).Create(&row).Error
	return row, err
}
func (m *Manager) roomDirectory(w http.ResponseWriter, r *http.Request) {
	rooms, err := m.Rooms(r.Context())
	if err != nil {
		writePeerError(w, err)
		return
	}
	visible := []RoomLocation{}
	for _, room := range rooms {
		if room.Joined {
			visible = append(visible, RoomLocation{ID: room.ID, Code: room.Code, Name: room.Name})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"rooms": visible})
}
func decodeRoomRequest(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || !errors.Is(decoder.Decode(new(any)), io.EOF) {
		http.Error(w, "invalid room message", http.StatusBadRequest)
		return false
	}
	return true
}
func (m *Manager) receiveRoomJoin(w http.ResponseWriter, r *http.Request) {
	var wire roomJoinWire
	if !decodeRoomRequest(w, r, &wire) {
		return
	}
	key := requestPeerKey(r)
	proof, err := base64.RawStdEncoding.DecodeString(wire.Proof)
	_, idErr := hex.DecodeString(wire.ID)
	if err != nil || len(key) != ed25519.PublicKeySize || !ed25519.Verify(key, joinProofBytes(wire), proof) || idErr != nil || len(wire.ID) != 32 || len([]rune(wire.Nickname)) > 80 || !m.roomMember(wire.RoomID, m.NodeID()) {
		writePeerError(w, ErrPeerNotTrusted)
		return
	}
	address, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || !validPeerEndpoint(address, wire.Port) {
		writePeerError(w, ErrInvalid)
		return
	}
	var count int64
	if err = m.db.Model(&model.LANRoomJoin{}).Where("outgoing = ? AND expires_at > ?", false, time.Now()).Count(&count).Error; err != nil {
		writePeerError(w, err)
		return
	}
	if count >= 64 {
		http.Error(w, "join inbox full", http.StatusTooManyRequests)
		return
	}
	var existing int64
	m.db.Model(&model.LANRoomJoin{}).Where("room_id = ? AND node_id = ? AND outgoing = ? AND status = ? AND expires_at > ?", wire.RoomID, requestPeerID(r), false, "pending", time.Now()).Count(&existing)
	if existing > 0 {
		http.Error(w, "join already pending", http.StatusConflict)
		return
	}
	row := model.LANRoomJoin{ID: wire.ID, RoomID: wire.RoomID, NodeID: requestPeerID(r), PublicKey: base64.RawStdEncoding.EncodeToString(key), Nickname: wire.Nickname,
		Address: address, Port: wire.Port, Proof: wire.Proof, Status: "pending", ExpiresAt: time.Now().UTC().Add(10 * time.Minute)}
	if err = m.db.Create(&row).Error; err != nil {
		writePeerError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"pending": true})
}
func (m *Manager) pollRoomJoin(w http.ResponseWriter, r *http.Request) {
	var row model.LANRoomJoin
	if m.db.First(&row, "id = ? AND outgoing = ?", r.PathValue("requestID"), false).Error != nil || time.Now().After(row.ExpiresAt) ||
		row.NodeID != requestPeerID(r) || row.PublicKey != base64.RawStdEncoding.EncodeToString(requestPeerKey(r)) {
		writePeerError(w, ErrPeerNotTrusted)
		return
	}
	events := []RoomEvent{}
	if row.Status == "approved" {
		if !m.roomAccess(row.RoomID, row.NodeID) {
			writePeerError(w, ErrPeerNotTrusted)
			return
		}
		var err error
		events, err = m.roomEvents(r.Context(), row.RoomID)
		if err != nil {
			writePeerError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": row.Status, "events": events})
}
func (m *Manager) exchangeRoomEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("roomID")
	var payload struct {
		Events []RoomEvent `json:"events"`
	}
	if !decodeRoomRequest(w, r, &payload) {
		return
	}
	candidate, err := evaluateRoom(payload.Events)
	if err != nil || candidate.ID != id {
		writePeerError(w, ErrInvalid)
		return
	}
	peerID := requestPeerID(r)
	peerKey := base64.RawStdEncoding.EncodeToString(requestPeerKey(r))
	currentEvents, err := m.roomEvents(r.Context(), id)
	if err != nil {
		writePeerError(w, err)
		return
	}
	current, err := m.roomState(r.Context(), id)
	if err != nil {
		writePeerError(w, err)
		return
	}
	localKey := base64.RawStdEncoding.EncodeToString(m.identity.PublicKey())
	local, localIsMember := current.Members[m.NodeID()]
	if !roomLedgerHasIdentity(currentEvents, peerID, peerKey) || !roomLedgerHasIdentity(currentEvents, m.NodeID(), localKey) {
		writePeerError(w, ErrPeerNotTrusted)
		return
	}
	peer, peerIsMember := current.Members[peerID]
	if peerIsMember && peer.PublicKey == peerKey && localIsMember && local.PublicKey == localKey {
		if _, err = m.mergeRoomEvents(r.Context(), id, payload.Events, false); err != nil {
			writePeerError(w, err)
			return
		}
	}
	events, err := m.roomEvents(r.Context(), id)
	if err != nil {
		writePeerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

func roomLedgerHasIdentity(events []RoomEvent, nodeID, publicKey string) bool {
	for _, event := range events {
		if event.Author == nodeID && event.PublicKey == publicKey {
			return true
		}
		if event.Subject != nil && event.Subject.NodeID == nodeID && event.Subject.PublicKey == publicKey {
			return true
		}
	}
	return false
}

func (m *Manager) syncRoomWithPeer(ctx context.Context, client *http.Client, base, peerID string) error {
	rooms, err := m.Rooms(ctx)
	if err != nil {
		return err
	}
	for _, room := range rooms {
		events, err := m.roomEvents(ctx, room.ID)
		if err != nil {
			return err
		}
		if !roomLedgerHasIdentity(events, peerID, m.roomPeerKey(peerID)) {
			continue
		}
		var reply struct {
			Events []RoomEvent `json:"events"`
		}
		if err = requestPeerJSON(ctx, client, http.MethodPost, base+"/rooms/"+room.ID+"/events", map[string]any{"events": events}, &reply); err != nil {
			return err
		}
		if _, err = m.mergeRoomEvents(ctx, room.ID, reply.Events, false); err != nil {
			return err
		}
	}
	return nil
}
func (m *Manager) refreshRooms(ctx context.Context) {
	requests, err := m.RoomRequests(ctx)
	if err != nil {
		return
	}
	for _, pending := range requests {
		if !pending.Outgoing || pending.Status != "pending" {
			continue
		}
		client, base, err := m.roomClient(pending.PeerID, pending.Address, pending.Port, pending.PeerKey)
		if err != nil {
			continue
		}
		var reply struct {
			Status string      `json:"status"`
			Events []RoomEvent `json:"events"`
		}
		err = requestPeerJSON(ctx, client, http.MethodGet, base+"/rooms/join/"+pending.ID, nil, &reply)
		client.CloseIdleConnections()
		if err != nil {
			continue
		}
		if reply.Status == "approved" {
			state, verifyErr := evaluateRoom(reply.Events)
			self, exists := state.Members[m.NodeID()]
			if verifyErr != nil || state.ID != pending.RoomID || !exists || state.Dissolved || self.PublicKey != pending.PublicKey {
				continue
			}
			sponsor, exists := state.Members[pending.PeerID]
			if !exists || sponsor.PublicKey != pending.PeerKey {
				continue
			}
			if _, err = m.mergeRoomEvents(ctx, state.ID, reply.Events, true); err != nil {
				continue
			}
			_ = m.rememberPeerEndpoint(ctx, pending.PeerID, pending.Address, pending.Port)
			m.scheduleKnownCatalogSync()
		}
		if reply.Status == "approved" || reply.Status == "rejected" {
			m.db.WithContext(ctx).Model(&pending).Update("status", reply.Status)
		}
	}
	var peers []model.LANTrustedPeer
	if m.db.WithContext(ctx).Where("trust_state IN ?", []string{"trusted", "room"}).Find(&peers).Error != nil {
		return
	}
	for _, peer := range peers {
		if ctx.Err() != nil {
			return
		}
		endpoints := m.collaborationPeerEndpoints(PeerSummary{PeerID: peer.NodeID, Address: peer.Address, Port: peer.Port})
		for _, endpoint := range endpoints {
			client, base, err := m.roomClient(peer.NodeID, endpoint.address, endpoint.port, peer.PublicKey)
			if err != nil {
				continue
			}
			err = m.syncRoomWithPeer(ctx, client, base, peer.NodeID)
			client.CloseIdleConnections()
			if err == nil {
				if m.isTrusted(peer.NodeID) {
					m.scheduleCatalogSync(peer.NodeID, endpoint.address, endpoint.port)
				}
				break
			}
		}
	}
}
func (m *Manager) runRoomRefresh(ctx context.Context) {
	timer := time.NewTicker(3 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			m.refreshRooms(ctx)
		}
	}
}
