import {
    decodeLanCollaborationCanvasUpdate,
    getLanCollaborationCanvases,
    getLanCollaborationCanvasUpdates,
    nextLanCollaborationUpdateCursor,
    type LanCollaborationCanvasUpdate,
} from "@/services/api/lan-collaboration";
import type { CanvasHistoryEntry } from "@/services/api/workspace-data";
import {
    applyCollaborativeCanvasUpdate,
    createCollaborativeCanvasDocument,
    readPortableSharedCanvasContent,
    restoreLocalSharedCanvasContent,
    type PortableSharedCanvasContent,
} from "@/services/collaborative-canvas-document";
import { createSharedCanvasLocalAssetResolver, getSharedCanvasLocalAssetBindings } from "@/services/lan-collaboration-assets";
import type { CanvasProject } from "@/stores/canvas/use-canvas-store";
import { assertUserScope, captureUserScope } from "@/lib/user-scope-guard";

export type SharedCanvasHistory = {
    canvasId: string;
    snapshots: CanvasHistoryEntry[];
    currentRevision: number;
    updates: LanCollaborationCanvasUpdate[];
};

// Freeze a local log boundary while the room continues receiving edits. Sample
// at most 100 checkpoints by update position, not arrival time: a new member
// can receive the entire history in one batch. Replay keeps every update.
export async function loadSharedCanvasHistory(canvasId: string, signal?: AbortSignal): Promise<SharedCanvasHistory> {
    const scope = captureUserScope();
    const { canvases } = await getLanCollaborationCanvases(signal);
    assertUserScope(scope);
    signal?.throwIfAborted();
    const canvas = canvases.find((item) => item.id === canvasId);
    if (!canvas) throw new Error("协作画布已删除或尚未同步到本机");
    const updates: LanCollaborationCanvasUpdate[] = [];
    const snapshots: CanvasHistoryEntry[] = [];
    const doc = createCollaborativeCanvasDocument();
    let cursor = "";
    const checkpointInterval = Math.max(1, Math.ceil(canvas.updateCount / 98));
    let pending: LanCollaborationCanvasUpdate | undefined;
    let lastContent = "";
    const checkpoint = () => {
        if (!pending) return;
        const content = readPortableSharedCanvasContent(doc);
        const serialized = JSON.stringify(content);
        if (serialized === lastContent) return;
        lastContent = serialized;
        snapshots.push({ id: pending.id, canvasId, revision: updates.length, title: content.title,
            nodeCount: content.nodes.length, connectionCount: content.connections.length,
            payloadBytes: new TextEncoder().encode(serialized).byteLength, reason: "automatic",
            createdAt: pending.createdAt, contentUpdatedAt: pending.createdAt });
        if (snapshots.length > 100) snapshots.shift();
    };
    try {
        while (updates.length < canvas.updateCount) {
            signal?.throwIfAborted();
            const page = await getLanCollaborationCanvasUpdates(canvasId, cursor, signal);
            assertUserScope(scope);
            signal?.throwIfAborted();
            cursor = nextLanCollaborationUpdateCursor(cursor, page);
            for (const update of page.updates) {
                if (updates.length >= canvas.updateCount) break;
                applyCollaborativeCanvasUpdate(doc, decodeLanCollaborationCanvasUpdate(update));
                updates.push(update);
                pending = update;
                if (updates.length === 1 || updates.length % checkpointInterval === 0) checkpoint();
            }
            if (!page.hasMore) break;
        }
        checkpoint();
        return { canvasId, snapshots: snapshots.reverse(), currentRevision: updates.length, updates };
    } finally { doc.destroy(); }
}

export async function readSharedCanvasHistoryContent(history: SharedCanvasHistory, snapshotId: string, signal?: AbortSignal): Promise<PortableSharedCanvasContent> {
    const snapshot = history.snapshots.find((entry) => entry.id === snapshotId);
    if (!snapshot) throw new Error("协作历史版本已失效，请刷新列表");
    const doc = createCollaborativeCanvasDocument();
    try {
        for (let index = 0; index < snapshot.revision; index++) {
            signal?.throwIfAborted();
            applyCollaborativeCanvasUpdate(doc, decodeLanCollaborationCanvasUpdate(history.updates[index]));
            if (index % 200 === 199) await new Promise((resolve) => setTimeout(resolve, 0));
        }
        signal?.throwIfAborted();
        return readPortableSharedCanvasContent(doc);
    } finally { doc.destroy(); }
}

export async function previewSharedCanvasHistory(project: CanvasProject, history: SharedCanvasHistory, snapshotId: string, signal?: AbortSignal) {
    const scope = captureUserScope();
    if (project.sharedCanvasId !== history.canvasId || !project.sharedCanvasAssetCatalogId) throw new Error("协作画布已切换，请重新打开历史版本");
    const content = await readSharedCanvasHistoryContent(history, snapshotId, signal);
    const bindings = await getSharedCanvasLocalAssetBindings(project.sharedCanvasAssetCatalogId);
    assertUserScope(scope);
    signal?.throwIfAborted();
    return { content, project: { ...project, ...restoreLocalSharedCanvasContent(content, createSharedCanvasLocalAssetResolver(bindings)),
        chatSessions: [], activeChatId: null, updatedAt: history.snapshots.find((entry) => entry.id === snapshotId)!.contentUpdatedAt } satisfies CanvasProject };
}
