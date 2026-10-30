package task

import (
	"context"
	"strings"

	"infinite-canvas/backend/internal/kernel"
	"infinite-canvas/backend/internal/model"
)

const (
	DrainRecoverMessage          = "服务正在重启，暂不执行结果取回"
	RecoverUnsupportedStatusText = "仅支持对失败或待入库任务执行结果取回"
	RecoverUnsupportedTypeText   = "该任务类型不支持免费结果取回"
)

// RecoverTask performs a free recovery pass (delivery resume, accepted image
// replay, or upstream video/upscale status query) after resetting any poll
// backoff. Structurally, it never references Store.Retry, Store.CreateWithLimit,
// Persistence.CreateAdmitted, or Service.Retry.
func (s *Service) RecoverTask(ctx context.Context, userID, id string) (*model.Task, error) {
	if s.store == nil {
		return nil, kernel.WrapAppError(kernel.CodeInternal, "任务服务不可用", errStoreRequired)
	}
	if s.deps.Runtime == nil || s.deps.Recovery == nil || s.deps.Present == nil {
		return nil, unavailable()
	}
	if s.deps.Runtime.IsDraining() {
		return nil, drainError(DrainRecoverMessage)
	}
	task, err := s.store.TaskForUser(userID, id)
	if mapped := mapTaskScopeError(err); mapped != nil {
		return nil, mapped
	}
	if mapped := mapTaskResourceError(err); mapped != nil {
		return nil, mapped
	}
	if err != nil {
		return nil, err
	}
	if RetiredAgentTask(task) {
		return nil, kernel.BadAuthRequest(RetiredAgentBoundaryMessage)
	}
	s.hydrateProviderRequestID(task)

	switch task.Status {
	case model.TaskStatusQueued, model.TaskStatusRunning:
		return presentTask(s.deps.Present, *task)
	case model.TaskStatusSucceeded:
		recovered, err := s.deps.Recovery.ResumeDelivery(ctx, userID, task.ID)
		if err != nil {
			return nil, err
		}
		if recovered == nil {
			return nil, unavailable()
		}
		return presentTask(s.deps.Present, *recovered)
	case model.TaskStatusFailed:
		if err := s.deps.Recovery.ResetBackoff(userID, task.ID); err != nil {
			return nil, err
		}
		var recovered *model.Task
		switch {
		case task.Type == "canvas_image":
			recovered, err = s.deps.Recovery.RecoverImage(ctx, userID, task.ID)
		case task.Type == "canvas_video" || strings.HasPrefix(task.Type, "video_"):
			recovered, err = s.deps.Recovery.RecoverVideoOrUpscale(ctx, userID, task.ID)
		default:
			return nil, kernel.BadAuthRequest(RecoverUnsupportedTypeText)
		}
		if err != nil {
			return nil, err
		}
		if recovered == nil {
			return nil, unavailable()
		}
		return presentTask(s.deps.Present, *recovered)
	default:
		return nil, kernel.BadAuthRequest(RecoverUnsupportedStatusText)
	}
}
