package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"infinite-canvas/backend/internal/app"
	"infinite-canvas/backend/internal/database"
	"infinite-canvas/backend/internal/lansharing"
	"infinite-canvas/backend/internal/model"
	"infinite-canvas/backend/internal/repository"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestLANAssetPublishCreatesVerifiedOwnerMappingAndPeerStream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service, db, manager := newLANAssetHandlerFixture(t)
	owner, err := service.LocalWorkspaceOwner()
	if err != nil {
		t.Fatal(err)
	}
	if owner.ID == "local" {
		t.Fatal("fixture should use the persisted workspace owner ID")
	}
	if err := db.Create(&model.LANShareCatalog{ID: "catalog-1", Name: "Shared", CreatedByNodeID: manager.NodeID()}).Error; err != nil {
		t.Fatal(err)
	}
	content := []byte("local asset bytes")
	resource, err := service.UploadLocalResourceFile(owner.ID, "source.bin", int64(len(content)), "file", 0, 0, 0, bytes.NewReader(content))
	if err != nil {
		t.Fatalf("upload fixture resource: %v", err)
	}
	handler := &lanAssetHandler{manager: manager, service: service, db: db}
	router := gin.New()
	router.POST("/catalogs/:catalogId/assets", handler.addAsset)
	request := httptest.NewRequest(http.MethodPost, "/catalogs/catalog-1/assets", bytes.NewBufferString(`{"resourceId":"`+resource.ID+`","entryId":"entry-1","name":"draft.bin"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("publish response = %d %s", response.Code, response.Body.String())
	}
	var envelope struct {
		Data struct {
			Asset lanCatalogAsset `json:"asset"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	expectedHash := hex.EncodeToString(digest[:])
	if envelope.Data.Asset.ContentHash != expectedHash || envelope.Data.Asset.Name != "draft.bin" || envelope.Data.Asset.Size != int64(len(content)) {
		t.Fatalf("published metadata = %#v", envelope.Data.Asset)
	}
	var mapping model.LANCatalogResource
	if err := db.First(&mapping, "catalog_id = ? AND content_hash = ? AND user_id = ?", "catalog-1", expectedHash, owner.ID).Error; err != nil {
		t.Fatalf("read published mapping: %v", err)
	}
	if mapping.ResourceID != resource.ID || mapping.HashAlgorithm != "sha256" || mapping.VerifiedAt.IsZero() {
		t.Fatalf("mapping is not bound to the verified local resource: %#v", mapping)
	}

	peerRequest := httptest.NewRequest(http.MethodGet, "/catalogs/catalog-1/assets/"+expectedHash, nil)
	peerRequest.SetPathValue("catalogID", "catalog-1")
	peerRequest.SetPathValue("contentHash", expectedHash)
	peerRequest.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{}}}
	peerResponse := httptest.NewRecorder()
	handler.serveAsset(peerResponse, peerRequest)
	if peerResponse.Code != http.StatusOK || !bytes.Equal(peerResponse.Body.Bytes(), content) {
		t.Fatalf("peer asset response = %d %q", peerResponse.Code, peerResponse.Body.Bytes())
	}
	if peerResponse.Header().Get("X-BeefTV-Content-SHA256") != expectedHash || peerResponse.Header().Get("X-BeefTV-Asset-Metadata") == "" {
		t.Fatal("peer response omitted verified identity headers")
	}
}

func TestLANAssetPeerStreamRequiresCatalogMappingAndVerifiedTimestamp(t *testing.T) {
	service, db, manager := newLANAssetHandlerFixture(t)
	owner, err := service.LocalWorkspaceOwner()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.LANShareCatalog{ID: "catalog-2", Name: "Shared"}).Error; err != nil {
		t.Fatal(err)
	}
	handler := &lanAssetHandler{manager: manager, service: service, db: db}
	request := httptest.NewRequest(http.MethodGet, "/catalogs/catalog-2/assets/"+string(bytes.Repeat([]byte("0"), 64)), nil)
	request.SetPathValue("catalogID", "catalog-2")
	request.SetPathValue("contentHash", string(bytes.Repeat([]byte("0"), 64)))
	request.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{}}}
	response := httptest.NewRecorder()
	handler.serveAsset(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("unmapped peer asset status = %d, want 404", response.Code)
	}
	if err := db.Create(&model.LANCatalogResource{
		CatalogID: "catalog-2", HashAlgorithm: "sha256", ContentHash: string(bytes.Repeat([]byte("0"), 64)),
		UserID: owner.ID, ResourceID: "missing", // VerifiedAt intentionally absent.
	}).Error; err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	handler.serveAsset(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("unverified peer asset status = %d, want 409", response.Code)
	}
}

func TestLANAssetTransferErrorsMatchPeerStatusContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name string
		err  error
		want int
	}{
		{name: "local peer is untrusted", err: lansharing.ErrPeerNotTrusted, want: http.StatusForbidden},
		{name: "remote peer rejects request", err: &lansharing.PeerAssetHTTPError{StatusCode: http.StatusForbidden}, want: http.StatusForbidden},
		{name: "remote asset is missing", err: &lansharing.PeerAssetHTTPError{StatusCode: http.StatusNotFound}, want: http.StatusNotFound},
		{name: "transport failure", err: errors.New("connection failed"), want: http.StatusBadGateway},
	} {
		t.Run(test.name, func(t *testing.T) {
			router := gin.New()
			router.GET("/", func(c *gin.Context) { writeLANAssetTransferError(c, test.err) })
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.want, response.Body.String())
			}
		})
	}
}

