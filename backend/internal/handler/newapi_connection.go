package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"infinite-canvas/backend/internal/agentops"
	"infinite-canvas/backend/internal/app"
	"infinite-canvas/backend/internal/newapi"
	"infinite-canvas/backend/internal/workspace"

	"github.com/gin-gonic/gin"
)

type newAPIChannelSyncRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// RegisterNewAPIConnectionRoutes exposes a local UI-only login-and-sync action.
// Dashboard credentials and access tokens are used only in this request; only
// the model API key is stored in the encrypted local provider configuration.
func RegisterNewAPIConnectionRoutes(r gin.IRouter, svc *app.Service, desktopTrust func(*http.Request) bool) {
	client := newapi.NewClient()
	var syncMu sync.Mutex
	r.POST("/newapi/connection/sync", func(c *gin.Context) {
		if !requireNewAPIManagementTrust(c, svc, desktopTrust) {
			return
		}
		if !enforceRateLimit(c, "newapi-connection-sync", 5, 10*time.Minute) {
			return
		}
		var request newAPIChannelSyncRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			fail(c, http.StatusBadRequest, errors.New("墨羽Canvas API登录信息格式错误"))
			return
		}
		if len(request.Username) > 256 || len(request.Password) > 4096 {
			fail(c, http.StatusBadRequest, errors.New("墨羽Canvas API登录信息长度无效"))
			return
		}
		// Serialize the entire read/remote-sync/write flow in this runtime so two
		// simultaneous logins cannot create duplicate remote keys or race config.
		syncMu.Lock()
		defer syncMu.Unlock()

		providerConfig := requestProviderConfig(c, svc)
		if err := ensureNewAPIChannelIDAvailable(providerConfig); err != nil {
			fail(c, http.StatusConflict, err)
			return
		}

		apiKey, err := client.GetOrCreateModelKey(c.Request.Context(), request.Username, request.Password)
		if err != nil {
			fail(c, http.StatusBadGateway, err)
			return
		}
		models, err := client.ListModels(c.Request.Context(), apiKey)
		if err != nil {
			fail(c, http.StatusBadGateway, err)
			return
		}
		channel := map[string]any{
			"id": newapi.ChannelID, "name": "墨羽Canvas API",
			"baseUrl": newapi.BaseURL + "/v1", "apiFormat": "openai", "scope": "user",
			"enabled": true, "apiKey": apiKey, "models": stringsToAny(models),
		}
		if versioned, versionedOK := providerConfig.(VersionedProviderConfig); versionedOK {
			effective, _, err := versioned.LoadEffectiveModelConfig()
			if err != nil {
				failService(c, err)
				return
			}
			config := effective.Config
			channels := channelList(config["channels"])
			channel["modelProfiles"] = preserveNewAPIProfiles(channels, models)
			config["channels"] = replaceNewAPIChannel(channels, channel)
			encoded, err := json.Marshal(config)
			if err != nil {
				failInternal(c, http.StatusInternalServerError, err)
				return
			}
			revision, err := versioned.SaveLocalModelConfigRevision(encoded, effective.Revision)
			if errors.Is(err, workspace.ErrProviderConfigRevisionConflict) {
				fail(c, http.StatusConflict, err)
				return
			}
			if err != nil {
				failService(c, err)
				return
			}
			ok(c, gin.H{"channel": channel, "revision": revision, "modelCount": len(models)})
			return
		}

		body, err := providerConfig.ReadLocalModelConfig()
		if err != nil {
			failService(c, err)
			return
		}
		config := map[string]any{}
		if len(body) > 0 {
			if err := json.Unmarshal(body, &config); err != nil {
				fail(c, http.StatusInternalServerError, errors.New("本地模型配置损坏"))
				return
			}
		}
		channels := channelList(config["channels"])
		channel["modelProfiles"] = preserveNewAPIProfiles(channels, models)
		config["channels"] = replaceNewAPIChannel(channels, channel)
		encoded, err := json.Marshal(config)
		if err != nil {
			failInternal(c, http.StatusInternalServerError, err)
			return
		}
		if err := providerConfig.SaveLocalModelConfig(encoded); err != nil {
			failService(c, err)
			return
		}
		ok(c, gin.H{"channel": channel, "modelCount": len(models)})
	})
}

