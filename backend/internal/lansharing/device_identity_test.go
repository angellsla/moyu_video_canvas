package lansharing

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"infinite-canvas/backend/internal/model"
)

const testSystemUUID = "00112233-4455-6677-8899-aabbccddeeff"

func TestHardwareNodeIDCanonicalizesUUID(t *testing.T) {
	want := sha256.Sum256([]byte(testSystemUUID))
	for _, input := range []string{testSystemUUID, strings.ToUpper(testSystemUUID), " {" + strings.ToUpper(testSystemUUID) + "} \n"} {
		id, err := hardwareNodeID(input)
		if err != nil || id != hex.EncodeToString(want[:]) {
			t.Fatalf("UUID normalization or hashing failed: %v", err)
		}
	}
	for _, input := range []string{"", "invalid", "00000000-0000-0000-0000-000000000000", "ffffffff-ffff-ffff-ffff-ffffffffffff"} {
		if _, err := hardwareNodeID(input); err == nil {
			t.Fatal("invalid SMBIOS UUID accepted")
		}
	}
}

func TestSMBIOSUUIDByteOrderAndMalformedTables(t *testing.T) {
	for _, version := range [][2]byte{{2, 5}, {2, 6}, {3, 7}} {
		// Skip a type-0 record with a string, then read the type-1 system UUID.
		table := append([]byte{0, 4, 0, 0, 'x', 0, 0}, make([]byte, 26)...)
		system := table[7:]
		system[0], system[1] = 1, 24
		canonical, _ := hex.DecodeString("00112233445566778899aabbccddeeff")
		copy(system[8:24], canonical)
		if version[0] > 2 || version[1] >= 6 {
			copy(system[8:16], []byte{0x33, 0x22, 0x11, 0x00, 0x55, 0x44, 0x77, 0x66})
		}
		raw := append([]byte{0, version[0], version[1], 0, 0, 0, 0, 0}, table...)
		binary.LittleEndian.PutUint32(raw[4:8], uint32(len(table)))
		got, err := systemUUIDFromSMBIOS(raw)
		if err != nil || got != testSystemUUID {
			t.Fatalf("SMBIOS version %v: unexpected UUID, err=%v", version, err)
		}
		for end := 0; end < len(raw); end++ {
			if _, err := systemUUIDFromSMBIOS(raw[:end]); err == nil {
				t.Fatalf("truncated table accepted at %d", end)
			}
		}
	}
}

func TestDeviceIdentityMigratesKeyAndSurvivesWorkspaceChange(t *testing.T) {
	workspace, documents := t.TempDir(), filepath.Join(t.TempDir(), "MoyuCanvas")
	legacy, err := LoadIdentity(workspace)
	if err != nil {
		t.Fatal(err)
	}
	first, err := loadDeviceIdentityAt(workspace, documents, testSystemUUID)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := hardwareNodeID(testSystemUUID)
	if first.ID() != want || !bytes.Equal(first.privateKey, legacy.privateKey) {
		t.Fatal("migration lost the key or used the legacy ID")
	}
	second, err := loadDeviceIdentityAt(t.TempDir(), documents, testSystemUUID)
	if err != nil || second.ID() != first.ID() || !bytes.Equal(first.privateKey, second.privateKey) {
		t.Fatalf("workspace change lost device identity: %v", err)
	}
	stored, err := os.ReadFile(filepath.Join(documents, identityFileName))
	if err != nil || bytes.Contains(stored, []byte(base64.RawStdEncoding.EncodeToString(first.privateKey))) {
		t.Fatal("identity file missing or contains plaintext key")
	}
	changedBoard, err := loadDeviceIdentityAt(t.TempDir(), documents, "11112233-4455-6677-8899-aabbccddeeff")
	if err != nil || changedBoard.ID() == first.ID() || !bytes.Equal(changedBoard.privateKey, first.privateKey) {
		t.Fatal("motherboard change should update the device ID and retain the signing key")
	}
	if err := os.Remove(filepath.Join(documents, ".settings-key")); err != nil {
		t.Fatal(err)
	}
	if _, err := loadDeviceIdentityAt(t.TempDir(), documents, testSystemUUID); err == nil {
		t.Fatal("missing encryption key was silently replaced")
	}
}

func TestPairedPeerCanRotateLostDeviceKeyOnlyWithPairingProof(t *testing.T) {
	_, manager := newTestManager(t)
	_, oldKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	peerID := manager.NodeID()
	if _, err := manager.saveTrustedPeer(context.Background(), peerID, oldKey.Public().(ed25519.PublicKey), "peer", "", 0); err != nil {
		t.Fatal(err)
	}
	_, newKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.saveTrustedPeer(context.Background(), peerID, newKey.Public().(ed25519.PublicKey), "peer", "", 0); err == nil {
		t.Fatal("unverified key rotation accepted")
	}
	updated, err := manager.savePairedPeer(context.Background(), peerID, newKey.Public().(ed25519.PublicKey), "peer", "", 0, true)
	if err != nil || updated.PeerID != peerID || !manager.isTrustedIdentity(peerID, newKey.Public().(ed25519.PublicKey)) {
		t.Fatalf("verified key rotation failed: %#v, %v", updated, err)
	}
}

