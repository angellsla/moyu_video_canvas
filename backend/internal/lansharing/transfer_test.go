package lansharing

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
)

func TestCopyVerifiedCopiesOnlyMatchingContent(t *testing.T) {
	data := []byte("video asset bytes")
	digest := sha256.Sum256(data)
	var destination bytes.Buffer
	if err := CopyVerified(context.Background(), &destination, bytes.NewReader(data), int64(len(data)), hex.EncodeToString(digest[:]), 1024); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(destination.Bytes(), data) {
		t.Fatalf("copied bytes = %q", destination.Bytes())
	}
}

func TestCopyVerifiedRejectsInvalidContentAndLimits(t *testing.T) {
	data := []byte("asset")
	digest := sha256.Sum256(data)
	for _, test := range []struct {
		name   string
		source []byte
		size   int64
		limit  int64
		hash   string
		want   error
	}{
		{name: "wrong hash", source: data, size: int64(len(data)), limit: 1024, hash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", want: ErrTransferHashMismatch},
		{name: "short body", source: data[:2], size: int64(len(data)), limit: 1024, hash: hex.EncodeToString(digest[:]), want: ErrTransferSizeMismatch},
		{name: "extra body", source: append(append([]byte(nil), data...), 0), size: int64(len(data)), limit: 1024, hash: hex.EncodeToString(digest[:]), want: ErrTransferSizeMismatch},
		{name: "too large", source: data, size: int64(len(data)), limit: 1, hash: hex.EncodeToString(digest[:]), want: ErrTransferTooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := CopyVerified(context.Background(), &bytes.Buffer{}, bytes.NewReader(test.source), test.size, test.hash, test.limit)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}
