package lansharing

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"infinite-canvas/backend/internal/model"
)

func TestCollaborationCanvasLifecycleAndUpdateLog(t *testing.T) {
	db, manager := newTestManager(t)
	catalog, err := manager.CreateCatalog(context.Background(), "assets")
	if err != nil {
		t.Fatal(err)
	}
	created, err := manager.CreateCollaborationCanvas(context.Background(), "  Shared scene  ", catalog.ID, []byte{1, 2})
	if err != nil {
		t.Fatalf("create collaboration canvas: %v", err)
	}
	if created.Name != "Shared scene" || created.AssetCatalogID != catalog.ID || created.CreatedByNodeID != manager.NodeID() || created.UpdateCount != 1 {
		t.Fatalf("created summary: %+v", created)
	}

	accepted, err := manager.AppendCollaborationCanvasUpdate(context.Background(), created.ID, []byte{3, 4})
	if err != nil || !accepted {
		t.Fatalf("append update accepted=%v err=%v", accepted, err)
	}
	accepted, err = manager.AppendCollaborationCanvasUpdate(context.Background(), created.ID, []byte{3, 4})
	if err != nil || accepted {
		t.Fatalf("duplicate update accepted=%v err=%v", accepted, err)
	}
	page, err := manager.CollaborationCanvasUpdates(context.Background(), created.ID, "", 1)
	if err != nil || len(page.Updates) != 1 || !page.HasMore || page.Cursor == "" {
		t.Fatalf("first update page: %+v err=%v", page, err)
	}
	second, err := manager.CollaborationCanvasUpdates(context.Background(), created.ID, page.Cursor, 1)
	if err != nil || len(second.Updates) != 1 || second.HasMore || second.Updates[0].OriginNodeID != manager.NodeID() {
		t.Fatalf("second update page: %+v err=%v", second, err)
	}

	newName := "Renamed"
	archived := true
	updated, err := manager.UpdateCollaborationCanvas(context.Background(), created.ID, &newName, &archived)
	if err != nil || updated.Name != newName || !updated.Archived {
		t.Fatalf("update metadata: %+v err=%v", updated, err)
	}
	archived = false
	updated, err = manager.UpdateCollaborationCanvas(context.Background(), created.ID, nil, &archived)
	if err != nil || updated.Archived {
		t.Fatalf("unarchive metadata: %+v err=%v", updated, err)
	}

	peerKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	peerID := Fingerprint(peerKey)
	if _, err := manager.saveTrustedPeer(context.Background(), peerID, peerKey, "peer", "192.168.1.5", 47000); err != nil {
		t.Fatal(err)
	}
	var stored model.LANCollaborationCanvas
	if err := db.First(&stored, "id = ?", created.ID).Error; err != nil {
		t.Fatal(err)
	}
	if canManageCollaborationCanvas(stored, peerID) {
		t.Fatal("trusted non-creator must not manage canvas metadata")
	}
	if !canManageCollaborationCanvas(stored, manager.NodeID()) {
		t.Fatal("creator should manage canvas metadata")
	}
	if err := manager.DeleteCollaborationCanvas(context.Background(), created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.CollaborationCanvases(context.Background()); err != nil {
		t.Fatal(err)
	}
	var deletedAt bool
	if err := db.Table("lan_collaboration_canvases").Select("deleted_at IS NOT NULL").Where("id = ?", created.ID).Scan(&deletedAt).Error; err != nil || !deletedAt {
		t.Fatalf("delete tombstone persisted=%v err=%v", deletedAt, err)
	}
	if _, err := manager.CollaborationCanvasUpdates(context.Background(), created.ID, "", 10); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted canvas update read error = %v, want ErrNotFound", err)
	}
}

