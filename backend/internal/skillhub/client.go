// Package skillhub is the read-only transport for the separately hosted public
// skill market.
package skillhub

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultURL is the legacy public, read-only SkillHub endpoint. Deployments can
// override it without changing any other cloud integration.
const DefaultURL = "https://beeftv.app/api/beeftv/skillhub/v1"

type File struct {
	Path     string `json:"path"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
	MimeType string `json:"mimeType"`
}
type Showcase struct {
	URL     string `json:"url"`
	Type    string `json:"type"`
	Caption string `json:"caption"`
}
type Metadata struct {
	SkillID          string     `json:"skillId,omitempty"`
	Name             string     `json:"name"`
	Description      string     `json:"description"`
	License          string     `json:"license"`
	SourceURL        string     `json:"sourceUrl"`
	VersionLabel     string     `json:"versionLabel"`
	Requirements     []string   `json:"requirements"`
	RequirementNotes string     `json:"requirementNotes,omitempty"`
	Showcase         []Showcase `json:"showcase"`
	MinClientVersion string     `json:"minClientVersion,omitempty"`
	Tags             []string   `json:"tags,omitempty"`
	Aliases          []string   `json:"aliases,omitempty"`
}
type Release struct {
	Metadata
	ReleaseID     string `json:"releaseId"`
	Author        string `json:"author"`
	ContentHash   string `json:"contentHash"`
	ArchiveSHA256 string `json:"archiveSha256"`
	ArchiveBytes  int64  `json:"archiveBytes"`
	Files         []File `json:"files"`
	DownloadURL   string `json:"downloadUrl"`
	PublishedAt   string `json:"publishedAt"`
}
type Catalog struct {
	Items    []Release `json:"items"`
	Total    int64     `json:"total"`
	Page     int       `json:"page"`
	PageSize int       `json:"pageSize"`
}

type Error struct {
	Status     int
	Reason     string
	Message    string
	RetryAfter string
}

func (e *Error) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return e.Reason
}

type Client struct {
	base *url.URL
	HTTP *http.Client
}

func (c *Client) BaseURL() string { return c.base.String() }

func New(base string) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1"))) {
		return nil, fmt.Errorf("技能市场地址必须是可信 HTTPS 服务")
	}
	c := &Client{base: u}
	c.HTTP = &http.Client{Timeout: 45 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != u.Scheme || req.URL.Host != u.Host || !strings.HasPrefix(req.URL.Path, u.Path+"/") {
			return fmt.Errorf("技能市场下载跳转到未信任地址")
		}
		if len(via) > 3 {
			return fmt.Errorf("技能市场跳转过多")
		}
		return nil
	}}
	return c, nil
}

func (c *Client) URL(route string) (string, error) {
	u, err := url.Parse(route)
	if err != nil {
		return "", err
	}
	if strings.Contains(u.Path, "..") || strings.Contains(u.Path, "\\") {
		return "", fmt.Errorf("技能市场路径无效")
	}
	if u.IsAbs() || u.Host != "" {
		if u.Scheme != c.base.Scheme || u.Host != c.base.Host {
			return "", fmt.Errorf("技能市场地址不受信任")
		}
		if !strings.HasPrefix(u.Path, c.base.Path+"/") {
			return "", fmt.Errorf("技能市场路径不受信任")
		}
		return u.String(), nil
	}
	copy := *c.base
	if strings.HasPrefix(u.Path, c.base.Path+"/") {
		copy.Path = u.Path
	} else {
		copy.Path += "/" + strings.TrimLeft(u.Path, "/")
	}
	copy.RawQuery = u.RawQuery
	return copy.String(), nil
}

func (c *Client) request(ctx context.Context, route string) (*http.Response, error) {
	target, err := c.URL(route)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	return c.HTTP.Do(req)
}

func Decode(res *http.Response, out any) error {
	defer res.Body.Close()
	var e struct {
		Success bool            `json:"success"`
		Data    json.RawMessage `json:"data"`
		Message string          `json:"message"`
		Reason  string          `json:"reason"`
		Error   struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&e); err != nil {
		return &Error{Status: 502, Reason: "market_unreadable", Message: "技能市场返回内容无法读取"}
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 || !e.Success {
		if e.Reason == "" {
			e.Reason = e.Error.Code
		}
		if e.Message == "" {
			e.Message = e.Error.Message
		}
		if e.Reason == "" {
			e.Reason = "market_request_failed"
		}
		return &Error{Status: res.StatusCode, Reason: e.Reason, Message: e.Message, RetryAfter: res.Header.Get("Retry-After")}
	}
	if out != nil {
		return json.Unmarshal(e.Data, out)
	}
	return nil
}

// GetJSON is intentionally the only JSON request surface. Restoring public
// discovery must not reintroduce account connection or publishing writes.
func (c *Client) GetJSON(ctx context.Context, route string, out any) error {
	res, err := c.request(ctx, route)
	if err != nil {
		return &Error{Status: 503, Reason: "market_unavailable", Message: "技能市场暂时无法连接，请稍后重试"}
	}
	return Decode(res, out)
}

// Archive only fetches the exact release URL from the configured market
// origin. VerifyArchive applies the archive, content and per-file digests
// before any file is written to disk.
func (c *Client) Archive(ctx context.Context, release *Release) ([]byte, error) {
	if release == nil {
		return nil, fmt.Errorf("技能发布版本为空")
	}
	res, err := c.request(ctx, release.DownloadURL)
	if err != nil {
		return nil, &Error{Status: 503, Reason: "market_unavailable", Message: "技能市场暂时无法连接，请稍后重试"}
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, Decode(res, nil)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, MaxArchiveBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxArchiveBytes {
		return nil, fmt.Errorf("技能下载包超过10MiB")
	}
	return data, nil
}
