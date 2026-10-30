package providerpreset

import (
	_ "embed"
	"encoding/json"
	"strings"
)

// Video contracts describe verified gateway routes, not every model belonging
// to the same vendor. The desktop catalog and web configuration share this file.
//
//go:embed video_contracts.json
var videoContractJSON []byte

type VideoContract struct {
	Models                 []string               `json:"models"`
	AllowSuffix            bool                   `json:"allowSuffix"`
	Protocol               string                 `json:"protocol"`
	InlineMedia            bool                   `json:"inlineMedia"`
	RequiresHTTPReferences bool                   `json:"requiresHTTPReferences,omitempty"`
	MaxReferences          map[string]int         `json:"maxReferences,omitempty"`
	Operations             []string               `json:"operations,omitempty"`
	Resolutions            []string               `json:"resolutions,omitempty"`
	ReferenceResolution    string                 `json:"referenceResolution,omitempty"`
	Ratios                 []string               `json:"ratios,omitempty"`
	Duration               *VideoContractDuration `json:"duration,omitempty"`
	MaxImageDuration       int                    `json:"maxImageDuration,omitempty"`
	GenerateAudio          bool                   `json:"generateAudio,omitempty"`
}

type VideoContractDuration struct {
	Min     int `json:"min"`
	Max     int `json:"max"`
	Default int `json:"default"`
}

var videoContracts = func() []VideoContract {
	var contracts []VideoContract
	if err := json.Unmarshal(videoContractJSON, &contracts); err != nil {
		panic(err)
	}
	return contracts
}()

func VideoContractForModel(model string) (VideoContract, bool) {
	model = strings.ToLower(strings.TrimSpace(model))
	for _, contract := range videoContracts {
		for _, name := range contract.Models {
			if model == name || contract.AllowSuffix && strings.HasPrefix(model, name+"-") {
				return contract, true
			}
		}
	}
	return VideoContract{}, false
}
