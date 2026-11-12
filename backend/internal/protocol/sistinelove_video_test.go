package protocol

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func TestSistineloveVideoRequestMatchesDocumentedJSON(t *testing.T) {
	a := officialPackageAdapter(t, "sistinelove-seedance-video.beeftv-plugin", "sistinelove-seedance-video")
	spec, err := a.BuildCreate(context.Background(), RequestContext{Request: GenerationRequest{
		Model: "seedance-2.0-gz", Prompt: "人物奔跑", Duration: 10, AspectRatio: "16:9", Resolution: "720p",
		Images: []MediaReference{{URL: "https://media.example/b.png", Order: 2}, {URL: "https://media.example/a.png", Order: 1}},
		Videos: []MediaReference{{URL: "https://media.example/ref.mp4"}}, Audios: []MediaReference{{URL: "https://media.example/ref.mp3"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Path != "/v1/videos" || spec.Method != "POST" || spec.ContentType != "application/json" || len(spec.Files) != 0 {
		t.Fatalf("wrong create request: %+v", spec)
	}
	encoded, err := json.Marshal(spec.Body)
	if err != nil {
		t.Fatal(err)
	}
	var body, want map[string]any
	if err := json.Unmarshal(encoded, &body); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"model":"seedance-2.0-gz","prompt":"人物奔跑","duration":10,"aspect_ratio":"16:9","resolution":"720p","images":["https://media.example/a.png","https://media.example/b.png"],"videos":["https://media.example/ref.mp4"],"audios":["https://media.example/ref.mp3"],"generate_audio":false,"watermark":false,"metadata":{}}`), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(body, want) {
		t.Fatalf("request mismatch: %s", encoded)
	}
	poll, err := a.BuildPoll(context.Background(), PollContext{TaskID: "original"})
	if err != nil || poll.Path != "/v1/videos/original" {
		t.Fatalf("poll: %+v %v", poll, err)
	}
	result, err := a.(ResultAdapter).BuildResult(context.Background(), PollContext{TaskID: "original"})
	if err != nil || result.Path != "/v1/videos/original/content" {
		t.Fatalf("result: %+v %v", result, err)
	}
}
