package skillhub

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFileChecksManifestDigestAndUTF8ChunkBoundaries(t *testing.T) {
	data := []byte("分镜正文")
	file := File{Path: "SKILL.md", Size: int64(len(data)), SHA256: Digest(data)}
	corrupt := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("path") != "" {
			if corrupt {
				_, _ = w.Write([]byte("tampered"))
			} else {
				_, _ = w.Write(data)
			}
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": Release{Metadata: Metadata{SkillID: "s"}, ReleaseID: "r1", Files: []File{file}}})
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	chunk, err := client.File(context.Background(), "s", "r1", "SKILL.md", 0, 4)
	if err != nil || chunk.Content != "分" || chunk.NextOffset != 3 || chunk.EOF {
		t.Fatalf("bad UTF8 chunk: %+v %v", chunk, err)
	}
	if _, err = client.File(context.Background(), "s", "r1", "SKILL.md", 1, 4); err == nil {
		t.Fatal("mid-rune offset accepted")
	}
	if _, err = client.File(context.Background(), "s", "r1", "../SKILL.md", 0, 4); err == nil {
		t.Fatal("traversal accepted")
	}
	corrupt = true
	_, err = client.File(context.Background(), "s", "r1", "SKILL.md", 0, 4)
	var failure *Error
	if !errors.As(err, &failure) || failure.Reason != "file_integrity" {
		t.Fatalf("corrupt file accepted: %v", err)
	}
	corrupt = false
	data = []byte{0xff, 0x00, 0x01, 0x02, 0x03}
	file = File{Path: "SKILL.md", Size: int64(len(data)), SHA256: Digest(data)}
	chunk, err = client.File(context.Background(), "s", "r1", "SKILL.md", 0, 4)
	if err != nil || chunk.Encoding != "base64" || chunk.Content != base64.StdEncoding.EncodeToString(data[:4]) {
		t.Fatalf("binary chunk: %+v %v", chunk, err)
	}
}
