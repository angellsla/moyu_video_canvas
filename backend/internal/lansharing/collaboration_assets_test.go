package lansharing

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"infinite-canvas/backend/internal/model"
)

func TestCatalogArrivalWakesCanvasOverLocalWebSocket(t *testing.T) {
	_, receiver := newTestManager(t)
	_, sender := newTestManager(t)
	ctx := context.Background()
	if _, err := receiver.saveTrustedPeer(ctx, sender.NodeID(), sender.identity.PublicKey(), "sender", "", 0); err != nil {
		t.Fatal(err)
	}
	catalog, err := receiver.CreateCatalog(ctx, "media")
	if err != nil {
		t.Fatal(err)
	}
	room, err := receiver.CreateCollaborationCanvas(ctx, "Shared", catalog.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	ticket, err := receiver.CreateCollaborationCanvasStreamTicket(ctx, room.ID)
	if err != nil {
		t.Fatal(err)
	}
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receiver.HandleCollaborationCanvasStream(w, r, room.ID)
	}))
	defer local.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(local.URL, "http")+"/?ticket="+url.QueryEscape(ticket), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("initial presence: %v", err)
	}
	peer := httptest.NewUnstartedServer(receiver.peerHTTPHandler())
	peer.TLS, err = receiver.identity.ServerTLSConfig(receiver.isTrustedIdentity)
	if err != nil {
		t.Fatal(err)
	}
	peer.StartTLS()
	defer peer.Close()
	config, err := sender.identity.ClientTLSConfig(receiver.NodeID(), Fingerprint(receiver.identity.PublicKey()))
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: config}, Timeout: 2 * time.Second}
	defer client.CloseIdleConnections()
	var result map[string]bool
	if err := requestPeerJSON(ctx, client, http.MethodPost, peer.URL+"/catalogs/"+catalog.ID+"/updates", map[string]string{
		"originNodeId": sender.NodeID(), "updateBase64": base64.StdEncoding.EncodeToString([]byte{1, 2, 3}),
	}, &result); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	var frame map[string]string
	if err := conn.ReadJSON(&frame); err != nil {
		t.Fatalf("catalog arrival was not pushed to the editor: %v", err)
	}
	if frame["type"] != "asset-catalog" || frame["canvasId"] != room.ID || frame["catalogId"] != catalog.ID {
		t.Fatalf("unexpected frame: %+v", frame)
	}
	page, err := receiver.CatalogUpdates(ctx, catalog.ID, "", 10)
	if err != nil || len(page.Updates) != 1 || !result["accepted"] {
		t.Fatalf("notification preceded durable catalog update: %+v, %v", page, err)
	}
}

func TestCatalogNotificationFiltersUnrelatedDeletedAndDuplicateUpdates(t *testing.T) {
	db, manager := newTestManager(t)
	ctx := context.Background()
	catalog, err := manager.CreateCatalog(ctx, "media")
	if err != nil {
		t.Fatal(err)
	}
	var matching *collaborationStreamClient
	var excluded []*collaborationStreamClient
	for _, kind := range []string{"matching", "unrelated", "deleted"} {
		catalogID := catalog.ID
		if kind == "unrelated" {
			catalogID = ""
		}
		room, err := manager.CreateCollaborationCanvas(ctx, kind, catalogID, nil)
		if err != nil {
			t.Fatal(err)
		}
		if kind == "deleted" {
			if err := db.Model(&model.LANCollaborationCanvas{}).Where("id = ?", room.ID).Update("deleted_at", time.Now()).Error; err != nil {
				t.Fatal(err)
			}
		}
		client := &collaborationStreamClient{canvasID: room.ID, send: make(chan []byte, 8), done: make(chan struct{})}
		manager.collaborationHub.add(client)
		t.Cleanup(func() { manager.collaborationHub.remove(client) })
		if kind == "matching" {
			matching = client
		} else {
			excluded = append(excluded, client)
		}
	}
	update := []byte{4, 5, 6}
	for attempt := 0; attempt < 2; attempt++ {
		accepted, err := manager.AppendCatalogUpdate(ctx, catalog.ID, update, manager.NodeID())
		if err != nil || accepted != (attempt == 0) {
			t.Fatalf("append attempt %d: accepted=%v, err=%v", attempt, accepted, err)
		}
	}
	if len(matching.send) != 1 {
		t.Fatalf("expected exactly one catalog notification, got %d", len(matching.send))
	}
	var frame map[string]string
	if err := json.Unmarshal(<-matching.send, &frame); err != nil || frame["type"] != "asset-catalog" {
		t.Fatalf("invalid notification: %+v, %v", frame, err)
	}
	for _, client := range excluded {
		if len(client.send) != 0 {
			t.Fatal("catalog notification leaked into unrelated or deleted room")
		}
	}
}
