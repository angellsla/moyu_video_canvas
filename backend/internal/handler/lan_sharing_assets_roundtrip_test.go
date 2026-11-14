package handler

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"infinite-canvas/backend/internal/app"
	"infinite-canvas/backend/internal/lansharing"
	"infinite-canvas/backend/internal/model"

	"github.com/gin-gonic/gin"
)

// Two isolated desktop data directories exchange actual bytes over pinned
// mutual TLS. No discovery broadcast or installed desktop database is used.
func TestLANAssetTwoDesktopRoundTrip(t *testing.T) {
	endpoint, err := lansharing.FindPrivateEndpoint()
	if err != nil {
		t.Skipf("private IPv4 interface required: %v", err)
	}
	gin.SetMode(gin.TestMode)
	a, adb, am := newLANAssetHandlerFixture(t)
	b, bdb, bm := newLANAssetHandlerFixture(t)
	ai, err := lansharing.LoadIdentity(a.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	bi, err := lansharing.LoadIdentity(b.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := adb.Create(&model.LANTrustedPeer{NodeID: bi.ID(), PublicKey: base64.RawStdEncoding.EncodeToString(bi.PublicKey()), TrustState: "trusted"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := bdb.Create(&model.LANTrustedPeer{NodeID: ai.ID(), PublicKey: base64.RawStdEncoding.EncodeToString(ai.PublicKey()), TrustState: "trusted"}).Error; err != nil {
		t.Fatal(err)
	}
	for _, service := range []*app.Service{a, b} {
		if err := service.Database().Create(&model.LANShareCatalog{ID: "roundtrip", Name: "Round trip", CreatedByNodeID: ai.ID()}).Error; err != nil {
			t.Fatal(err)
		}
	}
	ah := &lanAssetHandler{manager: am, service: a, db: adb}
	bh := &lanAssetHandler{manager: bm, service: b, db: bdb}
	var blocked atomic.Pointer[lanAssetStreamGate]
	start := func(h *lanAssetHandler, identity *lansharing.Identity, trusted string) int {
		t.Helper()
		mux := http.NewServeMux()
		mux.HandleFunc("GET /catalogs/{catalogID}/assets/{contentHash}", func(w http.ResponseWriter, r *http.Request) {
			if gate := blocked.Load(); gate != nil && r.PathValue("contentHash") == gate.hash {
				w = &gatedLANAssetWriter{ResponseWriter: w, ctx: r.Context(), gate: gate}
			}
			h.serveAsset(w, r)
		})
		server := httptest.NewUnstartedServer(mux)
		_ = server.Listener.Close()
		listener, err := net.Listen("tcp4", net.JoinHostPort(endpoint.Address.String(), "0"))
		if err != nil {
			t.Fatal(err)
		}
		server.Listener = listener
		server.TLS, err = identity.ServerTLSConfig(func(id string, key ed25519.PublicKey) bool {
			return id == trusted && lansharing.Fingerprint(key) == trusted
		})
		if err != nil {
			t.Fatal(err)
		}
		server.StartTLS()
		t.Cleanup(server.Close)
		return listener.Addr().(*net.TCPAddr).Port
	}
	aport := start(ah, ai, bi.ID())
	bport := start(bh, bi, ai.ID())
	img := image.NewRGBA(image.Rect(0, 0, 32, 24))
	for y := 0; y < 24; y++ {
		for x := 0; x < 32; x++ {
			img.SetRGBA(x, y, color.RGBA{uint8(x * 7), uint8(y * 9), 120, 255})
		}
	}
	for _, format := range []string{"png", "jpeg", "gif"} {
		t.Run(format, func(t *testing.T) {
			for direction, pair := range []struct {
				from, to *lanAssetHandler
				identity *lansharing.Identity
				port     int
			}{{ah, bh, ai, aport}, {bh, ah, bi, bport}} {
				// Different bytes in each direction force an actual peer download,
				// rather than the reverse import finding the original local mapping.
				for y := 0; y < 16; y++ {
					for x := 0; x < 16; x++ {
						img.SetRGBA(x, y, color.RGBA{uint8(direction * 255), 30, 220, 255})
					}
				}
				var encoded bytes.Buffer
				switch format {
				case "png":
					err = png.Encode(&encoded, img)
				case "jpeg":
					err = jpeg.Encode(&encoded, img, nil)
				case "gif":
					err = gif.Encode(&encoded, img, nil)
				}
				if err != nil {
					t.Fatal(err)
				}
				content := encoded.Bytes()
				owner, err := pair.from.localOwner()
				if err != nil {
					t.Fatal(err)
				}
				resource, err := pair.from.service.UploadLocalResourceFile(owner.ID, "upload."+format, int64(len(content)), "image", 32, 24, 0, bytes.NewReader(content))
				if err != nil {
					t.Fatal(err)
				}
				published := postLANAssetFixture(t, pair.from, false, map[string]any{"resourceId": resource.ID})
				var envelope struct {
					Data struct {
						Asset lanCatalogAsset `json:"asset"`
					} `json:"data"`
				}
				if err := json.Unmarshal(published.Body.Bytes(), &envelope); err != nil {
					t.Fatal(err)
				}
				asset := envelope.Data.Asset
				request := map[string]any{"peerId": pair.identity.ID(), "address": endpoint.Address.String(), "port": pair.port, "contentHash": asset.ContentHash, "name": asset.Name, "mimeType": asset.MIMEType, "size": asset.Size}
				response := postLANAssetFixture(t, pair.to, true, request)
				var imported struct {
					Data struct {
						Asset    lanCatalogAsset `json:"asset"`
						Resource struct {
							ID string `json:"id"`
						} `json:"resource"`
					} `json:"data"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &imported); err != nil {
					t.Fatal(err)
				}
				receipt := imported.Data.Asset
				if receipt.ContentHash != asset.ContentHash || receipt.Size != asset.Size || receipt.MIMEType != asset.MIMEType || receipt.CreatedBy != pair.to.manager.NodeID() || receipt.EntryID == "" || len(receipt.EntryID) > 80 {
					t.Fatalf("invalid local asset receipt: %+v", receipt)
				}
				receiver, err := pair.to.localOwner()
				if err != nil {
					t.Fatal(err)
				}
				r, stream, err := pair.to.service.OpenResource(receiver.ID, imported.Data.Resource.ID)
				if err != nil {
					t.Fatal(err)
				}
				actual, err := io.ReadAll(stream)
				_ = stream.Close()
				if err != nil {
					t.Fatal(err)
				}
				digest := sha256.Sum256(actual)
				if !bytes.Equal(content, actual) || hex.EncodeToString(digest[:]) != asset.ContentHash {
					t.Fatal("transferred bytes differ from original upload")
				}
				if _, _, err := image.Decode(bytes.NewReader(actual)); err != nil {
					t.Fatalf("downloaded image does not decode: %v", err)
				}
				// Reimport is idempotent, and a deleted local file can be recovered.
				repeated := postLANAssetFixture(t, pair.to, true, request)
				if err := json.Unmarshal(repeated.Body.Bytes(), &imported); err != nil {
					t.Fatal(err)
				}
				if imported.Data.Asset.EntryID != receipt.EntryID || imported.Data.Resource.ID != r.ID {
					t.Fatal("repeat import changed local receipt or resource identity")
				}
				if err := os.Remove(filepath.Join(pair.to.service.DataDir(), "resources", filepath.FromSlash(r.ObjectKey))); err != nil {
					t.Fatal(err)
				}
				restored := postLANAssetFixture(t, pair.to, true, request)
				if err := json.Unmarshal(restored.Body.Bytes(), &imported); err != nil {
					t.Fatal(err)
				}
				if imported.Data.Asset.EntryID != receipt.EntryID {
					t.Fatal("recovering missing bytes changed catalog claim identity")
				}
				var mapping model.LANCatalogResource
				if err := pair.to.db.First(&mapping, "catalog_id = ? AND content_hash = ? AND user_id = ?", "roundtrip", asset.ContentHash, receiver.ID).Error; err != nil {
					t.Fatal(err)
				}
				recovered, err := pair.to.service.Resource(receiver.ID, mapping.ResourceID)
				if err != nil {
					t.Fatal(err)
				}
				if hash, err := hashOwnedLocalResource(context.Background(), pair.to.service, receiver.ID, recovered, int64(len(content)+1)); err != nil || hash != asset.ContentHash {
					t.Fatalf("recovery hash = %s: %v", hash, err)
				}
			}
		})
	}
	t.Run("concurrent streams and status", func(t *testing.T) {
		owner, err := ah.localOwner()
		if err != nil {
			t.Fatal(err)
		}
		var assets []lanCatalogAsset
		for _, shade := range []uint8{50, 200} {
			img := image.NewRGBA(image.Rect(0, 0, 12, 12))
			for y := 0; y < 12; y++ {
				for x := 0; x < 12; x++ {
					img.SetRGBA(x, y, color.RGBA{shade, 80, 20, 255})
				}
			}
			var encoded bytes.Buffer
			if err := png.Encode(&encoded, img); err != nil {
				t.Fatal(err)
			}
			resource, err := a.UploadLocalResourceFile(owner.ID, "concurrent.png", int64(encoded.Len()), "image", 12, 12, 0, bytes.NewReader(encoded.Bytes()))
			if err != nil {
				t.Fatal(err)
			}
			published := postLANAssetFixture(t, ah, false, map[string]any{"resourceId": resource.ID})
			var envelope struct {
				Data struct {
					Asset lanCatalogAsset `json:"asset"`
				} `json:"data"`
			}
			if err := json.Unmarshal(published.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			assets = append(assets, envelope.Data.Asset)
		}
		gate := &lanAssetStreamGate{hash: assets[0].ContentHash, started: make(chan struct{}), release: make(chan struct{})}
		blocked.Store(gate)
		defer blocked.Store(nil)
		var releaseOnce sync.Once
		release := func() { releaseOnce.Do(func() { close(gate.release) }) }
		defer release()
		router := gin.New()
		router.POST("/catalogs/:catalogId/assets", bh.importAsset)
		router.GET("/catalogs/:catalogId/assets/status", bh.assetStatuses)
		local := httptest.NewServer(router)
		defer func() { release(); local.Close() }()
		client := &http.Client{Timeout: 3 * time.Second}
		defer client.CloseIdleConnections()
		pull := func(asset lanCatalogAsset) error {
			body, err := json.Marshal(map[string]any{"peerId": ai.ID(), "address": endpoint.Address.String(), "port": aport, "contentHash": asset.ContentHash, "name": asset.Name, "mimeType": asset.MIMEType, "size": asset.Size})
			if err != nil {
				return err
			}
			response, err := client.Post(local.URL+"/catalogs/roundtrip/assets", "application/json", bytes.NewReader(body))
			if err != nil {
				return err
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK {
				return fmt.Errorf("import returned %d", response.StatusCode)
			}
			var receipt struct {
				Data struct {
					Asset lanCatalogAsset `json:"asset"`
				} `json:"data"`
			}
			if err := json.NewDecoder(response.Body).Decode(&receipt); err != nil {
				return err
			}
			if receipt.Data.Asset.ContentHash != asset.ContentHash {
				return fmt.Errorf("import receipt hash mismatch")
			}
			return nil
		}
		slowResult := make(chan error, 1)
		go func() { slowResult <- pull(assets[0]) }()
		select {
		case <-gate.started:
		case <-time.After(2 * time.Second):
			t.Fatal("first stream did not start")
		}
		response, err := client.Get(local.URL + "/catalogs/roundtrip/assets/status")
		if err != nil {
			t.Fatalf("status blocked by file transfer: %v", err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("status returned %d", response.StatusCode)
		}
		if err := pull(assets[1]); err != nil {
			t.Fatalf("second stream blocked by first: %v", err)
		}
		select {
		case err := <-slowResult:
			t.Fatalf("first stream unexpectedly completed before release: %v", err)
		default:
		}
		release()
		if err := <-slowResult; err != nil {
			t.Fatal(err)
		}
	})
}

type lanAssetStreamGate struct {
	hash    string
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

type gatedLANAssetWriter struct {
	http.ResponseWriter
	ctx  context.Context
	gate *lanAssetStreamGate
}

func (w *gatedLANAssetWriter) Write(data []byte) (int, error) {
	w.gate.once.Do(func() { close(w.gate.started) })
	select {
	case <-w.gate.release:
		return w.ResponseWriter.Write(data)
	case <-w.ctx.Done():
		return 0, w.ctx.Err()
	}
}

func postLANAssetFixture(t *testing.T, handler *lanAssetHandler, importing bool, body any) *httptest.ResponseRecorder {
	t.Helper()
	router := gin.New()
	if importing {
		router.POST("/catalogs/:catalogId/assets", handler.importAsset)
	} else {
		router.POST("/catalogs/:catalogId/assets", handler.addAsset)
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/catalogs/roundtrip/assets", bytes.NewReader(encoded))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("asset operation returned %d: %s", response.Code, response.Body.String())
	}
	return response
}
