package lansharing

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"infinite-canvas/backend/internal/kernel"
	"infinite-canvas/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const maxCollaborationCanvasUpdate = 4 << 20

type CollaborationCanvasSummary struct {
	RoomID          string     `json:"roomId,omitempty"`
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	CreatedByNodeID string     `json:"createdByNodeId"`
	AssetCatalogID  string     `json:"assetCatalogId,omitempty"`
	Archived        bool       `json:"archived"`
	UpdateCount     int64      `json:"updateCount"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
	ArchivedAt      *time.Time `json:"archivedAt,omitempty"`
}

type CollaborationCanvasUpdatePage struct {
	Updates []model.LANCollaborationCanvasYjsUpdate `json:"updates"`
	Cursor  string                                  `json:"cursor,omitempty"`
	HasMore bool                                    `json:"hasMore"`
}

type collaborationCanvasMetadata struct {
	RoomID           string     `json:"roomId,omitempty"`
	MetadataAuthor   string     `json:"metadataAuthor,omitempty"`
	ID               string     `json:"id"`
	Name             string     `json:"name"`
	CreatedByNodeID  string     `json:"createdByNodeId"`
	AssetCatalogID   string     `json:"assetCatalogId,omitempty"`
	MetadataRevision int64      `json:"metadataRevision"`
	CreatedAt        time.Time  `json:"createdAt"`
	UpdatedAt        time.Time  `json:"updatedAt"`
	ArchivedAt       *time.Time `json:"archivedAt,omitempty"`
	DeletedAt        *time.Time `json:"deletedAt,omitempty"`
}

func (m *Manager) CollaborationCanvases(ctx context.Context) ([]CollaborationCanvasSummary, error) {
	var rows []model.LANCollaborationCanvas
	if err := m.db.WithContext(ctx).Where("deleted_at IS NULL").Order("updated_at DESC, id").Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]CollaborationCanvasSummary, 0, len(rows))
	for _, row := range rows {
		if row.RoomID != "" && !m.roomMember(row.RoomID, m.NodeID()) {
			continue
		}
		var count int64
		if err := m.db.WithContext(ctx).Model(&model.LANCollaborationCanvasYjsUpdate{}).Where("canvas_id = ?", row.ID).Count(&count).Error; err != nil {
			return nil, err
		}
		result = append(result, collaborationCanvasSummary(row, count))
	}
	return result, nil
}

func (m *Manager) CreateCollaborationCanvas(ctx context.Context, name, assetCatalogID string, initialUpdate []byte) (CollaborationCanvasSummary, error) {
	return m.CreateRoomCollaborationCanvas(ctx, "", name, assetCatalogID, initialUpdate)
}
func (m *Manager) CreateRoomCollaborationCanvas(ctx context.Context, roomID, name, assetCatalogID string, initialUpdate []byte) (CollaborationCanvasSummary, error) {
	if roomID != "" && !m.roomMember(roomID, m.NodeID()) {
		return CollaborationCanvasSummary{}, ErrPeerNotTrusted
	}
	name = strings.TrimSpace(name)
	assetCatalogID = strings.TrimSpace(assetCatalogID)
	if !validCollaborationCanvasName(name) || len(initialUpdate) > maxCollaborationCanvasUpdate || (assetCatalogID != "" && !validCollaborationCanvasID(assetCatalogID)) {
		return CollaborationCanvasSummary{}, ErrInvalid
	}
	if assetCatalogID != "" {
		var count int64
		if err := m.db.WithContext(ctx).Model(&model.LANShareCatalog{}).Where("id = ? AND COALESCE(room_id, '') = ?", assetCatalogID, roomID).Count(&count).Error; err != nil {
			return CollaborationCanvasSummary{}, err
		}
		if count != 1 {
			return CollaborationCanvasSummary{}, ErrNotFound
		}
	}
	if len(initialUpdate) > 0 {
		return m.createCollaborationCanvasWithUpdate(ctx, roomID, name, assetCatalogID, initialUpdate)
	}
	now := time.Now().UTC()
	row := model.LANCollaborationCanvas{RoomID: roomID, MetadataAuthor: m.NodeID(), ID: kernel.NewID(), Name: name, CreatedByNodeID: m.NodeID(), AssetCatalogID: assetCatalogID, MetadataRevision: 1, CreatedAt: now, UpdatedAt: now}
	if err := m.db.WithContext(ctx).Create(&row).Error; err != nil {
		return CollaborationCanvasSummary{}, err
	}
	m.scheduleKnownCatalogSync()
	return collaborationCanvasSummary(row, 0), nil
}

func (m *Manager) createCollaborationCanvasWithUpdate(ctx context.Context, roomID, name, assetCatalogID string, update []byte) (CollaborationCanvasSummary, error) {
	now := time.Now().UTC()
	row := model.LANCollaborationCanvas{RoomID: roomID, MetadataAuthor: m.NodeID(), ID: kernel.NewID(), Name: name, CreatedByNodeID: m.NodeID(), AssetCatalogID: assetCatalogID, MetadataRevision: 1, CreatedAt: now, UpdatedAt: now}
	var stored model.LANCollaborationCanvasYjsUpdate
	err := m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		stored = collaborationUpdateRow(row.ID, update, m.NodeID(), row.CreatedAt)
		if err := tx.Create(&stored).Error; err != nil {
			return err
		}
		return tx.Model(&row).Update("updated_at", row.CreatedAt).Error
	})
	if err != nil {
		return CollaborationCanvasSummary{}, err
	}
	m.scheduleKnownCatalogSync()
	return collaborationCanvasSummary(row, 1), nil
}

func (m *Manager) UpdateCollaborationCanvas(ctx context.Context, canvasID string, name *string, archived *bool) (CollaborationCanvasSummary, error) {
	canvasID = strings.TrimSpace(canvasID)
	if !validCollaborationCanvasID(canvasID) || (name == nil && archived == nil) {
		return CollaborationCanvasSummary{}, ErrInvalid
	}
	var row model.LANCollaborationCanvas
	if err := m.db.WithContext(ctx).First(&row, "id = ?", canvasID).Error; err != nil {
		return CollaborationCanvasSummary{}, collaborationRecordError(err)
	}
	if row.DeletedAt != nil {
		return CollaborationCanvasSummary{}, ErrNotFound
	}
	if !m.canManageCanvas(row) {
		return CollaborationCanvasSummary{}, ErrPeerNotTrusted
	}
	updates := map[string]any{"metadata_author": m.NodeID(), "updated_at": time.Now().UTC(), "metadata_revision": gorm.Expr("metadata_revision + 1")}
	if name != nil {
		value := strings.TrimSpace(*name)
		if !validCollaborationCanvasName(value) {
			return CollaborationCanvasSummary{}, ErrInvalid
		}
		updates["name"] = value
	}
	if archived != nil {
		if *archived {
			now := time.Now().UTC()
			updates["archived_at"] = &now
		} else {
			updates["archived_at"] = nil
		}
	}
	if err := m.db.WithContext(ctx).Model(&row).Updates(updates).Error; err != nil {
		return CollaborationCanvasSummary{}, err
	}
	if err := m.db.WithContext(ctx).First(&row, "id = ?", canvasID).Error; err != nil {
		return CollaborationCanvasSummary{}, err
	}
	var count int64
	if err := m.db.WithContext(ctx).Model(&model.LANCollaborationCanvasYjsUpdate{}).Where("canvas_id = ?", row.ID).Count(&count).Error; err != nil {
		return CollaborationCanvasSummary{}, err
	}
	m.scheduleKnownCatalogSync()
	return collaborationCanvasSummary(row, count), nil
}

func (m *Manager) DeleteCollaborationCanvas(ctx context.Context, canvasID string) error {
	canvasID = strings.TrimSpace(canvasID)
	if !validCollaborationCanvasID(canvasID) {
		return ErrInvalid
	}
	var row model.LANCollaborationCanvas
	if err := m.db.WithContext(ctx).First(&row, "id = ?", canvasID).Error; err != nil {
		return collaborationRecordError(err)
	}
	if row.DeletedAt != nil {
		return ErrNotFound
	}
	if !m.canManageCanvas(row) {
		return ErrPeerNotTrusted
	}
	now := time.Now().UTC()
	if err := m.db.WithContext(ctx).Model(&row).Updates(map[string]any{"metadata_author": m.NodeID(), "deleted_at": &now, "updated_at": now, "metadata_revision": gorm.Expr("metadata_revision + 1")}).Error; err != nil {
		return err
	}
	m.scheduleKnownCatalogSync()
	return nil
}

func (m *Manager) CollaborationCanvasUpdates(ctx context.Context, canvasID, after string, limit int) (CollaborationCanvasUpdatePage, error) {
	canvasID = strings.TrimSpace(canvasID)
	if !validCollaborationCanvasID(canvasID) {
		return CollaborationCanvasUpdatePage{}, ErrInvalid
	}
	var canvas model.LANCollaborationCanvas
	if err := m.db.WithContext(ctx).First(&canvas, "id = ?", canvasID).Error; err != nil {
		return CollaborationCanvasUpdatePage{}, collaborationRecordError(err)
	}
	if canvas.DeletedAt != nil {
		return CollaborationCanvasUpdatePage{}, ErrNotFound
	}
	if canvas.RoomID != "" && !m.roomMember(canvas.RoomID, m.NodeID()) {
		return CollaborationCanvasUpdatePage{}, ErrPeerNotTrusted
	}
	query := m.db.WithContext(ctx).Where("canvas_id = ?", canvasID)
	if after != "" {
		var cursor model.LANCollaborationCanvasYjsUpdate
		if err := m.db.WithContext(ctx).Where("canvas_id = ? AND id = ?", canvasID, after).First(&cursor).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return CollaborationCanvasUpdatePage{}, ErrInvalid
			}
			return CollaborationCanvasUpdatePage{}, err
		}
		query = query.Where("created_at > ? OR (created_at = ? AND id > ?)", cursor.CreatedAt, cursor.CreatedAt, cursor.ID)
	}
	if limit < 1 {
		limit = defaultUpdatePage
	}
	if limit > maximumUpdatePage {
		limit = maximumUpdatePage
	}
	var rows []model.LANCollaborationCanvasYjsUpdate
	if err := query.Order("created_at ASC, id ASC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return CollaborationCanvasUpdatePage{}, err
	}
	page := CollaborationCanvasUpdatePage{HasMore: len(rows) > limit}
	if page.HasMore {
		rows = rows[:limit]
	}
	page.Updates = rows
	if len(rows) > 0 {
		page.Cursor = rows[len(rows)-1].ID
	}
	return page, nil
}

func (m *Manager) AppendCollaborationCanvasUpdate(ctx context.Context, canvasID string, update []byte) (bool, error) {
	return m.appendCollaborationCanvasUpdate(ctx, canvasID, update, m.NodeID(), true)
}

func (m *Manager) appendCollaborationCanvasUpdate(ctx context.Context, canvasID string, update []byte, originNodeID string, broadcast bool) (bool, error) {
	canvasID = strings.TrimSpace(canvasID)
	if !validCollaborationCanvasID(canvasID) || len(update) == 0 || !validNodeID(strings.TrimSpace(originNodeID)) {
		return false, ErrInvalid
	}
	if len(update) > maxCollaborationCanvasUpdate {
		return false, ErrCatalogUpdateBig
	}
	var canvas model.LANCollaborationCanvas
	if err := m.db.WithContext(ctx).First(&canvas, "id = ?", canvasID).Error; err != nil {
		return false, collaborationRecordError(err)
	}
	if canvas.DeletedAt != nil {
		return false, ErrNotFound
	}
	if canvas.RoomID != "" && !m.roomMember(canvas.RoomID, m.NodeID()) {
		return false, ErrPeerNotTrusted
	}
	row := collaborationUpdateRow(canvasID, update, originNodeID, time.Now().UTC())
	result := m.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "canvas_id"}, {Name: "update_hash"}}, DoNothing: true,
	}).Create(&row)
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected == 0 {
		return false, nil
	}
	if err := m.db.WithContext(ctx).Model(&canvas).Update("updated_at", row.CreatedAt).Error; err != nil {
		return false, err
	}
	if broadcast {
		m.collaborationHub.broadcastUpdate(canvasID, originNodeID, base64.StdEncoding.EncodeToString(update))
		m.broadcastCollaborationUpdate(ctx, canvasID, update, originNodeID, "")
		m.scheduleKnownCatalogSync()
	} else {
		m.collaborationHub.broadcastUpdate(canvasID, originNodeID, base64.StdEncoding.EncodeToString(update))
	}
	return true, nil
}

func (m *Manager) collaborationCanvasMetadata(ctx context.Context) ([]collaborationCanvasMetadata, error) {
	var rows []model.LANCollaborationCanvas
	if err := m.db.WithContext(ctx).Order("id").Find(&rows).Error; err != nil {
		return nil, err
	}
	items := make([]collaborationCanvasMetadata, 0, len(rows))
	for _, row := range rows {
		items = append(items, collaborationCanvasMetadata{
			RoomID: row.RoomID, MetadataAuthor: row.MetadataAuthor, ID: row.ID, Name: row.Name, CreatedByNodeID: row.CreatedByNodeID,
			CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, AssetCatalogID: row.AssetCatalogID, MetadataRevision: row.MetadataRevision, ArchivedAt: row.ArchivedAt, DeletedAt: row.DeletedAt,
		})
	}
	return items, nil
}

func (m *Manager) upsertCollaborationCanvasMetadata(ctx context.Context, item collaborationCanvasMetadata) error {
	if item.RoomID != "" && (!m.roomMember(item.RoomID, m.NodeID()) || !validNodeID(item.MetadataAuthor)) {
		return ErrPeerNotTrusted
	}
	if item.AssetCatalogID != "" {
		var catalog model.LANShareCatalog
		if err := m.db.WithContext(ctx).First(&catalog, "id = ?", item.AssetCatalogID).Error; err != nil {
			return collaborationRecordError(err)
		}
		if catalog.RoomID != item.RoomID {
			return ErrConflict
		}
	}
	item.ID = strings.TrimSpace(item.ID)
	item.Name = strings.TrimSpace(item.Name)
	item.CreatedByNodeID = strings.ToLower(strings.TrimSpace(item.CreatedByNodeID))
	if !validCollaborationCanvasID(item.ID) || !validCollaborationCanvasName(item.Name) || !validNodeID(item.CreatedByNodeID) || item.MetadataRevision < 1 || item.CreatedAt.IsZero() || item.UpdatedAt.IsZero() {
		return ErrInvalid
	}
	var existing model.LANCollaborationCanvas
	err := m.db.WithContext(ctx).First(&existing, "id = ?", item.ID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return m.db.WithContext(ctx).Create(&model.LANCollaborationCanvas{
			RoomID: item.RoomID, MetadataAuthor: item.MetadataAuthor, ID: item.ID, Name: item.Name, CreatedByNodeID: item.CreatedByNodeID,
			CreatedAt: item.CreatedAt.UTC(), UpdatedAt: item.UpdatedAt.UTC(), AssetCatalogID: item.AssetCatalogID, MetadataRevision: item.MetadataRevision, ArchivedAt: item.ArchivedAt, DeletedAt: item.DeletedAt,
		}).Error
	}
	if err != nil {
		return err
	}
	if existing.RoomID != item.RoomID || existing.AssetCatalogID != item.AssetCatalogID || existing.CreatedByNodeID != item.CreatedByNodeID || !existing.CreatedAt.Equal(item.CreatedAt) {
		return ErrConflict
	}
	updates := map[string]any{}
	if (item.MetadataRevision > existing.MetadataRevision || (item.RoomID != "" && item.MetadataRevision == existing.MetadataRevision && item.MetadataAuthor > existing.MetadataAuthor) || item.DeletedAt != nil) && existing.DeletedAt == nil {
		updates["metadata_author"] = item.MetadataAuthor
		updates["name"] = item.Name
		updates["asset_catalog_id"] = item.AssetCatalogID
		updates["metadata_revision"] = item.MetadataRevision
		if item.UpdatedAt.After(existing.UpdatedAt) {
			updates["updated_at"] = item.UpdatedAt.UTC()
		}
		updates["archived_at"] = item.ArchivedAt
		if item.DeletedAt != nil {
			updates["deleted_at"] = item.DeletedAt.UTC()
		}
	}
	if len(updates) == 0 {
		return nil
	}
	return m.db.WithContext(ctx).Model(&existing).UpdateColumns(updates).Error
}

func (m *Manager) upsertPeerCollaborationCanvasMetadata(ctx context.Context, peerID string, item collaborationCanvasMetadata) error {
	if !validNodeID(peerID) || !m.canvasPeerAllowed(item, peerID) || (item.RoomID == "" && peerID != item.CreatedByNodeID) {
		return ErrPeerNotTrusted
	}
	return m.upsertCollaborationCanvasMetadata(ctx, item)
}

func (m *Manager) handlePeerGetCollaborationCanvases(w http.ResponseWriter, r *http.Request) {
	items, err := m.collaborationCanvasMetadata(r.Context())
	if err != nil {
		writePeerError(w, err)
		return
	}
	owned := make([]collaborationCanvasMetadata, 0, len(items))
	for _, item := range items {
		if m.canvasPeerAllowed(item, requestPeerID(r)) && (item.RoomID != "" || item.CreatedByNodeID == m.NodeID()) {
			owned = append(owned, item)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"canvases": owned})
}

func (m *Manager) requireCollaborationPeerAccess(ctx context.Context, canvasID, peerID string) (model.LANCollaborationCanvas, error) {
	if !validCollaborationCanvasID(canvasID) || !validNodeID(peerID) || !m.isTrusted(peerID) {
		return model.LANCollaborationCanvas{}, ErrPeerNotTrusted
	}
	var room model.LANCollaborationCanvas
	if err := m.db.WithContext(ctx).First(&room, "id = ?", canvasID).Error; err != nil {
		return model.LANCollaborationCanvas{}, collaborationRecordError(err)
	}
	if room.DeletedAt != nil {
		return model.LANCollaborationCanvas{}, ErrNotFound
	}
	if (room.RoomID != "" && !m.roomAccess(room.RoomID, peerID)) || (room.RoomID == "" && (!m.pairedPeer(peerID) || (room.CreatedByNodeID != m.NodeID() && room.CreatedByNodeID != peerID))) {
		return model.LANCollaborationCanvas{}, ErrPeerNotTrusted
	}
	return room, nil
}

func (m *Manager) handlePeerPostCollaborationCanvas(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var item collaborationCanvasMetadata
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&item); err != nil {
		http.Error(w, "invalid collaboration canvas metadata", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		http.Error(w, "invalid collaboration canvas metadata", http.StatusBadRequest)
		return
	}
	if err := m.upsertPeerCollaborationCanvasMetadata(r.Context(), requestPeerID(r), item); err != nil {
		writePeerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"accepted": true})
}

func (m *Manager) handlePeerGetCollaborationCanvasUpdates(w http.ResponseWriter, r *http.Request) {
	if _, err := m.requireCollaborationPeerAccess(r.Context(), r.PathValue("canvasID"), requestPeerID(r)); err != nil {
		writePeerError(w, err)
		return
	}
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > maximumUpdatePage {
			http.Error(w, "invalid page size", http.StatusBadRequest)
			return
		}
		limit = parsed
	}
	page, err := m.CollaborationCanvasUpdates(r.Context(), r.PathValue("canvasID"), r.URL.Query().Get("after"), limit)
	if err != nil {
		writePeerError(w, err)
		return
	}
	updates := encodeCollaborationUpdates(page.Updates)
	writeJSON(w, http.StatusOK, map[string]any{"updates": updates, "cursor": page.Cursor, "hasMore": page.HasMore})
}

func (m *Manager) handlePeerPostCollaborationCanvasUpdate(w http.ResponseWriter, r *http.Request) {
	peerID := requestPeerID(r)
	room, accessErr := m.requireCollaborationPeerAccess(r.Context(), r.PathValue("canvasID"), peerID)
	if accessErr != nil {
		writePeerError(w, accessErr)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxCollaborationCanvasUpdate*2)
	var payload struct {
		UpdateBase64 string `json:"updateBase64"`
		OriginNodeID string `json:"originNodeId"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		http.Error(w, "invalid collaboration canvas update", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		http.Error(w, "invalid collaboration canvas update", http.StatusBadRequest)
		return
	}
	update, err := base64.StdEncoding.DecodeString(payload.UpdateBase64)
	if err != nil || len(update) == 0 {
		http.Error(w, "invalid collaboration canvas update", http.StatusBadRequest)
		return
	}
	origin := strings.ToLower(strings.TrimSpace(payload.OriginNodeID))
	if !validNodeID(origin) {
		origin = peerID
	}
	if room.RoomID == "" && room.CreatedByNodeID == m.NodeID() && origin != peerID {
		writePeerError(w, ErrPeerNotTrusted)
		return
	}
	accepted, err := m.appendCollaborationCanvasUpdate(r.Context(), r.PathValue("canvasID"), update, origin, false)
	if err != nil {
		writePeerError(w, err)
		return
	}
	if accepted {
		m.broadcastCollaborationUpdate(r.Context(), r.PathValue("canvasID"), update, origin, requestPeerID(r))
	}
	writeJSON(w, http.StatusOK, map[string]bool{"accepted": accepted})
}

