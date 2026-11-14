package lansharing

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"infinite-canvas/backend/internal/kernel"
	"infinite-canvas/backend/internal/model"
)

type RoomSummary struct {
	RoomState
	Joined bool `json:"joined"`
}

func (m *Manager) Rooms(ctx context.Context) ([]RoomSummary, error) {
	var rows []model.LANRoom
	if err := m.db.WithContext(ctx).Order("id").Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]RoomSummary, 0, len(rows))
	for _, row := range rows {
		var state RoomState
		if err := json.Unmarshal([]byte(row.State), &state); err != nil {
			return nil, err
		}
		_, joined := state.Members[m.NodeID()]
		result = append(result, RoomSummary{state, joined && !state.Dissolved})
	}
	return result, nil
}
func (m *Manager) roomState(ctx context.Context, id string) (RoomState, error) {
	var row model.LANRoom
	if err := m.db.WithContext(ctx).First(&row, "id = ?", id).Error; err != nil {
		return RoomState{}, collaborationRecordError(err)
	}
	var state RoomState
	err := json.Unmarshal([]byte(row.State), &state)
	return state, err
}
func (m *Manager) roomMember(roomID, nodeID string) bool {
	if roomID == "" {
		return false
	}
	state, err := m.roomState(context.Background(), roomID)
	_, exists := state.Members[nodeID]
	return err == nil && !state.Dissolved && exists
}
func (m *Manager) roomAccess(roomID, peerID string) bool {
	return m.roomMember(roomID, m.NodeID()) && m.roomMember(roomID, peerID)
}
func (m *Manager) activeRoomPeer(peerID string) bool {
	rooms, err := m.Rooms(context.Background())
	if err != nil {
		return false
	}
	for _, room := range rooms {
		if room.Joined {
			if _, ok := room.Members[peerID]; ok {
				return true
			}
		}
	}
	return false
}
func (m *Manager) CreateRoom(ctx context.Context, name string) (RoomState, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 80 {
		return RoomState{}, ErrInvalid
	}
	number, err := rand.Int(rand.Reader, big.NewInt(100000000))
	if err != nil {
		return RoomState{}, err
	}
	event := signRoomEvent(RoomEvent{Kind: "create", Name: name, Code: fmt.Sprintf("%08d", number.Int64()), Nonce: kernel.NewID(), Subject: &RoomMember{m.NodeID(), base64.RawStdEncoding.EncodeToString(m.identity.PublicKey()), m.LocalNickname()}}, m.identity)
	return m.mergeRoomEvents(ctx, event.ID, []RoomEvent{event}, true)
}
func (m *Manager) roomEvents(ctx context.Context, id string) ([]RoomEvent, error) {
	var rows []model.LANRoomEvent
	if err := m.db.WithContext(ctx).Where("room_id = ?", id).Find(&rows).Error; err != nil {
		return nil, err
	}
	events := make([]RoomEvent, 0, len(rows))
	for _, row := range rows {
		var event RoomEvent
		if err := json.Unmarshal([]byte(row.Payload), &event); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	sort.Slice(events, func(i, j int) bool { return events[i].ID < events[j].ID })
	return events, nil
}
func (m *Manager) mergeRoomEvents(ctx context.Context, id string, incoming []RoomEvent, allowNew bool) (RoomState, error) {
	m.roomMu.Lock()
	defer m.roomMu.Unlock()
	return m.mergeRoomEventsLocked(ctx, id, incoming, allowNew)
}
func (m *Manager) mergeRoomEventsLocked(ctx context.Context, id string, incoming []RoomEvent, allowNew bool) (RoomState, error) {
	if !validNodeID(id) {
		return RoomState{}, ErrInvalid
	}
	existing, err := m.roomEvents(ctx, id)
	if err != nil {
		return RoomState{}, err
	}
	if len(existing) == 0 && !allowNew {
		return RoomState{}, ErrNotFound
	}
	known := map[string]bool{}
	for _, e := range existing {
		known[e.ID] = true
	}
	for _, e := range incoming {
		if !known[e.ID] {
			existing = append(existing, e)
			known[e.ID] = true
		}
	}
	state, err := evaluateRoom(existing)
	if err != nil {
		return RoomState{}, err
	}
	if state.ID != id {
		return RoomState{}, ErrConflict
	}
	body, err := json.Marshal(state)
	if err != nil {
		return RoomState{}, err
	}
	err = m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, e := range incoming {
			payload, err := json.Marshal(e)
			if err != nil {
				return err
			}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.LANRoomEvent{ID: e.ID, RoomID: id, Payload: string(payload)}).Error; err != nil {
				return err
			}
		}
		if err := tx.Save(&model.LANRoom{ID: id, State: string(body), UpdatedAt: time.Now().UTC()}).Error; err != nil {
			return err
		}
		// Room trust never grants access to the user's older paired-only catalogs.
		for node, member := range state.Members {
			if node == m.NodeID() {
				continue
			}
			var peer model.LANTrustedPeer
			err := tx.First(&peer, "node_id = ?", node).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				now := time.Now().UTC()
				peer = model.LANTrustedPeer{NodeID: node, PublicKey: member.PublicKey, Nickname: member.Nickname, TrustState: "room", FirstSeenAt: now, UpdatedAt: now}
				if err := tx.Create(&peer).Error; err != nil {
					return err
				}
			} else if err != nil {
				return err
			} else if peer.PublicKey != member.PublicKey {
				return ErrConflict
			}
		}
		return nil
	})
	return state, err
}
func (m *Manager) RoomAction(ctx context.Context, id, kind, target string) (RoomState, error) {
	if kind != "leave" && kind != "remove" && kind != "dissolve" && kind != "vote" {
		return RoomState{}, ErrInvalid
	}
	m.roomMu.Lock()
	state, err := m.roomState(ctx, id)
	if err == nil {
		if _, ok := state.Members[m.NodeID()]; !ok || state.Dissolved {
			err = ErrPeerNotTrusted
		}
		if kind == "leave" && len(state.Members) == 1 {
			err = ErrConflict
		}
	}
	if err != nil {
		m.roomMu.Unlock()
		return RoomState{}, err
	}
	event := RoomEvent{RoomID: id, Kind: kind, Parents: state.Heads, Nonce: kernel.NewID(), Target: target}
	if kind == "leave" {
		event.Target = m.NodeID()
	}
	if kind == "vote" {
		event.Proposal = target
		event.Target = ""
	}
	event = signRoomEvent(event, m.identity)
	state, err = m.mergeRoomEventsLocked(ctx, id, []RoomEvent{event}, false)
	m.roomMu.Unlock()
	if err == nil {
		m.scheduleKnownCatalogSync()
	}
	return state, err
}

