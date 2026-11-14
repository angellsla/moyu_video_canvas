package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"infinite-canvas/backend/internal/desktopstorage"
)

func TestMigrateAssetStorageKeepsDatabaseAndResourceAccess(t *testing.T) {
	dataDir := t.TempDir()
	target := filepath.Join(t.TempDir(), "assets")
	app := newDesktopApp(dataDir)
	if err := app.start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer app.stop(context.Background())

	request := func(method, route string, body io.Reader, contentType string) []byte {
		t.Helper()
		runtime := app.runtime()
		req, err := http.NewRequest(method, runtime.BaseURL()+route, body)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Desktop-Token", runtime.LaunchToken())
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, _ := io.ReadAll(response.Body)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s %s status %d: %s", method, route, response.StatusCode, data)
		}
		return data
	}

	var upload bytes.Buffer
	writer := multipart.NewWriter(&upload)
	part, err := writer.CreateFormFile("file", "asset-image.png")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte("synthetic image bytes"))
	_ = writer.WriteField("kind", "image")
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	var uploaded struct {
		Data struct {
			Resource struct {
				ID string `json:"id"`
			} `json:"resource"`
		} `json:"data"`
	}
	if err := json.Unmarshal(request(http.MethodPost, "/resources", &upload, writer.FormDataContentType()), &uploaded); err != nil {
		t.Fatal(err)
	}
	if uploaded.Data.Resource.ID == "" {
		t.Fatal("upload response omitted resource id")
	}

	settings, err := app.AssetStorageSettings()
	if err != nil {
		t.Fatal(err)
	}
	oldResourceDir := settings.ResourceDir
	databasePath := filepath.Join(dataDir, "open_ai_canvas.db")
	if _, err := os.Stat(databasePath); err != nil {
		t.Fatalf("workspace database missing before migration: %v", err)
	}
	occupied := filepath.Join(t.TempDir(), "occupied")
	if err := os.MkdirAll(occupied, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(occupied, "keep.txt"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := app.MigrateAssetStorage(occupied); err == nil {
		t.Fatal("non-empty target was accepted")
	}
	if app.runtime() == nil || !app.runtime().Ready() {
		t.Fatal("invalid target stopped the workspace")
	}
	if _, err := app.MigrateAssetStorage(target); err != nil {
		t.Fatal(err)
	}
	if app.dataDir != dataDir {
		t.Fatalf("workspace data directory changed to %q", app.dataDir)
	}
	settings, err = app.AssetStorageSettings()
	if err != nil {
		t.Fatal(err)
	}
	if settings.ResourceDir != target || settings.PreviousDir != oldResourceDir {
		t.Fatalf("asset storage settings = %#v", settings)
	}
	if _, err := os.Stat(databasePath); err != nil {
		t.Fatalf("workspace database moved or removed: %v", err)
	}
	if got := string(request(http.MethodGet, "/resources/"+uploaded.Data.Resource.ID+"/file", nil, "")); got != "synthetic image bytes" {
		t.Fatalf("resource response after migration = %q", got)
	}
	if _, err := os.Stat(filepath.Join(oldResourceDir, filepath.FromSlash("users"))); err != nil {
		t.Fatalf("original asset copy was not retained: %v", err)
	}

	if _, err := app.CleanupPreviousAssetStorage(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldResourceDir); !os.IsNotExist(err) {
		t.Fatalf("old asset directory remains after cleanup: %v", err)
	}
	if err := app.stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := app.start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := string(request(http.MethodGet, "/resources/"+uploaded.Data.Resource.ID+"/file", nil, "")); got != "synthetic image bytes" {
		t.Fatalf("resource response after restart = %q", got)
	}
	if _, err := desktopstorage.ResolveAssetDir(dataDir); err != nil {
		t.Fatal(err)
	}
}

func TestRebaseMigratedAssetLocationKeepsPreviousAssetReceiptUsable(t *testing.T) {
	source := t.TempDir()
	target := filepath.Join(t.TempDir(), "workspace")
	external := filepath.Join(t.TempDir(), "external-assets")
	previous := filepath.Join(source, "resources")
	if err := os.MkdirAll(previous, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(target, 0700); err != nil {
		t.Fatal(err)
	}
	assetPath := filepath.Join(previous, "users", "local", "image", "asset.png")
	if err := os.MkdirAll(filepath.Dir(assetPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(assetPath, []byte("asset"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(external, 0700); err != nil {
		t.Fatal(err)
	}
	files, err := desktopstorage.Snapshot(previous)
	if err != nil {
		t.Fatal(err)
	}
	location := desktopstorage.AssetLocation{ResourceDir: external, PreviousDir: previous, PreviousFiles: files}
	if err := desktopstorage.SaveAssetLocation(source, location); err != nil {
		t.Fatal(err)
	}
	if err := desktopstorage.SaveAssetLocation(target, location); err != nil {
		t.Fatal(err)
	}
	if err := desktopstorage.CopyClosedWorkspace(context.Background(), previous, filepath.Join(target, "resources")); err != nil {
		t.Fatal(err)
	}
	if err := rebaseMigratedAssetLocation(source, target); err != nil {
		t.Fatal(err)
	}
	got, err := desktopstorage.ResolveAssetLocation(target)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(target, "resources"); got.PreviousDir != want {
		t.Fatalf("rebased previous asset directory = %q, want %q", got.PreviousDir, want)
	}
	if err := desktopstorage.VerifySnapshot(got.PreviousDir, got.PreviousFiles, false); err != nil {
		t.Fatalf("rebased previous asset receipt cannot be verified: %v", err)
	}
}