func (m *Manager) syncPeerCollaborationCanvases(ctx context.Context, client *http.Client, baseURL, peerID string) error {
	var remote struct {
		Canvases []collaborationCanvasMetadata `json:"canvases"`
	}
	if err := requestPeerJSON(ctx, client, http.MethodGet, baseURL+"/collaboration/canvases", nil, &remote); err != nil {
		return err
	}
	for _, item := range remote.Canvases {
		if !m.canvasPeerAllowed(item, peerID) || (item.RoomID == "" && item.CreatedByNodeID != peerID) {
			continue
		}
		if err := m.upsertPeerCollaborationCanvasMetadata(ctx, peerID, item); err != nil {
			return err
		}
	}
	local, err := m.collaborationCanvasMetadata(ctx)
	if err != nil {
		return err
	}
	for _, item := range local {
		if !m.canvasPeerAllowed(item, peerID) || (item.RoomID == "" && item.CreatedByNodeID != m.NodeID()) {
			continue
		}
		var reply map[string]bool
		if err := requestPeerJSON(ctx, client, http.MethodPost, baseURL+"/collaboration/canvases", item, &reply); err != nil {
			return err
		}
	}
	for _, item := range local {
		if item.DeletedAt != nil {
			continue
		}
		if !m.canvasPeerAllowed(item, peerID) {
			continue
		}
		if err := m.pullPeerCollaborationCanvasUpdates(ctx, client, baseURL, peerID, item.ID); err != nil {
			return err
		}
		if err := m.pushPeerCollaborationCanvasUpdates(ctx, client, baseURL, peerID, item.ID, item.RoomID != "" || item.CreatedByNodeID == m.NodeID()); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) pullPeerCollaborationCanvasUpdates(ctx context.Context, client *http.Client, baseURL, peerID, canvasID string) error {
	var room model.LANCollaborationCanvas
	if err := m.db.WithContext(ctx).First(&room, "id = ?", canvasID).Error; err != nil {
		return collaborationRecordError(err)
	}
	after := ""
	for {
		endpoint := baseURL + "/collaboration/canvases/" + url.PathEscape(canvasID) + "/updates?limit=" + strconv.Itoa(syncUpdatePageSize)
		if after != "" {
			endpoint += "&after=" + url.QueryEscape(after)
		}
		var page struct {
			Updates []encodedCollaborationUpdate `json:"updates"`
			Cursor  string                       `json:"cursor"`
			HasMore bool                         `json:"hasMore"`
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
			if room.RoomID == "" && room.CreatedByNodeID == m.NodeID() && origin != peerID {
				continue
			}
			if _, err := m.appendCollaborationCanvasUpdate(ctx, canvasID, update, origin, false); err != nil {
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

func (m *Manager) pushPeerCollaborationCanvasUpdates(ctx context.Context, client *http.Client, baseURL, peerID, canvasID string, creator bool) error {
	after := ""
	for {
		page, err := m.CollaborationCanvasUpdates(ctx, canvasID, after, syncUpdatePageSize)
		if err != nil {
			return err
		}
		for _, item := range page.Updates {
			if (!creator && item.OriginNodeID != m.NodeID()) || (creator && item.OriginNodeID == peerID) {
				continue
			}
			payload := map[string]string{"updateBase64": base64.StdEncoding.EncodeToString(item.Update), "originNodeId": item.OriginNodeID}
			var reply map[string]bool
			endpoint := baseURL + "/collaboration/canvases/" + url.PathEscape(canvasID) + "/updates"
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

type encodedCollaborationUpdate struct {
	ID           string    `json:"id"`
	UpdateBase64 string    `json:"updateBase64"`
	OriginNodeID string    `json:"originNodeId"`
	CreatedAt    time.Time `json:"createdAt"`
}

func encodeCollaborationUpdates(rows []model.LANCollaborationCanvasYjsUpdate) []encodedCollaborationUpdate {
	updates := make([]encodedCollaborationUpdate, 0, len(rows))
	for _, row := range rows {
		updates = append(updates, encodedCollaborationUpdate{
			ID: row.ID, UpdateBase64: base64.StdEncoding.EncodeToString(row.Update), OriginNodeID: row.OriginNodeID, CreatedAt: row.CreatedAt,
		})
	}
	return updates
}

func collaborationUpdateRow(canvasID string, update []byte, originNodeID string, now time.Time) model.LANCollaborationCanvasYjsUpdate {
	digest := sha256.Sum256(update)
	return model.LANCollaborationCanvasYjsUpdate{
		ID: kernel.NewID(), CanvasID: canvasID, UpdateHash: hex.EncodeToString(digest[:]),
		OriginNodeID: strings.TrimSpace(originNodeID), Update: append([]byte(nil), update...), CreatedAt: now.UTC(),
	}
}

func collaborationCanvasSummary(row model.LANCollaborationCanvas, updateCount int64) CollaborationCanvasSummary {
	return CollaborationCanvasSummary{
		ID: row.ID, RoomID: row.RoomID, Name: row.Name, CreatedByNodeID: row.CreatedByNodeID, AssetCatalogID: row.AssetCatalogID, Archived: row.ArchivedAt != nil,
		UpdateCount: updateCount, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, ArchivedAt: row.ArchivedAt,
	}
}

func collaborationRecordError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	return err
}

func validCollaborationCanvasID(value string) bool {
	return value != "" && len(value) <= 80 && !strings.ContainsAny(value, "/\\?#")
}

func validCollaborationCanvasName(value string) bool {
	return value != "" && len([]rune(value)) <= 120 && !strings.ContainsAny(value, "\r\n\x00")
}

func canManageCollaborationCanvas(row model.LANCollaborationCanvas, actorNodeID string) bool {
	return strings.TrimSpace(actorNodeID) != "" && row.CreatedByNodeID == actorNodeID
}

func requireJSONEOF(decoder *json.Decoder) error {
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return fmt.Errorf("trailing JSON data")
	}
	return nil
}
