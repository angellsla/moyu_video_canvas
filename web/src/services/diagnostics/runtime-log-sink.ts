import type { ClientDiagnosticEvent } from "./client-diagnostics";
import { apiClient } from "@/services/api/request";
import { isDesktopRuntimeConfigured } from "@/services/desktop-runtime";

const BATCH_SIZE = 100;
const QUEUE_LIMIT = 1000;

const queue: ClientDiagnosticEvent[] = [];
let timer: ReturnType<typeof setTimeout> | undefined;
let sending = false;

export function appendDesktopRuntimeLog(event: ClientDiagnosticEvent) {
    if (!isDesktopRuntimeConfigured()) return;
    queue.push(event);
    if (queue.length > QUEUE_LIMIT) queue.splice(0, queue.length - QUEUE_LIMIT);
    if (queue.length >= BATCH_SIZE) void flush();
    else if (!timer) timer = setTimeout(() => { timer = undefined; void flush(); }, 250);
}

async function flush() {
    if (sending || !queue.length || !isDesktopRuntimeConfigured()) return;
    sending = true;
    if (timer) clearTimeout(timer);
    timer = undefined;
    const batch = queue.splice(0, BATCH_SIZE);
    try {
        await apiClient.post("/runtime-logs/frontend", { entries: batch.map(toRuntimeLogEntry) }, { timeout: 5000 });
    } catch {
        queue.unshift(...batch);
        if (queue.length > QUEUE_LIMIT) queue.length = QUEUE_LIMIT;
        timer = setTimeout(() => { timer = undefined; void flush(); }, 5000);
    } finally {
        sending = false;
        if (queue.length && !timer) timer = setTimeout(() => { timer = undefined; void flush(); }, 0);
    }
}

function toRuntimeLogEntry(event: ClientDiagnosticEvent) {
    return {
        timestamp: event.timestamp,
        level: event.level,
        category: event.category,
        code: event.code,
        message: event.message,
        stack: event.stack,
        taskId: event.taskId,
        canvasId: event.canvasId,
        nodeId: event.nodeId,
        requestId: event.requestId,
        traceId: event.traceId,
        launchId: event.launchId,
        reason: event.reason,
        route: event.route,
        durationMs: event.durationMs,
        httpStatus: event.httpStatus,
    };
}
