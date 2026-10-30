package modelcatalog

import (
	"testing"
)

func TestAssistantModelsCanBeSelectedFromConfiguredChannel(t *testing.T) {
	channel := AssistantChannel{
		ID: "custom", Enabled: true, APIKey: "synthetic", BaseURL: "https://example.test",
		Models: []string{"deepseek-v4.1-flash", "gpt-6-astra"},
		ModelProfiles: []AssistantModelProfile{
			{Model: "deepseek-v4.1-flash", Capability: "text", Protocol: "chat-completion"},
			{Model: "gpt-6-astra", Capability: "text", Protocol: "chat-completion"},
		},
	}
	for _, model := range channel.Models {
		snapshot := AssistantConfigSnapshot{AssistantModel: "custom::" + model, Channels: []AssistantChannel{channel}}
		provider, err := ResolveAssistantProvider(snapshot, nil)
		if err != nil || provider.Model != model {
			t.Fatalf("configured model %q unavailable: %v %#v", model, err, provider)
		}
	}
}
