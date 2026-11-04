package handler

import (
	"context"
	"errors"
	"log"
	"os"
	"time"

	"infinite-canvas/backend/internal/app"
	"infinite-canvas/backend/internal/model"

	"gorm.io/gorm"
)

const (
	lanAssetScanInterval = 5 * time.Minute
	lanAssetScanPageSize = 100
	lanAssetPresent      = "present"
	lanAssetMissing      = "missing"
	lanAssetUnknown      = "unknown"
)

// RunLANAssetScan checks only this workspace's declared local copies. A scan
// never removes catalog entries or changes another node's availability.
func RunLANAssetScan(ctx context.Context, service *app.Service) {
	if ctx == nil || service == nil || service.Database() == nil {
		return
	}
	if err := scanLANAssetsOnce(ctx, service); err != nil && ctx.Err() == nil {
		log.Printf("局域网素材巡检失败: %v", err)
	}
	ticker := time.NewTicker(lanAssetScanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := scanLANAssetsOnce(ctx, service); err != nil && ctx.Err() == nil {
				log.Printf("局域网素材巡检失败: %v", err)
			}
		}
	}
}

func scanLANAssetsOnce(ctx context.Context, service *app.Service) error {
	return scanLANAssetsOnceWithProbe(ctx, service, inspectLANAsset)
}

func scanLANAssetsOnceWithProbe(ctx context.Context, service *app.Service, probe func(context.Context, *app.Service, string, *model.LANCatalogResource) string) error {
	owner, err := service.LocalWorkspaceOwner()
	if err != nil {
		return err
	}
	db := service.Database()
	for offset := 0; ; offset += lanAssetScanPageSize {
		if err := ctx.Err(); err != nil {
			return err
		}
		var rows []model.LANCatalogResource
		err := db.WithContext(ctx).Where("user_id = ?", owner.ID).
			Order("catalog_id, hash_algorithm, content_hash, user_id").
			Limit(lanAssetScanPageSize).Offset(offset).Find(&rows).Error
		if err != nil {
			return err
		}
		for _, row := range rows {
			if err := ctx.Err(); err != nil {
				return err
			}
			availability := probe(ctx, service, owner.ID, &row)
			if err := ctx.Err(); err != nil {
				return err
			}
			now := time.Now().UTC()
			if err := db.WithContext(ctx).Model(&model.LANCatalogResource{}).Where(
				"catalog_id = ? AND hash_algorithm = ? AND content_hash = ? AND user_id = ? AND resource_id = ? AND verified_at = ?",
				row.CatalogID, row.HashAlgorithm, row.ContentHash, row.UserID, row.ResourceID, row.VerifiedAt,
			).Updates(map[string]any{"availability": availability, "checked_at": now}).Error; err != nil {
				return err
			}
		}
		if len(rows) < lanAssetScanPageSize {
			return nil
		}
	}
}

func inspectLANAsset(ctx context.Context, service *app.Service, userID string, row *model.LANCatalogResource) string {
	if row == nil || row.VerifiedAt.IsZero() {
		return lanAssetUnknown
	}
	if ctx.Err() != nil {
		return lanAssetUnknown
	}
	resource, err := service.Resource(userID, row.ResourceID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return lanAssetMissing
	}
	if err != nil {
		return lanAssetUnknown
	}
	if !readyLocalResource(resource, userID) {
		return lanAssetMissing
	}
	stream, err := service.OpenResourceRange(userID, resource.ID, "")
	if errors.Is(err, os.ErrNotExist) {
		return lanAssetMissing
	}
	if err != nil {
		return lanAssetUnknown
	}
	defer stream.Body.Close()
	if stream.ContentLength != resource.Size || stream.ContentLength < 1 {
		return lanAssetUnknown
	}
	return lanAssetPresent
}
