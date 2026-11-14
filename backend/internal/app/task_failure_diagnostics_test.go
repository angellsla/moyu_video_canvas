package app

import (
	"errors"
	"strings"
	"testing"

	"infinite-canvas/backend/internal/generation"
	"infinite-canvas/backend/internal/model"
	localtask "infinite-canvas/backend/internal/task"
)

func TestTerminalVideoReceiptPersistsAndUnlocksChangedGeneration(t *testing.T) {
	for _, providerCode := range []string{"upstream_task_failed", ""} {
		task := &model.Task{ID: "terminal-video", Type: "canvas_video", Status: model.TaskStatusRunning, ProviderRequestID: "original"}
		repo := &taskTerminalRepositoryStub{task: task}
		c := newTaskTerminalCoordinatorForTest(repo, &taskTerminalReplayStub{}, &taskTerminalLoggerStub{}, &taskTerminalOutputStub{})
		cause := generation.NewRawPayloadError(`{"error":{"code":"` + providerCode + `","message":"OutputVideoSensitiveContentDetected.PolicyViolation: 生成结果未通过审核"}}`)
		err := generation.ProviderTaskTerminalError{TaskID: "original", Status: "failed", Cause: cause}
		if got := c.handleExecutionFailure(task, err, false, false); !errors.Is(got, err) {
			t.Fatal(got)
		}
		if task.FailureDiagnostics == nil || task.FailureDiagnostics.ProviderStatus != "failed" || task.FailureDiagnostics.ProviderTaskID != "original" {
			t.Fatalf("terminal receipt lost: %+v", task.FailureDiagnostics)
		}
		outcome := localtask.ComputeOutcome(*task, "", localtask.OutcomeFacts{})
		if outcome.Phase != "failed" || outcome.Regenerate != "confirm" {
			t.Fatalf("terminal task still blocked: %+v", outcome)
		}
	}
}

func TestStoredVideoModerationUsesDiagnosticEvidenceInBothViews(t *testing.T) {
	task := model.Task{ID: "old-video", Type: "canvas_video", Status: model.TaskStatusFailed, ProviderRequestID: "original", Error: "生成失败。请查看详情后再决定是否重试。", FailureDiagnostics: &model.TaskFailureDiagnostics{
		Source: "upstream_response", ProviderCode: "upstream_task_failed", ProviderTaskID: "original", Summary: "OutputVideoSensitiveContentDetected.PolicyViolation: 生成结果疑似涉及版权内容，未通过审核，本次未扣费。",
	}}
	before := task.Error
	for _, code := range []string{taskForOutput(task).ErrorCode, taskSummaryForOutput(task).ErrorCode} {
		if code != "moderation_output" {
			t.Fatalf("persisted error remains generic: %s", code)
		}
	}
	if !strings.Contains(taskForOutput(task).Error, "版权审核") || task.Error != before {
		t.Fatal("projection changed history or lost cause")
	}
	task.FailureDiagnostics.Source = "local_result"
	if taskForOutput(task).ErrorCode == "moderation_output" {
		t.Fatal("local message was treated as upstream evidence")
	}
}

func TestTaskFailureDiagnosticsPreserveLocalConstraint(t *testing.T) {
	task := &model.Task{ID: "task-local", Stage: "校验参考图"}
	captureTaskFailureDiagnostics(task, BadAuthRequest("当前图片模型最多支持 2 张参考图"), "unknown")
	d := taskSummaryForOutput(*task).FailureDiagnostics
	if d == nil || d.Source != "local_validation" || d.Summary != "当前图片模型最多支持 2 张参考图" || d.ProviderCode != "" || d.CapturedAt == "" {
		t.Fatalf("diagnostics = %+v", d)
	}
}

func TestTaskFailureDiagnosticsDiskFullStage(t *testing.T) {
	task := &model.Task{ID: "task_12345678", Stage: "处理中"}
	captureTaskFailureDiagnostics(task, errors.New("生成内容写入资源存储失败：write local resource: no space left on device"), "local_result")
	d := task.FailureDiagnostics
	if d == nil || d.Source != "local_result" || d.Stage != "保存生成结果（磁盘空间不足）" || !strings.Contains(d.Summary, "no space left on device") {
		t.Fatalf("lost storage stage: %#v", d)
	}
}

func TestTaskFailureDiagnosticsPreserveHTTPHeaderAndRedact(t *testing.T) {
	task := &model.Task{ID: "task-http", Stage: "正在生成"}
	err := providerHTTPError{StatusCode: 400, RequestID: "req-header-123", Body: `{"error":{"code":"invalid_size","param":"size","message":"size must be 1024x1024; Authorization: Bearer PRIVATE credential"}}`}
	captureTaskFailureDiagnostics(task, err, "unknown")
	d := taskForOutput(*task).FailureDiagnostics
	if d.Source != "upstream_http" || d.HTTPStatus != 400 || d.RequestID != "req-header-123" || d.ProviderCode != "invalid_size" || d.Param != "size" || !strings.Contains(d.Summary, "1024x1024") || strings.Contains(d.Summary, "PRIVATE") {
		t.Fatalf("diagnostics = %+v", d)
	}
	if taskSummaryForOutput(*task).FailureDiagnostics.Summary != d.Summary {
		t.Fatal("detail and summary disagree")
	}
}

func TestTaskTerminalPersistsDiagnosticBeforeReplacingError(t *testing.T) {
	task := &model.Task{ID: "task-terminal", Status: model.TaskStatusRunning, Stage: "素材下载"}
	repo := &taskTerminalRepositoryStub{task: task}
	c := newTaskTerminalCoordinatorForTest(repo, &taskTerminalReplayStub{}, &taskTerminalLoggerStub{}, &taskTerminalOutputStub{})
	err := errors.New("cannot save /Users/private-name/image.png")
	if got := c.handleExecutionFailure(task, err, true, false); !errors.Is(got, err) {
		t.Fatal(got)
	}
	if task.FailureDiagnostics == nil || task.FailureDiagnostics.Source != "local_result" || strings.Contains(task.FailureDiagnostics.Summary, "private-name") {
		t.Fatalf("diagnostics = %+v", task.FailureDiagnostics)
	}
}
