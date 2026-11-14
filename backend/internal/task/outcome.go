package task

import (
	"strings"

	"infinite-canvas/backend/internal/model"
)

// Outcome 是只读投影，不持有状态，也不授权任何提交。
type Outcome = model.TaskOutcome

const (
	RecoveryNoResult      = "no_result"
	RecoveryUnrecoverable = "unrecoverable"
)

// OutcomeFacts 来自持久化证据。读失败必须显式标记，不能当成没有证据。
// RequestNotSent 由写入任务阶段的一方给出，投影不读界面显示的阶段文字。
type OutcomeFacts struct {
	Unreadable            bool
	DispatchState         string
	HasImageReceipt       bool
	ImageResponseAccepted bool
	SafeImageReplay       bool
	RequestNotSent        bool
}

func ComputeOutcome(task model.Task, resultState string, facts OutcomeFacts) Outcome {
	out := func(phase, regenerate, reason string) Outcome {
		return Outcome{Phase: phase, Regenerate: regenerate, Reason: reason}
	}
	if task.Status == model.TaskStatusQueued || task.Status == model.TaskStatusRunning {
		return out("running", "blocked", "task_running")
	}
	if task.Status == model.TaskStatusSucceeded {
		if resultState == ResultStateReady {
			return out("ready", "allowed", "result_ready")
		}
		if resultState == ResultStateFailedPermanent {
			return out("failed", "confirm", "result_unrecoverable")
		}
		if resultState == ResultStatePendingMaterialization || resultState == ResultStateMaterializing {
			return out("fetching", "blocked", "result_fetching")
		}
		return out("retrievable", "blocked", "delivery_pending")
	}
	if facts.Unreadable {
		return out("retrievable", "blocked", "evidence_unavailable")
	}
	regenerate := "confirm"
	if task.Type == model.TaskTypeDepthCapture || provablyNotSubmitted(task, facts) {
		regenerate = "allowed"
	}
	// 只读结构化上游错误码；不从 Error 文案重新推断审核或计费。
	// 审核失败不代表没扣钱，改提示词后可以重新生成，但要先确认。
	if d := task.FailureDiagnostics; d != nil && isInputRejectedCode(d.ProviderCode) {
		return out("failed", "confirm", "input_rejected")
	}
	if ProviderReportedNoResult(task) {
		return out("failed", regenerate, "no_result")
	}
	if task.RecoveryOutcome != nil {
		switch *task.RecoveryOutcome {
		case RecoveryUnrecoverable:
			return out("failed", "confirm", "result_unrecoverable")
		case RecoveryNoResult:
			return out("failed", regenerate, "no_result")
		}
	}
	if task.Type == "canvas_image" && (facts.HasImageReceipt || facts.ImageResponseAccepted || facts.SafeImageReplay || facts.DispatchState == "accepted" || facts.DispatchState == "submission_unknown" || task.Stage == "submission_unknown") {
		return out("retrievable", "blocked", "image_recovery_pending")
	}
	if task.ProviderRequestID != "" || ((task.Type == "canvas_video" || strings.HasPrefix(task.Type, "video_")) && (facts.DispatchState == "submission_unknown" || task.Stage == "submission_unknown")) {
		return out("retrievable", "blocked", "provider_result_pending")
	}
	return out("failed", regenerate, "task_failed")
}

// provablyNotSubmitted 只在持久证据能证明上游没有接到请求时成立。
func provablyNotSubmitted(task model.Task, facts OutcomeFacts) bool {
	if facts.ImageResponseAccepted || task.ProviderRequestID != "" {
		return false
	}
	switch facts.DispatchState {
	case "not_sent", "rejected_no_job":
		return true
	case "accepted", "submission_unknown":
		return false
	}
	return facts.RequestNotSent
}

func isInputRejectedCode(code string) bool {
	return code == "content_policy_violation" || code == "content_moderation" || strings.HasPrefix(code, "SAFETY.INPUT.")
}

// Use persisted receipt evidence, not a task ID alone or error prose. The
// gateway code also covers older records saved before providerStatus existed.
func ProviderReportedNoResult(task model.Task) bool {
	d := task.FailureDiagnostics
	if task.Status != model.TaskStatusFailed || d == nil || d.Source != "upstream_response" {
		return false
	}
	if d.ProviderTaskID != "" && task.ProviderRequestID != "" && d.ProviderTaskID != task.ProviderRequestID {
		return false
	}
	return d.ProviderStatus == "failed" || d.ProviderStatus == "cancelled" || d.ProviderCode == "upstream_task_failed"
}
