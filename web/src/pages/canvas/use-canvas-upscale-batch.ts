import { useCallback, useEffect, useRef, type Dispatch, type SetStateAction } from "react";
import type { CanvasNodeData, CanvasConnection } from "@/types/canvas";
import type { CanvasUpscaleBatchItem } from "@/lib/canvas/canvas-upscale";
import { captureUserScope, assertUserScope, type CapturedUserScope } from "@/lib/user-scope-guard";
import { findGenerationTaskByOperation, queryGenerationTask, type GenerationTask } from "@/services/api/task-center";
import { flushCanvasStorePersistence, useCanvasStore } from "@/stores/canvas/use-canvas-store";

type Dependencies = {
    projectId: string; nodesRef: { current: CanvasNodeData[] }; connectionsRef: { current: CanvasConnection[] };
    setNodes: Dispatch<SetStateAction<CanvasNodeData[]>>;
    observeTask: (task: GenerationTask, nodeId: string, scope: CapturedUserScope) => void;
    bindTask?: (task: GenerationTask, nodeId: string) => void;
};

/** Reconcile old upscale batches only. The retired managed channel is never re-submitted. */
export function useCanvasUpscaleBatch({ projectId, nodesRef, connectionsRef, setNodes, observeTask, bindTask }: Dependencies) {
    const locks = useRef(new Set<string>());
    const mounted = useRef(true);
    const lifetime = useRef(0);
    useEffect(() => {
        mounted.current = true;
        return () => { mounted.current = false; lifetime.current += 1; };
    }, []);
    const liveProject = useRef(projectId); liveProject.current = projectId;
    const startUpscaleBatch = useCallback(async (processor: CanvasNodeData) => {
        if (locks.current.has(processor.id)) return;
        locks.current.add(processor.id);
        const scope = captureUserScope();
        const capturedLifetime = lifetime.current;
        const expectedBatchId = nodesRef.current.find(n => n.id === processor.id)?.metadata?.upscaleBatch?.id;
        const assertCurrent = () => {
            assertUserScope(scope);
            if (!mounted.current || lifetime.current !== capturedLifetime) throw new Error("画布已关闭，停止查询超分任务");
            if (liveProject.current !== projectId) throw new Error("画布已切换，停止查询超分任务");
            const live = nodesRef.current.find(n => n.id === processor.id);
            if (!live) throw new Error("超分节点已删除");
            if (live.metadata?.upscaleBatch?.id !== expectedBatchId) throw new Error("超分批次已更换，停止查询旧批次");
        };
        const patch = (transform: (nodes: CanvasNodeData[]) => CanvasNodeData[]) => {
            assertCurrent();
            nodesRef.current = transform(nodesRef.current);
            setNodes(nodesRef.current);
            useCanvasStore.getState().updateProject(projectId, { nodes: nodesRef.current, connections: connectionsRef.current });
        };
        const save = async () => { assertCurrent(); await flushCanvasStorePersistence(); assertCurrent(); };
        try {
            const processorNode = nodesRef.current.find(n => n.id === processor.id);
            const batch = processorNode?.metadata?.upscaleBatch;
            if (!batch) throw new Error("超分服务已下线，无法创建新批次");
            const update = (item: CanvasUpscaleBatchItem, values: Partial<CanvasUpscaleBatchItem>, outputValues?: Partial<NonNullable<CanvasNodeData["metadata"]>>) => {
                patch(nodes => nodes.map(node => {
                    if (node.id === processor.id && node.metadata?.upscaleBatch?.id === batch.id) {
                        return { ...node, metadata: { ...node.metadata, upscaleBatch: { ...node.metadata.upscaleBatch, items: node.metadata.upscaleBatch.items.map(candidate => candidate.outputNodeId === item.outputNodeId ? { ...candidate, ...values } : candidate) } } };
                    }
                    if (node.id === item.outputNodeId && outputValues) return { ...node, metadata: { ...node.metadata, ...outputValues } };
                    return node;
                }));
            };
            for (const item of batch.items) {
                assertCurrent();
                if (!nodesRef.current.some(node => node.id === item.outputNodeId)) continue;
                let task: GenerationTask | undefined;
                try {
                    task = item.taskId
                        ? await queryGenerationTask(item.taskId, { expectedScope: scope })
                        : await findGenerationTaskByOperation(item.clientOperationId, projectId);
                    assertCurrent();
                } catch (cause) {
                    assertCurrent();
                    update(item, { error: cause instanceof Error ? cause.message : "原任务查询失败" });
                    await save();
                    continue;
                }
                if (task) {
                    update(item, { status: "accepted", taskId: task.id, error: undefined }, { taskId: task.id, taskStatus: task.status, generationSubmissionState: "accepted" });
                    bindTask?.(task, item.outputNodeId);
                    observeTask(task, item.outputNodeId, scope);
                } else if (item.status === "waiting" || item.status === "submitting" || item.status === "unknown") {
                    // No original operation receipt exists; do not dispatch it again.
                    update(item, { status: "rejected", error: "超分服务已下线；未找到原任务，不会重新提交" }, { generationSubmissionState: "rejected", errorDetails: "超分服务已下线；未找到原任务，不会重新提交" });
                }
                await save();
            }
        } finally {
            locks.current.delete(processor.id);
        }
    }, [projectId, nodesRef, connectionsRef, setNodes, observeTask, bindTask]);
    return { startUpscaleBatch, resumeUpscaleBatch: startUpscaleBatch };
}
