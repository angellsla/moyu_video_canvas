package lansharing

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"infinite-canvas/backend/internal/kernel"
	"infinite-canvas/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestPairingCodeExpiresAndIsConsumedOnce(t *testing.T) {
	manager := &Manager{}
	manager.setPairingCode("PAIRING-CODE", time.Now().Add(time.Minute))
	if err := manager.consumePairingCode("wrong"); !errors.Is(err, ErrPairingCode) {
		t.Fatalf("wrong code error = %v", err)
	}
	if err := manager.consumePairingCode("PAIRING-CODE"); err != nil {
		t.Fatalf("consume valid code: %v", err)
	}
	if got := manager.pairingCodeForProbe(); got != "PAIRING-CODE" {
		t.Fatalf("consumed code must remain available for trusted retry, got %q", got)
	}
	if err := manager.consumePairingCode("PAIRING-CODE"); !errors.Is(err, ErrPairingCode) {
		t.Fatalf("reused code error = %v", err)
	}
	manager.setPairingCode("EXPIRED", time.Now().Add(-time.Second))
	if err := manager.consumePairingCode("EXPIRED"); !errors.Is(err, ErrPairingCode) {
		t.Fatalf("expired code error = %v", err)
	}
	if got := manager.pairingCodeForProbe(); got != "" {
		t.Fatalf("expired code should not answer probes, got %q", got)
	}
}

func TestPairRequestRejectsInvalidDiscoveryFields(t *testing.T) {
	_, manager := newTestManager(t)
	for _, request := range []PairRequest{
		{Address: "192.168.1.20", PairingCode: "ONE-TIME", PeerID: strings.Repeat("a", 64)},
		{Address: "192.168.1.20", PairingCode: "ONE-TIME", Port: -1},
	} {
		if _, err := manager.PairPeer(context.Background(), request); !errors.Is(err, ErrInvalid) {
			t.Fatalf("partial discovery fields accepted: %#v, err=%v", request, err)
		}
	}
}

func TestManualPairingWithPortWorksWithoutUDPDiscovery(t *testing.T) {
	if _, err := FindPrivateEndpoints(); err != nil {
		t.Skipf("private LAN adapter unavailable: %v", err)
	}
	var blocker *net.UDPConn
	for attempt := 0; attempt < 20; attempt++ {
		var err error
		blocker, err = net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: broadcastDiscoveryPort})
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if blocker == nil {
		t.Skip("UDP discovery port is occupied by another process")
	}
	defer blocker.Close()
	_, initiator := newTestManager(t)
	_, recipient := newTestManager(t)
	recipientStatus, err := recipient.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := initiator.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	address, portText, err := net.SplitHostPort(recipientStatus.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	request := PairRequest{Address: address, Port: port, PairingCode: "WRONG"}
	if _, err := initiator.PairPeer(context.Background(), request); !errors.Is(err, ErrPairingCode) {
		t.Fatalf("incorrect direct pairing code error = %v", err)
	}
	if recipient.isTrusted(initiator.NodeID()) {
		t.Fatal("wrong direct pairing code established trust")
	}
	request.PairingCode = recipientStatus.PairingCode
	paired, err := initiator.PairPeer(context.Background(), request)
	if err != nil {
		t.Fatalf("manual direct pair: %v", err)
	}
	if paired.PeerID != recipient.NodeID() || !recipient.isTrusted(initiator.NodeID()) || !initiator.isTrusted(recipient.NodeID()) {
		t.Fatalf("direct pairing trust mismatch: %#v", paired)
	}
}

