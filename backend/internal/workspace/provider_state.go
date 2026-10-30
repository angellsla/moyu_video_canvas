package workspace

import (
	"encoding/json"
	"net/url"
	"strings"
)

const providerStateSchemaVersion = 1

type ConfigHealth string

const (
	ConfigHealthReady     ConfigHealth = "ready"
	ConfigHealthDefault   ConfigHealth = "default"
	ConfigHealthMigrated  ConfigHealth = "migrated"
	ConfigHealthRecovered ConfigHealth = "recovered"
)

type ProviderStateDocument struct {
	SchemaVersion                 int            `json:"schemaVersion"`
	Revision                      int64          `json:"revision"`
	PresetVersions                map[string]int `json:"presetVersions"`
	Config                        map[string]any `json:"config"`
	AssistantDefaultAuthorization string         `json:"assistantDefaultAuthorization,omitempty"`
}

type EffectiveModelConfig struct {
	SchemaVersion  int            `json:"schemaVersion"`
	Revision       int64          `json:"revision"`
	PresetVersions map[string]int `json:"presetVersions"`
	Config         map[string]any `json:"config"`
}

func newProviderState(config map[string]any, revision int64) ProviderStateDocument {
	return ProviderStateDocument{
		SchemaVersion:  providerStateSchemaVersion,
		Revision:       revision,
		PresetVersions: builtinPresetVersions(),
		Config:         config,
	}
}

func effectiveProviderState(document ProviderStateDocument) (EffectiveModelConfig, error) {
	config, err := mergeBuiltinProviderPresets(document.Config)
	if err != nil {
		return EffectiveModelConfig{}, err
	}
	return EffectiveModelConfig{
		SchemaVersion:  providerStateSchemaVersion,
		Revision:       document.Revision,
		PresetVersions: builtinPresetVersions(),
		Config:         config,
	}, nil
}

func mergeBuiltinProviderPresets(config map[string]any) (map[string]any, error) {
	result, err := cloneMap(config)
	if err != nil {
		return nil, err
	}
	channels := map[string]map[string]any{}
	order := make([]string, 0)
	if rawChannels, ok := result["channels"].([]any); ok {
		for _, raw := range rawChannels {
			channel, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			id, _ := channel["id"].(string)
			if id == "" {
				continue
			}
			// Remove only the retired managed channel. The ID itself is user data:
			// a custom provider can legitimately have reused "beefapi" and must not
			// be silently discarded just because the former built-in was removed.
			if isRetiredManagedChannel(channel) {
				continue
			}
			channels[id] = channel
			order = append(order, id)
		}
	}

	mergedChannels := make([]any, 0, len(order))
	for _, id := range order {
		if channel := channels[id]; channel != nil {
			mergedChannels = append(mergedChannels, channel)
		}
	}
	result["channels"] = mergedChannels
	return result, nil
}

func isRetiredManagedChannel(channel map[string]any) bool {
	id, _ := channel["id"].(string)
	if id != "beefapi" {
		return false
	}
	credentialRef, _ := channel["credentialRef"].(string)
	if strings.TrimSpace(credentialRef) == "beefapi-enterprise" {
		return true
	}
	baseURL, _ := channel["baseUrl"].(string)
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return false
	}
	switch strings.ToLower(parsed.Hostname()) {
	case "beeftv.app", "enterprise.beefapi.com":
		// Remove the former managed endpoints even when older configs lack the
		// credential marker. A pinned flag alone is not sufficient evidence:
		// users may have reused the historic ID for a custom provider.
		return true
	default:
		return false
	}
}

func builtinPresetVersions() map[string]int {
	versions := make(map[string]int)
	return versions
}

func cloneMap(value map[string]any) (map[string]any, error) {
	if value == nil {
		return map[string]any{}, nil
	}
	body, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var clone map[string]any
	if err := json.Unmarshal(body, &clone); err != nil {
		return nil, err
	}
	return clone, nil
}

func containsString(values []string, candidate string) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}
