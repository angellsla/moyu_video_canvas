package runtimelog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLoggerSeparatesSourcesRedactsAndRotates(t *testing.T) {
	dir := t.TempDir()
	logger := &Logger{dir: dir, now: func() time.Time { return time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC) }}
	var wait sync.WaitGroup
	for index := 0; index < 20; index++ {
		wait.Add(2)
		go func() {
			defer wait.Done()
			if err := logger.Append("frontend", []Entry{{Level: "error", Message: `token=secret-value pairingCode: 123456 prompt="hidden" https://example.test/path?q=private`}}); err != nil {
				t.Error(err)
			}
		}()
		go func() {
			defer wait.Done()
			if err := logger.Append("backend", []Entry{{Level: "info", Message: "task running"}}); err != nil {
				t.Error(err)
			}
		}()
	}
	wait.Wait()
	for _, source := range []string{"frontend", "backend"} {
		data, err := os.ReadFile(filepath.Join(dir, source+"-2026-11-11.log"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Count(string(data), "\n") != 20 {
			t.Fatalf("%s lines = %d, want 20", source, strings.Count(string(data), "\n"))
		}
		if source == "frontend" {
			if strings.Contains(string(data), "secret-value") || strings.Contains(string(data), "123456") || strings.Contains(string(data), "hidden") || strings.Contains(string(data), "?q=private") {
				t.Fatalf("sensitive data persisted: %s", data)
			}
			var row map[string]any
			if err := json.Unmarshal(data[:strings.IndexByte(string(data), '\n')], &row); err != nil {
				t.Fatal(err)
			}
			if row["source"] != "frontend" {
				t.Fatalf("frontend row source = %v", row["source"])
			}
		}
	}
}

func TestLoggerRetentionRemovesOnlyExpiredLogFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"frontend-2026-10-12.log", "backend-2026-10-13.log", "frontend-2026-10-14.log", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	logger := &Logger{dir: dir, now: func() time.Time { return time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC) }}
	if err := logger.cleanup(); err != nil {
		t.Fatal(err)
	}
	for name, exists := range map[string]bool{"frontend-2026-10-12.log": false, "backend-2026-10-13.log": true, "frontend-2026-10-14.log": true, "notes.txt": true} {
		_, err := os.Stat(filepath.Join(dir, name))
		if (err == nil) != exists {
			t.Errorf("%s exists=%v, want %v", name, err == nil, exists)
		}
	}
}

func TestRedactDoesNotPersistPairingCredentialsOrURLs(t *testing.T) {
	value := Redact(`pairing_code=654321 Authorization: Bearer eyJabcdefghi123456 url=https://host/path?token=secret`, 1000)
	for _, secret := range []string{"654321", "eyJabcdefghi123456", "?token=secret", "/path"} {
		if strings.Contains(value, secret) {
			t.Fatalf("secret %q survived redaction: %s", secret, value)
		}
	}
}

func TestStandardWriterMarksFailedHTTPResponsesAsErrors(t *testing.T) {
	dir := t.TempDir()
	logger := &Logger{dir: dir, now: func() time.Time { return time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC) }}
	if _, err := logger.Writer().Write([]byte(`127.0.0.1 "POST /api/tasks" 500 4ms`)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "backend-2026-11-11.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"level":"error"`) {
		t.Fatalf("HTTP 500 was not marked as error: %s", data)
	}
}

func TestProgramDirectoryUsesBundleParentOnMacOS(t *testing.T) {
	got := ProgramDirectory(filepath.Join("/Applications", "BeefTV.app", "Contents", "MacOS", "BeefTV"))
	if got != filepath.Join("/Applications") {
		t.Fatalf("program directory = %q", got)
	}
	got = ProgramDirectory(filepath.Join("C:\\Apps", "BeefTV", "BeefTV.exe"))
	if got != filepath.Join("C:\\Apps", "BeefTV") {
		t.Fatalf("program directory = %q", got)
	}
}