func TestDeviceIDRecreatedFromSameHardwareHasIndependentSecret(t *testing.T) {
	first, err := loadDeviceIdentityAt(t.TempDir(), t.TempDir(), testSystemUUID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadDeviceIdentityAt(t.TempDir(), t.TempDir(), testSystemUUID)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID() != second.ID() || bytes.Equal(first.privateKey, second.privateKey) {
		t.Fatal("hardware identity must be stable while signing keys remain random")
	}
}

func TestDeviceMigrationPreservesLocalAndRemoteCanvasOwnership(t *testing.T) {
	db, manager := newTestManager(t)
	ctx := context.Background()
	// Recreate the pre-upgrade identity record while retaining the old key file.
	manager.identity.nodeID = ""
	if err := db.Model(&model.LANNodeIdentity{}).Where("1 = 1").Update("node_id", manager.NodeID()).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SetLocalNickname(ctx, "Studio"); err != nil {
		t.Fatal(err)
	}
	catalog, err := manager.CreateCatalog(ctx, "Assets")
	if err != nil {
		t.Fatal(err)
	}
	canvas, err := manager.CreateCollaborationCanvas(ctx, "Scene", catalog.ID, []byte{1, 2, 3})
	if err != nil {
		t.Fatal(err)
	}
	_, peer := newTestManager(t)
	if _, err := peer.saveTrustedPeer(ctx, manager.NodeID(), manager.identity.PublicKey(), "Studio", "", 0); err != nil {
		t.Fatal(err)
	}
	remote := model.LANCollaborationCanvas{ID: canvas.ID, Name: canvas.Name, CreatedByNodeID: manager.NodeID(), MetadataRevision: 1}
	if err := peer.db.Create(&remote).Error; err != nil {
		t.Fatal(err)
	}
	identity, err := loadDeviceIdentityAt(manager.dataDir, t.TempDir(), testSystemUUID)
	if err != nil {
		t.Fatal(err)
	}
	upgraded, err := NewManager(manager.dataDir, db, identity)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close(ctx)
	var stored model.LANCollaborationCanvas
	if err := db.First(&stored, "id = ?", canvas.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.CreatedByNodeID != upgraded.NodeID() || upgraded.LocalNickname() != "Studio" {
		t.Fatal("local migration lost ownership or nickname")
	}
	if _, err := peer.saveTrustedPeer(ctx, upgraded.NodeID(), identity.PublicKey(), "Studio", "", 0); err != nil {
		t.Fatal(err)
	}
	if err := peer.db.First(&remote, "id = ?", canvas.ID).Error; err != nil {
		t.Fatal(err)
	}
	if remote.CreatedByNodeID != upgraded.NodeID() || !peer.isTrustedIdentity(upgraded.NodeID(), identity.PublicKey()) {
		t.Fatal("paired peer did not migrate ownership and trust")
	}
	var update model.LANCollaborationCanvasYjsUpdate
	if err := db.First(&update, "canvas_id = ?", canvas.ID).Error; err != nil {
		t.Fatal(err)
	}
	if update.OriginNodeID != upgraded.NodeID() {
		t.Fatal("update origin not migrated")
	}
	changedBoard, err := loadDeviceIdentityAt(manager.dataDir, t.TempDir(), "11112233-4455-6677-8899-aabbccddeeff")
	if err != nil {
		t.Fatal(err)
	}
	boardChange, err := NewManager(manager.dataDir, db, changedBoard)
	if err != nil {
		t.Fatalf("migrate after motherboard UUID change: %v", err)
	}
	defer boardChange.Close(ctx)
	if err := db.First(&stored, "id = ?", canvas.ID).Error; err != nil || stored.CreatedByNodeID != boardChange.NodeID() {
		t.Fatal("motherboard UUID change did not migrate local canvas ownership")
	}
}

func TestHardwareIDCannotReplacePinnedTLSKey(t *testing.T) {
	_, serverManager := newTestManager(t)
	_, clientManager := newTestManager(t)
	ctx := context.Background()
	if _, err := serverManager.saveTrustedPeer(ctx, clientManager.NodeID(), clientManager.identity.PublicKey(), "client", "", 0); err != nil {
		t.Fatal(err)
	}
	serverTLS, err := serverManager.identity.ServerTLSConfig(serverManager.isTrustedIdentity)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(serverManager.peerHTTPHandler())
	server.TLS = serverTLS
	server.StartTLS()
	defer server.Close()
	_, fakeKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	spoof := &Identity{nodeID: clientManager.NodeID(), privateKey: fakeKey}
	config, err := spoof.ClientTLSConfig(serverManager.NodeID(), Fingerprint(serverManager.identity.PublicKey()))
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: config}}
	defer client.CloseIdleConnections()
	if response, err := client.Get(server.URL + "/catalogs"); err == nil {
		response.Body.Close()
		t.Fatal("spoofed hardware ID passed mutual TLS")
	}
	// During pairing the handshake accepts unknown keys, but normal routes must not.
	certificate, err := spoof.TLSCertificate()
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/catalogs", nil)
	request.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{certificate.Leaf}}
	recorder := httptest.NewRecorder()
	serverManager.peerHTTPHandler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("spoofed request status = %d", recorder.Code)
	}
	valid, err := clientManager.identity.ClientTLSConfig(serverManager.NodeID(), Fingerprint(fakeKey.Public().(ed25519.PublicKey)))
	if err != nil {
		t.Fatal(err)
	}
	if err := valid.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{server.TLS.Certificates[0].Leaf}}); err == nil {
		t.Fatal("client accepted server with wrong pinned key")
	}
}
