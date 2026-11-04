package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"infinite-canvas/backend/internal/app"
	"infinite-canvas/backend/internal/kernel"
	"infinite-canvas/backend/internal/lansharing"
	"infinite-canvas/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const lanAssetImportBodyLimit = 32 << 10

type lanAssetHandler struct {
	manager *lansharing.Manager
	service *app.Service
	db      *gorm.DB
}

type lanCatalogAsset struct {
	EntryID     string    `json:"entryId"`
	ContentHash string    `json:"contentHash"`
	Name        string    `json:"name"`
	MIMEType    string    `json:"mimeType"`
	Kind        string    `json:"kind"`
	Size        int64     `json:"size"`
	Width       int       `json:"width,omitempty"`
	Height      int       `json:"height,omitempty"`
	DurationMs  int64     `json:"durationMs,omitempty"`
	CreatedBy   string    `json:"createdBy"`
	CreatedAt   time.Time `json:"createdAt"`
}

type addLANCatalogAssetRequest struct {
	ResourceID string `json:"resourceId"`
	EntryID    string `json:"entryId"`
	Name       string `json:"name"`
}

type importLANCatalogAssetRequest struct {
	PeerID      string `json:"peerId"`
	Address     string `json:"address"`
	Port        int    `json:"port"`
	EntryID     string `json:"entryId"`
	ContentHash string `json:"contentHash"`
	Name        string `json:"name"`
	MIMEType    string `json:"mimeType"`
	Kind        string `json:"kind"`
	Size        int64  `json:"size"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	DurationMs  int64  `json:"durationMs"`
}

// RegisterLANAssetRoutes attaches the local user-facing LAN asset operations.
// Both routes are limited to the trusted desktop write principal.
func RegisterLANAssetRoutes(api *gin.RouterGroup, manager *lansharing.Manager, service *app.Service) {
	if api == nil || manager == nil || service == nil || service.Database() == nil {
		return
	}
	handler := &lanAssetHandler{manager: manager, service: service, db: service.Database()}
	routes := api.Group("/lan-sharing")
	routes.Use(func(c *gin.Context) {
		if !requireTrustedDesktopWritePrincipal(c, "局域网共享只能由受信任的桌面界面操作") {
			c.Abort()
			return
		}
		c.Next()
	})
	routes.POST("/catalogs/:catalogId/assets", handler.addAsset)
	routes.POST("/catalogs/:catalogId/assets/import", handler.importAsset)
	routes.GET("/catalogs/:catalogId/assets/status", handler.assetStatuses)
}

// RegisterLANAssetPeerHandler must run before the LAN Manager starts. The
// manager's peer server adds its mTLS identity and current-trust gate around
// every registered handler.
func RegisterLANAssetPeerHandler(manager *lansharing.Manager, service *app.Service) error {
	if manager == nil || service == nil || service.Database() == nil {
		return errors.New("LAN asset peer handler dependencies are incomplete")
	}
	handler := &lanAssetHandler{manager: manager, service: service, db: service.Database()}
	return manager.RegisterPeerHandler("GET /catalogs/{catalogID}/assets/{contentHash}", http.HandlerFunc(handler.serveAsset))
}

func (h *lanAssetHandler) addAsset(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, lanAssetImportBodyLimit)
	var request addLANCatalogAssetRequest
	if err := bindStrictJSON(c, &request); err != nil {
		fail(c, http.StatusBadRequest, app.BadAuthRequest("共享素材请求无效"))
		return
	}
	catalogID := strings.TrimSpace(c.Param("catalogId"))
	if !validLANCatalogID(catalogID) || strings.TrimSpace(request.ResourceID) == "" {
		fail(c, http.StatusBadRequest, app.BadAuthRequest("共享素材请求无效"))
		return
	}
	if err := h.requireCatalog(c.Request.Context(), catalogID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			fail(c, http.StatusNotFound, app.NotFound("共享清单不存在"))
		} else {
			failInternal(c, http.StatusInternalServerError, err)
		}
		return
	}
	name := strings.TrimSpace(request.Name)
	if name != "" && !validLANAssetName(name) {
		fail(c, http.StatusBadRequest, app.BadAuthRequest("素材名称无效"))
		return
	}
	owner, err := h.localOwner()
	if err != nil {
		failService(c, err)
		return
	}
	resource, err := h.service.Resource(owner.ID, strings.TrimSpace(request.ResourceID))
	if err != nil {
		failService(c, err)
		return
	}
	if !readyLocalResource(resource, owner.ID) {
		fail(c, http.StatusBadRequest, app.BadAuthRequest("只能共享已就绪的本地素材"))
		return
	}
	maxBytes, err := lanResourceMaximumBytes(h.service)
	if err != nil {
		failService(c, err)
		return
	}
	if resource.Size < 1 || resource.Size >= maxBytes {
		fail(c, http.StatusBadRequest, app.BadAuthRequest("素材大小超出局域网共享限制"))
		return
	}
	contentHash, err := hashOwnedLocalResource(c.Request.Context(), h.service, owner.ID, resource, maxBytes)
	if err != nil {
		failInternal(c, http.StatusInternalServerError, fmt.Errorf("hash local LAN asset: %w", err))
		return
	}
	entryID := strings.TrimSpace(request.EntryID)
	if entryID == "" {
		entryID = kernel.NewID()
	}
	if !validLANAssetEntryID(entryID) {
		fail(c, http.StatusBadRequest, app.BadAuthRequest("共享素材标识无效"))
		return
	}
	if name == "" {
		name = fallbackLANAssetName(resource)
	}
	now := time.Now().UTC()
	if err := h.saveCatalogResource(c.Request.Context(), catalogID, contentHash, owner.ID, resource.ID, now); err != nil {
		failInternal(c, http.StatusInternalServerError, err)
		return
	}
	asset := lanCatalogAsset{
		EntryID: entryID, ContentHash: contentHash, Name: name, MIMEType: safeMIMEType(resource.MimeType),
		Kind: resource.Kind, Size: resource.Size, Width: resource.Width, Height: resource.Height,
		DurationMs: resource.DurationMs, CreatedBy: h.manager.NodeID(), CreatedAt: now,
	}
	ok(c, gin.H{"asset": asset})
}

func (h *lanAssetHandler) importAsset(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, lanAssetImportBodyLimit)
	var request importLANCatalogAssetRequest
	if err := bindStrictJSON(c, &request); err != nil {
		fail(c, http.StatusBadRequest, app.BadAuthRequest("素材导入请求无效"))
		return
	}
	catalogID := strings.TrimSpace(c.Param("catalogId"))
	request.PeerID = strings.ToLower(strings.TrimSpace(request.PeerID))
	request.Address = strings.TrimSpace(request.Address)
	request.ContentHash = strings.ToLower(strings.TrimSpace(request.ContentHash))
	request.Name = strings.TrimSpace(request.Name)
	if !validLANCatalogID(catalogID) || !validLANPeerID(request.PeerID) || !validLANPrivateIPv4(request.Address) || request.Port < 1 || request.Port > 65535 || !isLowerHexSHA256(request.ContentHash) || request.Size < 1 || (request.EntryID != "" && !validLANAssetEntryID(request.EntryID)) {
		fail(c, http.StatusBadRequest, app.BadAuthRequest("素材导入请求无效"))
		return
	}
	if err := h.requireCatalog(c.Request.Context(), catalogID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			fail(c, http.StatusNotFound, app.NotFound("共享清单不存在"))
		} else {
			failInternal(c, http.StatusInternalServerError, err)
		}
		return
	}
	if (request.Name != "" && !validLANAssetName(request.Name)) || !validLANAssetMIME(request.MIMEType) || (request.Kind != "" && !validLANAssetKind(request.Kind)) || request.Width < 0 || request.Height < 0 || request.DurationMs < 0 {
		fail(c, http.StatusBadRequest, app.BadAuthRequest("清单素材元数据无效"))
		return
	}
	owner, err := h.localOwner()
	if err != nil {
		failService(c, err)
		return
	}
	maxBytes, err := lanResourceMaximumBytes(h.service)
	if err != nil {
		failService(c, err)
		return
	}
	if request.Size >= maxBytes {
		fail(c, http.StatusRequestEntityTooLarge, app.BadAuthRequest("素材大小超出本地资源限制"))
		return
	}
	contentHash := strings.ToLower(request.ContentHash)
	var existing model.LANCatalogResource
	lookupErr := h.db.WithContext(c.Request.Context()).Where(
		"catalog_id = ? AND hash_algorithm = ? AND content_hash = ? AND user_id = ?",
		catalogID, "sha256", contentHash, owner.ID,
	).First(&existing).Error
	replacingMissing := false
	if lookupErr == nil {
		switch inspectLANAsset(c.Request.Context(), h.service, owner.ID, &existing) {
		case lanAssetMissing:
			replacingMissing = true
		case lanAssetUnknown:
			fail(c, http.StatusServiceUnavailable, app.NewAppError(http.StatusServiceUnavailable, "本地映射素材状态暂时无法确认"))
			return
		default:
			resource, resourceErr := h.service.Resource(owner.ID, existing.ResourceID)
			if resourceErr != nil {
				failInternal(c, http.StatusInternalServerError, resourceErr)
				return
			}
			if !readyLocalResource(resource, owner.ID) || resource.Size != request.Size || safeMIMEType(resource.MimeType) != request.MIMEType || (request.Kind != "" && resource.Kind != request.Kind) {
				fail(c, http.StatusConflict, app.NewAppError(http.StatusConflict, "本地映射素材与清单元数据不一致"))
				return
			}
			actualHash, hashErr := hashOwnedLocalResource(c.Request.Context(), h.service, owner.ID, resource, maxBytes)
			if hashErr == nil && actualHash == contentHash && !existing.VerifiedAt.IsZero() {
				ok(c, gin.H{"resource": localAssetResourceSummary(resource)})
				return
			}
			if errors.Is(hashErr, os.ErrNotExist) {
				replacingMissing = true
				break
			}
			fail(c, http.StatusConflict, app.NewAppError(http.StatusConflict, "本地映射素材未通过内容校验"))
			return
		}
	} else if !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
		failInternal(c, http.StatusInternalServerError, lookupErr)
		return
	}
	dataDir := strings.TrimSpace(h.service.DataDir())
	if dataDir == "" {
		failInternal(c, http.StatusInternalServerError, errors.New("local workspace data directory is unavailable"))
		return
	}
	temporaryDir := filepath.Join(dataDir, "lan-sharing", "tmp")
	if err := os.MkdirAll(temporaryDir, 0o700); err != nil {
		failInternal(c, http.StatusInternalServerError, err)
		return
	}
	if err := os.Chmod(temporaryDir, 0o700); err != nil {
		failInternal(c, http.StatusInternalServerError, err)
		return
	}
	temporary, err := os.CreateTemp(temporaryDir, "asset-*.part")
	if err != nil {
		failInternal(c, http.StatusInternalServerError, err)
		return
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		failInternal(c, http.StatusInternalServerError, err)
		return
	}
	metadata, transferErr := h.manager.DownloadPeerAsset(
		c.Request.Context(), request.PeerID, request.Address, request.Port,
		catalogID, contentHash, temporary, maxBytes-1,
	)
	if transferErr != nil {
		_ = temporary.Close()
		writeLANAssetTransferError(c, transferErr)
		return
	}
	if metadata.ContentHash != contentHash || metadata.Size != request.Size || metadata.MIMEType != request.MIMEType || (request.Kind != "" && metadata.Kind != request.Kind) || (request.Width > 0 && metadata.Width != request.Width) || (request.Height > 0 && metadata.Height != request.Height) || (request.DurationMs > 0 && metadata.DurationMs != request.DurationMs) {
		_ = temporary.Close()
		fail(c, http.StatusConflict, app.NewAppError(http.StatusConflict, "局域网素材与清单元数据不一致"))
		return
	}
	if _, err := temporary.Seek(0, io.SeekStart); err != nil {
		_ = temporary.Close()
		failInternal(c, http.StatusInternalServerError, err)
		return
	}
	name := request.Name
	if name == "" {
		name = metadata.Name
	}
	if !validLANAssetName(name) {
		_ = temporary.Close()
		fail(c, http.StatusConflict, app.NewAppError(http.StatusConflict, "素材名称无效"))
		return
	}
	kind := metadata.Kind
	if request.Kind != "" {
		kind = request.Kind
	}
	width, height, durationMs := metadata.Width, metadata.Height, metadata.DurationMs
	if request.Width > 0 {
		width = request.Width
	}
	if request.Height > 0 {
		height = request.Height
	}
	if request.DurationMs > 0 {
		durationMs = request.DurationMs
	}
	uploadIdentity := "lan-share:" + catalogID + ":" + contentHash
	if replacingMissing {
		// The previous READY resource may still own the deterministic upload key.
		uploadIdentity += ":recovery:" + kernel.NewID()
	}
	resource, err := h.service.UploadLocalResourceFile(
		owner.ID, name, metadata.Size, kind, width, height, durationMs,
		temporary, uploadIdentity,
	)
	closeErr := temporary.Close()
	if err == nil && closeErr != nil {
		err = closeErr
	}
	if err != nil {
		failService(c, err)
		return
	}
	if !readyLocalResource(resource, owner.ID) || resource.Size != metadata.Size {
		failInternal(c, http.StatusInternalServerError, errors.New("imported LAN resource did not become ready"))
		return
	}
	actualHash, err := hashOwnedLocalResource(c.Request.Context(), h.service, owner.ID, resource, maxBytes)
	if err != nil || actualHash != contentHash {
		failInternal(c, http.StatusInternalServerError, fmt.Errorf("imported LAN resource failed final verification: %v", err))
		return
	}
	if err := h.saveCatalogResource(c.Request.Context(), catalogID, contentHash, owner.ID, resource.ID, time.Now().UTC()); err != nil {
		failInternal(c, http.StatusInternalServerError, err)
		return
	}
	ok(c, gin.H{"resource": localAssetResourceSummary(resource)})
}

func (h *lanAssetHandler) assetStatuses(c *gin.Context) {
	catalogID := strings.TrimSpace(c.Param("catalogId"))
	if !validLANCatalogID(catalogID) {
		fail(c, http.StatusBadRequest, app.BadAuthRequest("共享清单标识无效"))
		return
	}
	if err := h.requireCatalog(c.Request.Context(), catalogID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			fail(c, http.StatusNotFound, app.NotFound("共享清单不存在"))
		} else {
			failInternal(c, http.StatusInternalServerError, err)
		}
		return
	}
	owner, err := h.localOwner()
	if err != nil {
		failService(c, err)
		return
	}
	var rows []model.LANCatalogResource
	if err := h.db.WithContext(c.Request.Context()).Where("catalog_id = ? AND user_id = ?", catalogID, owner.ID).
		Order("content_hash").Find(&rows).Error; err != nil {
		failInternal(c, http.StatusInternalServerError, err)
		return
	}
	assets := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		availability := row.Availability
		if availability != lanAssetPresent && availability != lanAssetMissing {
			availability = lanAssetUnknown
		}
		asset := gin.H{"contentHash": row.ContentHash, "resourceId": row.ResourceID, "availability": availability, "checkedAt": row.CheckedAt}
		assets = append(assets, asset)
	}
	ok(c, gin.H{"assets": assets})
}

func writeLANAssetTransferError(c *gin.Context, err error) {
	var peerHTTPError *lansharing.PeerAssetHTTPError
	switch {
	case errors.Is(err, lansharing.ErrPeerNotTrusted):
		fail(c, http.StatusForbidden, app.Forbidden("设备尚未建立信任关系"))
	case errors.As(err, &peerHTTPError) && peerHTTPError.StatusCode == http.StatusForbidden:
		fail(c, http.StatusForbidden, app.Forbidden("对方设备拒绝了资产请求"))
	case errors.As(err, &peerHTTPError) && peerHTTPError.StatusCode == http.StatusNotFound:
		fail(c, http.StatusNotFound, app.NotFound("对方设备上不存在该资产"))
	default:
		fail(c, http.StatusBadGateway, app.NewAppError(http.StatusBadGateway, "局域网素材传输失败"))
	}
}

func (h *lanAssetHandler) serveAsset(w http.ResponseWriter, r *http.Request) {
	if r == nil || r.TLS == nil || len(r.TLS.PeerCertificates) != 1 {
		http.Error(w, "peer client identity required", http.StatusUnauthorized)
		return
	}
	owner, err := h.localOwner()
	if err != nil {
		http.Error(w, "local workspace is unavailable", http.StatusServiceUnavailable)
		return
	}
	catalogID := r.PathValue("catalogID")
	contentHash := r.PathValue("contentHash")
	if !validLANCatalogID(catalogID) || len(contentHash) != 64 || !isLowerHexSHA256(contentHash) {
		http.Error(w, "invalid shared asset identity", http.StatusBadRequest)
		return
	}
	if err := h.requireCatalog(r.Context(), catalogID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			http.NotFound(w, r)
		} else {
			http.Error(w, "shared catalog lookup failed", http.StatusInternalServerError)
		}
		return
	}
	var mapping model.LANCatalogResource
	if err := h.db.WithContext(r.Context()).Where(
		"catalog_id = ? AND hash_algorithm = ? AND content_hash = ? AND user_id = ?",
		catalogID, "sha256", contentHash, owner.ID,
	).First(&mapping).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			http.NotFound(w, r)
		} else {
			http.Error(w, "shared asset lookup failed", http.StatusInternalServerError)
		}
		return
	}
	if mapping.VerifiedAt.IsZero() {
		http.Error(w, "shared asset mapping is not verified", http.StatusConflict)
		return
	}
	resource, err := h.service.Resource(owner.ID, mapping.ResourceID)
	if err != nil || !readyLocalResource(resource, owner.ID) {
		http.NotFound(w, r)
		return
	}
	stream, err := h.service.OpenResourceRange(owner.ID, resource.ID, "")
	if err != nil {
		http.Error(w, "shared asset is unavailable", http.StatusGone)
		return
	}
	defer stream.Body.Close()
	if stream.ContentLength != resource.Size || stream.ContentLength < 1 {
		http.Error(w, "shared asset size is inconsistent", http.StatusConflict)
		return
	}
	metadata := lansharing.AssetMetadata{
		ContentHash: contentHash, Size: stream.ContentLength,
		Name: fallbackLANAssetName(resource), MIMEType: safeMIMEType(resource.MimeType),
		Kind: resource.Kind, Width: resource.Width, Height: resource.Height, DurationMs: resource.DurationMs,
	}
	encodedMetadata, err := lansharing.EncodeAssetMetadata(metadata)
	if err != nil {
		http.Error(w, "shared asset metadata is invalid", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", metadata.MIMEType)
	w.Header().Set("Content-Length", fmt.Sprint(stream.ContentLength))
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": metadata.Name}))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-BeefTV-Content-SHA256", contentHash)
	w.Header().Set("X-BeefTV-Asset-Metadata", encodedMetadata)
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, stream.Body); err != nil {
		return
	}
}

func (h *lanAssetHandler) localOwner() (*model.User, error) {
	if h == nil || h.service == nil || !h.service.IsLocalMode() {
		return nil, app.Forbidden("局域网素材仅适用于本地工作区")
	}
	return h.service.LocalWorkspaceOwner()
}

func (h *lanAssetHandler) saveCatalogResource(ctx context.Context, catalogID, contentHash, userID, resourceID string, verifiedAt time.Time) error {
	row := model.LANCatalogResource{
		CatalogID: catalogID, HashAlgorithm: "sha256", ContentHash: contentHash,
		UserID: userID, ResourceID: resourceID, VerifiedAt: verifiedAt, Availability: lanAssetPresent,
		CheckedAt: &verifiedAt, CreatedAt: verifiedAt, UpdatedAt: verifiedAt,
	}
	return h.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "catalog_id"}, {Name: "hash_algorithm"}, {Name: "content_hash"}, {Name: "user_id"}},
		DoUpdates: clause.Assignments(map[string]any{"resource_id": resourceID, "verified_at": verifiedAt, "availability": lanAssetPresent, "checked_at": verifiedAt, "updated_at": verifiedAt}),
	}).Create(&row).Error
}

func (h *lanAssetHandler) requireCatalog(ctx context.Context, catalogID string) error {
	var catalog model.LANShareCatalog
	return h.db.WithContext(ctx).First(&catalog, "id = ?", catalogID).Error
}

func bindStrictJSON(c *gin.Context, destination any) error {
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("unexpected trailing JSON data")
	}
	return nil
}

func readyLocalResource(resource *model.Resource, userID string) bool {
	return resource != nil && resource.UserID == userID && resource.Status == model.ResourceStatusReady && resource.Provider == "local" && resource.Size > 0
}

func hashOwnedLocalResource(ctx context.Context, service *app.Service, userID string, resource *model.Resource, maxBytes int64) (string, error) {
	if !readyLocalResource(resource, userID) || resource.Size >= maxBytes {
		return "", errors.New("local resource is not eligible for LAN sharing")
	}
	opened, body, err := service.OpenResource(userID, resource.ID)
	if err != nil {
		return "", err
	}
	defer body.Close()
	if !readyLocalResource(opened, userID) || opened.ID != resource.ID || opened.Size != resource.Size {
		return "", errors.New("local resource owner or size changed")
	}
	hash := sha256.New()
	reader := lanAssetContextReader{ctx: ctx, source: body}
	written, err := io.CopyN(hash, reader, resource.Size)
	if err != nil || written != resource.Size {
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return "", err
	}
	var extra [1]byte
	if count, readErr := reader.Read(extra[:]); count != 0 || !errors.Is(readErr, io.EOF) {
		return "", errors.New("local resource byte count does not match its record")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

type lanAssetContextReader struct {
	ctx    context.Context
	source io.Reader
}

func (r lanAssetContextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.source.Read(buffer)
}

func lanResourceMaximumBytes(service *app.Service) (int64, error) {
	policy, err := service.RuntimePolicy()
	if err != nil {
		return 0, err
	}
	if policy.Resource.ResourceUploadMB < 1 || policy.Resource.ResourceUploadMB > (1<<62)/(1<<20) {
		return 0, errors.New("resource upload policy is invalid")
	}
	return policy.Resource.ResourceUploadMB * (1 << 20), nil
}

func validLANCatalogID(value string) bool {
	if value == "" || len(value) > 80 {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '-' || char == '_' {
			continue
		}
		return false
	}
	return true
}

func validLANAssetEntryID(value string) bool {
	return validLANCatalogID(value)
}

func validLANPrivateIPv4(value string) bool {
	ip := net.ParseIP(value)
	return ip != nil && ip.To4() != nil && ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsUnspecified() && !ip.IsMulticast()
}

func validLANPeerID(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func isLowerHexSHA256(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func validLANAssetName(value string) bool {
	if value == "" || len(value) > 255 || value == "." || value == ".." || strings.ContainsAny(value, "/\\\r\n\x00") {
		return false
	}
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return false
		}
	}
	return filepath.Base(value) == value
}

func validLANAssetMIME(value string) bool {
	if len(value) > 120 {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(value)
	return err == nil && mediaType != ""
}

func validLANAssetKind(value string) bool {
	return value == "image" || value == "video" || value == "audio" || value == "file"
}

func safeMIMEType(value string) string {
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(value))
	if err != nil || mediaType == "" {
		return "application/octet-stream"
	}
	return mediaType
}

func fallbackLANAssetName(resource *model.Resource) string {
	if resource == nil {
		return "asset.bin"
	}
	name := strings.TrimSpace(resource.ID)
	if name == "" {
		name = "asset"
	}
	extensions, _ := mime.ExtensionsByType(safeMIMEType(resource.MimeType))
	if len(extensions) > 0 {
		name += extensions[0]
	} else {
		name += ".bin"
	}
	return name
}

func localAssetResourceSummary(resource *model.Resource) gin.H {
	return gin.H{
		"id": resource.ID, "userId": resource.UserID, "kind": resource.Kind, "status": resource.Status,
		"provider": "local", "endpoint": "", "bucket": "", "objectKey": "", "publicUrl": "",
		"mimeType": safeMIMEType(resource.MimeType), "size": resource.Size,
		"width": resource.Width, "height": resource.Height, "durationMs": resource.DurationMs,
		"createdAt": resource.CreatedAt, "updatedAt": resource.UpdatedAt,
	}
}