func TestPeerCanOnlyReplicateItsOwnCanvasMetadata(t *testing.T) {
	db, manager := newTestManager(t)
	ownerRoom, err := manager.CreateCollaborationCanvas(context.Background(), "Owner room", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	peerPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	peerID := Fingerprint(peerPublic)
	if _, err := manager.saveTrustedPeer(context.Background(), peerID, peerPublic, "peer", "192.168.1.5", 47000); err != nil {
		t.Fatal(err)
	}
	items, err := manager.collaborationCanvasMetadata(context.Background())
	if err != nil || len(items) != 1 {
		t.Fatalf("canvas metadata = %+v, err = %v", items, err)
	}
	forged := items[0]
	forged.Name = "Forged rename"
	forged.UpdatedAt = forged.UpdatedAt.Add(time.Minute)
	deletedAt := forged.UpdatedAt
	forged.DeletedAt = &deletedAt
	post := func(item collaborationCanvasMetadata) *httptest.ResponseRecorder {
		t.Helper()
		body, marshalErr := json.Marshal(item)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		request := httptest.NewRequest(http.MethodPost, "/collaboration/canvases", strings.NewReader(string(body)))
		request.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{PublicKey: peerPublic, Subject: pkix.Name{CommonName: peerID}}}}
		response := httptest.NewRecorder()
		manager.peerHTTPHandler().ServeHTTP(response, request)
		return response
	}
	if response := post(forged); response.Code != http.StatusForbidden {
		t.Fatalf("non-creator metadata mutation status = %d, want 403", response.Code)
	}
	var unchanged model.LANCollaborationCanvas
	if err := db.First(&unchanged, "id = ?", ownerRoom.ID).Error; err != nil {
		t.Fatal(err)
	}
	if unchanged.Name != ownerRoom.Name || unchanged.DeletedAt != nil {
		t.Fatalf("non-creator changed metadata: %+v", unchanged)
	}

	peerRoom := collaborationCanvasMetadata{
		ID: "peer-room", Name: "Peer room", CreatedByNodeID: peerID, MetadataRevision: 1,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if response := post(peerRoom); response.Code != http.StatusOK {
		t.Fatalf("creator metadata creation status = %d, body = %s", response.Code, response.Body.String())
	}
	peerRoom.Name = "Peer renamed"
	peerRoom.MetadataRevision++
	peerRoom.UpdatedAt = peerRoom.UpdatedAt.Add(time.Minute)
	if response := post(peerRoom); response.Code != http.StatusOK {
		t.Fatalf("creator metadata update status = %d, body = %s", response.Code, response.Body.String())
	}
	var stored model.LANCollaborationCanvas
	if err := db.First(&stored, "id = ?", peerRoom.ID).Error; err != nil || stored.Name != peerRoom.Name {
		t.Fatalf("creator metadata not replicated: %+v, err = %v", stored, err)
	}
}

func TestCreatorMetadataRevisionBeatsNewerLocalContentTimestamp(t *testing.T) {
	db, manager := newTestManager(t)
	room, err := manager.CreateCollaborationCanvas(context.Background(), "Before", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	var stored model.LANCollaborationCanvas
	if err := db.First(&stored, "id = ?", room.ID).Error; err != nil {
		t.Fatal(err)
	}
	localContentTime := time.Now().UTC().Add(time.Hour)
	if err := db.Model(&stored).Update("updated_at", localContentTime).Error; err != nil {
		t.Fatal(err)
	}
	metadata := collaborationCanvasMetadata{
		ID: room.ID, Name: "After", CreatedByNodeID: room.CreatedByNodeID,
		MetadataRevision: stored.MetadataRevision + 1, CreatedAt: stored.CreatedAt, UpdatedAt: time.Now().UTC(),
	}
	if err := manager.upsertCollaborationCanvasMetadata(context.Background(), metadata); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&stored, "id = ?", room.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Name != "After" || stored.MetadataRevision != metadata.MetadataRevision || !stored.UpdatedAt.Equal(localContentTime) {
		t.Fatalf("creator metadata did not converge without losing content timestamp: %+v", stored)
	}
	metadata.Name = "Stale"
	metadata.MetadataRevision--
	metadata.UpdatedAt = localContentTime.Add(time.Hour)
	if err := manager.upsertCollaborationCanvasMetadata(context.Background(), metadata); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&stored, "id = ?", room.ID).Error; err != nil || stored.Name != "After" {
		t.Fatalf("stale metadata overwrote creator revision: %+v, err = %v", stored, err)
	}
}

