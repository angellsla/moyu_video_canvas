package generation

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"infinite-canvas/backend/internal/protocol"
)

func TestSistineloveVideoRequestNormalizesPersistedResolution(t *testing.T) {
	adapter := unifiedVideoAdapter(t, "sistinelove-seedance-video")
	for _, profile := range []struct {
		name  string
		value *VideoCapabilityConfig
	}{
		{name: "persisted input without runtime capability"},
		{name: "canonical capability", value: &VideoCapabilityConfig{Resolutions: []string{"480p", "720p", "1080p"}}},
		{name: "numeric capability", value: &VideoCapabilityConfig{Resolutions: []string{"480", "720", "1080"}}},
	} {
		for _, tc := range []struct {
			model   string
			quality string
			want    string
		}{
			{model: "seedance-2.0-gz", quality: "1080", want: "720p"},
			{model: "seedance-2.5-gz", quality: "1080p", want: "720p"},
			{model: "seedance-2.0-gz", quality: "", want: "720p"},
			{model: "seedance-2.0-gz", quality: "480", want: "720p"},
			{model: "seedance-2.0-480", quality: "1080", want: "480p"},
			{model: "seedance2.5-gz-1080", quality: "720", want: "1080p"},
			{model: "SD-2.5-1080P-15s", quality: " 1080P ", want: "1080p"},
		} {
			t.Run(profile.name+"/"+tc.model+"/"+tc.quality, func(t *testing.T) {
				var input Input
				if err := json.Unmarshal([]byte(`{"mode":"video","prompt":"animate reference","config":{"interfaceType":"sistinelove-seedance-video","model":"seedance-2.0-gz","size":"16:9","videoSeconds":"5","vquality":"1080","videoGenerateAudio":"false","videoWatermark":"false","capabilityConfig":{"version":1,"video":{"resolutions":["480p","720p","1080p"]}}},"referenceImages":[{"url":"https://media.example/reference.png"}]}`), &input); err != nil {
					t.Fatal(err)
				}
				input.Config.VQuality = tc.quality
				input.Config.Model = tc.model
				input.VideoCapability = profile.value
				request := ProtocolRequestFromInput(input)
				if request.Resolution != tc.want || request.Output.Resolution != tc.want {
					t.Fatalf("resolution=%q output=%q, want %q", request.Resolution, request.Output.Resolution, tc.want)
				}
				spec, err := adapter.BuildCreate(context.Background(), protocol.RequestContext{Request: request})
				if err != nil {
					t.Fatal(err)
				}
				if spec.Method != "POST" || spec.Path != "/v1/videos" || spec.ContentType != "application/json" {
					t.Fatalf("unexpected request: %+v", spec)
				}
				body, err := RequestAsMap(spec.Body)
				if err != nil {
					t.Fatal(err)
				}
				want := map[string]any{
					"model": tc.model, "prompt": "animate reference", "duration": float64(5),
					"aspect_ratio": "16:9", "resolution": tc.want,
					"images":         []any{"https://media.example/reference.png"},
					"generate_audio": false, "watermark": false, "metadata": map[string]any{},
				}
				if !reflect.DeepEqual(body, want) {
					t.Fatalf("body=%+v, want %+v", body, want)
				}
				if input.Config.VQuality != tc.quality {
					t.Fatal("request conversion mutated saved quality")
				}
			})
		}
	}
}

func TestProtocolRequestKeepsOtherResolutionFormats(t *testing.T) {
	for _, tc := range []struct {
		mode, protocol, quality string
	}{
		{mode: "video", protocol: "video-generations-json", quality: "1080"},
		{mode: "video", protocol: "custom-video", quality: "cinema"},
		{mode: "image", protocol: "openai-image", quality: "1080"},
	} {
		t.Run(tc.protocol, func(t *testing.T) {
			request := ProtocolRequestFromInput(Input{Mode: tc.mode, Config: Config{InterfaceType: tc.protocol, VQuality: tc.quality}})
			if request.Resolution != tc.quality || request.Output.Resolution != tc.quality {
				t.Fatalf("unrelated resolution changed: %+v", request.Output)
			}
		})
	}
}
