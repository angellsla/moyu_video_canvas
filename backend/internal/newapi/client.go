package newapi

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	BaseURL   = "https://video.sistinelove.cn"
	ChannelID = "newapi"
	KeyName   = "videoCanvas"
	pageSize  = 100
	maxPages  = 1000
)

var ErrKeyUnavailable = errors.New("墨羽Canvas API 的 videoCanvas 密钥已存在，但服务未返回完整密钥")

// Client implements only the New API management and OpenAI-compatible model
// catalog calls used by the local connection flow. The origin is fixed so a
// request body cannot turn this management proxy into an arbitrary URL client.
type Client struct {
	http *http.Client
}

func NewClient() *Client {
	return &Client{http: &http.Client{
		Timeout: 25 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			// Never forward login credentials or a dashboard bearer token through a redirect.
			return http.ErrUseLastResponse
		},
	}}
}

type Account struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Group    string `json:"group"`
}

type Token struct {
	ID     int64
	Name   string
	Status int
	Key    string
}

type httpStatusError struct {
	status int
}

func (e httpStatusError) Error() string {
	return fmt.Sprintf("墨羽Canvas API返回 HTTP %d", e.status)
}

// GetOrCreateModelKey logs in, confirms the authenticated account, reuses an
// exact-name token when present, and creates one only when the name is absent.
func (c *Client) GetOrCreateModelKey(ctx context.Context, username, password string) (string, error) {
	username = strings.TrimSpace(username)
	if username == "" || strings.TrimSpace(password) == "" {
		return "", errors.New("请输入墨羽Canvas用户名和密码")
	}
	accessToken, err := c.login(ctx, username, password)
	if err != nil {
		return "", err
	}
	if _, err := c.self(ctx, accessToken); err != nil {
		return "", err
	}
	tokens, err := c.listTokens(ctx, accessToken)
	if err != nil {
		return "", err
	}
	var inactiveMatch *Token
	activeMatch := false
	for _, token := range tokens {
		if token.Name != KeyName {
			continue
		}
		if token.Status != 1 {
			if inactiveMatch == nil {
				inactiveMatch = &token
			}
			continue
		}
		activeMatch = true
		if key := unmasked(token.Key); key != "" {
			return key, nil
		}
		if key, revealErr := c.revealTokenKey(ctx, accessToken, token.ID); revealErr == nil {
			return key, nil
		}
	}
	if activeMatch {
		// The key exists, so creating another one would violate the exact-name
		// reuse contract and can leave duplicate credentials on the account.
		return "", ErrKeyUnavailable
	}
	if inactiveMatch != nil {
		return "", errors.New("墨羽Canvas API 中已存在停用的 videoCanvas 密钥，请先在管理界面启用或处理该密钥")
	}

	created, err := c.createToken(ctx, accessToken)
	if err != nil {
		return "", err
	}
	if key := unmasked(created.Key); key != "" {
		return key, nil
	}
	if created.ID == 0 {
		// Some versions return only an acknowledgement from create; resolve the
		// newly-created record by exact name before using the dedicated reveal API.
		refreshed, listErr := c.listTokens(ctx, accessToken)
		if listErr != nil {
			return "", listErr
		}
		for _, token := range refreshed {
			if token.Name == KeyName && token.Status == 1 {
				created = token
				break
			}
		}
	}
	if created.ID == 0 {
		return "", ErrKeyUnavailable
	}
	return c.revealTokenKey(ctx, accessToken, created.ID)
}

func (c *Client) ListModels(ctx context.Context, apiKey string) ([]string, error) {
	var response struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		Error json.RawMessage `json:"error"`
	}
	if err := c.doJSON(ctx, http.MethodGet, BaseURL+"/v1/models", apiKey, nil, &response); err != nil {
		return nil, fmt.Errorf("读取墨羽Canvas API模型目录失败: %w", err)
	}
	seen := make(map[string]struct{}, len(response.Data))
	models := make([]string, 0, len(response.Data))
	for _, item := range response.Data {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		models = append(models, id)
	}
	if len(models) == 0 {
		return nil, errors.New("墨羽Canvas API模型目录为空或响应格式不受支持")
	}
	return models, nil
}

