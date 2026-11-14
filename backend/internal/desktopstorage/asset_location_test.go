package desktopstorage

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveAssetDirDefaultsToWorkspaceResourceDirectory(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "workspace")
	got, err := ResolveAssetDir(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dataDir, "resources")
	if got != want {
		t.Fatalf("ResolveAssetDir() = %q, want %q", got, want)
	}
}

func TestResolveAssetDirDoesNotSilentlyFallBackWhenSelectedVolumeIsMissing(t *testing.T) {
	dataDir := t.TempDir()
	resourceDir := filepath.Join(t.TempDir(), "assets")
	if err := os.MkdirAll(resourceDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := SaveAssetLocation(dataDir, AssetLocation{ResourceDir: resourceDir}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(resourceDir); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveAssetDir(dataDir); err == nil {
		t.Fatal("missing selected asset volume silently fell back to the workspace directory")
	}
}

func TestValidateAssetTargetRejectsWorkspaceAndSourceOverlap(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "workspace")
	source := filepath.Join(dataDir, "resources")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{
		"inside workspace":   filepath.Join(dataDir, "external-assets"),
		"workspace ancestor": filepath.Dir(dataDir),
		"inside source":      filepath.Join(source, "nested"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ValidateAssetTarget(dataDir, source, target); err == nil {
				t.Fatal("overlapping target was accepted")
			}
		})
	}
}

func TestCleanupPreviousAssetDirVerifiesBeforeDeleting(t *testing.T) {
	dataDir := t.TempDir()
	previous := filepath.Join(t.TempDir(), "old-assets")
	current := filepath.Join(t.TempDir(), "new-assets")
	oldFile := filepath.Join(previous, "users", "local", "image", "one.png")
	newFile := filepath.Join(current, "users", "local", "image", "one.png")
	for _, path := range []string{oldFile, newFile} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("verified asset"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	files, err := Snapshot(previous)
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveAssetLocation(dataDir, AssetLocation{ResourceDir: current, PreviousDir: previous, PreviousFiles: files}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldFile, []byte("changed outside the app"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := CleanupPreviousAssetDir(dataDir); err == nil {
		t.Fatal("changed old asset directory was deleted")
	}
	if _, err := os.Stat(oldFile); err != nil {
		t.Fatalf("changed source asset was removed: %v", err)
	}
	if err := os.WriteFile(oldFile, []byte("verified asset"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := CleanupPreviousAssetDir(dataDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(previous); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("previous asset directory remains: %v", err)
	}
	location, err := ResolveAssetLocation(dataDir)
	if err != nil || location.ResourceDir != current || location.PreviousDir != "" {
		t.Fatalf("location after cleanup = %#v err=%v", location, err)
	}
	if got, err := os.ReadFile(newFile); err != nil || string(got) != "verified asset" {
		t.Fatalf("current copy changed: %q err=%v", got, err)
	}
}
