package lansharing

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

func TestDownloadPeerAssetRejectsNonPrivateAddressAndUntrustedIdentity(t *testing.T) {
	contentHash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	manager := &Manager{}
	_, err := manager.DownloadPeerAsset(context.Background(), "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "8.8.8.8", 1234, "catalog-1", contentHash, &bytes.Buffer{}, 1024)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("public peer address error = %v, want ErrInvalid", err)
	}

	manager = &Manager{}
	_, err = manager.DownloadPeerAsset(context.Background(), "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "10.0.0.2", 1234, "catalog-1", contentHash, &bytes.Buffer{}, 1024)
	if !errors.Is(err, ErrPeerNotTrusted) {
		t.Fatalf("untrusted peer error = %v, want ErrPeerNotTrusted", err)
	}
}

func TestEncodeAssetMetadataRequiresSafeFilenameAndValidContentIdentity(t *testing.T) {
	metadata := AssetMetadata{
		ContentHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Size:        12, Name: "clip.mp4", MIMEType: "video/mp4", Kind: "video",
	}
	if _, err := EncodeAssetMetadata(metadata); err != nil {
		t.Fatalf("EncodeAssetMetadata(valid) error = %v", err)
	}
	metadata.Name = "../clip.mp4"
	if _, err := EncodeAssetMetadata(metadata); err == nil {
		t.Fatal("EncodeAssetMetadata accepted a path-like filename")
	}
	metadata.Name = "clip.mp4"
	metadata.ContentHash = "not-a-hash"
	if _, err := EncodeAssetMetadata(metadata); err == nil {
		t.Fatal("EncodeAssetMetadata accepted an invalid content hash")
	}
}
