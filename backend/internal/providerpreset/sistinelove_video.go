package providerpreset

import (
	_ "embed"
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
)

//go:embed sistinelove_video.json
var sistineloveVideoJSON []byte

type sistineloveVideoContract struct {
	Protocol               string            `json:"protocol"`
	GenerationOrigin       string            `json:"generationOrigin"`
	MediaOrigin            string            `json:"mediaOrigin"`
	ModelPattern           string            `json:"modelPattern"`
	DefaultResolution      string            `json:"defaultResolution"`
	ModelResolutionPattern string            `json:"modelResolutionPattern"`
	ResolutionAliases      map[string]string `json:"resolutionAliases"`
}

var SistineloveVideo = func() sistineloveVideoContract {
	var contract sistineloveVideoContract
	if err := json.Unmarshal(sistineloveVideoJSON, &contract); err != nil {
		panic(err)
	}
	return contract
}()

var sistineloveSeedanceModel = regexp.MustCompile(SistineloveVideo.ModelPattern)
var sistineloveSeedanceResolution = regexp.MustCompile("(?i)" + SistineloveVideo.ModelResolutionPattern)

// 墨羽线路按模型名区分分辨率 SKU；未标明档位的模型使用 720p。
func SistineloveSeedanceResolution(model string) string {
	match := sistineloveSeedanceResolution.FindStringSubmatch(strings.TrimSpace(model))
	if len(match) > 1 {
		if resolution := SistineloveVideo.ResolutionAliases[strings.TrimSuffix(strings.ToLower(match[1]), "p")]; resolution != "" {
			return resolution
		}
	}
	return SistineloveVideo.DefaultResolution
}

// The supplied gateway contract is scoped to its exact origin. A model name
// alone must never send another provider's local assets to this upload service.
func IsSistineloveSeedance(baseURL, model string) bool {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.User != nil || u.Scheme != "https" || !strings.EqualFold(u.Hostname(), "video.sistinelove.cn") || (u.Port() != "" && u.Port() != "443") {
		return false
	}
	return sistineloveSeedanceModel.MatchString(strings.ToLower(strings.TrimSpace(model)))
}
