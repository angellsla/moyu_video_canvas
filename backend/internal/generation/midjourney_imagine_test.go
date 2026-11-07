package generation

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const tinyPNG = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/+ioAAAAASUVORK5CYII="

func TestPrepareMidjourneyImagineInput(t *testing.T) {
	input := Input{Mode: "image", Prompt: "a red cube --stylize 100", Config: Config{InterfaceType: "openai-image", Model: "mj_imagine", Size: "1920x1080", Count: "1", SystemPrompt: "cinematic"}, ReferenceImages: []Media{{DataURL: tinyPNG}}}
	prepared, err := prepareMidjourneyImagineInput(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Prompt != "cinematic\n\na red cube --stylize 100 --ar 16:9" {
		t.Fatalf("prompt = %q", prepared.Prompt)
	}
	if prepared.Config.SystemPrompt != "" || len(prepared.ReferenceImages) != 1 || !strings.HasPrefix(prepared.ReferenceImages[0].DataURL, "data:image/png;base64,") {
		t.Fatalf("prepared = %#v", prepared)
	}
	input.Prompt = "a red cube --ar 3:2 --seed 10"
	prepared, err = prepareMidjourneyImagineInput(context.Background(), input)
	if err != nil || strings.Contains(prepared.Prompt, "--ar 16:9") {
		t.Fatalf("manual --ar was overwritten: prompt=%q, err=%v", prepared.Prompt, err)
	}
}

func TestPrepareMidjourneyImagineInputRejectsInvalidInput(t *testing.T) {
	base := Input{Mode: "image", Prompt: "scene", Config: Config{InterfaceType: "openai-image", Model: "mj_imagine", Size: "1:1"}}
	for name, mutate := range map[string]func(*Input){
		"model":     func(input *Input) { input.Config.Model = "other" },
		"count":     func(input *Input) { input.Config.Count = "4" },
		"quality":   func(input *Input) { input.Config.Quality = "high" },
		"mask":      func(input *Input) { input.Mask = &Media{DataURL: tinyPNG} },
		"ratio":     func(input *Input) { input.Config.Size = "wide" },
		"reference": func(input *Input) { input.ReferenceImages = []Media{{DataURL: "data:text/plain;base64,dGV4dA=="}} },
	} {
		t.Run(name, func(t *testing.T) {
			input := base
			mutate(&input)
			if _, err := prepareMidjourneyImagineInput(context.Background(), input); err == nil {
				t.Fatal("invalid request accepted")
			}
		})
	}
}

func TestPrepareMidjourneyImagineInputReadsURLReference(t *testing.T) {
	data, _, err := MediaBytes(Media{DataURL: tinyPNG})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "image/png")
		writer.Write(data)
	}))
	defer server.Close()
	input := Input{Mode: "image", Prompt: "scene", Config: Config{InterfaceType: "openai-image", Model: "mj_imagine", BaseURL: server.URL + "/v1"}, ReferenceImages: []Media{{URL: server.URL + "/reference.png"}}}
	prepared, err := prepareMidjourneyImagineInput(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.ReferenceImages[0].URL != "" || !strings.HasPrefix(prepared.ReferenceImages[0].DataURL, "data:image/png;base64,") {
		t.Fatalf("reference = %#v", prepared.ReferenceImages[0])
	}
}

func TestMidjourneyImagineImageTaskKeepsExistingEndpoint(t *testing.T) {
	var submitted []map[string]any
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/images/generations":
			if request.Method != http.MethodPost || request.Header.Get("Authorization") != "Bearer test-key" {
				t.Errorf("create method/auth = %s/%s", request.Method, request.Header.Get("Authorization"))
			}
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Errorf("decode request: %v", err)
			}
			submitted = append(submitted, body)
			writer.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(writer, `{"data":[{"url":%q}]}`, server.URL+"/grid.png")
		default:
			t.Errorf("unexpected request path %s", request.URL.Path)
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	input := Input{Mode: "image", Prompt: "film still", Config: Config{InterfaceType: "openai-image", Model: "mj_imagine", BaseURL: server.URL + "/v1", APIKey: "test-key", Size: "16:9", Count: "1"}, ReferenceImages: []Media{{DataURL: tinyPNG}}}
	result, err := RunImageTask(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if len(submitted) != 1 || len(submitted[0]) != 3 || submitted[0]["model"] != "mj_imagine" || submitted[0]["prompt"] != "film still --ar 16:9" {
		t.Fatalf("submitted body = %#v", submitted)
	}
	if refs, ok := submitted[0]["base64Array"].([]any); !ok || len(refs) != 1 || !strings.HasPrefix(refs[0].(string), "data:image/png;base64,") {
		t.Fatalf("submitted references = %#v", submitted[0]["base64Array"])
	}
	if result["mode"] != "image" {
		t.Fatalf("result = %#v", result)
	}
	images, ok := result["images"].([]map[string]string)
	if !ok || len(images) != 1 {
		t.Fatalf("result images = %#v", result["images"])
	}
	if images[0]["dataUrl"] != server.URL+"/grid.png" {
		t.Fatalf("result image = %#v", images[0])
	}
	input.ReferenceImages = nil
	if _, err := RunImageTask(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if len(submitted) != 2 || len(submitted[1]) != 2 || submitted[1]["model"] != "mj_imagine" || submitted[1]["prompt"] != "film still --ar 16:9" {
		t.Fatalf("text-only body = %#v", submitted)
	}
}