func (c *Client) login(ctx context.Context, username, password string) (string, error) {
	loginBody := map[string]string{"username": username, "password": password}
	var encryption struct {
		Success bool `json:"success"`
		Data    struct {
			Enabled   bool   `json:"enabled"`
			KeyID     string `json:"kid"`
			PublicKey string `json:"public_key"`
		} `json:"data"`
	}
	encryptionErr := c.doJSON(ctx, http.MethodGet, BaseURL+"/api/user/login/encryption-key", "", nil, &encryption)
	useEncryptedPassword := encryptionErr == nil && encryption.Success && encryption.Data.Enabled
	if encryptionErr != nil {
		var statusErr httpStatusError
		if !errors.As(encryptionErr, &statusErr) || statusErr.status != http.StatusNotFound {
			return "", errors.New("墨羽Canvas API登录加密状态读取失败")
		}
		// Some compatible deployments do not expose the optional password-
		// encryption endpoint. Their standard login route accepts password.
	} else if !encryption.Success {
		return "", errors.New("墨羽Canvas API登录加密状态读取失败")
	}
	if useEncryptedPassword {
		if strings.TrimSpace(encryption.Data.PublicKey) == "" || strings.TrimSpace(encryption.Data.KeyID) == "" {
			return "", errors.New("墨羽Canvas API登录加密已启用，但没有返回有效公钥")
		}
		ciphertext, err := encryptPassword(password, encryption.Data.PublicKey, encryption.Data.KeyID)
		if err != nil {
			return "", errors.New("墨羽Canvas API登录密码加密初始化失败")
		}
		loginBody = map[string]string{
			"username":           username,
			"password_encrypted": ciphertext,
			"encryption_key_id":  encryption.Data.KeyID,
		}
	}
	var response struct {
		Success bool `json:"success"`
		Data    struct {
			AccessToken string `json:"access_token"`
		} `json:"data"`
	}
	err := c.doJSON(ctx, http.MethodPost, BaseURL+"/api/user/login", "", loginBody, &response)
	if err != nil {
		return "", errors.New("墨羽Canvas API登录失败，请确认用户名、密码和账号状态")
	}
	if !response.Success || strings.TrimSpace(response.Data.AccessToken) == "" {
		return "", errors.New("墨羽Canvas API登录未返回管理凭据")
	}
	return response.Data.AccessToken, nil
}

func encryptPassword(password, publicKeyPEM, keyID string) (string, error) {
	block, _ := pem.Decode([]byte(publicKeyPEM))
	if block == nil || block.Type != "PUBLIC KEY" {
		return "", errors.New("New API password encryption key is invalid")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return "", err
	}
	publicKey, ok := parsed.(*rsa.PublicKey)
	if !ok || publicKey.N == nil || publicKey.N.BitLen() < 2048 {
		return "", errors.New("New API password encryption key is unsupported")
	}
	if strings.TrimSpace(keyID) == "" {
		return "", errors.New("New API password encryption key ID is missing")
	}
	contentKey := make([]byte, 32)
	if _, err := rand.Read(contentKey); err != nil {
		return "", err
	}
	wrappedKey, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, publicKey, contentKey, []byte("password-v2"))
	if err != nil {
		return "", err
	}
	cipherBlock, err := aes.NewCipher(contentKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(cipherBlock)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nil, nonce, []byte(password), []byte("password-v2:"+keyID))
	return "v2." + base64.StdEncoding.EncodeToString(wrappedKey) + "." + base64.StdEncoding.EncodeToString(nonce) + "." + base64.StdEncoding.EncodeToString(ciphertext), nil
}

func (c *Client) self(ctx context.Context, accessToken string) (Account, error) {
	var response struct {
		Success bool    `json:"success"`
		Data    Account `json:"data"`
	}
	if err := c.doJSON(ctx, http.MethodGet, BaseURL+"/api/user/self", accessToken, nil, &response); err != nil || !response.Success {
		return Account{}, errors.New("墨羽Canvas API身份验证失败")
	}
	return response.Data, nil
}

func (c *Client) listTokens(ctx context.Context, accessToken string) ([]Token, error) {
	all := make([]Token, 0)
	seen := make(map[string]struct{})
	for page := 1; page <= maxPages; page++ {
		endpoint := BaseURL + "/api/token/?p=" + strconv.Itoa(page) + "&page_size=" + strconv.Itoa(pageSize)
		var response struct {
			Success bool `json:"success"`
			Data    struct {
				Page     int64             `json:"page"`
				PageSize int64             `json:"page_size"`
				Total    int64             `json:"total"`
				Items    []json.RawMessage `json:"items"`
			} `json:"data"`
		}
		if err := c.doJSON(ctx, http.MethodGet, endpoint, accessToken, nil, &response); err != nil || !response.Success {
			return nil, errors.New("读取墨羽Canvas API Key列表失败")
		}
		pageTokens := make([]Token, 0, len(response.Data.Items))
		pageSeen := make(map[string]struct{}, len(response.Data.Items))
		for _, raw := range response.Data.Items {
			token, err := decodeToken(raw)
			if err != nil {
				return nil, errors.New("墨羽Canvas API Key列表格式不受支持")
			}
			identity := tokenIdentity(token)
			if _, duplicate := pageSeen[identity]; duplicate {
				continue
			}
			pageSeen[identity] = struct{}{}
			pageTokens = append(pageTokens, token)
		}
		if response.Data.Page > 0 && response.Data.Page != int64(page) {
			return nil, errors.New("墨羽Canvas API Key列表分页游标无效")
		}
		added := 0
		for _, token := range pageTokens {
			identity := tokenIdentity(token)
			if _, duplicate := seen[identity]; duplicate {
				continue
			}
			seen[identity] = struct{}{}
			all = append(all, token)
			added++
		}
		if response.Data.Total > 0 && int64(len(all)) >= response.Data.Total {
			return all, nil
		}
		if len(pageTokens) == 0 || added == 0 {
			if response.Data.Total > 0 {
				return nil, errors.New("墨羽Canvas API Key列表未能读取完整，请重试")
			}
			return all, nil
		}
		if response.Data.Total == 0 {
			actualPageSize := response.Data.PageSize
			if actualPageSize <= 0 {
				actualPageSize = pageSize
			}
			if int64(len(response.Data.Items)) < actualPageSize {
				return all, nil
			}
		}
	}
	return nil, errors.New("墨羽Canvas API Key列表页数超出安全上限")
}

