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

func TestSistineloveVideoPollingStates(t *testing.T) {
	a := officialPackageAdapter(t, "sistinelove-seedance-video.beeftv-plugin", "sistinelove-seedance-video")
	for _, tc := range []struct {
		name, body   string
		status       Status
		url, message string
	}{
		{name: "queued", body: `{"id":"original","status":"queued","progress":0}`, status: StatusPending},
		{name: "processing at 100 percent", body: `{"id":"original","status":"in_progress","progress":100}`, status: StatusProcessing},
		{name: "live NT processing", body: `{"created_at":1700000000,"id":"original","model":"seedance-2.0-nt-720","object":"video","progress":50,"status":"in_progress","task_id":"original"}`, status: StatusProcessing},
		{name: "live NT completed", body: `{"completed_at":1700000408,"created_at":1700000000,"id":"original","metadata":{"url":"https://media.example/video.mp4"},"model":"seedance-2.0-nt-720","object":"video","progress":100,"status":"completed","task_id":"original"}`, status: StatusSucceeded, url: "https://media.example/video.mp4"},
		{name: "metadata completed", body: `{"id":"original","status":"in_progress","metadata":{"generation_status":"completed","url":"https://media.example/video.mp4"}}`, status: StatusSucceeded, url: "https://media.example/video.mp4"},
		{name: "nested metadata completed", body: `{"data":{"id":"original","status":"in_progress","metadata":{"generationStatus":"succeeded","url":"https://media.example/video.mp4"}}}`, status: StatusSucceeded, url: "https://media.example/video.mp4"},
		{name: "completed with content endpoint", body: `{"id":"original","status":"completed"}`, status: StatusSucceeded},
		{name: "nested failure", body: `{"data":{"id":"original","status":"failed","error":{"code":"rejected","message":"reference rejected"}}}`, status: StatusFailed, message: "reference rejected"},
		{name: "metadata failure", body: `{"id":"original","status":"in_progress","metadata":{"generation_status":"failed","error":{"code":"rejected","message":"reference rejected"}}}`, status: StatusFailed, message: "reference rejected"},
		{name: "cancelled", body: `{"data":{"id":"original","state":"cancelled"}}`, status: StatusCancelled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, err := a.ParsePoll(context.Background(), PollContext{TaskID: "original"}, []byte(tc.body))
			if err != nil || state.Status != tc.status || state.TaskID != "original" || state.Message != tc.message {
				t.Fatalf("state=%+v err=%v", state, err)
			}
			if tc.url != "" && (state.Result == nil || len(state.Result.Videos) != 1 || state.Result.Videos[0].URL != tc.url) {
				t.Fatalf("missing video: %+v", state.Result)
			}
		})
	}
}
