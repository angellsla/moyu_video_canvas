import type { TaskStatus } from "@/services/api/task-center";
import { getDiagnosticTraceId, recordDiagnosticEvent } from "./client-diagnostics";

export type TaskLifecycleState = {
    id: string;
    status: TaskStatus;
    projectId?: string;
    clientContext?: { nodeId?: string };
    traceId?: string;
};

export function recordTaskLifecycleTransition(task: TaskLifecycleState, previousStatus?: TaskStatus) {
    if (task.status === previousStatus) return;
    const terminal = task.status === "succeeded" || task.status === "failed" || task.status === "cancelled";
    try {
        recordDiagnosticEvent({
            level: task.status === "failed" ? "error" : "info",
            category: "task",
            code: terminal ? `task_${task.status}` : task.status === "running" ? "task_started" : "task_queued",
            message: terminal ? `任务${task.status === "succeeded" ? "完成" : task.status === "failed" ? "失败" : "已取消"}` : task.status === "running" ? "任务开始执行" : "任务进入队列",
            taskId: task.id,
            projectId: task.projectId,
            canvasId: task.projectId,
            nodeId: task.clientContext?.nodeId,
            traceId: task.traceId || getDiagnosticTraceId(task.id),
        });
    } catch {
        // Local diagnostics must not alter task polling or result delivery.
    }
}
