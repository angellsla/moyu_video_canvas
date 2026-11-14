package generation

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"infinite-canvas/backend/internal/outbound"
	"infinite-canvas/backend/internal/protocol"
	"infinite-canvas/backend/internal/providerpreset"
)

const sistineloveUploadWorkers = 3
const sistineloveMediaURLLifetime = 25 * time.Minute

func normalizeSistineloveVideoConfig(config Config) Config {
	if providerpreset.IsSistineloveSeedance(config.BaseURL, config.Model) {
		config.InterfaceType = providerpreset.SistineloveVideo.Protocol
		config.APIFormat = "openai"
	}
	return config
}

func usesSistineloveUpload(input Input) bool {
	return input.Mode == "video" && (input.Config.InterfaceType == providerpreset.SistineloveVideo.Protocol || providerpreset.IsSistineloveSeedance(input.Config.BaseURL, input.Config.Model))
}

type sistineloveMediaSource struct {
	io.ReadCloser
	size int64
	mime string
	name string
}

type temporaryMediaFile struct {
	*os.File
	path string
}

func (f *temporaryMediaFile) Close() error {
	closeErr := f.File.Close()
	removeErr := os.Remove(f.path)
	if closeErr != nil {
		return closeErr
	}
	return removeErr
}

type sistineloveMediaClient struct {
	baseURL       string
	client        *http.Client
	sourceClient  *http.Client
	queryInterval time.Duration
	queryAttempts int
}

type sistineloveUploadJob struct {
	kind  string
	index int
	media *Media
}

func newSistineloveMediaClient() sistineloveMediaClient {
	client := outbound.OutboundHTTPClient(10 * time.Minute)
	// img_id identifies the uploaded file. Never forward it or the file to a redirect.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	sourceClient := outbound.OutboundHTTPClient(10 * time.Minute)
	sourceClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("参考素材重定向次数过多")
		}
		_, err := outbound.ValidatePublicHTTPSURLContext(req.Context(), req.URL.String())
		return err
	}
	return sistineloveMediaClient{baseURL: providerpreset.SistineloveVideo.MediaOrigin, client: client, sourceClient: sourceClient, queryInterval: time.Second, queryAttempts: 30}
}

func sistineloveReferenceMaxBytes(kind string) int64 {
	switch kind {
	case "video":
		return seedance2ReferenceMaxBytes
	case "audio":
		return 15 << 20
	default:
		return 30 << 20
	}
}

func openSistineloveMedia(ctx context.Context, kind string, media Media, client sistineloveMediaClient) (sistineloveMediaSource, error) {
	runtime, _ := RuntimeFromContext(ctx)
	limit := sistineloveReferenceMaxBytes(kind)
	if runtime.Limits != nil {
		var err error
		runtimeLimit, err := runtime.Limits.ResourceUploadBytes(ctx)
		if err != nil {
			return sistineloveMediaSource{}, err
		}
		limit = min(limit, runtimeLimit)
	}
	if strings.HasPrefix(media.StorageKey, "resource:") {
		if runtime.Resources == nil {
			return sistineloveMediaSource{}, errors.New("读取任务参考资源失败：资源端口未接入")
		}
		resource, body, err := runtime.Resources.Open(runtime.Call.UserID, strings.TrimPrefix(media.StorageKey, "resource:"))
		if err != nil {
			return sistineloveMediaSource{}, err
		}
		if resource.Status != "ready" || resource.Size <= 0 || resource.Size > limit {
			_ = body.Close()
			return sistineloveMediaSource{}, errors.New("参考素材尚未就绪或超过文件大小限制")
		}
		return sistineloveMediaSource{ReadCloser: body, size: resource.Size, mime: firstNonEmpty(resource.MimeType, media.MimeType), name: media.Name}, nil
	}
	raw := strings.TrimSpace(firstNonEmpty(media.DataURL, media.URL))
	if strings.HasPrefix(raw, "data:") {
		if int64(len(raw)) > (limit+2)/3*4+1024 {
			return sistineloveMediaSource{}, errors.New("参考素材超过文件大小限制")
		}
		mimeType, data, err := DecodeProviderDataURL(raw)
		if err != nil {
			return sistineloveMediaSource{}, err
		}
		if len(data) == 0 || int64(len(data)) > limit {
			return sistineloveMediaSource{}, errors.New("参考素材为空或超过文件大小限制")
		}
		return sistineloveMediaSource{ReadCloser: io.NopCloser(bytes.NewReader(data)), size: int64(len(data)), mime: mimeType, name: media.Name}, nil
	}
	return client.download(ctx, raw, media, limit)
}

