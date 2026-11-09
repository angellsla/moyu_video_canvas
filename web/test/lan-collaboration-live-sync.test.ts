import { expect, mock, test } from "bun:test";
import * as React from "react";
import { localForageInstance, SHARED_CATALOG_STORE_NAME } from "@/lib/localforage-storage";
import { getActiveUserScope, scopedStorageKey } from "@/lib/user-scope";
import { applyCollaborativeCanvasUpdate, createCollaborativeCanvasDocument, encodeCollaborativeCanvasState, patchCollaborativeCanvasNode, readPortableSharedCanvasContent, toPortableSharedCanvasContent, type SharedCanvasContent } from "@/services/collaborative-canvas-document";
import type { CanvasProject } from "@/stores/canvas/use-canvas-store";

// Exercise the effect with real Yjs and persistence, while controlling the LAN
// transport and hook lifecycle. This file runs in its own test worker.
const effects: Array<() => void | (() => void)> = [];
mock.module("react", () => ({
    ...React,
    useEffect: (effect: () => void | (() => void)) => { effects.push(effect); },
    useMemo: (compute: () => unknown) => compute(),
    useRef: (current: unknown) => ({ current }),
    useState: (initial: unknown) => [initial, () => {}],
}));

let projects: CanvasProject[] = [];
const subscribers = new Set<(state: { projects: CanvasProject[] }, before: { projects: CanvasProject[] }) => void>();
function updateProject(id: string, patch: Partial<CanvasProject>) {
    const before = { projects };
    projects = projects.map((item) => item.id === id ? { ...item, ...patch } : item);
    for (const listener of subscribers) listener({ projects }, before);
}
mock.module("@/stores/canvas/use-canvas-store", () => ({ useCanvasStore: {
    getState: () => ({ projects, updateProject }),
    subscribe: (listener: typeof subscribers extends Set<infer T> ? T : never) => { subscribers.add(listener); return () => subscribers.delete(listener); },
} }));

const api = await import("@/services/api/lan-collaboration");
const sharing = await import("@/services/api/lan-sharing");
let publishFails = false;
const published: Uint8Array[] = [];
mock.module("@/services/api/lan-collaboration", () => ({
    ...api,
    createLanCollaborationStreamTicket: async () => ({ ticket: "test-ticket" }),
    getLanCollaborationCanvasStreamUrl: () => "ws://fixture/canvas",
    getLanCollaborationCanvasUpdates: async () => ({ updates: [], cursor: "", hasMore: false }),
    publishLanCollaborationCanvasUpdate: async (_id: string, update: Uint8Array) => {
        if (publishFails) throw new Error("connection interrupted");
        published.push(update);
        return { accepted: true };
    },
}));
mock.module("@/services/api/lan-sharing", () => ({
    ...sharing,
    getLanSharingStatus: async () => ({ nodeId: "local-peer", nickname: "Local" }),
    getLanSharingPeers: async () => ({ trusted: [], discovered: [] }),
}));

const assets = await import("@/services/lan-collaboration-assets");
let releaseDownload: (resources: Record<string, string>) => void = () => {};
let resources: Record<string, string> = {};
let download = new Promise<Record<string, string>>((resolve) => { releaseDownload = resolve; });
mock.module("@/services/lan-collaboration-assets", () => ({
    ...assets,
    getSharedCanvasLocalAssetBindings: async () => resources,
    prepareSharedCanvasLocalAssetValues: async () => download,
    queueSharedCanvasAssetRetries: async () => {},
    getSharedCanvasAssetReadiness: async () => ({ requiredCount: 1, local: { mappedCount: Object.keys(resources).length, missingHashes: [] }, peers: [] }),
    makePortableSharedCanvasContent: async (_id: string, content: SharedCanvasContent) => toPortableSharedCanvasContent(content, () => "hash-image"),
}));

class Socket {
    static OPEN = 1;
    static instances: Socket[] = [];
    readyState = 1;
    onopen?: () => void;
    onmessage?: (event: { data: string }) => void;
    onclose?: () => void;
    onerror?: () => void;
    sent: string[] = [];
    constructor() { Socket.instances.push(this); }
    send(value: string) { this.sent.push(value); }
    close() { this.readyState = 3; }
}

const { useLanCollaborativeCanvas } = await import("@/pages/canvas/use-lan-collaborative-canvas");
const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));
async function until(predicate: () => boolean) {
    for (let i = 0; i < 100 && !predicate(); i++) await sleep(10);
    expect(predicate()).toBe(true);
}