func ensureNewAPIChannelIDAvailable(providerConfig ProviderConfig) error {
	var body []byte
	if versioned, ok := providerConfig.(VersionedProviderConfig); ok {
		effective, _, err := versioned.LoadEffectiveModelConfig()
		if err != nil {
			return errors.New("读取本地模型配置失败")
		}
		body, err = json.Marshal(effective.Config)
		if err != nil {
			return errors.New("本地模型配置无法解析")
		}
	} else {
		var err error
		body, err = providerConfig.ReadLocalModelConfig()
		if err != nil {
			return errors.New("读取本地模型配置失败")
		}
	}
	if len(body) == 0 {
		return nil
	}
	var config map[string]any
	if err := json.Unmarshal(body, &config); err != nil {
		return errors.New("本地模型配置损坏")
	}
	for _, channel := range channelList(config["channels"]) {
		if channel["id"] != newapi.ChannelID {
			continue
		}
		if strings.TrimRight(strings.TrimSpace(stringValue(channel["baseUrl"])), "/") != newapi.BaseURL+"/v1" {
			return errors.New("本地已有其他模型渠道使用 newapi 标识，请先为该渠道更换标识")
		}
	}
	return nil
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func requireNewAPIManagementTrust(c *gin.Context, svc *app.Service, desktopTrust func(*http.Request) bool) bool {
	if strings.TrimSpace(c.GetHeader("X-Beeftv-Client")) != "" {
		fail(c, http.StatusForbidden, app.BadAuthRequest("外部客户端不能管理模型供应商登录"))
		return false
	}
	if !isLoopbackRequest(c.Request) {
		fail(c, http.StatusForbidden, app.BadAuthRequest("只接受本机同源请求"))
		return false
	}
	ownerOK := svc != nil && agentops.OwnerTokenMatches(svc.DataDir(), strings.TrimSpace(c.GetHeader("X-Beeftv-Owner")))
	devBootstrap := strings.TrimSpace(os.Getenv("BEEFTV_UI_BOOTSTRAP")) == "1"
	desktopShell := desktopTrust != nil && desktopTrust(c.Request)
	if !ownerOK && !devBootstrap && !desktopShell {
		fail(c, http.StatusForbidden, app.BadAuthRequest("管理模型供应商需要受信任的本机界面"))
		return false
	}
	return true
}

func stringsToAny(values []string) []any {
	result := make([]any, len(values))
	for index, value := range values {
		result[index] = value
	}
	return result
}

func channelList(value any) []map[string]any {
	raw, _ := value.([]any)
	channels := make([]map[string]any, 0, len(raw))
	for _, entry := range raw {
		channel, ok := entry.(map[string]any)
		if ok {
			channels = append(channels, channel)
		}
	}
	return channels
}

func replaceNewAPIChannel(channels []map[string]any, updated map[string]any) []any {
	result := make([]any, 0, len(channels)+1)
	for _, channel := range channels {
		if channel["id"] == newapi.ChannelID {
			continue
		}
		result = append(result, channel)
	}
	return append(result, updated)
}

func preserveNewAPIProfiles(channels []map[string]any, models []string) []any {
	allowed := make(map[string]struct{}, len(models))
	for _, model := range models {
		allowed[model] = struct{}{}
	}
	for _, channel := range channels {
		if channel["id"] != newapi.ChannelID {
			continue
		}
		profiles, _ := channel["modelProfiles"].([]any)
		result := make([]any, 0, len(profiles))
		for _, entry := range profiles {
			profile, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			model, _ := profile["model"].(string)
			if _, exists := allowed[model]; exists {
				result = append(result, profile)
			}
		}
		return result
	}
	return []any{}
}
