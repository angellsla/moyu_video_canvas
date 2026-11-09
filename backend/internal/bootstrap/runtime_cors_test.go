package bootstrap

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestDesktopCORSAllowsModelRelayPreflight(t *testing.T) {
	router := gin.New()
	router.Use(desktopCORSMiddleware())
	forwarded := false
	router.POST("/api/ai/custom", func(c *gin.Context) {
		forwarded = true
		c.Status(http.StatusNoContent)
	})
	for _, origin := range []string{"wails://wails", "http://wails.localhost"} {
		t.Run(origin, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodOptions, "/api/ai/custom", nil)
			request.Header.Set("Origin", origin)
			requested := []string{"authorization", "content-type", "traceparent", "x-canvas-trace-id", "x-desktop-token", "x-canvas-upstream-url", "x-canvas-upstream-format", "x-canvas-upstream-base-url", "x-canvas-upstream-headers"}
			request.Header.Set("Access-Control-Request-Method", "POST")
			request.Header.Set("Access-Control-Request-Headers", strings.Join(requested, ","))
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusNoContent || forwarded {
				t.Fatalf("preflight must finish without invoking the relay: status=%d forwarded=%v", response.Code, forwarded)
			}
			if response.Header().Get("Access-Control-Allow-Origin") != origin || response.Header().Get("Access-Control-Allow-Credentials") != "true" {
				t.Fatal("desktop origin and credential policy were not retained")
			}
			allowed := map[string]bool{}
			for _, name := range strings.Split(response.Header().Get("Access-Control-Allow-Headers"), ",") {
				allowed[strings.ToLower(strings.TrimSpace(name))] = true
			}
			for _, name := range requested {
				if !allowed[name] {
					t.Errorf("browser will block model relay header %q", name)
				}
			}
		})
	}
}

func TestDesktopSharedAssetProbeRoundTrip(t *testing.T) {
	harness := newDesktopHarness(t)
	owner, err := harness.rt.service.LocalWorkspaceOwner()
	if err != nil {
		t.Fatal(err)
	}
	for _, content := range [][]byte{[]byte("first asset"), []byte("second asset")} {
		resource, err := harness.rt.service.UploadLocalResourceFile(owner.ID, "probe.txt", int64(len(content)), "file", 0, 0, 0, bytes.NewReader(content))
		if err != nil {
			t.Fatal(err)
		}
		for _, origin := range []string{"wails://wails", "http://wails.localhost"} {
			path := "/resources/" + resource.ID + "/file"
			preflight := harness.call(requestOptions{method: http.MethodOptions, path: path, origin: origin,
				extraHeaders: map[string]string{"Access-Control-Request-Method": http.MethodGet, "Access-Control-Request-Headers": "cache-control,range,x-desktop-token,x-beeftv-ui-bootstrap"}})
			if preflight.Code != http.StatusNoContent {
				t.Fatalf("desktop resource preflight = %d", preflight.Code)
			}
			allowed := map[string]bool{}
			for _, name := range strings.Split(preflight.Header().Get("Access-Control-Allow-Headers"), ",") {
				allowed[strings.ToLower(strings.TrimSpace(name))] = true
			}
			for _, name := range []string{"cache-control", "range", "x-desktop-token", "x-beeftv-ui-bootstrap"} {
				if !allowed[name] {
					t.Fatalf("desktop resource preflight omitted %s", name)
				}
			}
			probe := requestOptions{method: http.MethodGet, path: path, origin: origin,
				launchToken: harness.rt.LaunchToken(), uiBootstrap: harness.rt.UIBootstrapToken(),
				extraHeaders: map[string]string{"Range": "bytes=0-0", "Cache-Control": "no-cache"}}
			response := harness.call(probe)
			if response.Code != http.StatusPartialContent || !bytes.Equal(response.Body.Bytes(), content[:1]) {
				t.Fatalf("desktop resource probe = %d %q", response.Code, response.Body.Bytes())
			}
			if response.Header().Get("Access-Control-Allow-Origin") != origin || response.Header().Get("Access-Control-Allow-Credentials") != "true" {
				t.Fatal("desktop resource response lost its CORS headers")
			}
			probe.launchToken, probe.uiBootstrap = "", ""
			response = harness.call(probe)
			if response.Code != http.StatusForbidden && response.Code != http.StatusUnauthorized {
				t.Fatalf("unauthenticated desktop resource probe = %d", response.Code)
			}
		}
	}
}

func TestDesktopCORSAllowsSharedAssetProbePreflight(t *testing.T) {
	router := gin.New()
	router.Use(desktopCORSMiddleware())
	forwarded := false
	router.GET("/api/resources/:id/file", func(c *gin.Context) {
		forwarded = true
		c.Status(http.StatusPartialContent)
	})
	for _, origin := range []string{"wails://wails", "http://wails.localhost"} {
		t.Run(origin, func(t *testing.T) {
			requested := []string{"cache-control", "range", "x-desktop-token", "x-beeftv-ui-bootstrap", "x-beeftv-ui-session"}
			request := httptest.NewRequest(http.MethodOptions, "/api/resources/asset-probe/file", nil)
			request.Header.Set("Origin", origin)
			request.Header.Set("Access-Control-Request-Method", http.MethodGet)
			request.Header.Set("Access-Control-Request-Headers", strings.Join(requested, ","))
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusNoContent || forwarded {
				t.Fatalf("asset probe preflight reached resource handler: status=%d forwarded=%v", response.Code, forwarded)
			}
			if response.Header().Get("Access-Control-Allow-Origin") != origin || response.Header().Get("Access-Control-Allow-Credentials") != "true" {
				t.Fatal("asset probe lost the desktop origin/credentials contract")
			}
			allowed := map[string]bool{}
			for _, name := range strings.Split(response.Header().Get("Access-Control-Allow-Headers"), ",") {
				allowed[strings.ToLower(strings.TrimSpace(name))] = true
			}
			for _, name := range requested {
				if !allowed[name] {
					t.Errorf("desktop browser blocks shared asset probe header %q", name)
				}
			}
		})
	}
}
