package handler

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"infinite-canvas/backend/internal/runtimelog"
)

func TestRuntimeLogEndpointRequiresTrustedDesktopAndRedacts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	logger, err := runtimelog.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	allowed := false
	router := gin.New()
	router.Use(RuntimeDependenciesMiddleware(RuntimeDependencies{DesktopTrust: func(*http.Request) bool { return allowed }}))
	RegisterRuntimeLogRoutes(router.Group("/api"), logger)
	request := httptest.NewRequest(http.MethodPost, "/api/runtime-logs/frontend", strings.NewReader(`{"entries":[{"level":"error","message":"pairingCode: 123456 failed"}]}`))
	request.Header.Set("Content-Type", "application/json")
	request.Host = "127.0.0.1:8080"
	request.RemoteAddr = "127.0.0.1:1234"
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("untrusted log write = %d, want 403", response.Code)
	}
	allowed = true
	request = httptest.NewRequest(http.MethodPost, "/api/runtime-logs/frontend", strings.NewReader(`{"entries":[{"level":"error","message":"pairingCode: 123456 failed"}]}`))
	request.Header.Set("Content-Type", "application/json")
	request.Host = "127.0.0.1:8080"
	request.RemoteAddr = "127.0.0.1:1234"
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("trusted log write = %d %s", response.Code, response.Body.String())
	}
	files, err := filepath.Glob(filepath.Join(dir, "frontend-*.log"))
	if err != nil || len(files) != 1 {
		t.Fatalf("frontend logs = %v, err=%v", files, err)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "123456") || !strings.Contains(string(data), "[REDACTED]") {
		t.Fatalf("log did not redact pairing code: %s", data)
	}
}
