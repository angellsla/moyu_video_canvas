import { expect, test } from "bun:test";
import { apiClient } from "@/services/api/request";
import { encodeLanSharingUpdate } from "@/services/api/lan-sharing";
import { createCollaborativeCanvasDocument, deleteCollaborativeCanvasNode, encodeCollaborativeCanvasState, patchCollaborativeCanvasNode, toPortableSharedCanvasContent, type SharedCanvasContent } from "@/services/collaborative-canvas-document";
import { loadSharedCanvasHistory, previewSharedCanvasHistory, readSharedCanvasHistoryContent } from "@/services/lan-collaboration-history";
import type { CanvasProject } from "@/stores/canvas/use-canvas-store";

test("shared history replays paginated durable updates at a fixed boundary and preserves portable media", async () => {
    const initial: SharedCanvasContent = { title: "Room", nodes: [{ id: "image", type: "image", title: "Original", position: { x: 0, y: 0 }, width: 100, height: 100, metadata: { content: "blob:old", storageKey: "resource:old" } }], connections: [], directorScenes: [] };
    const doc = createCollaborativeCanvasDocument(toPortableSharedCanvasContent(initial, () => "image-hash"));
    const updates = [{ id: "u1", updateBase64: encodeLanSharingUpdate(encodeCollaborativeCanvasState(doc)), originNodeId: "owner", createdAt: "2026-11-10T00:00:00Z" }];
    // Replicated history can arrive on another device at one timestamp.
    doc.on("update", (update: Uint8Array) => { updates.push({ id: `u${updates.length + 1}`, updateBase64: encodeLanSharingUpdate(update), originNodeId: "member", createdAt: "2026-11-10T00:00:00Z" }); });
    patchCollaborativeCanvasNode(doc, "image", { title: "Edited by member" });
    deleteCollaborativeCanvasNode(doc, "image");
    patchCollaborativeCanvasNode(doc, "later", { title: "Arrived after opening history", type: "text" });
    const original = apiClient.request;
    const paths: string[] = [];
    const signal = new AbortController().signal;
    apiClient.request = (async (config) => {
        paths.push(config.url || "");
        expect(config.signal).toBe(signal);
        let data: unknown;
        if (config.url === "/lan-sharing/canvases") data = { canvases: [{ id: "room", updateCount: 3 }] };
        else if (config.url === "/lan-sharing/canvases/room/updates") {
            data = config.params?.after ? { updates: updates.slice(2), cursor: "u4", hasMore: false } : { updates: updates.slice(0, 2), cursor: "u2", hasMore: true };
        } else throw new Error(`Unexpected personal history endpoint: ${config.url}`);
        return { status: 200, headers: {}, data: { code: 0, data, msg: "ok" } };
    }) as typeof apiClient.request;
    try {
        const history = await loadSharedCanvasHistory("room", signal);
        expect(history.snapshots.map((entry) => entry.id)).toEqual(["u3", "u2", "u1"]);
        expect(history.currentRevision).toBe(3);
        expect(history.updates).toHaveLength(3);
        expect((await readSharedCanvasHistoryContent(history, "u1")).nodes[0]).toMatchObject({ title: "Original", metadata: { content: { contentHash: "image-hash" } } });
        expect((await readSharedCanvasHistoryContent(history, "u2")).nodes[0]).toMatchObject({ title: "Edited by member" });
        expect((await readSharedCanvasHistoryContent(history, "u3")).nodes).toEqual([]);
        const current = { ...initial, id: "local-room", sharedCanvasId: "room", sharedCanvasAssetCatalogId: "catalog", chatSessions: [{ id: "private-chat" }], activeChatId: "private-chat", viewport: { x: 10, y: 20, k: 2 } } as CanvasProject;
        const preview = await previewSharedCanvasHistory(current, history, "u1");
        expect(preview.project.nodes[0].metadata?.storageKey).toBeUndefined();
        expect(preview.content.nodes[0]).toMatchObject({ metadata: { storageKey: { contentHash: "image-hash" } } });
        expect(preview.project.chatSessions).toEqual([]);
        expect(current.chatSessions).toHaveLength(1);
        expect(paths.every((path) => path.startsWith("/lan-sharing/canvases"))).toBe(true);
        await expect(readSharedCanvasHistoryContent(history, "unknown")).rejects.toThrow();
        const aborted = new AbortController(); aborted.abort();
        await expect(readSharedCanvasHistoryContent(history, "u1", aborted.signal)).rejects.toThrow();
        // Reopening history reconstructs the same versions without any live room session.
        expect((await loadSharedCanvasHistory("room", signal)).snapshots).toEqual(history.snapshots);
    } finally { apiClient.request = original; doc.destroy(); }
});

test("shared history reports an unavailable room rather than falling back to personal ownership APIs", async () => {
    const original = apiClient.request;
    apiClient.request = (async (config) => {
        expect(config.url).toBe("/lan-sharing/canvases");
        return { status: 200, headers: {}, data: { code: 0, data: { canvases: [] }, msg: "ok" } };
    }) as typeof apiClient.request;
    try { await expect(loadSharedCanvasHistory("deleted-room")).rejects.toThrow("协作画布已删除"); }
    finally { apiClient.request = original; }
});