// Work on copies so partial upload success never leaves a mixed request. Each
// worker writes one original index, independent of completion order.
func prepareSistineloveReferences(ctx context.Context, input *Input, client sistineloveMediaClient) error {
	if input == nil || !usesSistineloveUpload(*input) || ResumedProviderRequestID(ctx) != "" {
		return nil
	}
	if input.Mask != nil {
		return errors.New("此视频接口不接收蒙版")
	}
	prepared := *input
	prepared.ReferenceImages = append([]Media(nil), input.ReferenceImages...)
	prepared.ReferenceVideos = append([]Media(nil), input.ReferenceVideos...)
	prepared.ReferenceAudios = append([]Media(nil), input.ReferenceAudios...)
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var jobs []sistineloveUploadJob
	for _, group := range providerMediaGroups(&prepared) {
		for i := range group.items {
			jobs = append(jobs, sistineloveUploadJob{group.kind, i, &group.items[i]})
		}
	}
	queue := make(chan sistineloveUploadJob, len(jobs))
	for _, job := range jobs {
		queue <- job
	}
	close(queue)
	var wg sync.WaitGroup
	var once sync.Once
	var firstErr error
	fail := func(job sistineloveUploadJob, err error) {
		once.Do(func() {
			firstErr = RequestPreparationError{Cause: &ReferenceUploadError{Meta: ReferenceUploadMeta{Kind: job.kind, Index: job.index + 1, Stage: "put"}, Cause: err, Detail: err.Error()}}
			cancel()
		})
	}
	for range min(sistineloveUploadWorkers, len(jobs)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range queue {
				if workCtx.Err() != nil {
					return
				}
				media := job.media
				source, err := openSistineloveMedia(workCtx, job.kind, *media, client)
				if err != nil {
					fail(job, err)
					return
				}
				link, expiresAt, err := client.upload(workCtx, source, referenceMediaLabel(job.kind, job.index))
				_ = source.Close()
				if err != nil {
					fail(job, err)
					return
				}
				media.URL, media.DataURL, media.StorageKey = link, "", ""
				media.Bytes, media.MimeType = source.size, source.mime
				media.temporaryURLExpiresAt = expiresAt
			}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	*input = prepared
	return nil
}

func validSistineloveMediaLink(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && strings.TrimSpace(raw) == raw && strings.EqualFold(u.Scheme, "https") && strings.EqualFold(u.Hostname(), "img.sistinelove.cn") && (u.Port() == "" || u.Port() == "443") && u.User == nil && u.Fragment == "" && u.Opaque == ""
}

func validateSistineloveCreateMedia(input Input, spec protocol.RequestSpec, now time.Time) error {
	if !usesSistineloveUpload(input) || input.Mode != "video" {
		return nil
	}
	body := ProtocolBodyObject(spec.Body)
	for _, group := range providerMediaGroups(&input) {
		key := map[string]string{"image": "images", "video": "videos", "audio": "audios"}[group.kind]
		var submitted []any
		if raw, exists := body[key]; exists {
			var ok bool
			submitted, ok = raw.([]any)
			if !ok {
				return fmt.Errorf("参考素材请求字段 %s 格式无效", key)
			}
		}
		if len(submitted) != len(group.items) {
			return fmt.Errorf("参考素材请求字段 %s 数量与已上传文件不一致", key)
		}
		for index, media := range group.items {
			if !validSistineloveMediaLink(media.URL) || media.DataURL != "" || media.StorageKey != "" || media.temporaryURLExpiresAt.IsZero() || !now.Before(media.temporaryURLExpiresAt) {
				return fmt.Errorf("%s 上传链接无效或已超过25分钟有效期，请重新提交", referenceMediaLabel(group.kind, index))
			}
			url, ok := submitted[index].(string)
			if !ok || url != media.URL {
				return fmt.Errorf("%s 请求链接与本次上传回执不一致", referenceMediaLabel(group.kind, index))
			}
		}
	}
	return nil
}

func (c sistineloveMediaClient) download(ctx context.Context, raw string, media Media, limit int64) (sistineloveMediaSource, error) {
	if _, err := outbound.ValidatePublicHTTPSURLContext(ctx, raw); err != nil {
		return sistineloveMediaSource{}, errors.New("参考素材需要可访问的公网 HTTPS 链接")
	}
	requestCtx := outbound.WithPublicHTTPS(WithRequestKind(WithoutCallAccounting(ctx), "reference_upload"))
	return downloadSistineloveMedia(requestCtx, raw, media, limit, c.sourceClient)
}

func downloadSistineloveMedia(ctx context.Context, raw string, media Media, limit int64, client *http.Client) (sistineloveMediaSource, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return sistineloveMediaSource{}, errors.New("参考素材链接无效")
	}
	req.Header.Set("Accept", "application/octet-stream, */*")
	outbound.ApplyDefaultOutboundHeaders(req)
	response, err := client.Do(req)
	if err != nil {
		return sistineloveMediaSource{}, fmt.Errorf("读取参考素材失败：%w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return sistineloveMediaSource{}, fmt.Errorf("读取参考素材返回 HTTP %d", response.StatusCode)
	}
	if response.ContentLength > limit {
		return sistineloveMediaSource{}, errors.New("参考素材超过文件大小限制")
	}
	file, err := os.CreateTemp("", "beeftv-seedance-reference-*")
	if err != nil {
		return sistineloveMediaSource{}, errors.New("无法暂存参考素材")
	}
	cleanup := func() {
		_ = file.Close()
		_ = os.Remove(file.Name())
	}
	written, err := io.Copy(file, io.LimitReader(response.Body, limit+1))
	if err != nil {
		cleanup()
		return sistineloveMediaSource{}, fmt.Errorf("暂存参考素材失败：%w", err)
	}
	if written == 0 || written > limit {
		cleanup()
		return sistineloveMediaSource{}, errors.New("参考素材为空或超过文件大小限制")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return sistineloveMediaSource{}, errors.New("无法读取暂存参考素材")
	}
	mimeType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mimeType == "" || mimeType == "application/octet-stream" {
		mimeType = firstNonEmpty(media.MimeType, "application/octet-stream")
	}
	name := strings.TrimSpace(media.Name)
	if name == "" {
		parsed, _ := url.Parse(raw)
		name = path.Base(parsed.Path)
		if name == "." || name == "/" || name == "" {
			name = "reference"
		}
	}
	return sistineloveMediaSource{ReadCloser: &temporaryMediaFile{File: file, path: file.Name()}, size: written, mime: mimeType, name: name}, nil
}

func (c sistineloveMediaClient) upload(ctx context.Context, source sistineloveMediaSource, label string) (string, time.Time, error) {
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", time.Time{}, err
	}
	id := hex.EncodeToString(random[:])
	uploadCtx, reporter := beginReferenceProgress(ctx, label, source.size)
	var envelope bytes.Buffer
	writer := multipart.NewWriter(&envelope)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": "file", "filename": SafeProtocolFilename(source.name)}))
	header.Set("Content-Type", defaultString(source.mime, "application/octet-stream"))
	if _, err := writer.CreatePart(header); err != nil {
		return "", time.Time{}, err
	}
	prefixSize := envelope.Len()
	if err := writer.Close(); err != nil {
		return "", time.Time{}, err
	}
	// Stream the owned file between the multipart prefix and suffix; memory does
	// not grow with video size, and no goroutine is left waiting on an io.Pipe.
	body := io.MultiReader(bytes.NewReader(envelope.Bytes()[:prefixSize]), referenceUploadReader(uploadCtx, io.LimitReader(source, source.size+1)), bytes.NewReader(envelope.Bytes()[prefixSize:]))
	requestCtx := WithRequestKind(WithoutCallAccounting(uploadCtx), "reference_upload")
	uploadedAt := time.Now()
	if _, err := c.request(requestCtx, http.MethodPost, "/img_up", id, writer.FormDataContentType(), body, source.size+int64(envelope.Len())); err != nil {
		return "", time.Time{}, err
	}
	reporter.report("processing", source.size)
	for attempt := 0; attempt < c.queryAttempts; attempt++ {
		if attempt > 0 {
			if err := SleepContext(ctx, c.queryInterval); err != nil {
				return "", time.Time{}, err
			}
		}
		payload, err := c.request(requestCtx, http.MethodGet, "/q", id, "", nil, 0)
		if err != nil {
			return "", time.Time{}, err
		}
		var receipt struct {
			ID  string `json:"img_id"`
			URL string `json:"img_url"`
		}
		if err := json.Unmarshal(payload, &receipt); err != nil {
			return "", time.Time{}, errors.New("素材链接查询返回了无效 JSON")
		}
		if receipt.ID != id {
			return "", time.Time{}, errors.New("素材链接查询返回的 img_id 与上传文件不一致")
		}
		if receipt.URL == "" {
			continue
		}
		if !validSistineloveMediaLink(receipt.URL) {
			return "", time.Time{}, errors.New("素材服务返回了无效 HTTPS 链接")
		}
		return receipt.URL, uploadedAt.Add(sistineloveMediaURLLifetime), nil
	}
	return "", time.Time{}, errors.New("素材上传成功，但等待链接超时，请稍后重试")
}

func (c sistineloveMediaClient) request(ctx context.Context, method, path, id, contentType string, body io.Reader, length int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.ContentLength = length
	req.Header.Set("img_id", id)
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	outbound.ApplyDefaultOutboundHeaders(req)
	// This service has its own img_id contract; never copy generation keys,
	// custom channel headers or cookies onto an upload or query request.
	response, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("素材服务请求失败：%w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("素材服务 %s 返回 HTTP %d", path, response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 1<<20 {
		return nil, errors.New("素材服务回执超过大小限制")
	}
	var payload map[string]any
	if json.Unmarshal(data, &payload) == nil {
		if success, ok := payload["success"].(bool); ok && !success {
			return nil, errors.New("素材服务返回失败状态")
		}
		if _, _, failed := PayloadBusinessFailure(payload); failed {
			return nil, errors.New("素材服务返回失败状态")
		}
	}
	return data, nil
}
