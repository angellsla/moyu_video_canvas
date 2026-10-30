package repository

import (
	"time"

	"infinite-canvas/backend/internal/model"
)

// FailedVideosForRecovery returns failed video and upscale tasks that still have
// an upstream task receipt (or uncertain submission stage) and have not yet
// converged to a local recovery outcome.
func (r *Repository) FailedVideosForRecovery(afterID string, limit int) ([]model.Task, error) {
	var tasks []model.Task
	err := r.db.Where("tasks.id > ? AND tasks.status = ?", afterID, model.TaskStatusFailed).
		Where("(tasks.type = ? OR tasks.type LIKE ? OR tasks.model IN ?)", "canvas_video", "video_%", upscaleModelKeys).
		Where("(recovery_outcome IS NULL OR recovery_outcome = '') AND (next_poll_at IS NULL OR next_poll_at <= ?)", time.Now()).
		Where(`provider_request_id <> '' OR stage = 'submission_unknown' OR EXISTS (SELECT 1 FROM api_call_logs l WHERE l.task_id = tasks.id AND l.user_id = tasks.user_id AND l.provider_request_id <> '') OR (SELECT a.dispatch_state FROM route_attempts a WHERE a.task_id = tasks.id AND a.route_run = tasks.route_run ORDER BY a.attempt_number DESC, a.id ASC LIMIT 1) = 'submission_unknown'`).
		Order("tasks.id ASC").Limit(limit).Find(&tasks).Error
	return tasks, err
}

// FinishFailedVideoRecovery records recovery_outcome and/or next_poll_at for a
// leased failed video/upscale task without changing status or route_run.
func (r *Repository) FinishFailedVideoRecovery(task model.Task, outcome *string, nextPollAt *time.Time) error {
	updates := map[string]any{
		"recovery_outcome": outcome,
		"next_poll_at":     nextPollAt,
		"updated_at":       time.Now(),
	}
	if task.ProviderRequestID != "" {
		updates["provider_request_id"] = task.ProviderRequestID
	}
	result := taskLeaseWriter(r.db.Model(&model.Task{}), task.LeaseOwner).
		Where("id = ? AND user_id = ? AND status = ? AND route_run = ?", task.ID, task.UserID, model.TaskStatusFailed, task.RouteRun).
		Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrTaskStateConflict
	}
	return nil
}

// ResetTaskRecoveryBackoff clears next_poll_at on a failed task so an explicit
// POST /api/tasks/:id/recover can immediately re-run free recovery.
func (r *Repository) ResetTaskRecoveryBackoff(userID, taskID string) error {
	return r.db.Model(&model.Task{}).
		Where("id = ? AND user_id = ? AND status = ?", taskID, userID, model.TaskStatusFailed).
		Updates(map[string]any{"next_poll_at": nil, "updated_at": time.Now()}).Error
}
