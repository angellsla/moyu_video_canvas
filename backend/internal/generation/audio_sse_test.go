package generation

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSpeechAudioEventsRequireCompletion(t *testing.T) {
	delta := `data: {"type":"speech.audio.delta","audio":"` + base64.StdEncoding.EncodeToString([]byte("ID3music")) + `"}` + "\n\n"
	done := "data: {\"type\":\"speech.audio.done\"}\n\n"
	for _, body := range []string{": PING\n\n", delta, done, delta + "data: {\"error\":{\"message\":\"failed\"}}\n\n", delta + done + delta, delta + done + done} {
		if _, _, err := readSpeechAudioEvents([]byte(body)); err == nil {
			t.Fatalf("accepted incomplete/invalid stream: %s", body)
		}
	}
	got, mime, err := readSpeechAudioEvents([]byte(": PING\n\n" + delta + done))
	if err != nil || string(got) != "ID3music" || mime != "audio/mpeg" {
		t.Fatalf("audio=%q mime=%s err=%v", got, mime, err)
	}
}

func TestRunMusicNormalizesOldFormatForCustomProvider(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["response_format"] != "mp3" {
			t.Errorf("invalid music request %v", body)
		}
		if _, ok := body["stream_format"]; ok {
			t.Errorf("custom provider received managed stream extension: %v", body)
		}
		setting := body["metadata"].(map[string]interface{})["audio_setting"].(map[string]interface{})
		if setting["bitrate"] != float64(256000) {
			t.Errorf("missing high quality default: %v", setting)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(": PING\n\ndata: {\"type\":\"speech.audio.delta\",\"audio\":\"SUQzbXVzaWM=\"}\n\ndata: {\"type\":\"speech.audio.done\"}\n\n"))
	}))
	defer server.Close()
	for _, withPlugin := range []bool{false, true} {
		ctx := context.Background()
		config := Config{Model: "minimax-music-v3.0", BaseURL: server.URL, AudioFormat: "flac"}
		if withPlugin {
			config.InterfaceType = "openai-audio"
			registry := LoadOfficialFallbackRegistry()
			if _, ok := registry.Resolve("openai-audio"); !ok {
				t.Fatal("official audio plugin missing; build plugin-packages first")
			}
			ctx = WithProtocolRegistry(ctx, registry)
		}
		result, err := RunAudioTask(ctx, Input{Mode: "audio", Config: config, Prompt: "piano"})
		if err != nil {
			t.Fatal(err)
		}
		audio := result["audio"].(map[string]interface{})
		if (audio["format"] != nil && audio["format"] != "mp3") || !strings.HasPrefix(audio["dataUrl"].(string), "data:audio/mpeg;base64,") {
			t.Fatal(audio)
		}
	}
}

func TestMusicPluginRejectsIncompleteOrNonAudioResponses(t *testing.T) {
	registry := LoadOfficialFallbackRegistry()
	if _, ok := registry.Resolve("openai-audio"); !ok {
		t.Fatal("official audio plugin missing")
	}
	for _, tc := range []struct{ name, mime, body string }{
		{"incomplete", "text/event-stream", ": PING\n\n"},
		{"non-audio-complete", "text/event-stream", "data: {\"type\":\"speech.audio.delta\",\"audio\":\"bm90IGF1ZGlv\"}\n\ndata: {\"type\":\"speech.audio.done\"}\n\n"},
		{"json-error", "application/json", `{"error":{"message":"upstream rejected"}}`},
		{"html-error", "text/html", "<html>failed</html>"},
		{"sse-error", "text/event-stream", "event: error\ndata: {\"error\":{\"message\":\"failed\"}}\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.mime)
				w.Write([]byte(tc.body))
			}))
			defer server.Close()
			_, err := RunAudioTask(WithProtocolRegistry(context.Background(), registry), Input{Mode: "audio", Config: Config{InterfaceType: "openai-audio", Model: "minimax-music-v3.0", BaseURL: server.URL, AudioFormat: "flac"}, Prompt: "piano"})
			if err == nil {
				t.Fatal("accepted invalid audio result")
			}
		})
	}
}

func TestMusicChannelNameDoesNotSelectManagedProtocol(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if _, ok := body["stream_format"]; ok {
			t.Errorf("custom origin received enterprise SSE extension: %v", body)
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte("ID3music"))
	}))
	defer server.Close()
	for _, withPlugin := range []bool{false, true} {
		ctx := context.Background()
		config := Config{ChannelID: "custom-provider", Model: "minimax-music-v3.0", BaseURL: server.URL}
		if withPlugin {
			config.InterfaceType = "openai-audio"
			ctx = WithProtocolRegistry(ctx, LoadOfficialFallbackRegistry())
		}
		if _, err := RunAudioTask(ctx, Input{Mode: "audio", Config: config, Prompt: "piano"}); err != nil {
			t.Fatal(err)
		}
	}
}