func TestLANAssetScanTracksMissingAndRestoredFile(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service, db, manager := newLANAssetHandlerFixture(t)
	owner, err := service.LocalWorkspaceOwner()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.LANShareCatalog{ID: "catalog-scan", Name: "Shared"}).Error; err != nil {
		t.Fatal(err)
	}
	content := []byte("asset scan fixture")
	resource, err := service.UploadLocalResourceFile(owner.ID, "source.bin", int64(len(content)), "file", 0, 0, 0, bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	hash := hex.EncodeToString(digest[:])
	h := &lanAssetHandler{manager: manager, service: service, db: db}
	if err := h.saveCatalogResource(context.Background(), "catalog-scan", hash, owner.ID, resource.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	assertStatus := func(want string) {
		t.Helper()
		if err := scanLANAssetsOnce(context.Background(), service); err != nil {
			t.Fatal(err)
		}
		var row model.LANCatalogResource
		if err := db.First(&row, "catalog_id = ? AND content_hash = ? AND user_id = ?", "catalog-scan", hash, owner.ID).Error; err != nil {
			t.Fatal(err)
		}
		if row.Availability != want || row.CheckedAt == nil {
			t.Fatalf("availability = %q, checkedAt = %v, want %q", row.Availability, row.CheckedAt, want)
		}
	}
	assertStatus(lanAssetPresent)
	path := filepath.Join(service.DataDir(), "resources", filepath.FromSlash(resource.ObjectKey))
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	assertStatus(lanAssetMissing)
	request := httptest.NewRequest(http.MethodGet, "/catalogs/catalog-scan/assets/status", nil)
	response := httptest.NewRecorder()
	router := gin.New()
	router.GET("/catalogs/:catalogId/assets/status", h.assetStatuses)
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status endpoint = %d: %s", response.Code, response.Body.String())
	}
	var envelope struct {
		Data struct {
			Assets []struct {
				ContentHash  string     `json:"contentHash"`
				ResourceID   string     `json:"resourceId"`
				Availability string     `json:"availability"`
				CheckedAt    *time.Time `json:"checkedAt"`
			} `json:"assets"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Data.Assets) != 1 || envelope.Data.Assets[0].ContentHash != hash || envelope.Data.Assets[0].ResourceID != resource.ID || envelope.Data.Assets[0].Availability != lanAssetMissing || envelope.Data.Assets[0].CheckedAt == nil {
		t.Fatalf("status response = %+v", envelope.Data.Assets)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	assertStatus(lanAssetPresent)
}

func TestLANAssetScanKeepsTransientProbeFailureUnknown(t *testing.T) {
	service, db, _ := newLANAssetHandlerFixture(t)
	owner, err := service.LocalWorkspaceOwner()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.LANShareCatalog{ID: "catalog-unknown", Name: "Shared"}).Error; err != nil {
		t.Fatal(err)
	}
	row := model.LANCatalogResource{CatalogID: "catalog-unknown", HashAlgorithm: "sha256", ContentHash: string(bytes.Repeat([]byte("a"), 64)), UserID: owner.ID, ResourceID: "unreadable", VerifiedAt: time.Now().UTC(), Availability: lanAssetPresent}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	if err := scanLANAssetsOnceWithProbe(context.Background(), service, func(context.Context, *app.Service, string, *model.LANCatalogResource) string {
		return lanAssetUnknown
	}); err != nil {
		t.Fatal(err)
	}
	var got model.LANCatalogResource
	if err := db.First(&got, "catalog_id = ? AND content_hash = ? AND user_id = ?", row.CatalogID, row.ContentHash, owner.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.Availability != lanAssetUnknown || got.CheckedAt == nil || got.ResourceID != row.ResourceID {
		t.Fatalf("transient probe changed mapping incorrectly: %+v", got)
	}
}

func TestLANAssetRecoveryUploadDoesNotReuseMissingReadyResource(t *testing.T) {
	service, _, _ := newLANAssetHandlerFixture(t)
	owner, err := service.LocalWorkspaceOwner()
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("recovery fixture bytes")
	digest := sha256.Sum256(content)
	hash := hex.EncodeToString(digest[:])
	identity := "lan-share:catalog-recovery:" + hash
	original, err := service.UploadLocalResourceFile(owner.ID, "source.bin", int64(len(content)), "file", 0, 0, 0, bytes.NewReader(content), identity)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(service.DataDir(), "resources", filepath.FromSlash(original.ObjectKey))
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	recovered, err := service.UploadLocalResourceFile(owner.ID, "source.bin", int64(len(content)), "file", 0, 0, 0, bytes.NewReader(content), identity+":recovery:test")
	if err != nil {
		t.Fatal(err)
	}
	if recovered.ID == original.ID {
		t.Fatal("recovery reused the READY record whose bytes are missing")
	}
	actual, err := hashOwnedLocalResource(context.Background(), service, owner.ID, recovered, int64(len(content)+1))
	if err != nil || actual != hash {
		t.Fatalf("recovered resource hash = %q, err = %v, want %q", actual, err, hash)
	}
}

func newLANAssetHandlerFixture(t *testing.T) (*app.Service, *gorm.DB, *lansharing.Manager) {
	t.Helper()
	dataDir := t.TempDir()
	db, err := gorm.Open(sqlite.Open(filepath.Join(dataDir, "test.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.MigrateLocalSchema(db); err != nil {
		t.Fatal(err)
	}
	service := app.NewLocal(repository.New(db), dataDir)
	manager, err := lansharing.NewManager(dataDir, db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = manager.Close(context.Background())
		if sqlDB, dbErr := db.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
	})
	return service, db, manager
}