func TestPeerSyncIgnoresMetadataClaimedForAnotherCreator(t *testing.T) {
	db, manager := newTestManager(t)
	room, err := manager.CreateCollaborationCanvas(context.Background(), "Owner room", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	peerPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	peerID := Fingerprint(peerPublic)
	items, err := manager.collaborationCanvasMetadata(context.Background())
	if err != nil || len(items) != 1 {
		t.Fatalf("canvas metadata = %+v, err = %v", items, err)
	}
	forged := items[0]
	forged.Name = "Forged from GET"
	forged.UpdatedAt = forged.UpdatedAt.Add(time.Minute)
	responseBody, err := json.Marshal(map[string]any{"canvases": []collaborationCanvasMetadata{forged}})
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: collaborationRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		body := []byte(`{"updates":[],"cursor":"","hasMore":false}`)
		if request.Method == http.MethodGet && request.URL.Path == "/collaboration/canvases" {
			body = responseBody
		} else if request.Method == http.MethodPost {
			body = []byte(`{"accepted":true}`)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(body))), Header: make(http.Header)}, nil
	})}
	if err := manager.syncPeerCollaborationCanvases(context.Background(), client, "https://peer.example", peerID); err != nil {
		t.Fatal(err)
	}
	var stored model.LANCollaborationCanvas
	if err := db.First(&stored, "id = ?", room.ID).Error; err != nil || stored.Name != room.Name {
		t.Fatalf("sync accepted forged metadata: %+v, err = %v", stored, err)
	}
}

