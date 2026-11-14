package lansharing

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"

	"gorm.io/gorm"
	"infinite-canvas/backend/internal/model"
)

func migrateNodeReferences(tx *gorm.DB, previousID, nodeID string) error {
	for _, reference := range []struct {
		model  any
		column string
	}{
		{&model.LANShareCatalog{}, "created_by_node_id"},
		{&model.LANCatalogYjsUpdate{}, "origin_node_id"},
		{&model.LANCollaborationCanvas{}, "created_by_node_id"},
		{&model.LANCollaborationCanvasYjsUpdate{}, "origin_node_id"},
	} {
		if err := tx.Model(reference.model).Where(reference.column+" = ?", previousID).UpdateColumn(reference.column, nodeID).Error; err != nil {
			return err
		}
	}
	return nil
}

// Called only after a successful code/key pairing. Existing trust is never
// transferred merely because a discovery packet claims a hardware ID.
func (m *Manager) migrateLegacyPeer(ctx context.Context, peerID string, key ed25519.PublicKey) error {
	return m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var previous model.LANTrustedPeer
		if err := tx.Where("public_key = ?", base64.RawStdEncoding.EncodeToString(key)).First(&previous).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if previous.NodeID == peerID {
			return nil
		}
		if err := migrateNodeReferences(tx, previous.NodeID, peerID); err != nil {
			return err
		}
		return tx.Model(&model.LANTrustedPeer{}).Where("node_id = ?", previous.NodeID).UpdateColumn("node_id", peerID).Error
	})
}
