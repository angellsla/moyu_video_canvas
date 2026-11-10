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
	if prepared.Prompt != "cinematic\n\na red cube --ar 1920:1080 --stylize 100" {
		t.Fatalf("prompt = %q", prepared.Prompt)
	}
	if prepared.Config.SystemPrompt != "" || len(prepared.ReferenceImages) != 1 || !strings.HasPrefix(prepared.ReferenceImages[0].DataURL, "data:image/png;base64,") {
		t.Fatalf("prepared = %#v", prepared)
	}
	input.Prompt = "a red cube --ar 3:2 --seed 10"
	prepared, err = prepareMidjourneyImagineInput(context.Background(), input)
	if err != nil || !strings.Contains(prepared.Prompt, "--ar 3:2") || strings.Contains(prepared.Prompt, "--ar 1920:1080") {
		t.Fatalf("manual --ar was overwritten: prompt=%q, err=%v", prepared.Prompt, err)
	}
}

func TestNormalizeMidjourneyPromptMovesAndMergesOptions(t *testing.T) {
	stylize := 250
	got, err := normalizeMidjourneyPrompt("cinematic --s 250 wide frame --ar=16:9", "4:3", MidjourneyImagineParams{Stylize: &stylize, Raw: true, No: "text, watermark"})
	if err != nil {
		t.Fatal(err)
	}
	want := "cinematic wide frame --ar 16:9 --stylize 250 --style raw --no text, watermark"
	if got != want {
		t.Fatalf("normalized prompt = %q, want %q", got, want)
	}
	if got, err = normalizeMidjourneyPrompt("--v 7", "auto", MidjourneyImagineParams{}); err != nil || got != "--v 7" {
		t.Fatalf("parameter-only prompt = %q, err=%v", got, err)
	}
}

func TestNormalizeMidjourneyPromptRejectsAmbiguousOrInvalidParameters(t *testing.T) {
	stylize := 250
	for name, input := range map[string]struct {
		prompt string
		params MidjourneyImagineParams
	}{
		"conflicting ui and prompt values": {prompt: "scene --s 100", params: MidjourneyImagineParams{Stylize: &stylize}},
		"duplicate prompt values":          {prompt: "scene --v 6 --version 7"},
		"unsupported parameter":            {prompt: "scene --banana 2"},
		"conflicting output options":       {prompt: "scene --hd --sd"},
		"conflicting speed options":        {prompt: "scene --fast --turbo"},
		"invalid aspect ratio":             {prompt: "scene --ar portrait"},
		"invalid quality for version":      {prompt: "scene --v 8.2 --q 2"},
		"missing option value":             {prompt: "scene --seed"},
		"fractional seed":                  {prompt: "scene --seed 42.5"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := normalizeMidjourneyPrompt(input.prompt, "1:1", input.params); err == nil {
				t.Fatal("invalid prompt accepted")
			}
		})
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
		"unsupported version option": func(input *Input) {
			input.Config.MidjourneyParams = MidjourneyImagineParams{Version: "8.2", Quality: "2"}
		},
		"out of range stylize": func(input *Input) {
			value := 1001
			input.Config.MidjourneyParams = MidjourneyImagineParams{Stylize: &value}
		},
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

func TestMidjourneyImagineParameterSuffixSupportsImageOptions(t *testing.T) {
	stylize, chaos, weird, exp, seed := 250, 12, 45, 10, uint32(42)
	repeat, stop, sw, ow := 2, 80, 150, 300
	imageWeight := 1.2
	params := MidjourneyImagineParams{
		Version: "7", Stylize: &stylize, Chaos: &chaos, Weird: &weird, Exp: &exp, No: "text, watermark", Seed: &seed,
		Raw: true, Tile: true, Repeat: &repeat, Stop: &stop, Quality: "2", Draft: true, SpeedMode: "fast", ImageWeight: &imageWeight,
		Sref: "https://assets.example/style.png::2", Sw: &sw, Sv: "6", Oref: "https://assets.example/object.png", Ow: &ow,
	}
	got, err := midjourneyImagineParameterSuffix(params)
	if err != nil {
		t.Fatal(err)
	}
	want := "--v 7 --stylize 250 --chaos 12 --weird 45 --exp 10 --no text, watermark --seed 42 --style raw --tile --repeat 2 --stop 80 --q 2 --draft --fast --iw 1.2 --sref https://assets.example/style.png::2 --sw 150 --sv 6 --oref https://assets.example/object.png --ow 300"
	if got != want {
		t.Fatalf("parameter suffix = %q, want %q", got, want)
	}

	if got, err = midjourneyImagineParameterSuffix(MidjourneyImagineParams{Version: "8.2", SDHD: "hd"}); err != nil || got != "--v 8.2 --hd" {
		t.Fatalf("V8 output suffix = %q, err=%v", got, err)
	}
	if got, err = midjourneyImagineParameterSuffix(MidjourneyImagineParams{Niji: "7", Version: "6"}); err == nil {
		t.Fatalf("conflicting version suffix accepted: %q", got)
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
	stylize, imageWeight := 250, 1.2
	input := Input{Mode: "image", Prompt: "film still", Config: Config{InterfaceType: "openai-image", Model: "mj_imagine", BaseURL: server.URL + "/v1", APIKey: "test-key", Size: "16:9", Count: "1", MidjourneyParams: MidjourneyImagineParams{Version: "7", Stylize: &stylize, Raw: true, ImageWeight: &imageWeight}}, ReferenceImages: []Media{{DataURL: tinyPNG}}}
	result, err := RunImageTask(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if len(submitted) != 1 || len(submitted[0]) != 3 || submitted[0]["model"] != "mj_imagine" || submitted[0]["prompt"] != "film still --ar 16:9 --v 7 --stylize 250 --style raw --iw 1.2" {
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
	if len(submitted) != 2 || len(submitted[1]) != 2 || submitted[1]["model"] != "mj_imagine" || submitted[1]["prompt"] != "film still --ar 16:9 --v 7 --stylize 250 --style raw --iw 1.2" {
		t.Fatalf("text-only body = %#v", submitted)
	}
}
