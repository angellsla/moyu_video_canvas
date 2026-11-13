package generation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestVideoPollingAndRecoveryRejectDifferentTask(t *testing.T) {
	t.Setenv("CANVAS_ALLOWED_PRIVATE_UPSTREAM_HOSTS", "127.0.0.1")
	for _, mode := range []string{"poll", "recover"} {
		for _, status := range []string{"processing", "completed"} {
			t.Run(mode+"/"+status, func(t *testing.T) {
				queries, otherRequests := 0, 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != http.MethodGet || r.URL.Path != "/v1/videos/original" {
						otherRequests++
						w.WriteHeader(http.StatusUnauthorized)
						return
					}
					queries++
					_ = json.NewEncoder(w).Encode(map[string]any{"id": "another-task", "status": status, "url": "/wrong-result"})
				}))
				defer server.Close()
				ctx := WithCallMeta(context.Background(), CallMeta{ProviderRequestID: "original"})
				input := Input{Mode: "video", Config: Config{BaseURL: server.URL, Model: "seedance-2.0"}}
				adapter := unifiedVideoAdapter(t, "sistinelove-seedance-video")
				var result map[string]interface{}
				var err error
				if mode == "poll" {
					policy := DefaultVideoPollPolicy()
					policy.Sleep = func(context.Context, time.Duration) error { return nil }
					result, err = RunProtocolAdapterTaskWithPolicy(ctx, input, adapter, policy)
				} else {
					result, _, err = QueryProtocolAdapterVideoTask(ctx, input, adapter, "original")
				}
				if err == nil || !strings.Contains(err.Error(), "不一致") || result != nil || queries != 1 || otherRequests != 0 {
					t.Fatalf("mismatched receipt accepted: result=%v err=%v polls=%d other=%d", result, err, queries, otherRequests)
				}
			})
		}
	}
}

func TestSistineloveVideoWaitsThenRetrievesMetadataResult(t *testing.T) {
	t.Setenv("CANVAS_ALLOWED_PRIVATE_UPSTREAM_HOSTS", "127.0.0.1")
	payload := unifiedVideoPayload(t)
	for _, recovery := range []bool{false, true} {
		t.Run(map[bool]string{false: "generate", true: "recover"}[recovery], func(t *testing.T) {
			creates, polls, downloads := 0, 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodPost && r.URL.Path == "/v1/videos":
					creates++
					_, _ = w.Write([]byte(`{"id":"original","status":"queued","progress":0}`))
				case r.Method == http.MethodGet && r.URL.Path == "/v1/videos/original":
					polls++
					if polls > 2 {
						w.WriteHeader(http.StatusUnauthorized)
						return
					}
					if !recovery && polls == 1 {
						_, _ = w.Write([]byte(`{"id":"original","status":"in_progress","metadata":{"generation_status":"running","progress":"45%"}}`))
						return
					}
					_, _ = w.Write([]byte(`{"id":"original","status":"in_progress","metadata":{"generation_status":"succeeded","progress":"100%","url":"/result.mp4"}}`))
				case r.Method == http.MethodGet && r.URL.Path == "/result.mp4":
					downloads++
					w.Header().Set("Content-Type", "video/mp4")
					_, _ = w.Write(payload)
				default:
					t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusBadRequest)
				}
			}))
			defer server.Close()
			input := Input{Mode: "video", Prompt: "fixture", Config: Config{BaseURL: server.URL, Model: "seedance-2.0"}}
			adapter := unifiedVideoAdapter(t, "sistinelove-seedance-video")
			var result map[string]interface{}
			var err error
			if recovery {
				result, _, err = QueryProtocolAdapterVideoTask(context.Background(), input, adapter, "original")
			} else {
				policy := DefaultVideoPollPolicy()
				policy.Sleep = func(context.Context, time.Duration) error { return nil }
				result, err = RunProtocolAdapterTaskWithPolicy(context.Background(), input, adapter, policy)
			}
			if err != nil || result == nil {
				t.Fatalf("metadata completion lost: result=%v err=%v", result, err)
			}
			unifiedVideoResult(t, result, payload)
			wantCreates, wantPolls := 1, 2
			if recovery {
				wantCreates, wantPolls = 0, 1
			}
			if creates != wantCreates || polls != wantPolls || downloads != 1 {
				t.Fatalf("creates=%d polls=%d downloads=%d", creates, polls, downloads)
			}
		})
	}
}
