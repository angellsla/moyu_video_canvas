package lansharing

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"infinite-canvas/backend/internal/kernel"
	"infinite-canvas/backend/internal/localcrypto"
	"infinite-canvas/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	pairingCodeTTL      = 5 * time.Minute
	discoveredPeerTTL   = 45 * time.Second
	maxCatalogUpdate    = 4 << 20
	defaultUpdatePage   = 100
	maximumUpdatePage   = 500
	peerPairRequestSize = 8 << 10
	syncUpdatePageSize  = 8
	maxPeerJSONResponse = 48 << 20
	catalogSyncInterval = 45 * time.Second
)

var (
	ErrInvalid          = errors.New("LAN sharing request is invalid")
	ErrNotFound         = errors.New("LAN sharing record was not found")
	ErrConflict         = errors.New("LAN sharing request conflicts with current state")
	ErrPairingCode      = errors.New("pairing code is invalid or expired")
	ErrPeerNotTrusted   = errors.New("LAN peer is not trusted")
	ErrManagerClosed    = errors.New("LAN sharing manager is closed")
	ErrCatalogUpdateBig = errors.New("catalog update exceeds size limit")
)

type Status struct {
	Enabled              bool       `json:"enabled"`
	NodeID               string     `json:"nodeId"`
	Nickname             string     `json:"nickname"`
	PairingCode          string     `json:"pairingCode,omitempty"`
	PairingCodeExpiresAt *time.Time `json:"pairingCodeExpiresAt,omitempty"`
	Endpoint             string     `json:"endpoint,omitempty"`
	Endpoints            []string   `json:"endpoints,omitempty"`
	AdvertisedEndpoints  []string   `json:"advertisedEndpoints,omitempty"`
}

type PairRequest struct {
	PeerID      string `json:"peerId"`
	Address     string `json:"address"`
	Port        int    `json:"port"`
	PairingCode string `json:"pairingCode"`
	Nickname    string `json:"nickname"`
}

type PeerSummary struct {
	PeerID    string    `json:"peerId"`
	Nickname  string    `json:"nickname,omitempty"`
	Address   string    `json:"address,omitempty"`
	Port      int       `json:"port,omitempty"`
	TrustedAt time.Time `json:"trustedAt"`
	LastSeen  time.Time `json:"lastSeenAt"`
}

type CatalogSummary struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	UpdateCount int64     `json:"updateCount"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type CatalogUpdatePage struct {
	Updates []model.LANCatalogYjsUpdate `json:"updates"`
	Cursor  string                      `json:"cursor,omitempty"`
	HasMore bool                        `json:"hasMore"`
}

type catalogMetadata struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	CreatedByNodeID string    `json:"createdByNodeId"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

type catalogSyncJob struct {
	current catalogSyncEndpoint
	pending []catalogSyncEndpoint
	dirty   bool
	cancel  context.CancelFunc
}

type catalogSyncEndpoint struct {
	address string
	port    int
}

func (job *catalogSyncJob) enqueue(endpoint catalogSyncEndpoint) {
	if endpoint == job.current {
		job.dirty = true
	} else if !slices.Contains(job.pending, endpoint) {
		job.pending = append(job.pending, endpoint)
	}
}

func (job *catalogSyncJob) nextAfter(err error) bool {
	retryCurrent := job.dirty
	job.dirty = false
	if err == nil {
		if retryCurrent {
			job.pending = nil
		}
		return retryCurrent
	}
	if retryCurrent && !slices.Contains(job.pending, job.current) {
		job.pending = append(job.pending, job.current)
	}
	if len(job.pending) > 0 {
		job.current = job.pending[0]
		job.pending = job.pending[1:]
		return true
	}
	return false
}

type Manager struct {
	dataDir  string
	db       *gorm.DB
	identity *Identity

	lifecycleMu     sync.Mutex
	mu              sync.Mutex
	nicknameMu      sync.RWMutex
	nickname        string
	peerServer      *PeerServer
	discoveryCancel context.CancelFunc
	endpoint        string
	closed          bool

	pairMu       sync.Mutex
	pairingCode  string
	pairingUntil time.Time

	discoveryMu         sync.RWMutex
	discovered          map[string]Peer
	discoveredAt        map[string]time.Time
	discoveredAddressAt map[string]map[string]time.Time

	peerHandlersMu   sync.Mutex
	peerHandlers     map[string]http.Handler
	collaborationHub *collaborationHub
	peerStreams      *peerCollaborationStreams

	syncMu      sync.Mutex
	syncJobs    map[string]*catalogSyncJob
	syncClosed  bool
	syncContext context.Context
	syncCancel  context.CancelFunc
	syncWG      sync.WaitGroup
}

func NewManager(dataDir string, db *gorm.DB) (*Manager, error) {
	if db == nil {
		return nil, errors.New("LAN sharing database is required")
	}
	identity, err := LoadIdentity(dataDir)
	if err != nil {
		return nil, err
	}
	syncContext, syncCancel := context.WithCancel(context.Background())
	manager := &Manager{
		dataDir: strings.TrimSpace(dataDir), db: db, identity: identity,
		discovered: make(map[string]Peer), discoveredAt: make(map[string]time.Time), discoveredAddressAt: make(map[string]map[string]time.Time), peerHandlers: make(map[string]http.Handler),
		collaborationHub: newCollaborationHub(),
		peerStreams:      newPeerCollaborationStreams(),
		syncJobs:         make(map[string]*catalogSyncJob), syncContext: syncContext, syncCancel: syncCancel,
	}
	if err := manager.persistIdentity(context.Background()); err != nil {
		return nil, fmt.Errorf("persist LAN sharing identity: %w", err)
	}
	return manager, nil
}

func (m *Manager) NodeID() string {
	if m == nil || m.identity == nil {
		return ""
	}
	return m.identity.ID()
}

