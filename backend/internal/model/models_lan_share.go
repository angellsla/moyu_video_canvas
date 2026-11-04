package model

import "time"

// LANNodeIdentity stores the installation's stable public identity. Private
// key material is persisted only as ciphertext produced by the workspace key.
type LANNodeIdentity struct {
	NodeID           string    `json:"nodeId" gorm:"primaryKey;size:64"`
	PublicKey        string    `json:"publicKey" gorm:"type:text;not null"`
	PrivateKeyCipher string    `json:"-" gorm:"type:text;not null"`
	Nickname         string    `json:"nickname" gorm:"size:80"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

type LANTrustedPeer struct {
	NodeID      string    `json:"nodeId" gorm:"primaryKey;size:64"`
	PublicKey   string    `json:"publicKey" gorm:"type:text;not null"`
	Nickname    string    `json:"nickname" gorm:"size:80"`
	Address     string    `json:"address,omitempty" gorm:"size:45"`
	Port        int       `json:"port,omitempty"`
	TrustState  string    `json:"trustState" gorm:"size:24;not null;index"`
	FirstSeenAt time.Time `json:"firstSeenAt"`
	LastSeenAt  time.Time `json:"lastSeenAt" gorm:"index"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type LANShareCatalog struct {
	ID              string    `json:"id" gorm:"primaryKey;size:36"`
	Name            string    `json:"name" gorm:"size:120;not null"`
	CreatedByNodeID string    `json:"createdByNodeId" gorm:"size:64;index"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

// LANCatalogYjsUpdate is an append-only synchronization record. UpdateHash
// deduplicates identical binary updates within a catalog.
type LANCatalogYjsUpdate struct {
	ID           string    `json:"id" gorm:"primaryKey;size:36"`
	CatalogID    string    `json:"catalogId" gorm:"size:36;not null;index:idx_lan_catalog_yjs_updates_catalog_created,priority:1;uniqueIndex:idx_lan_catalog_yjs_updates_catalog_hash,priority:1"`
	UpdateHash   string    `json:"updateHash" gorm:"size:64;not null;uniqueIndex:idx_lan_catalog_yjs_updates_catalog_hash,priority:2"`
	OriginNodeID string    `json:"originNodeId" gorm:"size:64;index"`
	Update       []byte    `json:"-" gorm:"type:blob;not null"`
	CreatedAt    time.Time `json:"createdAt" gorm:"index:idx_lan_catalog_yjs_updates_catalog_created,priority:2"`
}

// LANCatalogResource maps a verified shared content identity to a normal,
// locally owned resource. It does not change Resource ownership semantics.
type LANCatalogResource struct {
	CatalogID     string    `json:"catalogId" gorm:"primaryKey;size:36"`
	HashAlgorithm string    `json:"hashAlgorithm" gorm:"primaryKey;size:16"`
	ContentHash   string    `json:"contentHash" gorm:"primaryKey;size:128"`
	UserID        string    `json:"userId" gorm:"primaryKey;size:36;index"`
	ResourceID    string    `json:"resourceId" gorm:"size:36;not null;index"`
	VerifiedAt    time.Time `json:"verifiedAt"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// LANCollaborationCanvas is a shared canvas room. It is independent from a
// user's CanvasProject so collaboration never changes personal CAS/history.
type LANCollaborationCanvas struct {
	ID               string     `json:"id" gorm:"primaryKey;size:36"`
	Name             string     `json:"name" gorm:"size:120;not null"`
	CreatedByNodeID  string     `json:"createdByNodeId" gorm:"size:64;not null;index"`
	AssetCatalogID   string     `json:"assetCatalogId,omitempty" gorm:"size:36;index"`
	MetadataRevision int64      `json:"metadataRevision" gorm:"not null;default:1"`
	CreatedAt        time.Time  `json:"createdAt"`
	UpdatedAt        time.Time  `json:"updatedAt"`
	ArchivedAt       *time.Time `json:"archivedAt,omitempty"`
	DeletedAt        *time.Time `json:"deletedAt,omitempty"`
}

func (LANCollaborationCanvas) TableName() string {
	return "lan_collaboration_canvases"
}

// LANCollaborationCanvasYjsUpdate stores opaque Yjs updates durably and
// deduplicates identical updates within a room.
type LANCollaborationCanvasYjsUpdate struct {
	ID           string    `json:"id" gorm:"primaryKey;size:36"`
	CanvasID     string    `json:"canvasId" gorm:"size:36;not null;index:idx_lan_collab_canvas_updates_created,priority:1;uniqueIndex:idx_lan_collab_canvas_updates_hash,priority:1"`
	UpdateHash   string    `json:"updateHash" gorm:"size:64;not null;uniqueIndex:idx_lan_collab_canvas_updates_hash,priority:2"`
	OriginNodeID string    `json:"originNodeId" gorm:"size:64;index"`
	Update       []byte    `json:"-" gorm:"type:blob;not null"`
	CreatedAt    time.Time `json:"createdAt" gorm:"index:idx_lan_collab_canvas_updates_created,priority:2"`
}