func TestTwoTrustedManagersSyncWebSocketCatchUpAndRevocation(t *testing.T) {
	_, first := newTestManager(t)
	_, second := newTestManager(t)
	for _, pair := range [][2]*Manager{{first, second}, {second, first}} {
		if _, err := pair[0].saveTrustedPeer(context.Background(), pair[1].NodeID(), pair[1].identity.PublicKey(), "peer", "", 0); err != nil {
			t.Fatal(err)
		}
	}
	firstTLS, err := first.identity.ServerTLSConfig(first.isTrustedIdentity)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(first.peerHTTPHandler())
	server.TLS = firstTLS
	server.StartTLS()
	t.Cleanup(server.Close)
	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	address, rawPort, err := net.SplitHostPort(serverURL.Host)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil {
		t.Fatal(err)
	}
	clientTLS, err := second.clientTLSConfig(first.NodeID())
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: clientTLS}, Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	room, err := first.CreateCollaborationCanvas(context.Background(), "Shared", "", []byte{1, 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := second.syncPeerCollaborationCanvases(context.Background(), client, server.URL, first.NodeID()); err != nil {
		t.Fatalf("initial metadata and update sync: %v", err)
	}
	rooms, err := second.CollaborationCanvases(context.Background())
	if err != nil || len(rooms) != 1 || rooms[0].ID != room.ID || rooms[0].UpdateCount != 1 {
		t.Fatalf("second manager rooms = %+v, err = %v", rooms, err)
	}
	update := []byte{3, 4, 5}
	second.mu.Lock()
	second.peerServer = &PeerServer{}
	second.mu.Unlock()
	if err := second.sendPeerCollaborationFrame(first.NodeID(), address, port, collaborationPeerFrame{
		Type: "update", CanvasID: room.ID, OriginNodeID: second.NodeID(), UpdateBase64: base64.StdEncoding.EncodeToString(update),
	}); err != nil {
		t.Fatalf("live mTLS websocket update: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		page, readErr := first.CollaborationCanvasUpdates(context.Background(), room.ID, "", 10)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if len(page.Updates) == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("live peer update was not stored")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := first.AppendCollaborationCanvasUpdate(context.Background(), room.ID, []byte{6, 7}); err != nil {
		t.Fatal(err)
	}
	if err := second.syncPeerCollaborationCanvases(context.Background(), client, server.URL, first.NodeID()); err != nil {
		t.Fatalf("offline update catch-up: %v", err)
	}
	page, err := second.CollaborationCanvasUpdates(context.Background(), room.ID, "", 10)
	if err != nil || len(page.Updates) != 3 {
		t.Fatalf("caught-up updates = %d, err = %v", len(page.Updates), err)
	}
	if err := first.RevokePeer(context.Background(), second.NodeID()); err != nil {
		t.Fatal(err)
	}
	second.peerStreams.closePeer(first.NodeID())
	if err := second.sendPeerCollaborationFrame(first.NodeID(), address, port, collaborationPeerFrame{
		Type: "update", CanvasID: room.ID, UpdateBase64: base64.StdEncoding.EncodeToString([]byte{8, 9}),
	}); err == nil {
		t.Fatal("revoked peer established a new websocket")
	}
	page, err = first.CollaborationCanvasUpdates(context.Background(), room.ID, "", 10)
	if err != nil || len(page.Updates) != 3 {
		t.Fatalf("revoked peer changed update log: count = %d, err = %v", len(page.Updates), err)
	}
}

func TestCreatorHubRelaysAcrossThreeDevicesWithoutMemberPairing(t *testing.T) {
	endpoint, err := FindPrivateEndpoint()
	if err != nil {
		t.Skipf("private test interface unavailable: %v", err)
	}
	_, creator := newTestManager(t)
	_, first := newTestManager(t)
	_, second := newTestManager(t)
	managers := []*Manager{creator, first, second}
	type testEndpoint struct {
		address string
		port    int
		server  *httptest.Server
	}
	servers := make(map[string]testEndpoint)
	for _, manager := range managers {
		listener, listenErr := net.Listen("tcp4", net.JoinHostPort(endpoint.Address.String(), "0"))
		if listenErr != nil {
			t.Skipf("private test listener unavailable: %v", listenErr)
		}
		server := httptest.NewUnstartedServer(manager.peerHTTPHandler())
		_ = server.Listener.Close()
		server.Listener = listener
		server.TLS, err = manager.identity.ServerTLSConfig(manager.isTrustedIdentity)
		if err != nil {
			t.Fatal(err)
		}
		server.StartTLS()
		t.Cleanup(server.Close)
		address, rawPort, splitErr := net.SplitHostPort(listener.Addr().String())
		if splitErr != nil {
			t.Fatal(splitErr)
		}
		port, convertErr := strconv.Atoi(rawPort)
		if convertErr != nil {
			t.Fatal(convertErr)
		}
		servers[manager.NodeID()] = testEndpoint{address: address, port: port, server: server}
		manager.mu.Lock()
		manager.peerServer = &PeerServer{}
		manager.mu.Unlock()
	}
	for _, pair := range [][2]*Manager{{creator, first}, {first, creator}, {creator, second}, {second, creator}} {
		remote := servers[pair[1].NodeID()]
		if _, err := pair[0].saveTrustedPeer(context.Background(), pair[1].NodeID(), pair[1].identity.PublicKey(), "peer", remote.address, remote.port); err != nil {
			t.Fatal(err)
		}
	}
	if first.isTrusted(second.NodeID()) || second.isTrusted(first.NodeID()) {
		t.Fatal("members unexpectedly trust each other")
	}
	room, err := creator.CreateCollaborationCanvas(context.Background(), "Shared", "", []byte{1})
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range []*Manager{first, second} {
		clientTLS, err := member.clientTLSConfig(creator.NodeID())
		if err != nil {
			t.Fatal(err)
		}
		client := &http.Client{Transport: &http.Transport{TLSClientConfig: clientTLS}, Timeout: 5 * time.Second}
		if err := member.syncPeerCollaborationCanvases(context.Background(), client, servers[creator.NodeID()].server.URL, creator.NodeID()); err != nil {
			t.Fatal(err)
		}
		client.CloseIdleConnections()
	}
	presenceClient := &collaborationStreamClient{canvasID: room.ID, send: make(chan []byte, 4), done: make(chan struct{})}
	second.collaborationHub.add(presenceClient)
	defer second.collaborationHub.remove(presenceClient)
	creatorAddress := servers[creator.NodeID()]
	if err := first.sendPeerCollaborationFrame(creator.NodeID(), creatorAddress.address, creatorAddress.port, collaborationPeerFrame{
		Type: "presence", CanvasID: room.ID, OriginNodeID: first.NodeID(), State: json.RawMessage(`{"online":true,"cursor":{"x":12,"y":34}}`),
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case raw := <-presenceClient.send:
		var presence collaborationPresenceFrame
		if err := json.Unmarshal(raw, &presence); err != nil {
			t.Fatal(err)
		}
		if presence.PeerID != first.NodeID() || !strings.Contains(string(presence.State), `"cursor"`) || !presence.ExpiresAt.After(time.Now()) || presence.ExpiresAt.After(time.Now().Add(31*time.Second)) {
			t.Fatalf("relayed presence = %+v", presence)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("creator did not relay member presence")
	}
	if targets := first.collaborationTargets(context.Background(), room.ID, ""); len(targets) != 1 || targets[0].PeerID != creator.NodeID() {
		t.Fatalf("member targets = %+v, want creator only", targets)
	}
	if targets := creator.collaborationTargets(context.Background(), room.ID, first.NodeID()); len(targets) != 1 || targets[0].PeerID != second.NodeID() {
		t.Fatalf("creator relay targets = %+v, want other member", targets)
	}
	if _, err := first.AppendCollaborationCanvasUpdate(context.Background(), room.ID, []byte{2}); err != nil {
		t.Fatal(err)
	}
	awaitCount := func(manager *Manager, want int) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			page, err := manager.CollaborationCanvasUpdates(context.Background(), room.ID, "", 10)
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Updates) == want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s update count = %d, want %d", manager.NodeID(), len(page.Updates), want)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	awaitCount(creator, 2)
	awaitCount(second, 2)
	select {
	case raw := <-presenceClient.send:
		var frame struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &frame); err != nil || frame.Type != "update" {
			t.Fatalf("unexpected queued frame before revocation: %s, err = %v", raw, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("member did not receive relayed update frame")
	}
	if err := creator.RevokePeer(context.Background(), second.NodeID()); err != nil {
		t.Fatal(err)
	}
	if err := first.sendPeerCollaborationFrame(creator.NodeID(), creatorAddress.address, creatorAddress.port, collaborationPeerFrame{
		Type: "presence", CanvasID: room.ID, OriginNodeID: first.NodeID(), State: json.RawMessage(`{"online":false}`),
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case raw := <-presenceClient.send:
		t.Fatalf("revoked member received presence: %s", raw)
	case <-time.After(150 * time.Millisecond):
	}
	if _, err := first.AppendCollaborationCanvasUpdate(context.Background(), room.ID, []byte{3}); err != nil {
		t.Fatal(err)
	}
	awaitCount(creator, 3)
	time.Sleep(150 * time.Millisecond)
	page, err := second.CollaborationCanvasUpdates(context.Background(), room.ID, "", 10)
	if err != nil || len(page.Updates) != 2 {
		t.Fatalf("revoked member updates = %d, err = %v", len(page.Updates), err)
	}
	if targets := creator.collaborationTargets(context.Background(), room.ID, ""); len(targets) != 1 || targets[0].PeerID != first.NodeID() {
		t.Fatalf("post-revocation targets = %+v", targets)
	}
}

func TestStopClosesLocalCollaborationStreamAndPreventsOutboundFrames(t *testing.T) {
	_, manager := newTestManager(t)
	room, err := manager.CreateCollaborationCanvas(context.Background(), "Shared", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	manager.peerServer = &PeerServer{}
	manager.mu.Unlock()
	ticket, err := manager.CreateCollaborationCanvasStreamTicket(context.Background(), room.ID)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		manager.HandleCollaborationCanvasStream(w, r, room.ID)
	}))
	defer server.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/?ticket="+url.QueryEscape(ticket), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("initial presence frame: %v", err)
	}
	if len(manager.collaborationHub.activeRooms()) != 1 {
		t.Fatal("local collaboration socket did not join")
	}
	if _, err := manager.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("local collaboration socket stayed open after stop")
	}
	if len(manager.collaborationHub.activeRooms()) != 0 {
		t.Fatal("local collaboration hub retained active room")
	}
	if err := manager.sendPeerCollaborationFrame(manager.NodeID(), "127.0.0.1", 1, collaborationPeerFrame{Type: "presence", CanvasID: room.ID}); !errors.Is(err, ErrPeerNotTrusted) {
		t.Fatalf("disabled LAN attempted outbound frame: %v", err)
	}
}

type collaborationRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn collaborationRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestCollaborationStreamTicketIsBoundAndSingleUse(t *testing.T) {
	_, manager := newTestManager(t)
	room, err := manager.CreateCollaborationCanvas(context.Background(), "Shared", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	ticket, err := manager.CreateCollaborationCanvasStreamTicket(context.Background(), room.ID)
	if err != nil || ticket == "" {
		t.Fatalf("issue stream ticket = %q, err=%v", ticket, err)
	}
	if !manager.collaborationHub.consumeTicket(room.ID, ticket) {
		t.Fatal("issued ticket did not authorize its room")
	}
	if manager.collaborationHub.consumeTicket(room.ID, ticket) {
		t.Fatal("stream ticket was reusable")
	}
	other, err := manager.CreateCollaborationCanvas(context.Background(), "Other", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	ticket, err = manager.CreateCollaborationCanvasStreamTicket(context.Background(), room.ID)
	if err != nil {
		t.Fatal(err)
	}
	if manager.collaborationHub.consumeTicket(other.ID, ticket) {
		t.Fatal("ticket authorized a different room")
	}
}
