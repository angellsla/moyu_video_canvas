package generation

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"infinite-canvas/backend/internal/protocol"
)

type sistineloveOwnedFixture struct {
	ResourcePort
	data  []byte
	reads atomic.Int32
	t     *testing.T
}

func (f *sistineloveOwnedFixture) Open(userID, id string) (ResourceInfo, io.ReadCloser, error) {
	if userID != "owner" || id != "reference" {
		f.t.Errorf("wrong resource identity: %s/%s", userID, id)
	}
	f.reads.Add(1)
	return ResourceInfo{Status: "ready", Size: int64(len(f.data)), MimeType: "image/png"}, io.NopCloser(bytes.NewReader(f.data)), nil
}

func TestSistineloveRepeatedGenerationUploadsOwnedReferenceAgain(t *testing.T) {
	t.Setenv("CANVAS_ALLOWED_PRIVATE_UPSTREAM_HOSTS", "127.0.0.1")
	video := unifiedVideoPayload(t)
	resources := &sistineloveOwnedFixture{t: t, data: []byte("owned reference bytes")}
	var uploadedIDs, submittedURLs []string
	var polls, downloads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/img_up":
			file, _, err := r.FormFile("file")
			if err != nil {
				t.Error(err)
				http.Error(w, "bad upload", 400)
				return
			}
			defer file.Close()
			defer r.MultipartForm.RemoveAll()
			data, _ := io.ReadAll(file)
			if !bytes.Equal(data, resources.data) {
				t.Error("reference bytes changed")
			}
			id := r.Header.Get("img_id")
			if len(id) != 64 {
				t.Error("invalid img_id")
			}
			uploadedIDs = append(uploadedIDs, id)
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/q":
			_ = json.NewEncoder(w).Encode(map[string]string{"img_id": r.Header.Get("img_id"), "img_url": "https://img.sistinelove.cn/files/" + r.Header.Get("img_id")})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/videos":
			var body struct {
				Images []string `json:"images"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Images) != 1 {
				t.Error("bad video body")
				http.Error(w, "bad body", 400)
				return
			}
			submittedURLs = append(submittedURLs, body.Images[0])
			if body.Images[0] != "https://img.sistinelove.cn/files/"+uploadedIDs[len(uploadedIDs)-1] {
				t.Error("stale or non-uploaded reference submitted")
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "video-" + string(rune('0'+len(submittedURLs))), "status": "queued"})
		case r.URL.Path == "/v1/videos/video-1":
			polls.Add(1)
			_, _ = w.Write([]byte(`{"id":"video-1","status":"failed","error":{"code":"upstream_task_failed","message":"OutputVideoSensitiveContentDetected.PolicyViolation: 生成结果未通过审核"}}`))
		case r.URL.Path == "/v1/videos/video-2":
			polls.Add(1)
			_, _ = w.Write([]byte(`{"id":"video-2","status":"completed","url":"/result.mp4"}`))
		case r.URL.Path == "/result.mp4":
			downloads.Add(1)
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write(video)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := newSistineloveMediaClient()
	client.baseURL = server.URL
	input := Input{Mode: "video", Prompt: "animate", Config: Config{InterfaceType: "sistinelove-seedance-video", BaseURL: server.URL, Model: "seedance-2.0-gz"}, ReferenceImages: []Media{{StorageKey: "resource:reference", URL: "blob:old-preview", Name: "reference.png"}}}
	original, _ := json.Marshal(input)
	ctx := WithRuntime(context.Background(), Runtime{Call: CallMeta{UserID: "owner"}, Resources: resources})
	policy := DefaultVideoPollPolicy()
	policy.Sleep = func(context.Context, time.Duration) error { return nil }
	_, err := runVideoTaskWithMediaClient(ctx, input, policy, client)
	var terminal ProviderTaskTerminalError
	if !errors.As(err, &terminal) || terminal.Status != "failed" || ClassifyError(err).Category != CategoryModerationOutput {
		t.Fatalf("terminal moderation lost: %v", err)
	}
	result, err := runVideoTaskWithMediaClient(ctx, input, policy, client)
	if err != nil {
		t.Fatal(err)
	}
	unifiedVideoResult(t, result, video)
	// Reconnecting to an accepted task polls it without uploading or paying again.
	_, err = runVideoTaskWithMediaClient(WithCallMeta(ctx, CallMeta{UserID: "owner", ProviderRequestID: "video-2"}), input, policy, client)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(input)
	if !reflect.DeepEqual(original, after) {
		t.Fatal("temporary links replaced persisted references")
	}
	if resources.reads.Load() != 2 || len(uploadedIDs) != 2 || len(submittedURLs) != 2 || uploadedIDs[0] == uploadedIDs[1] || polls.Load() != 3 || downloads.Load() != 2 {
		t.Fatalf("reads=%d uploads=%d creates=%d polls=%d downloads=%d", resources.reads.Load(), len(uploadedIDs), len(submittedURLs), polls.Load(), downloads.Load())
	}
}

type sistineloveFixtureUpload struct {
	name string
	mime string
	data []byte
}

func TestPrepareSistineloveReferencesUploadsEveryMediaInOrder(t *testing.T) {
	type receipt struct {
		ID  string `json:"img_id"`
		URL string `json:"img_url"`
	}
	var mu sync.Mutex
	uploads := map[string]sistineloveFixtureUpload{}
	var active, maxActive atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("img_id")
		if len(id) != 64 {
			t.Errorf("img_id length = %d, want 64", len(id))
		}
		if decoded, err := hex.DecodeString(id); err != nil || len(decoded) != 32 {
			t.Errorf("img_id is not 64 hex characters: %q", id)
		}
		switch r.URL.Path {
		case "/img_up":
			if r.Method != http.MethodPost || !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data; boundary=") {
				t.Errorf("unexpected upload request: %s %s %q", r.Method, r.URL.Path, r.Header.Get("Content-Type"))
			}
			current := active.Add(1)
			for previous := maxActive.Load(); current > previous && !maxActive.CompareAndSwap(previous, current); previous = maxActive.Load() {
			}
			defer active.Add(-1)
			reader, err := r.MultipartReader()
			if err != nil {
				t.Errorf("multipart reader: %v", err)
				http.Error(w, "bad multipart", http.StatusBadRequest)
				return
			}
			part, err := reader.NextPart()
			if err != nil {
				t.Errorf("file part missing: %v", err)
				http.Error(w, "missing file", http.StatusBadRequest)
				return
			}
			if part.FormName() != "file" {
				t.Errorf("multipart field = %q, want file", part.FormName())
			}
			data, err := io.ReadAll(part)
			if err != nil {
				t.Errorf("read file part: %v", err)
			}
			if _, err := reader.NextPart(); err != io.EOF {
				t.Errorf("got extra multipart part: %v", err)
			}
			mu.Lock()
			if _, exists := uploads[id]; exists {
				t.Errorf("duplicate img_id %q", id)
			}
			uploads[id] = sistineloveFixtureUpload{name: part.FileName(), mime: part.Header.Get("Content-Type"), data: data}
			mu.Unlock()
			time.Sleep(15 * time.Millisecond)
			w.WriteHeader(http.StatusNoContent)
		case "/q":
			if r.Method != http.MethodGet {
				t.Errorf("query method = %s, want GET", r.Method)
			}
			mu.Lock()
			_, exists := uploads[id]
			mu.Unlock()
			if !exists {
				http.Error(w, "unknown img_id", http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(receipt{ID: id, URL: "https://img.sistinelove.cn/files/" + id})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	dataURL := func(kind, data string) string {
		return "data:" + kind + ";base64," + base64.StdEncoding.EncodeToString([]byte(data))
	}
	input := Input{
		Mode:   "video",
		Config: Config{InterfaceType: "sistinelove-seedance-video"},
		ReferenceImages: []Media{
			{Name: "first.png", DataURL: dataURL("image/png", "image-1")},
			{Name: "second.png", DataURL: dataURL("image/png", "image-2")},
			{Name: "third.png", DataURL: dataURL("image/png", "image-3")},
		},
		ReferenceVideos: []Media{{Name: "reference.mp4", DataURL: dataURL("video/mp4", "video-1")}},
		ReferenceAudios: []Media{{Name: "reference.wav", DataURL: dataURL("audio/wav", "audio-1")}},
	}
	client := newSistineloveMediaClient()
	client.baseURL, client.queryInterval = server.URL, 0
	if err := prepareSistineloveReferences(context.Background(), &input, client); err != nil {
		t.Fatal(err)
	}
	if len(uploads) != 5 || maxActive.Load() < 2 || maxActive.Load() > sistineloveUploadWorkers {
		t.Fatalf("uploads=%d max concurrent=%d", len(uploads), maxActive.Load())
	}
	want := [][]string{{"image-1", "image-2", "image-3"}, {"video-1"}, {"audio-1"}}
	for groupIndex, group := range providerMediaGroups(&input) {
		for itemIndex, media := range group.items {
			if !strings.HasPrefix(media.URL, "https://img.sistinelove.cn/files/") || media.DataURL != "" || media.StorageKey != "" || time.Until(media.temporaryURLExpiresAt) > sistineloveMediaURLLifetime || time.Until(media.temporaryURLExpiresAt) <= 0 {
				t.Fatalf("reference not replaced by service link: %+v", media)
			}
			id := strings.TrimPrefix(media.URL, "https://img.sistinelove.cn/files/")
			mu.Lock()
			upload, ok := uploads[id]
			mu.Unlock()
			if !ok || string(upload.data) != want[groupIndex][itemIndex] || upload.mime != media.MimeType {
				t.Fatalf("upload mismatch for %s %d: %+v", group.kind, itemIndex, upload)
			}
		}
	}
	adapter := unifiedVideoAdapter(t, "sistinelove-seedance-video")
	spec, err := adapter.BuildCreate(context.Background(), protocol.RequestContext{Request: ProtocolRequestFromInput(input)})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateSistineloveCreateMedia(input, spec, time.Now()); err != nil {
		t.Fatalf("valid image/video/audio URLs were rejected from the create request: %v", err)
	}
}

func TestSistineloveCreateChecksEveryMediaURLAndLifetime(t *testing.T) {
	now := time.Now()
	input := Input{Mode: "video", Config: Config{InterfaceType: "sistinelove-seedance-video"},
		ReferenceImages: []Media{{URL: "https://img.sistinelove.cn/files/image", temporaryURLExpiresAt: now.Add(sistineloveMediaURLLifetime)}},
		ReferenceVideos: []Media{{URL: "https://img.sistinelove.cn/files/video", temporaryURLExpiresAt: now.Add(sistineloveMediaURLLifetime)}},
		ReferenceAudios: []Media{{URL: "https://img.sistinelove.cn/files/audio", temporaryURLExpiresAt: now.Add(sistineloveMediaURLLifetime)}},
	}
	spec := protocol.RequestSpec{Body: map[string]any{
		"images": []any{input.ReferenceImages[0].URL}, "videos": []any{input.ReferenceVideos[0].URL}, "audios": []any{input.ReferenceAudios[0].URL},
	}}
	if err := validateSistineloveCreateMedia(input, spec, now); err != nil {
		t.Fatalf("valid uploaded media rejected: %v", err)
	}
	for name, change := range map[string]func(*Input){
		"expired":         func(value *Input) { value.ReferenceVideos[0].temporaryURLExpiresAt = now },
		"wrong order/url": func(value *Input) { value.ReferenceAudios[0].URL += "-wrong" },
		"local URL":       func(value *Input) { value.ReferenceImages[0].URL = "blob:stale-preview" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := input
			changed.ReferenceImages = append([]Media(nil), input.ReferenceImages...)
			changed.ReferenceVideos = append([]Media(nil), input.ReferenceVideos...)
			changed.ReferenceAudios = append([]Media(nil), input.ReferenceAudios...)
			change(&changed)
			if err := validateSistineloveCreateMedia(changed, spec, now); err == nil {
				t.Fatal("invalid media request accepted")
			}
		})
	}
}

func TestSistineloveCreateSubmitsFreshImageVideoAndAudioLinks(t *testing.T) {
	t.Setenv("CANVAS_ALLOWED_PRIVATE_UPSTREAM_HOSTS", "127.0.0.1")
	var mu sync.Mutex
	uploadedByName := map[string]string{}
	var submitted struct {
		Images []string `json:"images"`
		Videos []string `json:"videos"`
		Audios []string `json:"audios"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/img_up":
			file, header, err := r.FormFile("file")
			if err != nil {
				http.Error(w, "missing file", http.StatusBadRequest)
				return
			}
			_, _ = io.Copy(io.Discard, file)
			_ = file.Close()
			id, name := r.Header.Get("img_id"), header.Filename
			mu.Lock()
			uploadedByName[name] = id
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case "/q":
			_ = json.NewEncoder(w).Encode(map[string]string{"img_id": r.Header.Get("img_id"), "img_url": "https://img.sistinelove.cn/files/" + r.Header.Get("img_id")})
		case "/v1/videos":
			if err := json.NewDecoder(r.Body).Decode(&submitted); err != nil {
				t.Errorf("decode video request: %v", err)
				http.Error(w, "bad body", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "failed-video", "status": "failed", "error": map[string]string{"code": "upstream_task_failed", "message": "fixture failure"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	input := Input{Mode: "video", Prompt: "use all references", Config: Config{BaseURL: server.URL, InterfaceType: "sistinelove-seedance-video", Model: "seedance-2.0-gz"},
		ReferenceImages: []Media{{Name: "reference.png", DataURL: "data:image/png;base64,aW1hZ2U="}},
		ReferenceVideos: []Media{{Name: "reference.mp4", DataURL: "data:video/mp4;base64,dmlkZW8="}},
		ReferenceAudios: []Media{{Name: "reference.wav", DataURL: "data:audio/wav;base64,YXVkaW8="}},
	}
	client := newSistineloveMediaClient()
	client.baseURL, client.queryAttempts = server.URL, 1
	_, err := runVideoTaskWithMediaClient(context.Background(), input, DefaultVideoPollPolicy(), client)
	var terminal ProviderTaskTerminalError
	if !errors.As(err, &terminal) || terminal.Status != "failed" {
		t.Fatalf("expected fixture terminal response after submit, got %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	for name, values := range map[string][]string{
		"reference.png": submitted.Images,
		"reference.mp4": submitted.Videos,
		"reference.wav": submitted.Audios,
	} {
		if len(values) != 1 || values[0] != "https://img.sistinelove.cn/files/"+uploadedByName[name] || uploadedByName[name] == "" {
			t.Fatalf("request URL for %s did not match its upload receipt: urls=%v upload=%q", name, values, uploadedByName[name])
		}
	}
}

func TestPrepareSistineloveReferencesRejectsMismatchedReceiptsWithoutMutatingInput(t *testing.T) {
	for _, tc := range []struct {
		name string
		id   string
		url  string
	}{
		{name: "id mismatch", id: strings.Repeat("a", 64), url: "https://img.sistinelove.cn/file"},
		{name: "foreign host", url: "https://media.example.test/file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/img_up" {
					_, _ = io.Copy(io.Discard, r.Body)
					w.WriteHeader(http.StatusNoContent)
					return
				}
				id := tc.id
				if id == "" {
					id = r.Header.Get("img_id")
				}
				_ = json.NewEncoder(w).Encode(map[string]string{"img_id": id, "img_url": tc.url})
			}))
			defer server.Close()
			input := Input{Mode: "video", Config: Config{InterfaceType: "sistinelove-seedance-video"}, ReferenceImages: []Media{{Name: "ref.png", DataURL: "data:image/png;base64,aW1hZ2U="}}}
			original := input.ReferenceImages[0]
			client := newSistineloveMediaClient()
			client.baseURL, client.queryAttempts = server.URL, 1
			if err := prepareSistineloveReferences(context.Background(), &input, client); err == nil {
				t.Fatal("invalid receipt accepted")
			}
			if input.ReferenceImages[0] != original {
				t.Fatalf("failed preparation mutated original input: %+v", input.ReferenceImages[0])
			}
		})
	}
}

func TestSistineloveUploadFailurePreventsVideoCreate(t *testing.T) {
	var creates atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/img_up":
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusNoContent)
		case "/q":
			_ = json.NewEncoder(w).Encode(map[string]string{"img_id": r.Header.Get("img_id"), "img_url": "https://untrusted.example/reference.png"})
		case "/v1/videos":
			creates.Add(1)
			w.WriteHeader(http.StatusCreated)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newSistineloveMediaClient()
	client.baseURL, client.queryAttempts = server.URL, 1
	_, err := runVideoTaskWithMediaClient(context.Background(), Input{
		Mode:            "video",
		Config:          Config{BaseURL: server.URL, InterfaceType: "sistinelove-seedance-video", Model: "seedance-2.0-gz"},
		ReferenceImages: []Media{{Name: "reference.png", DataURL: "data:image/png;base64,aW1hZ2U="}},
	}, DefaultVideoPollPolicy(), client)
	if err == nil || !strings.Contains(err.Error(), "无效 HTTPS 链接") {
		t.Fatalf("invalid receipt error = %v", err)
	}
	if creates.Load() != 0 {
		t.Fatalf("video create called %d times after upload failure", creates.Load())
	}
}

