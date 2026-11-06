package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"infinite-canvas/backend/internal/bootstrap"
)

func TestSaveOwnedArchiveStreamsLargeResourceAndMetadata(t *testing.T) {
	app := newDesktopApp(t.TempDir())
	app.startup(context.Background())
	defer app.shutdown(context.Background())
	payload := bytes.Repeat([]byte("archive-source-"), (33<<20)/15+1)
	resourceID := uploadDesktopResource(t, app, payload)
	dest := filepath.Join(t.TempDir(), "canvas.zip")
	app.chooseSavePath = func(context.Context, string) (string, error) { return dest, nil }
	metadata := []byte(`{"projects":[]}`)
	entries := []bootstrap.OwnedArchiveEntry{{Name: "projects.json", Data: metadata, SHA256: fmtSHA256(metadata)}, {Name: "media/video.mp4", ResourceID: resourceID, SHA256: fmtSHA256(payload)}}
	saved, err := app.SaveOwnedArchive("canvas.zip", entries)
	if err != nil || !saved {
		t.Fatalf("save=%v err=%v", saved, err)
	}
	archive, err := zip.OpenReader(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	if len(archive.File) != 2 {
		t.Fatal("missing archive entries")
	}
	reader, err := archive.File[1].Open()
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(reader)
	reader.Close()
	if err != nil || sha256.Sum256(body) != sha256.Sum256(payload) {
		t.Fatal("media changed or truncated")
	}
	if info, err := os.Stat(dest); err != nil || info.Size() <= 32<<20 {
		t.Fatal("large archive was not saved")
	}
}

func TestSaveOwnedArchiveCancelAndInvalidReferencesKeepDestination(t *testing.T) {
	app := newDesktopApp(t.TempDir())
	app.wailsCtx = context.Background()
	app.chooseSavePath = func(context.Context, string) (string, error) { return "", nil }
	if saved, err := app.SaveOwnedArchive("canvas.zip", nil); saved || err != nil {
		t.Fatalf("cancel=%v %v", saved, err)
	}
	app.startup(context.Background())
	defer app.shutdown(context.Background())
	dest := filepath.Join(t.TempDir(), "existing.zip")
	if err := os.WriteFile(dest, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	app.chooseSavePath = func(context.Context, string) (string, error) { return dest, nil }
	for _, entries := range [][]bootstrap.OwnedArchiveEntry{
		{{Name: "../private", Data: []byte("bad")}},
		{{Name: "same", Data: []byte("a")}, {Name: "same", Data: []byte("b")}},
		{{Name: "media/foreign.mp4", ResourceID: "not-owned"}},
		{{Name: "media/private.mp4", ResourceID: "../private"}},
		{{Name: "media/video.mp4", ResourceID: "not-owned", SHA256: "invalid"}},
		{{Name: "metadata.json", Data: []byte("body"), SHA256: strings.Repeat("A", 64)}},
	} {
		if saved, err := app.SaveOwnedArchive("canvas.zip", entries); saved || err == nil {
			t.Fatalf("invalid archive saved: %+v", entries)
		}
		body, err := os.ReadFile(dest)
		if err != nil || string(body) != "existing" {
			t.Fatal("existing destination changed")
		}
	}
	leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(dest), ".beeftv-save-*"))
	if len(leftovers) != 0 {
		t.Fatalf("temporary archive leaked: %v", leftovers)
	}
}

func TestSaveOwnedArchiveHashMismatchKeepsDestination(t *testing.T) {
	app := newDesktopApp(t.TempDir())
	app.startup(context.Background())
	defer app.shutdown(context.Background())
	resourceID := uploadDesktopResource(t, app, []byte("asset bytes"))
	dest := filepath.Join(t.TempDir(), "existing.zip")
	if err := os.WriteFile(dest, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	app.chooseSavePath = func(context.Context, string) (string, error) { return dest, nil }
	for _, entry := range []bootstrap.OwnedArchiveEntry{
		{Name: "media/asset.bin", ResourceID: resourceID, SHA256: fmtSHA256([]byte("other bytes"))},
		{Name: "metadata.json", Data: []byte(`{"asset":true}`), SHA256: fmtSHA256([]byte("other metadata"))},
	} {
		if saved, err := app.SaveOwnedArchive("canvas.zip", []bootstrap.OwnedArchiveEntry{entry}); saved || err == nil || !strings.Contains(err.Error(), "校验失败") {
			t.Fatalf("hash mismatch: saved=%v err=%v", saved, err)
		}
		body, err := os.ReadFile(dest)
		if err != nil || string(body) != "existing" {
			t.Fatal("existing destination changed")
		}
	}
	leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(dest), ".beeftv-save-*"))
	if len(leftovers) != 0 {
		t.Fatalf("temporary archive leaked: %v", leftovers)
	}
}

func fmtSHA256(body []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(body))
}