func (m *Manager) persistIdentity(ctx context.Context) error {
	publicKey := m.identity.PublicKey()
	privateKey := base64.RawStdEncoding.EncodeToString(m.identity.privateKey)
	ciphertext, err := localcrypto.Encrypt(m.dataDir, privateKey)
	if err != nil {
		return err
	}
	var existing model.LANNodeIdentity
	err = m.db.WithContext(ctx).First(&existing).Error
	if err == nil {
		if existing.NodeID != m.identity.ID() || existing.PublicKey != base64.RawStdEncoding.EncodeToString(publicKey) {
			return errors.New("stored LAN sharing identity does not match the local identity file")
		}
		storedKey, decryptErr := localcrypto.Decrypt(m.dataDir, existing.PrivateKeyCipher)
		if decryptErr != nil || storedKey != privateKey {
			return errors.New("stored LAN sharing private key does not match the local identity file")
		}
		m.setNickname(existing.Nickname)
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	now := time.Now().UTC()
	if err := m.db.WithContext(ctx).Create(&model.LANNodeIdentity{
		NodeID: m.identity.ID(), PublicKey: base64.RawStdEncoding.EncodeToString(publicKey),
		PrivateKeyCipher: ciphertext, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		return err
	}
	m.setNickname("")
	return nil
}

func (m *Manager) LocalNickname() string {
	if m == nil {
		return systemNickname()
	}
	m.nicknameMu.RLock()
	nickname := m.nickname
	m.nicknameMu.RUnlock()
	if nickname == "" {
		return systemNickname()
	}
	return nickname
}

func (m *Manager) setNickname(nickname string) {
	if m == nil {
		return
	}
	m.nicknameMu.Lock()
	m.nickname = normalizeNickname(nickname)
	m.nicknameMu.Unlock()
}

func (m *Manager) SetLocalNickname(ctx context.Context, nickname string) (Status, error) {
	if m == nil || m.db == nil || m.identity == nil {
		return Status{}, ErrManagerClosed
	}
	nickname = normalizeNickname(nickname)
	result := m.db.WithContext(ctx).Model(&model.LANNodeIdentity{}).Where("node_id = ?", m.NodeID()).Updates(map[string]any{
		"nickname": nickname, "updated_at": time.Now().UTC(),
	})
	if result.Error != nil {
		return Status{}, result.Error
	}
	if result.RowsAffected == 0 {
		return Status{}, ErrNotFound
	}
	m.setNickname(nickname)
	m.mu.Lock()
	server := m.peerServer
	m.mu.Unlock()
	if server != nil {
		if err := server.SetNickname(nickname); err != nil {
			return m.Status(), err
		}
	}
	return m.Status(), nil
}

func (m *Manager) Status() Status {
	if m == nil {
		return Status{}
	}
	m.mu.Lock()
	server, endpoint := m.peerServer, m.endpoint
	m.mu.Unlock()
	enabled := server != nil
	status := Status{Enabled: enabled, NodeID: m.NodeID(), Nickname: m.LocalNickname()}
	m.pairMu.Lock()
	if m.pairingCode != "" && time.Now().Before(m.pairingUntil) {
		status.PairingCode = m.pairingCode
		expires := m.pairingUntil
		status.PairingCodeExpiresAt = &expires
	}
	m.pairMu.Unlock()
	if enabled {
		status.Endpoint = endpoint
		status.Endpoints, status.AdvertisedEndpoints = server.EndpointStatus()
	}
	return status
}

// Start creates the listener and a fresh one-time pairing code. Identity creation
// happens in NewManager, while all network activity stays explicitly opt-in.
func (m *Manager) Start(ctx context.Context) (Status, error) {
	if m == nil {
		return Status{}, ErrManagerClosed
	}
	if ctx != nil && ctx.Err() != nil {
		return Status{}, ctx.Err()
	}
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return Status{}, ErrManagerClosed
	}
	code, err := newPairingCode()
	if err != nil {
		m.mu.Unlock()
		return Status{}, err
	}
	m.setPairingCode(code, time.Now().Add(pairingCodeTTL))
	if m.peerServer != nil {
		m.mu.Unlock()
		return m.Status(), nil
	}
	server, err := NewPeerServer(m.identity, func(id string) bool {
		return m.isTrusted(id) || m.hasActivePairingCode()
	}, m.peerHTTPHandler())
	if err != nil {
		m.clearPairingCode()
		m.mu.Unlock()
		return Status{}, err
	}
	if err := server.SetNickname(m.LocalNickname()); err != nil {
		m.clearPairingCode()
		m.mu.Unlock()
		return Status{}, err
	}
	endpoints, port, err := server.Start()
	if err != nil {
		m.clearPairingCode()
		m.mu.Unlock()
		return Status{}, err
	}
	m.peerServer = server
	m.endpoint = net.JoinHostPort(endpoints[0].Address.String(), fmt.Sprint(port))
	discoveryCtx, cancel := context.WithCancel(context.Background())
	m.discoveryCancel = cancel
	go m.runDiscovery(discoveryCtx)
	go m.runCatalogSyncRefresh(discoveryCtx)
	go m.runCollaborationPresenceHeartbeat(discoveryCtx)
	m.mu.Unlock()
	m.scheduleKnownCatalogSync()
	return m.Status(), nil
}

func (m *Manager) Stop(ctx context.Context) (Status, error) {
	if m == nil {
		return Status{}, nil
	}
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	return m.stop(ctx)
}

func (m *Manager) stop(ctx context.Context) (Status, error) {
	m.mu.Lock()
	server := m.peerServer
	cancel := m.discoveryCancel
	m.peerServer, m.discoveryCancel, m.endpoint = nil, nil, ""
	m.clearPairingCode()
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	m.collaborationHub.close()
	m.peerStreams.closeAll()
	if server != nil {
		if err := server.Stop(ctx); err != nil {
			return m.Status(), err
		}
	}
	return m.Status(), nil
}

func (m *Manager) Close(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	_, err := m.stop(ctx)
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
	m.syncMu.Lock()
	m.syncClosed = true
	m.syncMu.Unlock()
	if m.syncCancel != nil {
		m.syncCancel()
	}
	if m.collaborationHub != nil {
		m.collaborationHub.close()
	}
	done := make(chan struct{})
	go func() {
		m.syncWG.Wait()
		close(done)
	}()
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-done:
	case <-ctx.Done():
		if err == nil {
			err = ctx.Err()
		}
	}
	return err
}