func TestSistineloveRemoteMediaDownloadUsesBoundedTemporaryFile(t *testing.T) {
	payload := bytes.Repeat([]byte("media"), 32)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("source credentials were forwarded")
		}
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	source, err := downloadSistineloveMedia(context.Background(), server.URL+"/ref.mp4", Media{}, int64(len(payload)+1), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	spool, ok := source.ReadCloser.(*temporaryMediaFile)
	if !ok {
		t.Fatalf("source reader = %T, want temporary file", source.ReadCloser)
	}
	name := spool.path
	data, err := io.ReadAll(source)
	if err != nil || !bytes.Equal(data, payload) || source.mime != "video/mp4" || source.name != "ref.mp4" {
		t.Fatalf("downloaded source mismatch: size=%d mime=%q name=%q err=%v", len(data), source.mime, source.name, err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(name); !os.IsNotExist(err) {
		t.Fatalf("temporary media file remains: %v", err)
	}
	if _, err := downloadSistineloveMedia(context.Background(), server.URL, Media{}, int64(len(payload)-1), server.Client()); err == nil || !strings.Contains(err.Error(), "大小限制") {
		t.Fatalf("oversize content length was accepted: %v", err)
	}
}

func TestSistineloveSourceURLsRequirePublicHTTPS(t *testing.T) {
	client := newSistineloveMediaClient()
	for _, raw := range []string{"http://example.test/media.png", "https://user@example.test/media.png", "data:image/png;base64,aW1hZ2U="} {
		if _, err := client.download(context.Background(), raw, Media{}, 1024); err == nil {
			t.Errorf("unsafe source URL accepted: %s", raw)
		}
	}
}

func TestSistineloveReferenceUploadLimitsMatchMediaKinds(t *testing.T) {
	for _, tc := range []struct {
		kind string
		want int64
	}{
		{kind: "image", want: 30 << 20},
		{kind: "video", want: 200 << 20},
		{kind: "audio", want: 15 << 20},
	} {
		if got := sistineloveReferenceMaxBytes(tc.kind); got != tc.want {
			t.Errorf("%s upload limit = %d, want %d", tc.kind, got, tc.want)
		}
	}
}