func TestManualPairingTrustsBothPeersAndRepairsOneSidedTrust(t *testing.T) {
	if _, err := FindPrivateEndpoints(); err != nil {
		t.Skipf("private LAN adapter unavailable: %v", err)
	}
	initiatorDB, initiator := newTestManager(t)
	_, recipient := newTestManager(t)
	recipientStatus, err := recipient.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	address, portText, err := net.SplitHostPort(recipientStatus.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	probeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if peerID, peerPort, err := queryPairEndpoint(probeCtx, net.ParseIP(address), broadcastDiscoveryPort, recipientStatus.PairingCode); err != nil || peerID != recipient.NodeID() || peerPort != port {
		t.Fatalf("recipient probe = %s:%d, err=%v", peerID, peerPort, err)
	}
	if _, err := initiator.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	request := PairRequest{Address: address, PairingCode: recipientStatus.PairingCode}
	paired, err := initiator.PairPeer(context.Background(), request)
	if err != nil {
		t.Fatalf("manual pair: %v", err)
	}
	if paired.PeerID != recipient.NodeID() || !recipient.isTrusted(initiator.NodeID()) || !initiator.isTrusted(recipient.NodeID()) {
		t.Fatalf("trust mismatch: paired=%s, initiator trusts=%t, recipient trusts=%t", paired.PeerID, initiator.isTrusted(recipient.NodeID()), recipient.isTrusted(initiator.NodeID()))
	}
	if recipient.hasActivePairingCode() {
		t.Fatal("recipient pairing code should be consumed")
	}

	// A prior client could lose the reply after the recipient persisted trust.
	if err := initiatorDB.Where("node_id = ?", recipient.NodeID()).Delete(&model.LANTrustedPeer{}).Error; err != nil {
		t.Fatal(err)
	}
	if initiator.isTrusted(recipient.NodeID()) {
		t.Fatal("test setup should leave trust only on the recipient")
	}
	if _, err := initiator.PairPeer(context.Background(), request); err != nil {
		t.Fatalf("repair one-sided trust after code consumption: %v", err)
	}
	if !initiator.isTrusted(recipient.NodeID()) {
		t.Fatal("initiator trust was not repaired")
	}
	if _, err := initiator.PairPeer(context.Background(), PairRequest{
		PeerID: recipient.NodeID(), Address: address, Port: port, PairingCode: recipientStatus.PairingCode,
	}); err != nil {
		t.Fatalf("discovered peer pairing path: %v", err)
	}
	if err := recipient.RevokePeer(context.Background(), initiator.NodeID()); err != nil {
		t.Fatal(err)
	}
	if _, err := initiator.PairPeer(context.Background(), request); err == nil {
		t.Fatal("revoked peer should not reconnect with a consumed pairing code")
	}
}

func TestRegisterPeerHandlerAcceptsMethodPatternAndReservesBuiltins(t *testing.T) {
	manager := &Manager{peerHandlers: make(map[string]http.Handler)}
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	if err := manager.RegisterPeerHandler("GET /catalogs/{catalogID}/assets/{contentHash}", handler); err != nil {
		t.Fatalf("register LAN asset route: %v", err)
	}
	if err := manager.RegisterPeerHandler("POST /pair", handler); !errors.Is(err, ErrInvalid) {
		t.Fatalf("reserved pairing route error = %v", err)
	}
	if err := manager.RegisterPeerHandler("GET /pair/probe", handler); !errors.Is(err, ErrInvalid) {
		t.Fatalf("reserved pairing proof route error = %v", err)
	}
	if err := manager.RegisterPeerHandler("GET /collaboration/canvases", handler); !errors.Is(err, ErrConflict) {
		t.Fatalf("built-in collaboration route error = %v", err)
	}
}

func TestLocalNicknamePersistsAndBlankUsesSystemName(t *testing.T) {
	db, manager := newTestManager(t)
	if manager.LocalNickname() != systemNickname() {
		t.Fatalf("default nickname = %q, want system name %q", manager.LocalNickname(), systemNickname())
	}

	status, err := manager.SetLocalNickname(context.Background(), "  Studio Editor  ")
	if err != nil {
		t.Fatalf("set local nickname: %v", err)
	}
	if status.Nickname != "Studio Editor" || manager.LocalNickname() != "Studio Editor" {
		t.Fatalf("nickname status=%q local=%q", status.Nickname, manager.LocalNickname())
	}
	var identity model.LANNodeIdentity
	if err := db.First(&identity, "node_id = ?", manager.NodeID()).Error; err != nil {
		t.Fatal(err)
	}
	if identity.Nickname != "Studio Editor" {
		t.Fatalf("persisted nickname = %q", identity.Nickname)
	}

	status, err = manager.SetLocalNickname(context.Background(), "  ")
	if err != nil {
		t.Fatalf("clear local nickname: %v", err)
	}
	if status.Nickname != systemNickname() || manager.LocalNickname() != systemNickname() {
		t.Fatalf("blank nickname status=%q local=%q, want system fallback %q", status.Nickname, manager.LocalNickname(), systemNickname())
	}
	if err := db.First(&identity, "node_id = ?", manager.NodeID()).Error; err != nil {
		t.Fatal(err)
	}
	if identity.Nickname != "" {
		t.Fatalf("cleared nickname should persist blank, got %q", identity.Nickname)
	}
}

func TestRoutedLocalEndpointUsesMatchingPrivateListener(t *testing.T) {
	available, err := FindPrivateEndpoints()
	if err != nil {
		t.Skipf("private LAN adapters unavailable: %v", err)
	}
	const port = 42317
	listeners := make([]string, 0, len(available))
	for _, endpoint := range available {
		listeners = append(listeners, net.JoinHostPort(endpoint.Address.String(), strconv.Itoa(port)))
	}
	for _, endpoint := range available {
		address, selectedPort, err := routedLocalEndpoint(endpoint.Address, port, listeners)
		if err != nil {
			t.Fatalf("select route to %s: %v", endpoint.Address, err)
		}
		if address != endpoint.Address.String() || selectedPort != port {
			t.Fatalf("route to %s selected %s:%d, want same interface and port %d", endpoint.Address, address, selectedPort, port)
		}
	}
	if _, _, err := routedLocalEndpoint(available[0].Address, port, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("route without a bound listener error = %v, want invalid endpoint", err)
	}
}

func TestDiscoveredPeerAggregatesInterfacesWithoutDuplicateAddresses(t *testing.T) {
	manager := &Manager{discovered: make(map[string]Peer), discoveredAt: make(map[string]time.Time)}
	now := time.Now()
	id := strings.Repeat("a", 64)
	first := Peer{ID: id, Name: "Studio", Addresses: []string{"192.168.4.22"}, Port: 42317, Version: protocol}
	second := Peer{ID: id, Name: "Studio", Addresses: []string{"10.20.30.40"}, Port: 42317, Version: protocol}
	if !manager.recordDiscoveredPeer(first, now) || !manager.recordDiscoveredPeer(second, now.Add(time.Second)) {
		t.Fatal("new addresses on separate adapters should change the discovered endpoint")
	}
	if manager.recordDiscoveredPeer(second, now.Add(2*time.Second)) {
		t.Fatal("repeated announcement of the same address should not change the endpoint")
	}
	peers := manager.DiscoveredPeers()
	if len(peers) != 1 || peers[0].ID != id || peers[0].Port != 42317 {
		t.Fatalf("discovered peers = %#v, want one aggregated node", peers)
	}
	if len(peers[0].Addresses) != 2 || !slices.Contains(peers[0].Addresses, first.Addresses[0]) || !slices.Contains(peers[0].Addresses, second.Addresses[0]) {
		t.Fatalf("discovered addresses = %v, want both adapter addresses once", peers[0].Addresses)
	}
}

func TestDiscoveredPeerExpiresEachInterfaceAddressSeparately(t *testing.T) {
	manager := &Manager{discovered: make(map[string]Peer), discoveredAt: make(map[string]time.Time)}
	now := time.Now()
	id := strings.Repeat("b", 64)
	first := Peer{ID: id, Addresses: []string{"192.168.4.22"}, Port: 42317, Version: protocol}
	second := Peer{ID: id, Addresses: []string{"10.20.30.40"}, Port: 42317, Version: protocol}
	manager.recordDiscoveredPeer(first, now.Add(-discoveredPeerTTL-10*time.Second))
	manager.recordDiscoveredPeer(second, now.Add(-discoveredPeerTTL+15*time.Second))
	peers := manager.DiscoveredPeers()
	if len(peers) != 1 || len(peers[0].Addresses) != 1 || peers[0].Addresses[0] != second.Addresses[0] {
		t.Fatalf("discovered peers = %#v, want only the fresh adapter address", peers)
	}
	manager.recordDiscoveredPeer(first, now.Add(-discoveredPeerTTL-10*time.Second))
	peers = manager.DiscoveredPeers()
	if len(peers) != 1 || len(peers[0].Addresses) != 1 || peers[0].Addresses[0] != second.Addresses[0] {
		t.Fatalf("stale announcement should not replace fresh address: %#v", peers)
	}

	expired := &Manager{discovered: make(map[string]Peer), discoveredAt: make(map[string]time.Time)}
	expired.recordDiscoveredPeer(first, now.Add(-discoveredPeerTTL-10*time.Second))
	if peers := expired.DiscoveredPeers(); len(peers) != 0 {
		t.Fatalf("expired node still discovered: %#v", peers)
	}
}

func TestCollaborationPeerEndpointPrefersVerifiedAddressAcrossAdapters(t *testing.T) {
	manager := &Manager{discovered: make(map[string]Peer), discoveredAt: make(map[string]time.Time)}
	id := strings.Repeat("c", 64)
	manager.recordDiscoveredPeer(Peer{ID: id, Addresses: []string{"192.168.44.2", "10.70.60.25", "10.70.60.25"}, Port: 42100, Version: protocol}, time.Now())
	endpoints := manager.collaborationPeerEndpoints(PeerSummary{PeerID: id, Address: "10.70.60.25", Port: 42000})
	want := []collaborationEndpoint{{address: "10.70.60.25", port: 42100}, {address: "192.168.44.2", port: 42100}, {address: "10.70.60.25", port: 42000}}
	if !slices.Equal(endpoints, want) {
		t.Fatalf("ordered collaboration endpoints = %#v, want %#v", endpoints, want)
	}
	address, port := manager.collaborationPeerEndpoint(PeerSummary{PeerID: id, Address: "10.70.60.25", Port: 42000})
	if address != "10.70.60.25" || port != 42100 {
		t.Fatalf("collaboration endpoint = %s:%d, want verified address on discovered port", address, port)
	}
	address, port = manager.collaborationPeerEndpoint(PeerSummary{PeerID: id, Address: "10.70.60.99", Port: 42000})
	if address != "192.168.44.2" || port != 42100 {
		t.Fatalf("collaboration fallback = %s:%d, want first discovered address", address, port)
	}
	endpoints = manager.collaborationPeerEndpoints(PeerSummary{PeerID: id, Address: "10.70.60.99", Port: 42000})
	want = []collaborationEndpoint{{address: "192.168.44.2", port: 42100}, {address: "10.70.60.25", port: 42100}, {address: "10.70.60.99", port: 42000}}
	if !slices.Equal(endpoints, want) {
		t.Fatalf("saved-address fallback endpoints = %#v, want %#v", endpoints, want)
	}
}

func TestTrustCanBeRevokedAndCatalogUpdatesAreDeduplicated(t *testing.T) {
	db, manager := newTestManager(t)
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	peerID := Fingerprint(publicKey)
	if _, err := manager.saveTrustedPeer(context.Background(), peerID, publicKey, "studio node", "192.168.4.10", 47831); err != nil {
		t.Fatalf("save peer trust: %v", err)
	}
	if !manager.isTrusted(peerID) {
		t.Fatal("peer should be trusted after pairing")
	}
	trustedPeers, err := manager.TrustedPeers(context.Background())
	if err != nil || len(trustedPeers) != 1 || trustedPeers[0].Address != "192.168.4.10" || trustedPeers[0].Port != 47831 {
		t.Fatalf("trusted peer endpoint = %#v, err=%v", trustedPeers, err)
	}
	if err := manager.RevokePeer(context.Background(), peerID); err != nil {
		t.Fatalf("revoke peer: %v", err)
	}
	if manager.isTrusted(peerID) {
		t.Fatal("revoked peer must no longer be trusted")
	}

	catalog, err := manager.CreateCatalog(context.Background(), "shared footage")
	if err != nil {
		t.Fatalf("create catalog: %v", err)
	}
	update := []byte{1, 2, 3, 4, 5}
	accepted, err := manager.AppendCatalogUpdate(context.Background(), catalog.ID, update, manager.NodeID())
	if err != nil || !accepted {
		t.Fatalf("first update accepted=%v err=%v", accepted, err)
	}
	accepted, err = manager.AppendCatalogUpdate(context.Background(), catalog.ID, update, manager.NodeID())
	if err != nil || accepted {
		t.Fatalf("duplicate update accepted=%v err=%v", accepted, err)
	}
	page, err := manager.CatalogUpdates(context.Background(), catalog.ID, "", 10)
	if err != nil {
		t.Fatalf("list catalog updates: %v", err)
	}
	if len(page.Updates) != 1 || page.HasMore || page.Cursor == "" || string(page.Updates[0].Update) != string(update) {
		t.Fatalf("unexpected catalog update page: %#v", page)
	}
	var rows int64
	if err := db.Model(&model.LANCatalogYjsUpdate{}).Where("catalog_id = ?", catalog.ID).Count(&rows).Error; err != nil || rows != 1 {
		t.Fatalf("stored update count=%d err=%v", rows, err)
	}
}

func TestScheduledSyncDoesNotPersistUnverifiedDiscoveredEndpoint(t *testing.T) {
	db, manager := newTestManager(t)
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	peerID := Fingerprint(publicKey)
	if _, err := manager.saveTrustedPeer(context.Background(), peerID, publicKey, "studio node", "10.0.0.2", 47000); err != nil {
		t.Fatalf("save peer trust: %v", err)
	}

	manager.scheduleCatalogSync(peerID, "10.0.0.99", 9)
	var peer model.LANTrustedPeer
	if err := db.First(&peer, "node_id = ?", peerID).Error; err != nil {
		t.Fatal(err)
	}
	if peer.Address != "10.0.0.2" || peer.Port != 47000 {
		t.Fatalf("unverified endpoint persisted as %s:%d", peer.Address, peer.Port)
	}
}

func TestCatalogSyncJobTriesDistinctAddressesAfterFailure(t *testing.T) {
	first := catalogSyncEndpoint{address: "192.168.44.2", port: 42100}
	second := catalogSyncEndpoint{address: "10.70.60.25", port: 42100}
	job := &catalogSyncJob{current: first}
	job.enqueue(second)
	job.enqueue(second)
	if len(job.pending) != 1 || job.pending[0] != second {
		t.Fatalf("pending sync endpoints = %#v, want the second adapter once", job.pending)
	}
	if !job.nextAfter(errors.New("first adapter unreachable")) || job.current != second {
		t.Fatalf("failed endpoint did not fall back to second adapter: %#v", job)
	}
	if job.nextAfter(nil) {
		t.Fatal("successful sync should not repeat a queued adapter")
	}
}

func TestCatalogSyncJobRetriesCurrentAddressWhenNewWorkArrives(t *testing.T) {
	endpoint := catalogSyncEndpoint{address: "10.70.60.25", port: 42100}
	job := &catalogSyncJob{current: endpoint}
	job.enqueue(endpoint)
	if !job.nextAfter(nil) || job.current != endpoint {
		t.Fatal("new work arriving during sync should trigger another pass")
	}
	if job.nextAfter(nil) {
		t.Fatal("sync job should finish after the follow-up pass")
	}
	other := catalogSyncEndpoint{address: "192.168.44.2", port: 42100}
	job.enqueue(other)
	job.enqueue(endpoint)
	if !job.nextAfter(errors.New("current adapter unreachable")) || job.current != other {
		t.Fatal("another adapter should be tried before repeating a failed current adapter")
	}
	if !job.nextAfter(errors.New("other adapter unreachable")) || job.current != endpoint {
		t.Fatal("new work on current adapter was lost after both adapters failed")
	}
}

func newTestManager(t *testing.T) (*gorm.DB, *Manager) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+kernel.NewID()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&model.LANNodeIdentity{}, &model.LANTrustedPeer{}, &model.LANShareCatalog{}, &model.LANCatalogYjsUpdate{}, &model.LANCatalogResource{}, &model.LANCollaborationCanvas{}, &model.LANCollaborationCanvasYjsUpdate{}); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(t.TempDir(), db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = manager.Close(context.Background())
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
	})
	return db, manager
}