func (m *Manager) DiscoveredPeers() []Peer {
	if m == nil {
		return []Peer{}
	}
	now := time.Now()
	m.discoveryMu.Lock()
	peers := make([]Peer, 0, len(m.discovered))
	for id, item := range m.discovered {
		addresses := item.Addresses[:0]
		for _, address := range item.Addresses {
			if now.Sub(m.discoveredAddressAt[id][address]) <= discoveredPeerTTL {
				addresses = append(addresses, address)
			} else {
				delete(m.discoveredAddressAt[id], address)
			}
		}
		if len(addresses) == 0 {
			delete(m.discovered, id)
			delete(m.discoveredAt, id)
			delete(m.discoveredAddressAt, id)
			continue
		}
		item.Addresses = append([]string(nil), addresses...)
		m.discovered[id] = item
		peers = append(peers, item)
	}
	m.discoveryMu.Unlock()
	sortPeers(peers)
	return peers
}

func (m *Manager) TrustedPeers(ctx context.Context) ([]PeerSummary, error) {
	var rows []model.LANTrustedPeer
	if err := m.db.WithContext(ctx).Where("trust_state = ?", "trusted").Order("last_seen_at DESC, node_id").Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]PeerSummary, 0, len(rows))
	for _, row := range rows {
		result = append(result, PeerSummary{
			PeerID: row.NodeID, Nickname: row.Nickname, Address: row.Address, Port: row.Port,
			TrustedAt: row.FirstSeenAt, LastSeen: row.LastSeenAt,
		})
	}
	return result, nil
}

func (m *Manager) PairPeer(ctx context.Context, request PairRequest) (PeerSummary, error) {
	peerID := strings.ToLower(strings.TrimSpace(request.PeerID))
	address := net.ParseIP(strings.TrimSpace(request.Address))
	code := strings.TrimSpace(request.PairingCode)
	nickname := normalizeNickname(request.Nickname)
	if !validNodeID(peerID) || peerID == m.NodeID() || address == nil || address.To4() == nil || !isPrivateLANAddress(address) || request.Port < 1 || request.Port > 65535 || code == "" {
		return PeerSummary{}, ErrInvalid
	}
	m.mu.Lock()
	server := m.peerServer
	m.mu.Unlock()
	if server == nil {
		return PeerSummary{}, ErrInvalid
	}
	localEndpoints, _ := server.EndpointStatus()
	localAddress, localPort, err := routedLocalEndpoint(address, request.Port, localEndpoints)
	if err != nil {
		return PeerSummary{}, ErrInvalid
	}
	clientTLS, err := m.identity.ClientTLSConfig(peerID)
	if err != nil {
		return PeerSummary{}, err
	}
	verifyPeer := clientTLS.VerifyConnection
	var remotePublicKey ed25519.PublicKey
	clientTLS.VerifyConnection = func(state tls.ConnectionState) error {
		if err := verifyPeer(state); err != nil {
			return err
		}
		remotePublicKey = append(ed25519.PublicKey(nil), state.PeerCertificates[0].PublicKey.(ed25519.PublicKey)...)
		return nil
	}
	transport := &http.Transport{TLSClientConfig: clientTLS, ForceAttemptHTTP2: false}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 15 * time.Second}
	payload, err := json.Marshal(peerPairPayload{
		PeerID: m.NodeID(), PublicKey: base64.RawStdEncoding.EncodeToString(m.identity.PublicKey()),
		PairingCode: code, Nickname: m.LocalNickname(), Address: localAddress, Port: localPort,
	})
	if err != nil {
		return PeerSummary{}, err
	}
	endpoint := pairURL(address, request.Port)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(payload)))
	if err != nil {
		return PeerSummary{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return PeerSummary{}, fmt.Errorf("pair with LAN peer: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		if response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusConflict {
			return PeerSummary{}, ErrPairingCode
		}
		return PeerSummary{}, fmt.Errorf("pair with LAN peer returned HTTP %d", response.StatusCode)
	}
	var reply peerPairReply
	if err := json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&reply); err != nil {
		return PeerSummary{}, err
	}
	if reply.PeerID != peerID || len(remotePublicKey) != ed25519.PublicKeySize {
		return PeerSummary{}, ErrConflict
	}
	peerNickname := nickname
	if peerNickname == "" {
		peerNickname = reply.Nickname
	}
	trusted, err := m.saveTrustedPeer(ctx, peerID, remotePublicKey, peerNickname, address.String(), request.Port)
	if err != nil {
		return PeerSummary{}, err
	}
	m.scheduleCatalogSync(peerID, address.String(), request.Port)
	return trusted, nil
}

func routedLocalEndpoint(peerIP net.IP, peerPort int, endpoints []string) (string, int, error) {
	connection, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: peerIP.To4(), Port: peerPort})
	if err != nil {
		return "", 0, err
	}
	localIP := connection.LocalAddr().(*net.UDPAddr).IP
	_ = connection.Close()
	for _, endpoint := range endpoints {
		address, portText, err := net.SplitHostPort(endpoint)
		if err != nil || !net.ParseIP(address).Equal(localIP) {
			continue
		}
		port, err := strconv.Atoi(portText)
		if err == nil && validPeerEndpoint(address, port) {
			return address, port, nil
		}
	}
	return "", 0, ErrInvalid
}

func (m *Manager) RevokePeer(ctx context.Context, peerID string) error {
	peerID = strings.ToLower(strings.TrimSpace(peerID))
	if !validNodeID(peerID) || peerID == m.NodeID() {
		return ErrInvalid
	}
	result := m.db.WithContext(ctx).Model(&model.LANTrustedPeer{}).Where("node_id = ? AND trust_state = ?", peerID, "trusted").Updates(map[string]any{
		"trust_state": "revoked", "updated_at": time.Now().UTC(),
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	m.syncMu.Lock()
	if job := m.syncJobs[peerID]; job != nil && job.cancel != nil {
		job.cancel()
	}
	m.syncMu.Unlock()
	m.peerStreams.closePeer(peerID)
	return nil
}

func (m *Manager) Catalogs(ctx context.Context) ([]CatalogSummary, error) {
	var rows []model.LANShareCatalog
	if err := m.db.WithContext(ctx).Order("updated_at DESC, id").Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]CatalogSummary, 0, len(rows))
	for _, row := range rows {
		var count int64
		if err := m.db.WithContext(ctx).Model(&model.LANCatalogYjsUpdate{}).Where("catalog_id = ?", row.ID).Count(&count).Error; err != nil {
			return nil, err
		}
		result = append(result, CatalogSummary{ID: row.ID, Name: row.Name, UpdateCount: count, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt})
	}
	return result, nil
}

