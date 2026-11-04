package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"infinite-canvas/backend/internal/lansharing"

	"github.com/gin-gonic/gin"
)

type lanSharingRouteFake struct{ statusCalls int }

func (f *lanSharingRouteFake) Status() lansharing.Status {
	f.statusCalls++
	return lansharing.Status{Enabled: true, NodeID: "local-node"}
}
func (*lanSharingRouteFake) SetLocalNickname(_ context.Context, nickname string) (lansharing.Status, error) {
	return lansharing.Status{Enabled: true, NodeID: "local-node", Nickname: nickname}, nil
}
func (*lanSharingRouteFake) Start(context.Context) (lansharing.Status, error) {
	return lansharing.Status{}, errors.New("unused")
}
func (*lanSharingRouteFake) Stop(context.Context) (lansharing.Status, error) {
	return lansharing.Status{}, errors.New("unused")
}
func (*lanSharingRouteFake) DiscoveredPeers() []lansharing.Peer { return nil }
func (*lanSharingRouteFake) TrustedPeers(context.Context) ([]lansharing.PeerSummary, error) {
	return nil, nil
}
func (*lanSharingRouteFake) PairPeer(context.Context, lansharing.PairRequest) (lansharing.PeerSummary, error) {
	return lansharing.PeerSummary{}, errors.New("unused")
}
func (*lanSharingRouteFake) RevokePeer(context.Context, string) error { return nil }
func (*lanSharingRouteFake) Catalogs(context.Context) ([]lansharing.CatalogSummary, error) {
	return nil, nil
}
func (*lanSharingRouteFake) CreateCatalog(context.Context, string) (lansharing.CatalogSummary, error) {
	return lansharing.CatalogSummary{}, nil
}
func (*lanSharingRouteFake) CatalogUpdates(context.Context, string, string, int) (lansharing.CatalogUpdatePage, error) {
	return lansharing.CatalogUpdatePage{}, nil
}
func (*lanSharingRouteFake) AppendCatalogUpdate(context.Context, string, []byte, string) (bool, error) {
	return false, nil
}
func (*lanSharingRouteFake) CollaborationCanvases(context.Context) ([]lansharing.CollaborationCanvasSummary, error) {
	return nil, nil
}
func (*lanSharingRouteFake) CreateCollaborationCanvas(context.Context, string, string, []byte) (lansharing.CollaborationCanvasSummary, error) {
	return lansharing.CollaborationCanvasSummary{}, nil
}
func (*lanSharingRouteFake) UpdateCollaborationCanvas(context.Context, string, *string, *bool) (lansharing.CollaborationCanvasSummary, error) {
	return lansharing.CollaborationCanvasSummary{}, nil
}
func (*lanSharingRouteFake) DeleteCollaborationCanvas(context.Context, string) error { return nil }
func (*lanSharingRouteFake) CollaborationCanvasUpdates(context.Context, string, string, int) (lansharing.CollaborationCanvasUpdatePage, error) {
	return lansharing.CollaborationCanvasUpdatePage{}, nil
}
func (*lanSharingRouteFake) AppendCollaborationCanvasUpdate(context.Context, string, []byte) (bool, error) {
	return false, nil
}
func (*lanSharingRouteFake) CreateCollaborationCanvasStreamTicket(context.Context, string) (string, error) {
	return "ticket", nil
}
func (*lanSharingRouteFake) HandleCollaborationCanvasStream(http.ResponseWriter, *http.Request, string) {
}

func TestLANSharingRoutesRequireTrustedDesktop(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fake := &lanSharingRouteFake{}
	allowed := false
	router := gin.New()
	router.Use(RuntimeDependenciesMiddleware(RuntimeDependencies{DesktopTrust: func(*http.Request) bool { return allowed }}))
	RegisterLANSharingRoutes(router.Group("/api"), nil, fake)

	request := httptest.NewRequest(http.MethodGet, "/api/lan-sharing/status", nil)
	request.Host = "127.0.0.1:8080"
	request.RemoteAddr = "127.0.0.1:1234"
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("untrusted desktop status = %d, want 403: %s", response.Code, response.Body.String())
	}
	if fake.statusCalls != 0 {
		t.Fatal("unauthenticated request reached the LAN manager")
	}

	allowed = true
	request = httptest.NewRequest(http.MethodGet, "/api/lan-sharing/status", nil)
	request.Host = "127.0.0.1:8080"
	request.RemoteAddr = "127.0.0.1:1234"
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("trusted desktop status = %d, want 200: %s", response.Code, response.Body.String())
	}
	if fake.statusCalls != 1 {
		t.Fatalf("manager status calls = %d, want 1", fake.statusCalls)
	}

	request = httptest.NewRequest(http.MethodPut, "/api/lan-sharing/nickname", strings.NewReader(`{"nickname":"Studio Editor"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Host = "127.0.0.1:8080"
	request.RemoteAddr = "127.0.0.1:1234"
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"nickname":"Studio Editor"`) {
		t.Fatalf("trusted nickname update = %d: %s", response.Code, response.Body.String())
	}
}
