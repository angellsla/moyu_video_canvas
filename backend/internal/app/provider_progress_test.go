package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"infinite-canvas/backend/internal/generation"
	"infinite-canvas/backend/internal/model"
	"infinite-canvas/backend/internal/protocol"
)

func TestProviderProgressFromResponse(t *testing.T) {
	tests := []struct {
		name string
		body string
		want int
		ok   bool
	}{
		{name: "direct number", body: `{"progress":35}`, want: 35, ok: true},
		{name: "live Seedance NT processing", body: `{"created_at":1700000000,"id":"original","model":"seedance-2.0-nt-720","object":"video","progress":50,"status":"in_progress","task_id":"original"}`, want: 50, ok: true},
		{name: "live Seedance NT completed", body: `{"completed_at":1700000408,"created_at":1700000000,"id":"original","metadata":{"url":"https://media.example/video.mp4"},"model":"seedance-2.0-nt-720","object":"video","progress":100,"status":"completed","task_id":"original"}`, want: 100, ok: true},
		{name: "nested percent string", body: `{"data":{"task":{"progress_percent":"68%"}}}`, want: 68, ok: true},
		{name: "fraction", body: `{"result":{"progress":0.42}}`, want: 42, ok: true},
		{name: "fractional explicit percent", body: `{"progress":"0.5%"}`, want: 1, ok: true},
		{name: "fractional percent field", body: `{"progress_percent":0.5}`, want: 1, ok: true},
		{name: "nested fractional percentage", body: `{"metadata":{"percentage":"0.42"}}`, want: 0, ok: true},
		{name: "one percent", body: `{"progress":1}`, want: 1, ok: true},
		{name: "zero percent", body: `{"progress":0}`, want: 0, ok: true},
		{name: "metadata percent", body: `{"metadata":{"progress":"85%"}}`, want: 85, ok: true},
		{name: "invalid number", body: `{"progress":"NaN"}`, ok: false},
		{name: "camel case", body: `{"operation":{"progressPercentage":87.6}}`, want: 88, ok: true},
		{name: "unrelated completion tokens", body: `{"usage":{"completion_tokens":120}}`, ok: false},
		{name: "invalid range", body: `{"progress":140}`, ok: false},
		{name: "not json", body: `not-json`, ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := providerProgressFromResponse([]byte(tt.body))
			if got != tt.want || ok != tt.ok {
				t.Fatalf("providerProgressFromResponse() = (%d, %v), want (%d, %v)", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestTaskUsesUpstreamReportedProgress(t *testing.T) {
	for _, taskType := range []string{"canvas_image", "canvas_video", "video_shot"} {
		if !taskUsesUpstreamReportedProgress(taskType) {
			t.Fatalf("task type %q should use upstream progress", taskType)
		}
	}
	for _, taskType := range []string{"canvas_text", "canvas_audio", "text_generation"} {
		if taskUsesUpstreamReportedProgress(taskType) {
			t.Fatalf("task type %q should keep internal progress", taskType)
		}
	}
}

func TestVideoPollReceiptPersistsProgressWithoutCompletingTask(t *testing.T) {
	allowLoopbackProviderTest(t)
	svc, db := openDBRuntimeMatrix(t, t.TempDir())
	defer closeDBRuntimeMatrix(t, svc, db)
	task := model.Task{ID: "local-video-progress", UserID: "local", Type: "canvas_video", Status: model.TaskStatusRunning, ProviderRequestID: "original", Stage: "正在查询上游任务"}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	body := `{"id":"original","status":"in_progress","data":{"progress":"42%"}}`
	status := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	ctx := withProviderAnalytics(context.Background(), svc, task)
	config := providerConfig{BaseURL: server.URL, Model: "seedance-2.0"}
	for _, tc := range []struct {
		kind, body   string
		status, want int
	}{
		{"poll", body, http.StatusOK, 42},
		{"poll", `{"id":"original","status":"in_progress","metadata":{"progressPercent":70}}`, http.StatusOK, 70},
		{"poll", `{"id":"original","progress":50}`, http.StatusOK, 70},
		{"download", `{"progress":95}`, http.StatusOK, 70},
		{"poll", `{"progress":96}`, http.StatusServiceUnavailable, 70},
		{"poll", `{"id":"original","status":"in_progress","progress":100}`, http.StatusOK, 100},
	} {
		body, status = tc.body, tc.status
		_, err := generation.ExecuteProtocolRequest(generation.WithRequestKind(ctx, tc.kind), config, protocol.RequestSpec{Method: http.MethodGet, Path: "/v1/videos/original"})
		if (err == nil) != (tc.status == http.StatusOK) {
			t.Fatalf("HTTP %d: %v", tc.status, err)
		}
		var saved model.Task
		if err := db.First(&saved, "id = ?", task.ID).Error; err != nil {
			t.Fatal(err)
		}
		if saved.Progress != tc.want || saved.Status != model.TaskStatusRunning || saved.ProviderRequestID != "original" {
			t.Fatalf("receipt corrupted task: progress=%d status=%s provider=%s", saved.Progress, saved.Status, saved.ProviderRequestID)
		}
	}
}