func (m *Manager) CreateCatalog(ctx context.Context, name string) (CatalogSummary, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 120 {
		return CatalogSummary{}, ErrInvalid
	}
	now := time.Now().UTC()
	catalog := model.LANShareCatalog{ID: kernel.NewID(), Name: name, CreatedByNodeID: m.NodeID(), CreatedAt: now, UpdatedAt: now}
	if err := m.db.WithContext(ctx).Create(&catalog).Error; err != nil {
		return CatalogSummary{}, err
	}
	m.scheduleKnownCatalogSync()
	return CatalogSummary{ID: catalog.ID, Name: catalog.Name, CreatedAt: catalog.CreatedAt, UpdatedAt: catalog.UpdatedAt}, nil
}

func (m *Manager) catalogMetadata(ctx context.Context) ([]catalogMetadata, error) {
	var catalogs []model.LANShareCatalog
	if err := m.db.WithContext(ctx).Order("id").Find(&catalogs).Error; err != nil {
		return nil, err
	}
	items := make([]catalogMetadata, 0, len(catalogs))
	for _, catalog := range catalogs {
		items = append(items, catalogMetadata{
			ID: catalog.ID, Name: catalog.Name, CreatedByNodeID: catalog.CreatedByNodeID,
			CreatedAt: catalog.CreatedAt, UpdatedAt: catalog.UpdatedAt,
		})
	}
	return items, nil
}

func (m *Manager) upsertCatalogMetadata(ctx context.Context, item catalogMetadata) error {
	item.ID = strings.TrimSpace(item.ID)
	item.Name = strings.TrimSpace(item.Name)
	item.CreatedByNodeID = strings.ToLower(strings.TrimSpace(item.CreatedByNodeID))
	if item.ID == "" || len(item.ID) > 36 || strings.ContainsAny(item.ID, "/\\?#") || item.Name == "" || len([]rune(item.Name)) > 120 || !validNodeID(item.CreatedByNodeID) || item.CreatedAt.IsZero() || item.UpdatedAt.IsZero() {
		return ErrInvalid
	}
	var existing model.LANShareCatalog
	err := m.db.WithContext(ctx).First(&existing, "id = ?", item.ID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return m.db.WithContext(ctx).Create(&model.LANShareCatalog{
			ID: item.ID, Name: item.Name, CreatedByNodeID: item.CreatedByNodeID,
			CreatedAt: item.CreatedAt.UTC(), UpdatedAt: item.UpdatedAt.UTC(),
		}).Error
	}
	if err != nil {
		return err
	}
	if existing.CreatedByNodeID != item.CreatedByNodeID || !existing.CreatedAt.Equal(item.CreatedAt) {
		return ErrConflict
	}
	if item.UpdatedAt.After(existing.UpdatedAt) {
		return m.db.WithContext(ctx).Model(&existing).Updates(map[string]any{"name": item.Name, "updated_at": item.UpdatedAt.UTC()}).Error
	}
	return nil
}

func (m *Manager) AppendCatalogUpdate(ctx context.Context, catalogID string, update []byte, originNodeID string) (bool, error) {
	if strings.TrimSpace(originNodeID) == "" {
		originNodeID = m.NodeID()
	}
	accepted, err := m.appendCatalogUpdate(ctx, catalogID, update, originNodeID, true)
	return accepted, err
}

func (m *Manager) appendCatalogUpdate(ctx context.Context, catalogID string, update []byte, originNodeID string, broadcast bool) (bool, error) {
	catalogID = strings.TrimSpace(catalogID)
	if catalogID == "" || len(update) == 0 {
		return false, ErrInvalid
	}
	if len(update) > maxCatalogUpdate {
		return false, ErrCatalogUpdateBig
	}
	var catalog model.LANShareCatalog
	if err := m.db.WithContext(ctx).First(&catalog, "id = ?", catalogID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, ErrNotFound
		}
		return false, err
	}
	digest := sha256.Sum256(update)
	row := model.LANCatalogYjsUpdate{
		ID: kernel.NewID(), CatalogID: catalogID, UpdateHash: hex.EncodeToString(digest[:]),
		OriginNodeID: strings.TrimSpace(originNodeID), Update: append([]byte(nil), update...), CreatedAt: time.Now().UTC(),
	}
	result := m.db.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "catalog_id"}, {Name: "update_hash"}}, DoNothing: true}).Create(&row)
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected > 0 {
		if err := m.db.WithContext(ctx).Model(&catalog).Update("updated_at", row.CreatedAt).Error; err != nil {
			return false, err
		}
		if broadcast {
			m.scheduleKnownCatalogSync()
		}
	}
	return result.RowsAffected > 0, nil
}

func (m *Manager) CatalogUpdates(ctx context.Context, catalogID, after string, limit int) (CatalogUpdatePage, error) {
	catalogID = strings.TrimSpace(catalogID)
	if catalogID == "" {
		return CatalogUpdatePage{}, ErrInvalid
	}
	var catalog model.LANShareCatalog
	if err := m.db.WithContext(ctx).First(&catalog, "id = ?", catalogID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return CatalogUpdatePage{}, ErrNotFound
		}
		return CatalogUpdatePage{}, err
	}
	query := m.db.WithContext(ctx).Where("catalog_id = ?", catalogID)
	if after != "" {
		var cursor model.LANCatalogYjsUpdate
		if err := m.db.WithContext(ctx).Where("catalog_id = ? AND id = ?", catalogID, after).First(&cursor).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return CatalogUpdatePage{}, ErrInvalid
			}
			return CatalogUpdatePage{}, err
		}
		query = query.Where("created_at > ? OR (created_at = ? AND id > ?)", cursor.CreatedAt, cursor.CreatedAt, cursor.ID)
	}
	if limit < 1 {
		limit = defaultUpdatePage
	}
	if limit > maximumUpdatePage {
		limit = maximumUpdatePage
	}
	var rows []model.LANCatalogYjsUpdate
	if err := query.Order("created_at ASC, id ASC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return CatalogUpdatePage{}, err
	}
	page := CatalogUpdatePage{HasMore: len(rows) > limit}
	if page.HasMore {
		rows = rows[:limit]
	}
	page.Updates = rows
	if len(rows) > 0 {
		page.Cursor = rows[len(rows)-1].ID
	}
	return page, nil
}