func (m *Manager) updateRoomNickname(ctx context.Context, nickname string) error {
	rooms, err := m.Rooms(ctx)
	if err != nil {
		return err
	}
	for _, room := range rooms {
		if !room.Joined {
			continue
		}
		event := signRoomEvent(RoomEvent{RoomID: room.ID, Kind: "profile", Parents: room.Heads, Nonce: kernel.NewID(), Name: nickname}, m.identity)
		if _, err := m.mergeRoomEvents(ctx, room.ID, []RoomEvent{event}, false); err != nil {
			return err
		}
	}
	m.scheduleKnownCatalogSync()
	return nil
}
func (m *Manager) RoomRequests(ctx context.Context) ([]model.LANRoomJoin, error) {
	var requests []model.LANRoomJoin
	err := m.db.WithContext(ctx).Where("expires_at > ?", time.Now().UTC()).Order("expires_at").Find(&requests).Error
	if requests == nil {
		requests = []model.LANRoomJoin{}
	}
	return requests, err
}
func (m *Manager) ApproveRoomJoin(ctx context.Context, requestID string, approve bool) (RoomState, error) {
	m.roomMu.Lock()
	defer m.roomMu.Unlock()
	var request model.LANRoomJoin
	if err := m.db.WithContext(ctx).First(&request, "id = ? AND outgoing = ?", requestID, false).Error; err != nil {
		return RoomState{}, collaborationRecordError(err)
	}
	if request.Status != "pending" || time.Now().After(request.ExpiresAt) {
		return RoomState{}, ErrConflict
	}
	state, err := m.roomState(ctx, request.RoomID)
	if err != nil {
		return RoomState{}, err
	}
	if _, ok := state.Members[m.NodeID()]; !ok || state.Dissolved {
		return RoomState{}, ErrPeerNotTrusted
	}
	if !approve {
		return state, m.db.WithContext(ctx).Model(&request).Update("status", "rejected").Error
	}
	event := signRoomEvent(RoomEvent{RoomID: state.ID, Kind: "join", Parents: state.Heads, Nonce: request.ID, Subject: &RoomMember{request.NodeID, request.PublicKey, request.Nickname}}, m.identity)
	state, err = m.mergeRoomEventsLocked(ctx, state.ID, []RoomEvent{event}, false)
	if err != nil {
		return RoomState{}, err
	}
	if err = m.db.WithContext(ctx).Model(&request).Update("status", "approved").Error; err != nil {
		return RoomState{}, err
	}
	// Endpoint is only a hint until the new member has fetched its certificate.
	if validPeerEndpoint(request.Address, request.Port) {
		m.recordDiscoveredPeer(Peer{ID: request.NodeID, Name: request.Nickname, Addresses: []string{request.Address}, Port: request.Port, Version: protocol}, time.Now())
	}
	return state, nil
}
func (m *Manager) catalogPeerAllowed(ctx context.Context, catalogID, peerID string) bool {
	var catalog model.LANShareCatalog
	if m.db.WithContext(ctx).First(&catalog, "id = ?", catalogID).Error != nil {
		return false
	}
	return m.catalogScopeAllowed(catalog.RoomID, peerID)
}
func (m *Manager) catalogScopeAllowed(roomID, peerID string) bool {
	if roomID != "" {
		return m.roomAccess(roomID, peerID)
	}
	return m.pairedPeer(peerID)
}
func (m *Manager) CheckLocalCatalogAccess(ctx context.Context, id string) error {
	if !m.localCatalogAllowed(ctx, id) {
		return ErrPeerNotTrusted
	}
	return nil
}
func (m *Manager) localCanvasAllowed(ctx context.Context, id string) bool {
	var canvas model.LANCollaborationCanvas
	if m.db.WithContext(ctx).First(&canvas, "id = ?", id).Error != nil || canvas.DeletedAt != nil {
		return false
	}
	return canvas.RoomID == "" || m.roomMember(canvas.RoomID, m.NodeID())
}
func (m *Manager) pairedPeer(peerID string) bool {
	var count int64
	return m.db.Model(&model.LANTrustedPeer{}).Where("node_id = ? AND trust_state = ?", peerID, "trusted").Count(&count).Error == nil && count == 1
}
func (m *Manager) localCatalogAllowed(ctx context.Context, catalogID string) bool {
	var catalog model.LANShareCatalog
	if m.db.WithContext(ctx).First(&catalog, "id = ?", catalogID).Error != nil {
		return false
	}
	return catalog.RoomID == "" || m.roomMember(catalog.RoomID, m.NodeID())
}
func (m *Manager) canManageCanvas(row model.LANCollaborationCanvas) bool {
	if row.RoomID != "" {
		return m.roomMember(row.RoomID, m.NodeID())
	}
	return canManageCollaborationCanvas(row, m.NodeID())
}
func (m *Manager) canvasPeerAllowed(item collaborationCanvasMetadata, peerID string) bool {
	if item.RoomID != "" {
		return m.roomAccess(item.RoomID, peerID)
	}
	return m.pairedPeer(peerID) && (item.CreatedByNodeID == m.NodeID() || item.CreatedByNodeID == peerID)
}
func (m *Manager) roomPeerKey(id string) string {
	var peer model.LANTrustedPeer
	if m.db.First(&peer, "node_id = ?", id).Error != nil {
		return ""
	}
	return peer.PublicKey
}
func (m *Manager) validRoomPeerKey(id string, key ed25519.PublicKey) bool {
	return m.activeRoomPeer(id) && m.roomPeerKey(id) == base64.RawStdEncoding.EncodeToString(key)
}
