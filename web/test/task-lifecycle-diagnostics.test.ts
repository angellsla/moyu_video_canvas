import { beforeEach, expect, test } from "bun:test";

import { getClientDiagnosticEvents, setDiagnosticUserScope } from "@/services/diagnostics/client-diagnostics";
import { recordTaskLifecycleTransition } from "@/services/diagnostics/task-lifecycle";

let scope = 0;
beforeEach(() => setDiagnosticUserScope(`task-lifecycle-${++scope}`));

test("task status transitions record start and terminal outcomes without duplicate polling events", () => {
    const task = { id: "task-1", projectId: "canvas-1", status: "queued" as const };
    recordTaskLifecycleTransition(task);
    recordTaskLifecycleTransition({ ...task, status: "running" }, "queued");
    recordTaskLifecycleTransition({ ...task, status: "running" }, "running");
    recordTaskLifecycleTransition({ ...task, status: "succeeded" }, "running");
    expect(getClientDiagnosticEvents().map(event => event.code)).toEqual(["task_queued", "task_started", "task_succeeded"]);
    expect(getClientDiagnosticEvents()[1]).toMatchObject({ category: "task", taskId: "task-1", canvasId: "canvas-1" });
});