// RegisterPeerHandler adds a trusted-peer-only route before Start. The pairing
// endpoint is reserved so application handlers cannot weaken its one-time gate.
func (m *Manager) RegisterPeerHandler(pattern string, handler http.Handler) error {
	pattern = strings.TrimSpace(pattern)
	path := pattern
	if _, value, hasMethod := strings.Cut(pattern, " "); hasMethod {
		path = value
	}
	if m == nil || handler == nil || !strings.HasPrefix(path, "/") || path == "/pair" || strings.HasPrefix(path, "/pair/") {
		return ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrManagerClosed
	}
	if m.peerServer != nil {
		return ErrConflict
	}
	m.peerHandlersMu.Lock()
	defer m.peerHandlersMu.Unlock()
	if _, exists := m.peerHandlers[pattern]; exists {
		return ErrConflict
	}
	if err := validatePeerHandlerPattern(m.peerHandlers, pattern, handler); err != nil {
		return ErrConflict
	}
	m.peerHandlers[pattern] = handler
	return nil
}

func (m *Manager) peerHTTPHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /pair", m.handlePair)
	mux.HandleFunc("GET /catalogs", m.handlePeerGetCatalogs)
	mux.HandleFunc("POST /catalogs", m.handlePeerPostCatalog)
	mux.HandleFunc("GET /catalogs/{catalogID}/updates", m.handlePeerGetUpdates)
	mux.HandleFunc("POST /catalogs/{catalogID}/updates", m.handlePeerPostUpdate)
	mux.HandleFunc("GET /collaboration/canvases", m.handlePeerGetCollaborationCanvases)
	mux.HandleFunc("POST /collaboration/canvases", m.handlePeerPostCollaborationCanvas)
	mux.HandleFunc("GET /collaboration/canvases/{canvasID}/updates", m.handlePeerGetCollaborationCanvasUpdates)
	mux.HandleFunc("POST /collaboration/canvases/{canvasID}/updates", m.handlePeerPostCollaborationCanvasUpdate)
	mux.HandleFunc("POST /collaboration/presence", m.HandlePeerCollaborationPresence)
	mux.HandleFunc("GET /collaboration/stream", m.handlePeerCollaborationStream)
	m.peerHandlersMu.Lock()
	for pattern, handler := range m.peerHandlers {
		mux.Handle(pattern, handler)
	}
	m.peerHandlersMu.Unlock()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peerID := requestPeerID(r)
		if peerID == "" {
			http.Error(w, "client identity required", http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/pair" {
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			mux.ServeHTTP(w, r)
			return
		}
		if !m.isTrusted(peerID) {
			http.Error(w, "peer is not trusted", http.StatusForbidden)
			return
		}
		m.markPeerSeen(peerID)
		mux.ServeHTTP(w, r)
	})
}

func (m *Manager) handlePair(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, peerPairRequestSize)
	var payload peerPairPayload
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		http.Error(w, "invalid pairing request", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		http.Error(w, "invalid pairing request", http.StatusBadRequest)
		return
	}
	peerID := requestPeerID(r)
	publicKey, err := base64.RawStdEncoding.DecodeString(payload.PublicKey)
	if err != nil || len(publicKey) != ed25519.PublicKeySize || Fingerprint(ed25519.PublicKey(publicKey)) != peerID || strings.ToLower(strings.TrimSpace(payload.PeerID)) != peerID {
		http.Error(w, "peer identity mismatch", http.StatusForbidden)
		return
	}
	if !validPeerEndpoint(payload.Address, payload.Port) {
		http.Error(w, "peer endpoint is invalid", http.StatusBadRequest)
		return
	}
	if err := m.consumePairingCode(strings.TrimSpace(payload.PairingCode)); err != nil {
		http.Error(w, "pairing code is invalid or expired", http.StatusForbidden)
		return
	}
	trusted, err := m.saveTrustedPeer(r.Context(), peerID, ed25519.PublicKey(publicKey), payload.Nickname, payload.Address, payload.Port)
	if err != nil {
		http.Error(w, "could not persist peer trust", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, peerPairReply{PeerID: trusted.PeerID, Nickname: m.LocalNickname(), TrustedAt: trusted.TrustedAt})
}

func (m *Manager) handlePeerGetUpdates(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > maximumUpdatePage {
			http.Error(w, "invalid page size", http.StatusBadRequest)
			return
		}
		limit = parsed
	}
	page, err := m.CatalogUpdates(r.Context(), r.PathValue("catalogID"), r.URL.Query().Get("after"), limit)
	if err != nil {
		writePeerError(w, err)
		return
	}
	type encodedUpdate struct {
		ID           string    `json:"id"`
		UpdateBase64 string    `json:"updateBase64"`
		OriginNodeID string    `json:"originNodeId"`
		CreatedAt    time.Time `json:"createdAt"`
	}
	updates := make([]encodedUpdate, 0, len(page.Updates))
	for _, update := range page.Updates {
		updates = append(updates, encodedUpdate{ID: update.ID, UpdateBase64: base64.StdEncoding.EncodeToString(update.Update), OriginNodeID: update.OriginNodeID, CreatedAt: update.CreatedAt})
	}
	writeJSON(w, http.StatusOK, map[string]any{"updates": updates, "cursor": page.Cursor, "hasMore": page.HasMore})
}