test("downloads do not block remote edits; continuous local edits and reconnect retain durable hashes", async () => {
    const originalWindow = globalThis.window;
    const originalSocket = globalThis.WebSocket;
    Object.defineProperty(globalThis, "window", { configurable: true, value: { setTimeout, clearTimeout, setInterval, clearInterval } });
    Object.defineProperty(globalThis, "WebSocket", { configurable: true, value: Socket });
    const roomId = `live-${crypto.randomUUID()}`;
    const content: SharedCanvasContent = { title: "Shared", nodes: [{ id: "image", title: "Image", type: "image", position: { x: 0, y: 0 }, width: 100, height: 100, metadata: { storageKey: "resource:original", content: "blob:original" } }], connections: [], directorScenes: [] };
    // Seed through the portable contract, never a device-specific resource URL.
    const remote = createCollaborativeCanvasDocument(toPortableSharedCanvasContent(content, () => "hash-image"));
    await localForageInstance(SHARED_CATALOG_STORE_NAME).setItem(scopedStorageKey(`lan-canvas:${roomId}:state`, getActiveUserScope()), encodeCollaborativeCanvasState(remote));
    projects = [{ ...content, id: "local-canvas", sharedCanvasId: roomId, sharedCanvasAssetCatalogId: "catalog", nodes: [] } as unknown as CanvasProject];
    let cleanup: (() => void) | undefined;
    try {
        useLanCollaborativeCanvas({ project: projects[0], projectLoaded: true, containerRef: { current: null }, viewport: { x: 0, y: 0, k: 1 }, selectedNodeIds: new Set() });
        cleanup = effects.pop()!() || undefined;
        await until(() => projects[0].nodes.length === 1 && Socket.instances.length === 1);
        expect(projects[0].nodes[0].metadata?.content).toBeUndefined();
        const socket = Socket.instances[0];
        socket.onopen?.();
        let remoteUpdate = new Uint8Array();
        remote.on("update", (update: Uint8Array) => { remoteUpdate = update; });
        patchCollaborativeCanvasNode(remote, "image", { title: "Remote edit before download finishes" });
        socket.onmessage?.({ data: JSON.stringify({ type: "update", originNodeId: "remote-peer", updateBase64: sharing.encodeLanSharingUpdate(remoteUpdate) }) });
        await until(() => projects[0].nodes[0].title === "Remote edit before download finishes");

        // An edit every 15 ms must publish while the drag is still ongoing.
        for (let x = 1; x <= 12; x++) {
            updateProject("local-canvas", { nodes: projects[0].nodes.map((node) => ({ ...node, position: { x, y: 0 } })) });
            await sleep(15);
            if (x === 8) expect(published.length).toBeGreaterThan(0);
        }
        await until(() => published.length > 1);
        await sleep(80);
        for (const update of published) applyCollaborativeCanvasUpdate(remote, update);
        expect(readPortableSharedCanvasContent(remote).nodes[0]).toMatchObject({ position: { x: 12 }, metadata: { content: { contentHash: "hash-image" } } });

        resources = { "hash-image": "downloaded" };
        releaseDownload(resources);
        await until(() => projects[0].nodes[0].metadata?.storageKey === "resource:downloaded");

        publishFails = true;
        const beforeFailure = published.length;
        updateProject("local-canvas", { nodes: projects[0].nodes.map((node) => ({ ...node, title: "Offline edit" })) });
        await sleep(100);
        expect(published.length).toBe(beforeFailure);
        const store = localForageInstance(SHARED_CATALOG_STORE_NAME);
        const outboxKey = scopedStorageKey(`lan-canvas:${roomId}:outbox`, getActiveUserScope());
        expect((await store.getItem<unknown[]>(outboxKey))!.length).toBeGreaterThan(0);
        publishFails = false;
        socket.onopen?.();
        await until(() => published.length > beforeFailure);
        await until(() => projects[0].nodes[0].title === "Offline edit");
        for (const update of published) applyCollaborativeCanvasUpdate(remote, update);
        expect(readPortableSharedCanvasContent(remote).nodes[0]).toMatchObject({ title: "Offline edit", metadata: { storageKey: { contentHash: "hash-image" } } });
        expect(socket.sent.some((frame) => JSON.parse(frame).type === "update")).toBe(false);
        await sleep(20);
        expect(await store.getItem(outboxKey)).toEqual([]);
    } finally {
        cleanup?.();
        remote.destroy();
        Object.defineProperty(globalThis, "window", { configurable: true, value: originalWindow });
        Object.defineProperty(globalThis, "WebSocket", { configurable: true, value: originalSocket });
    }
});