func tokenIdentity(token Token) string {
	if token.ID > 0 {
		return "id:" + strconv.FormatInt(token.ID, 10)
	}
	return "fallback:" + token.Name + "\x00" + token.Key + "\x00" + strconv.Itoa(token.Status)
}

func (c *Client) createToken(ctx context.Context, accessToken string) (Token, error) {
	// Unlimited quota and disabled model limits leave model access to the
	// account's default group, as requested. The key itself remains account-scoped.
	request := map[string]any{
		"name":                 KeyName,
		"remain_quota":         0,
		"expired_time":         -1,
		"unlimited_quota":      true,
		"model_limits_enabled": false,
		"model_limits":         "",
		"allow_ips":            "",
	}
	// New API stores an empty token group as “inherit the user's default
	// group”. Sending the literal name (often "default") pins the token to a
	// concrete group and may not match configured group identifiers.
	var response struct {
		Success bool            `json:"success"`
		Data    json.RawMessage `json:"data"`
	}
	if err := c.doJSON(ctx, http.MethodPost, BaseURL+"/api/token/", accessToken, request, &response); err != nil || !response.Success {
		return Token{}, errors.New("墨羽Canvas API videoCanvas Key创建失败")
	}
	if len(response.Data) == 0 || string(response.Data) == "null" {
		return Token{}, nil
	}
	return decodeToken(response.Data)
}

func (c *Client) revealTokenKey(ctx context.Context, accessToken string, id int64) (string, error) {
	if id <= 0 {
		return "", ErrKeyUnavailable
	}
	// New API v1.0.0-rc.47 registers this as POST /api/token/:id/key.
	// The handler returns { success: true, data: { key: "..." } }.
	endpoint := BaseURL + "/api/token/" + strconv.FormatInt(id, 10) + "/key"
	var response struct {
		Success bool            `json:"success"`
		Data    json.RawMessage `json:"data"`
		Key     string          `json:"key"`
	}
	if err := c.doJSON(ctx, http.MethodPost, endpoint, accessToken, nil, &response); err != nil || !response.Success {
		return "", ErrKeyUnavailable
	}
	if key := unmasked(response.Key); key != "" {
		return key, nil
	}
	var data struct {
		Key string `json:"key"`
	}
	if json.Unmarshal(response.Data, &data) == nil {
		if key := unmasked(data.Key); key != "" {
			return key, nil
		}
	}
	var raw string
	if json.Unmarshal(response.Data, &raw) == nil {
		if key := unmasked(raw); key != "" {
			return key, nil
		}
	}
	return "", ErrKeyUnavailable
}

func decodeToken(raw json.RawMessage) (Token, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return Token{}, err
	}
	var result Token
	_ = json.Unmarshal(fields["id"], &result.ID)
	_ = json.Unmarshal(fields["name"], &result.Name)
	_ = json.Unmarshal(fields["status"], &result.Status)
	_ = json.Unmarshal(fields["key"], &result.Key)
	if result.Name == "" {
		return Token{}, errors.New("token missing name")
	}
	return result, nil
}

func unmasked(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsAny(value, "*…") || strings.Contains(value, "****") || strings.HasPrefix(value, "<") {
		return ""
	}
	return value
}

func (c *Client) doJSON(ctx context.Context, method, endpoint, bearer string, requestBody, responseBody any) error {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "video.sistinelove.cn" {
		return errors.New("墨羽Canvas API地址无效")
	}
	var body io.Reader
	if requestBody != nil {
		encoded, err := json.Marshal(requestBody)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	if requestBody != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return errors.New("墨羽Canvas API请求失败")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return httpStatusError{status: response.StatusCode}
	}
	limited := io.LimitReader(response.Body, 2<<20)
	if err := json.NewDecoder(limited).Decode(responseBody); err != nil {
		return errors.New("墨羽Canvas API响应格式无效")
	}
	return nil
}
