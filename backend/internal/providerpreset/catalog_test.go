package providerpreset

import "testing"

func TestBuiltinChannelsAreUserConfigured(t *testing.T) {
	if got := BuiltinChannels(); len(got) != 0 {
		t.Fatalf("retired managed channels must not be injected: %#v", got)
	}
}

func TestCatalogValidationRejectsUnsafeOrAmbiguousPresets(t *testing.T) {
	valid := ChannelPreset{
		ID:            "newapi",
		DisplayName:   "墨羽Canvas API",
		BaseURL:       "https://video.sistinelove.cn",
		PresetVersion: 1,
		Pinned:        true,
	}
	valid.Models = []ModelProfile{{Model: "test", Capability: "text", Protocol: "chat-completion", CapabilityConfig: []byte(`{"version":1,"text":{"streaming":true}}`)}}
	tests := []struct {
		name   string
		mutate func(*ChannelPreset)
	}{
		{name: "duplicate model", mutate: func(preset *ChannelPreset) { preset.Models = append(preset.Models, preset.Models[0]) }},
		{name: "unknown protocol", mutate: func(preset *ChannelPreset) { preset.Models[0].Protocol = "mystery" }},
		{name: "missing capability", mutate: func(preset *ChannelPreset) { preset.Models[0].Capability = "" }},
		{name: "secret field", mutate: func(preset *ChannelPreset) { preset.Metadata = map[string]any{"apiKey": "must-not-ship"} }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid.Clone()
			test.mutate(&candidate)
			if err := Validate(candidate); err == nil {
				t.Fatal("invalid preset was accepted")
			}
		})
	}
}
