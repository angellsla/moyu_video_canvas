package database

import (
	"bytes"
	"reflect"
	"testing"
	"time"

	"infinite-canvas/backend/internal/model"
)

var lanShareModels = []any{
	&model.LANNodeIdentity{},
	&model.LANTrustedPeer{},
	&model.LANShareCatalog{},
	&model.LANCatalogYjsUpdate{},
	&model.LANCatalogResource{},
	&model.LANCollaborationCanvas{},
	&model.LANCollaborationCanvasYjsUpdate{},
}

func TestFreshLocalSchemaIncludesLANSharingTables(t *testing.T) {
	db, _ := openFileDB(t)
	if err := MigrateLocalSchema(db); err != nil {
		t.Fatal(err)
	}
	if err := RequireLocalSchema(db); err != nil {
		t.Fatal(err)
	}
	for _, value := range lanShareModels {
		if !db.Migrator().HasTable(value) {
			t.Fatalf("LAN sharing table missing for %T", value)
		}
	}

	identity := model.LANNodeIdentity{NodeID: "fingerprint-a", PublicKey: "public-key", PrivateKeyCipher: "encrypted-private-key", Nickname: "editor-a"}
	peer := model.LANTrustedPeer{
		NodeID: "fingerprint-b", PublicKey: "peer-key", Nickname: "editor-b", Address: "192.168.1.20", Port: 47831, TrustState: "trusted",
	}
	catalog := model.LANShareCatalog{ID: "catalog-1", Name: "Team assets", CreatedByNodeID: identity.NodeID}
	update := model.LANCatalogYjsUpdate{ID: "update-1", CatalogID: catalog.ID, UpdateHash: "update-hash", OriginNodeID: identity.NodeID, Update: []byte{0, 1, 2}}
	resource := model.LANCatalogResource{CatalogID: catalog.ID, HashAlgorithm: "sha256", ContentHash: "asset-hash", UserID: "local-user", ResourceID: "owned-resource", VerifiedAt: time.Now().UTC()}
	canvas := model.LANCollaborationCanvas{ID: "shared-canvas-1", Name: "Shared canvas", CreatedByNodeID: identity.NodeID, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	canvasUpdate := model.LANCollaborationCanvasYjsUpdate{ID: "canvas-update-1", CanvasID: canvas.ID, UpdateHash: "canvas-update-hash", OriginNodeID: identity.NodeID, Update: []byte{3, 2, 1}}
	for _, value := range []any{&identity, &peer, &catalog, &update, &resource, &canvas, &canvasUpdate} {
		if err := db.Create(value).Error; err != nil {
			t.Fatalf("persist %T: %v", value, err)
		}
	}
	var storedPeer model.LANTrustedPeer
	if err := db.First(&storedPeer, "node_id = ?", peer.NodeID).Error; err != nil || storedPeer.Address != peer.Address || storedPeer.Port != peer.Port {
		t.Fatalf("trusted peer endpoint was not persisted: %+v, %v", storedPeer, err)
	}
	duplicateUpdate := update
	duplicateUpdate.ID = "update-2"
	if err := db.Create(&duplicateUpdate).Error; err == nil {
		t.Fatal("duplicate Yjs update hash was accepted within one catalog")
	}
	var stored model.LANCatalogYjsUpdate
	if err := db.First(&stored, "id = ?", update.ID).Error; err != nil || !bytes.Equal(stored.Update, update.Update) {
		t.Fatalf("Yjs update bytes changed: %v %v", stored.Update, err)
	}
}

func TestLANSharingMigrationFromV28PreservesOwnedResourcesAndLedger(t *testing.T) {
	db, _ := openFileDB(t)
	models := make([]any, 0, len(LocalModels())-len(lanShareModels))
	for _, value := range LocalModels() {
		if isLANShareModel(value) {
			continue
		}
		models = append(models, value)
	}
	if err := db.AutoMigrate(models...); err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&localSchemaMigration{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Workspace{ID: "legacy-owner", Name: "Legacy workspace"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Resource{ID: "resource-owned", UserID: "legacy-owner", Provider: "local", ObjectKey: "users/legacy-owner/image/owned.png"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Asset{ID: "asset-owned", UserID: "legacy-owner", Title: "Owned asset", PayloadJSON: `{"id":"asset-owned"}`}).Error; err != nil {
		t.Fatal(err)
	}
	for _, migration := range canonicalLocalMigrations() {
		if migration.version > 28 {
			continue
		}
		row := localSchemaMigration{Version: migration.version, Name: migration.name, AppliedAt: time.Unix(migration.version, 0).UTC()}
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("stamp historical migration v%d: %v", migration.version, err)
		}
	}
	var before []localSchemaMigration
	if err := db.Order("version").Find(&before).Error; err != nil {
		t.Fatal(err)
	}
	for _, value := range lanShareModels {
		if db.Migrator().HasTable(value) {
			t.Fatalf("v28 fixture unexpectedly includes %T", value)
		}
	}

	if err := MigrateLocalSchema(db); err != nil {
		t.Fatal(err)
	}
	if err := RequireLocalSchema(db); err != nil {
		t.Fatal(err)
	}
	for _, value := range lanShareModels {
		if !db.Migrator().HasTable(value) {
			t.Fatalf("v29 migration did not create %T", value)
		}
	}
	var resource model.Resource
	if err := db.First(&resource, "id = ?", "resource-owned").Error; err != nil || resource.UserID != "legacy-owner" {
		t.Fatalf("resource ownership changed: %+v %v", resource, err)
	}
	var asset model.Asset
	if err := db.First(&asset, "id = ?", "asset-owned").Error; err != nil || asset.UserID != "legacy-owner" {
		t.Fatalf("asset ownership changed: %+v %v", asset, err)
	}
	var after []localSchemaMigration
	if err := db.Order("version").Find(&after).Error; err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before)+2 || !reflect.DeepEqual(before, after[:len(before)]) {
		t.Fatalf("existing migration ledger changed: before=%+v after=%+v", before, after)
	}
	if last := after[len(after)-1]; last.Version != 30 || last.Name != "lan-collaboration-canvases" {
		t.Fatalf("new migration identity = %+v", last)
	}
}

func TestRequireLocalSchemaRejectsMissingLANSharingTable(t *testing.T) {
	db, _ := openFileDB(t)
	if err := MigrateLocalSchema(db); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropTable(&model.LANCatalogResource{}); err != nil {
		t.Fatal(err)
	}
	if err := RequireLocalSchema(db); err == nil {
		t.Fatal("schema with a missing LAN sharing table was accepted")
	}
}

func isLANShareModel(value any) bool {
	switch value.(type) {
	case *model.LANNodeIdentity, *model.LANTrustedPeer, *model.LANShareCatalog, *model.LANCatalogYjsUpdate, *model.LANCatalogResource, *model.LANCollaborationCanvas, *model.LANCollaborationCanvasYjsUpdate:
		return true
	default:
		return false
	}
}
