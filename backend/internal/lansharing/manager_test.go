package lansharing

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net/http"
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
	if err := manager.consumePairingCode("PAIRING-CODE"); !errors.Is(err, ErrPairingCode) {
		t.Fatalf("reused code error = %v", err)
	}
	manager.setPairingCode("EXPIRED", time.Now().Add(-time.Second))
	if err := manager.consumePairingCode("EXPIRED"); !errors.Is(err, ErrPairingCode) {
		t.Fatalf("expired code error = %v", err)
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