func (m *Manager) handlePeerPostUpdate(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxCatalogUpdate*2)
	var payload struct {
		UpdateBase64 string `json:"updateBase64"`
		OriginNodeID string `json:"originNodeId"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		http.Error(w, "invalid catalog update", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		http.Error(w, "invalid catalog update", http.StatusBadRequest)
		return
	}
	update, err := base64.StdEncoding.DecodeString(payload.UpdateBase64)
	if err != nil || len(update) == 0 {
		http.Error(w, "invalid catalog update", http.StatusBadRequest)
		return
	}
	originNodeID := strings.ToLower(strings.TrimSpace(payload.OriginNodeID))
	if !validNodeID(originNodeID) {
		originNodeID = requestPeerID(r)
	}
	accepted, err := m.appendCatalogUpdate(r.Context(), r.PathValue("catalogID"), update, originNodeID, false)
	if err != nil {
		writePeerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"accepted": accepted})
}

func (m *Manager) handlePeerGetCatalogs(w http.ResponseWriter, r *http.Request) {
	items, err := m.catalogMetadata(r.Context())
	if err != nil {
		writePeerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"catalogs": items})
}

func (m *Manager) handlePeerPostCatalog(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var item catalogMetadata
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&item); err != nil {
		http.Error(w, "invalid catalog metadata", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		http.Error(w, "invalid catalog metadata", http.StatusBadRequest)
		return
	}
	if err := m.upsertCatalogMetadata(r.Context(), item); err != nil {
		writePeerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"accepted": true})
}

func (m *Manager) saveTrustedPeer(ctx context.Context, peerID string, publicKey ed25519.PublicKey, nickname, address string, port int) (PeerSummary, error) {
	if !validNodeID(peerID) || len(publicKey) != ed25519.PublicKeySize || Fingerprint(publicKey) != peerID {
		return PeerSummary{}, ErrInvalid
	}
	if address != "" && !validPeerEndpoint(address, port) {
		return PeerSummary{}, ErrInvalid
	}
	now := time.Now().UTC()
	nickname = normalizeNickname(nickname)
	var row model.LANTrustedPeer
	err := m.db.WithContext(ctx).First(&row, "node_id = ?", peerID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		row = model.LANTrustedPeer{
			NodeID: peerID, PublicKey: base64.RawStdEncoding.EncodeToString(publicKey), Nickname: nickname,
			Address: address, Port: port, TrustState: "trusted", FirstSeenAt: now, LastSeenAt: now, UpdatedAt: now,
		}
		if err := m.db.WithContext(ctx).Create(&row).Error; err != nil {
			return PeerSummary{}, err
		}
	} else if err != nil {
		return PeerSummary{}, err
	} else {
		if row.PublicKey != base64.RawStdEncoding.EncodeToString(publicKey) {
			return PeerSummary{}, ErrConflict
		}
		row.Nickname, row.TrustState, row.LastSeenAt, row.UpdatedAt = nickname, "trusted", now, now
		if address != "" {
			row.Address, row.Port = address, port
		}
		if err := m.db.WithContext(ctx).Save(&row).Error; err != nil {
			return PeerSummary{}, err
		}
	}
	return PeerSummary{
		PeerID: peerID, Nickname: row.Nickname, Address: row.Address, Port: row.Port,
		TrustedAt: row.FirstSeenAt, LastSeen: row.LastSeenAt,
	}, nil
}

func (m *Manager) isTrusted(peerID string) bool {
	if m == nil || !validNodeID(peerID) {
		return false
	}
	var count int64
	return m.db.Model(&model.LANTrustedPeer{}).Where("node_id = ? AND trust_state = ?", peerID, "trusted").Count(&count).Error == nil && count == 1
}

func (m *Manager) markPeerSeen(peerID string) {
	_ = m.db.Model(&model.LANTrustedPeer{}).Where("node_id = ? AND trust_state = ?", peerID, "trusted").Update("last_seen_at", time.Now().UTC()).Error
}

func (m *Manager) runDiscovery(ctx context.Context) {
	for ctx.Err() == nil {
		Browse(ctx, func(peer Peer) {
			if peer.ID == m.NodeID() {
				return
			}
			if m.recordDiscoveredPeer(peer, time.Now()) && m.isTrusted(peer.ID) {
				for _, discovered := range m.DiscoveredPeers() {
					if discovered.ID != peer.ID {
						continue
					}
					for _, address := range discovered.Addresses {
						m.scheduleCatalogSync(peer.ID, address, discovered.Port)
					}
					break
				}
			}
		})
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

func (m *Manager) recordDiscoveredPeer(peer Peer, now time.Time) bool {
	m.discoveryMu.Lock()
	defer m.discoveryMu.Unlock()
	if m.discoveredAddressAt == nil {
		m.discoveredAddressAt = make(map[string]map[string]time.Time)
	}
	previous, existed := m.discovered[peer.ID]
	reset := !existed || m.discoveredAddressAt[peer.ID] == nil || previous.Port != peer.Port || now.Sub(m.discoveredAt[peer.ID]) > discoveredPeerTTL
	if reset {
		previous = peer
		previous.Addresses = nil
		m.discoveredAddressAt[peer.ID] = make(map[string]time.Time)
	} else {
		active := make([]string, 0, len(previous.Addresses))
		for _, address := range previous.Addresses {
			if now.Sub(m.discoveredAddressAt[peer.ID][address]) <= discoveredPeerTTL {
				active = append(active, address)
			} else {
				delete(m.discoveredAddressAt[peer.ID], address)
			}
		}
		previous.Addresses = active
	}
	changed := reset
	if peer.Name != "" {
		previous.Name = peer.Name
	}
	previous.Port = peer.Port
	previous.Version = peer.Version
	for _, address := range peer.Addresses {
		if !isPrivateLANAddress(net.ParseIP(address)) {
			continue
		}
		if _, known := m.discoveredAddressAt[peer.ID][address]; !known {
			previous.Addresses = append(previous.Addresses, address)
			changed = true
		}
		m.discoveredAddressAt[peer.ID][address] = now
	}
	if len(previous.Addresses) == 0 {
		return false
	}
	m.discovered[peer.ID] = previous
	m.discoveredAt[peer.ID] = now
	return changed
}

func (m *Manager) runCatalogSyncRefresh(ctx context.Context) {
	ticker := time.NewTicker(catalogSyncInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.scheduleKnownCatalogSync()
		}
	}
}

func (m *Manager) scheduleKnownCatalogSync() {
	if m == nil {
		return
	}
	trusted, err := m.TrustedPeers(context.Background())
	if err != nil {
		return
	}
	discoveredPeers := m.DiscoveredPeers()
	discovered := make(map[string]Peer, len(discoveredPeers))
	for _, peer := range discoveredPeers {
		discovered[peer.ID] = peer
	}
	for _, peer := range trusted {
		if item, ok := discovered[peer.PeerID]; ok && len(item.Addresses) > 0 {
			for _, address := range item.Addresses {
				m.scheduleCatalogSync(peer.PeerID, address, item.Port)
			}
		} else if peer.Address != "" && peer.Port > 0 {
			m.scheduleCatalogSync(peer.PeerID, peer.Address, peer.Port)
		}
	}
}

func (m *Manager) scheduleCatalogSync(peerID, address string, port int) {
	peerID = strings.ToLower(strings.TrimSpace(peerID))
	parsed := net.ParseIP(strings.TrimSpace(address))
	if m == nil || !validNodeID(peerID) || parsed == nil || parsed.To4() == nil || !isPrivateLANAddress(parsed) || port < 1 || port > 65535 || !m.isTrusted(peerID) {
		return
	}
	m.syncMu.Lock()
	if m.syncClosed {
		m.syncMu.Unlock()
		return
	}
	endpoint := catalogSyncEndpoint{address: parsed.String(), port: port}
	if running, ok := m.syncJobs[peerID]; ok {
		running.enqueue(endpoint)
		m.syncMu.Unlock()
		return
	}
	job := &catalogSyncJob{current: endpoint}
	m.syncJobs[peerID] = job
	m.syncWG.Add(1)
	m.syncMu.Unlock()
	go func() {
		defer m.syncWG.Done()
		for {
			m.syncMu.Lock()
			endpoint := job.current
			ctx, cancel := context.WithTimeout(m.syncContext, 2*time.Minute)
			job.cancel = cancel
			m.syncMu.Unlock()
			err := m.syncPeerCatalogs(ctx, peerID, endpoint.address, endpoint.port)
			cancel()
			m.syncMu.Lock()
			job.cancel = nil
			if !m.syncClosed && job.nextAfter(err) {
				m.syncMu.Unlock()
				continue
			}
			delete(m.syncJobs, peerID)
			m.syncMu.Unlock()
			return
		}
	}()
}

func (m *Manager) syncPeerCatalogs(ctx context.Context, peerID, address string, port int) error {
	parsed := net.ParseIP(strings.TrimSpace(address))
	if !validNodeID(peerID) || !m.isTrusted(peerID) || parsed == nil || parsed.To4() == nil || !isPrivateLANAddress(parsed) || port < 1 || port > 65535 {
		return ErrPeerNotTrusted
	}
	config, err := m.identity.ClientTLSConfig(peerID)
	if err != nil {
		return err
	}
	transport := &http.Transport{TLSClientConfig: config, ForceAttemptHTTP2: false}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 20 * time.Second}
	baseURL := "https://" + net.JoinHostPort(parsed.String(), fmt.Sprint(port))
	var remote struct {
		Catalogs []catalogMetadata `json:"catalogs"`
	}
	if err := requestPeerJSON(ctx, client, http.MethodGet, baseURL+"/catalogs", nil, &remote); err != nil {
		return err
	}
	if err := m.rememberPeerEndpoint(ctx, peerID, parsed.String(), port); err != nil {
		return err
	}
	for _, catalog := range remote.Catalogs {
		if err := m.upsertCatalogMetadata(ctx, catalog); err != nil {
			return err
		}
	}
	localCatalogs, err := m.catalogMetadata(ctx)
	if err != nil {
		return err
	}
	for _, catalog := range localCatalogs {
		var reply map[string]bool
		if err := requestPeerJSON(ctx, client, http.MethodPost, baseURL+"/catalogs", catalog, &reply); err != nil {
			return err
		}
	}
	for _, catalog := range localCatalogs {
		if err := m.pullPeerCatalogUpdates(ctx, client, baseURL, peerID, catalog.ID); err != nil {
			return err
		}
		if err := m.pushPeerCatalogUpdates(ctx, client, baseURL, catalog.ID); err != nil {
			return err
		}
	}
	return m.syncPeerCollaborationCanvases(ctx, client, baseURL, peerID)
}

func (m *Manager) rememberPeerEndpoint(ctx context.Context, peerID, address string, port int) error {
	if !m.isTrusted(peerID) {
		return ErrPeerNotTrusted
	}
	result := m.db.WithContext(ctx).Model(&model.LANTrustedPeer{}).Where("node_id = ? AND trust_state = ?", peerID, "trusted").Updates(map[string]any{
		"address": address, "port": port, "last_seen_at": time.Now().UTC(),
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrPeerNotTrusted
	}
	return nil
}

func (m *Manager) pullPeerCatalogUpdates(ctx context.Context, client *http.Client, baseURL, peerID, catalogID string) error {
	after := ""
	for {
		endpoint := baseURL + "/catalogs/" + url.PathEscape(catalogID) + "/updates?limit=" + strconv.Itoa(syncUpdatePageSize)
		if after != "" {
			endpoint += "&after=" + url.QueryEscape(after)
		}
		var page struct {
			Updates []struct {
				UpdateBase64 string `json:"updateBase64"`
				OriginNodeID string `json:"originNodeId"`
			} `json:"updates"`
			Cursor  string `json:"cursor"`
			HasMore bool   `json:"hasMore"`
		}
		if err := requestPeerJSON(ctx, client, http.MethodGet, endpoint, nil, &page); err != nil {
			return err
		}
		for _, item := range page.Updates {
			update, err := base64.StdEncoding.DecodeString(item.UpdateBase64)
			if err != nil || len(update) == 0 {
				return ErrInvalid
			}
			origin := strings.ToLower(strings.TrimSpace(item.OriginNodeID))
			if !validNodeID(origin) {
				origin = peerID
			}
			if _, err := m.appendCatalogUpdate(ctx, catalogID, update, origin, false); err != nil {
				return err
			}
		}
		if !page.HasMore {
			return nil
		}
		if page.Cursor == "" || page.Cursor == after {
			return ErrConflict
		}
		after = page.Cursor
	}
}

func (m *Manager) pushPeerCatalogUpdates(ctx context.Context, client *http.Client, baseURL, catalogID string) error {
	after := ""
	for {
		page, err := m.CatalogUpdates(ctx, catalogID, after, syncUpdatePageSize)
		if err != nil {
			return err
		}
		for _, item := range page.Updates {
			payload := map[string]string{
				"updateBase64": base64.StdEncoding.EncodeToString(item.Update),
				"originNodeId": item.OriginNodeID,
			}
			var reply map[string]bool
			endpoint := baseURL + "/catalogs/" + url.PathEscape(catalogID) + "/updates"
			if err := requestPeerJSON(ctx, client, http.MethodPost, endpoint, payload, &reply); err != nil {
				return err
			}
		}
		if !page.HasMore {
			return nil
		}
		if page.Cursor == "" || page.Cursor == after {
			return ErrConflict
		}
		after = page.Cursor
	}
}

func requestPeerJSON(ctx context.Context, client *http.Client, method, endpoint string, payload, result any) error {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = strings.NewReader(string(encoded))
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return fmt.Errorf("trusted peer returned HTTP %d", response.StatusCode)
	}
	if result == nil {
		_, err := io.Copy(io.Discard, io.LimitReader(response.Body, maxPeerJSONResponse))
		return err
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxPeerJSONResponse+1))
	return decoder.Decode(result)
}

func (m *Manager) setPairingCode(code string, expires time.Time) {
	m.pairMu.Lock()
	m.pairingCode, m.pairingUntil = code, expires
	m.pairMu.Unlock()
}

func (m *Manager) clearPairingCode() {
	m.pairMu.Lock()
	m.pairingCode, m.pairingUntil = "", time.Time{}
	m.pairMu.Unlock()
}

func (m *Manager) hasActivePairingCode() bool {
	m.pairMu.Lock()
	defer m.pairMu.Unlock()
	return m.pairingCode != "" && time.Now().Before(m.pairingUntil)
}

func (m *Manager) consumePairingCode(code string) error {
	m.pairMu.Lock()
	defer m.pairMu.Unlock()
	if code == "" || m.pairingCode == "" || !time.Now().Before(m.pairingUntil) || !constantTimeStringEqual(code, m.pairingCode) {
		return ErrPairingCode
	}
	m.pairingCode, m.pairingUntil = "", time.Time{}
	return nil
}

func newPairingCode() (string, error) {
	var bytes [7]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return strings.TrimRight(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(bytes[:]), "="), nil
}

func requestPeerID(r *http.Request) string {
	if r == nil || r.TLS == nil || len(r.TLS.PeerCertificates) != 1 {
		return ""
	}
	publicKey, ok := r.TLS.PeerCertificates[0].PublicKey.(ed25519.PublicKey)
	if !ok {
		return ""
	}
	return Fingerprint(publicKey)
}

func validNodeID(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}

func validPeerEndpoint(address string, port int) bool {
	parsed := net.ParseIP(strings.TrimSpace(address))
	return parsed != nil && parsed.To4() != nil && isPrivateLANAddress(parsed) && port >= 1 && port <= 65535
}

func normalizeNickname(value string) string {
	value = strings.TrimSpace(value)
	if len([]rune(value)) > 80 {
		value = string([]rune(value)[:80])
	}
	return value
}

func systemNickname() string {
	name, err := os.Hostname()
	if err != nil {
		return "BeefTV"
	}
	name = normalizeNickname(name)
	if name == "" {
		return "BeefTV"
	}
	return name
}

func sortPeers(peers []Peer) {
	sort.Slice(peers, func(i, j int) bool { return peers[i].ID < peers[j].ID })
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writePeerError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, ErrInvalid), errors.Is(err, ErrCatalogUpdateBig):
		status = http.StatusBadRequest
	case errors.Is(err, ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, ErrConflict):
		status = http.StatusConflict
	case errors.Is(err, ErrPeerNotTrusted), errors.Is(err, ErrPairingCode):
		status = http.StatusForbidden
	}
	http.Error(w, http.StatusText(status), status)
}

func constantTimeStringEqual(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	var diff byte
	for i := range left {
		diff |= left[i] ^ right[i]
	}
	return diff == 0
}

type peerPairPayload struct {
	PeerID      string `json:"peerId"`
	PublicKey   string `json:"publicKey"`
	PairingCode string `json:"pairingCode"`
	Nickname    string `json:"nickname"`
	Address     string `json:"address"`
	Port        int    `json:"port"`
}

type peerPairReply struct {
	PeerID    string    `json:"peerId"`
	Nickname  string    `json:"nickname,omitempty"`
	TrustedAt time.Time `json:"trustedAt"`
}

func pairURL(address net.IP, port int) string {
	return "https://" + net.JoinHostPort(address.String(), fmt.Sprint(port)) + "/pair"
}

func validatePeerHandlerPattern(existing map[string]http.Handler, pattern string, handler http.Handler) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrConflict
		}
	}()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /pair", func(http.ResponseWriter, *http.Request) {})
	mux.HandleFunc("GET /catalogs", func(http.ResponseWriter, *http.Request) {})
	mux.HandleFunc("POST /catalogs", func(http.ResponseWriter, *http.Request) {})
	mux.HandleFunc("GET /catalogs/{catalogID}/updates", func(http.ResponseWriter, *http.Request) {})
	mux.HandleFunc("POST /catalogs/{catalogID}/updates", func(http.ResponseWriter, *http.Request) {})
	mux.HandleFunc("GET /collaboration/canvases", func(http.ResponseWriter, *http.Request) {})
	mux.HandleFunc("POST /collaboration/canvases", func(http.ResponseWriter, *http.Request) {})
	mux.HandleFunc("GET /collaboration/canvases/{canvasID}/updates", func(http.ResponseWriter, *http.Request) {})
	mux.HandleFunc("POST /collaboration/canvases/{canvasID}/updates", func(http.ResponseWriter, *http.Request) {})
	mux.HandleFunc("POST /collaboration/presence", func(http.ResponseWriter, *http.Request) {})
	mux.HandleFunc("GET /collaboration/stream", func(http.ResponseWriter, *http.Request) {})
	for registeredPattern, registeredHandler := range existing {
		mux.Handle(registeredPattern, registeredHandler)
	}
	mux.Handle(pattern, handler)
	return nil
}
