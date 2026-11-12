import { expect, mock, test } from "bun:test";
import * as React from "react";
import { localForageInstance, SHARED_CATALOG_STORE_NAME } from "@/lib/localforage-storage";
import { getActiveUserScope, scopedStorageKey } from "@/lib/user-scope";
import { applyCollaborativeCanvasUpdate, collectSharedCanvasAssetReferences, createCollaborativeCanvasDocument, deleteCollaborativeCanvasNode, encodeCollaborativeCanvasState, patchCollaborativeCanvasNode, readPortableSharedCanvasContent, toPortableSharedCanvasContent, writeSharedCanvasContent, type SharedCanvasContent } from "@/services/collaborative-canvas-document";
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
let prepareUpload = async (_id: string, _references: ReturnType<typeof collectSharedCanvasAssetReferences>, cache: Map<string, string>, _onPrepared?: (hash: string, resourceId: string) => void) => {
    cache.set("resource:original", "hash-image");
    return cache;
};
let prepareDownload = async (_id: string, _hashes: string[], _progress?: (resources: Record<string, string>) => void) => download;
let queryReadiness = async (): Promise<assets.SharedCanvasAssetReadiness> => ({ requiredCount: 1, local: { mappedCount: Object.keys(resources).length, missingHashes: [] }, peers: [] });
mock.module("@/services/lan-collaboration-assets", () => ({
    ...assets,
    getSharedCanvasLocalAssetBindings: async () => resources,
    prepareSharedCanvasLocalAssetValues: (id: string, hashes: string[], progress?: (resources: Record<string, string>) => void) => prepareDownload(id, hashes, progress),
    queueSharedCanvasAssetRetries: async () => {},
    getSharedCanvasAssetReadiness: () => queryReadiness(),
    prepareSharedCanvasAssetHashes: (id: string, references: ReturnType<typeof collectSharedCanvasAssetReferences>, cache: Map<string, string>, onPrepared?: (hash: string, resourceId: string) => void) => prepareUpload(id, references, cache, onPrepared),
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

test("outgoing preparation stalls and failures never block bidirectional edits or lose pending media", async () => {
    const originalWindow = globalThis.window;
    const originalSocket = globalThis.WebSocket;
    const intervals = new Map<number, () => void>();
    Object.defineProperty(globalThis, "window", { configurable: true, value: { setTimeout, clearTimeout, clearInterval,
        setInterval: (callback: () => void, ms: number) => { intervals.set(ms, callback); return setInterval(callback, ms); } } });
    Object.defineProperty(globalThis, "WebSocket", { configurable: true, value: Socket });
    Socket.instances = []; published.length = 0; publishFails = false; resources = {};
    download = new Promise(() => {});
    const roomId = `outgoing-${crypto.randomUUID()}`;
    const content: SharedCanvasContent = { title: "Shared", nodes: [], connections: [], directorScenes: [] };
    const remote = createCollaborativeCanvasDocument(content);
    await localForageInstance(SHARED_CATALOG_STORE_NAME).setItem(scopedStorageKey(`lan-canvas:${roomId}:state`, getActiveUserScope()), encodeCollaborativeCanvasState(remote));
    projects = [{ ...content, id: "outgoing", sharedCanvasId: roomId, sharedCanvasAssetCatalogId: "catalog" } as CanvasProject];
    let rejectUpload: (error: Error) => void = () => {};
    let preparing = 0;
    let finishPreparation: () => void = () => {};
    prepareUpload = async () => { preparing++; return new Promise((_resolve, reject) => { rejectUpload = reject; }); };
    let cleanup: (() => void) | undefined;
    try {
        useLanCollaborativeCanvas({ project: projects[0], projectLoaded: true, containerRef: { current: null }, viewport: { x: 0, y: 0, k: 1 }, selectedNodeIds: new Set() });
        cleanup = effects.pop()!() || undefined;
        await until(() => Socket.instances.length === 1);
        const socket = Socket.instances[0]; socket.onopen?.();
        const image = { id: "upload", type: "image", title: "Uploading", position: { x: 0, y: 0 }, width: 100, height: 100, metadata: { fileUpload: "uploading" } } as SharedCanvasContent["nodes"][number];
        updateProject("outgoing", { nodes: [image] });
        await until(() => published.length > 0);
        for (const update of published) applyCollaborativeCanvasUpdate(remote, update);
        expect(JSON.stringify(readPortableSharedCanvasContent(remote))).not.toContain("fileUpload");
        expect(projects[0].nodes[0].metadata?.fileUpload).toBe("uploading");
        updateProject("outgoing", { nodes: [{ ...image, metadata: { content: "blob:pending", storageKey: "resource:pending" } }] });
        await until(() => preparing === 1);
        const before = published.length;
        updateProject("outgoing", { nodes: projects[0].nodes.map((node) => ({ ...node, position: { x: 123, y: 0 } })) });
        await until(() => published.length > before);
        for (const update of published) applyCollaborativeCanvasUpdate(remote, update);
        expect(readPortableSharedCanvasContent(remote).nodes[0]).toMatchObject({ position: { x: 123 } });
        let remoteUpdate = new Uint8Array();
        remote.on("update", (update: Uint8Array) => { remoteUpdate = update; });
        patchCollaborativeCanvasNode(remote, "upload", { title: "Remote during preparation" });
        socket.onmessage?.({ data: JSON.stringify({ type: "update", originNodeId: "peer", updateBase64: sharing.encodeLanSharingUpdate(remoteUpdate) }) });
        await until(() => projects[0].nodes[0].title === "Remote during preparation");
        expect(projects[0].nodes[0].metadata?.content).toBe("blob:pending");
        rejectUpload(new Error("disk temporarily unavailable"));
        await sleep(80);
        patchCollaborativeCanvasNode(remote, "upload", { title: "Remote after failure" });
        socket.onmessage?.({ data: JSON.stringify({ type: "update", originNodeId: "peer", updateBase64: sharing.encodeLanSharingUpdate(remoteUpdate) }) });
        await until(() => projects[0].nodes[0].title === "Remote after failure");
        expect(projects[0].nodes[0].metadata?.storageKey).toBe("resource:pending");
        prepareUpload = async (_id, _references, cache, onPrepared) => {
            cache.set("resource:pending", "hash-upload");
            resources = { "hash-upload": "pending" };
            onPrepared?.("hash-upload", "pending");
            await new Promise<void>((resolve) => { finishPreparation = resolve; });
            return cache;
        };
        // Release any preparation started by an intervening graph edit, then retry.
        rejectUpload(new Error("retry")); await sleep(20);
        const originalNow = Date.now;
        Date.now = () => originalNow() + 6_000;
        try {
            intervals.get(5_000)?.();
            await until(() => projects[0].nodes[0].metadata?.content?.includes("/resources/pending/file") === true);
        } finally { Date.now = originalNow; }
        for (const update of published) applyCollaborativeCanvasUpdate(remote, update);
        expect(readPortableSharedCanvasContent(remote).nodes[0]).toMatchObject({ title: "Remote after failure", position: { x: 123 }, metadata: { content: { contentHash: "hash-upload" }, storageKey: { contentHash: "hash-upload" } } });
        expect(projects[0].nodes[0].metadata?.fileUpload).toBeUndefined();
        // The completed file is visible while the rest of preparation is pending.
        finishPreparation();
        await sleep(20);
    } finally {
        finishPreparation();
        cleanup?.(); remote.destroy();
        Object.defineProperty(globalThis, "window", { configurable: true, value: originalWindow });
        Object.defineProperty(globalThis, "WebSocket", { configurable: true, value: originalSocket });
    }
});

test("history restoration uses the live room outbox, revives nodes and preserves device-local state", async () => {
    const originalWindow = globalThis.window;
    const originalSocket = globalThis.WebSocket;
    Object.defineProperty(globalThis, "window", { configurable: true, value: { setTimeout, clearTimeout, setInterval, clearInterval } });
    Object.defineProperty(globalThis, "WebSocket", { configurable: true, value: Socket });
    Socket.instances = []; published.length = 0; publishFails = false; resources = {};
    download = new Promise(() => {});
    const roomId = `history-${crypto.randomUUID()}`;
    const content: SharedCanvasContent = { title: "Current name", nodes: [{ id: "deleted", type: "text", title: "Historic node", position: { x: 0, y: 0 }, width: 100, height: 100 }], connections: [], directorScenes: [] };
    const remote = createCollaborativeCanvasDocument(content);
    const historic = { ...readPortableSharedCanvasContent(remote), title: "Old room name" };
    deleteCollaborativeCanvasNode(remote, "deleted");
    await localForageInstance(SHARED_CATALOG_STORE_NAME).setItem(scopedStorageKey(`lan-canvas:${roomId}:state`, getActiveUserScope()), encodeCollaborativeCanvasState(remote));
    projects = [{ ...content, nodes: [], id: "history-local", sharedCanvasId: roomId, sharedCanvasAssetCatalogId: "catalog", chatSessions: [{ id: "private" }], activeChatId: "private", viewport: { x: 10, y: 20, k: 2 } } as CanvasProject];
    let cleanup: (() => void) | undefined;
    try {
        const controller = useLanCollaborativeCanvas({ project: projects[0], projectLoaded: true, containerRef: { current: null }, viewport: { x: 0, y: 0, k: 1 }, selectedNodeIds: new Set() });
        cleanup = effects.pop()!() || undefined;
        await until(() => Socket.instances.length === 1);
        const socket = Socket.instances[0]; socket.onopen?.();
        publishFails = true;
        await expect(controller.restoreVersion(historic)).rejects.toThrow("历史内容已恢复到本机");
        expect(projects[0].nodes[0].title).toBe("Historic node");
        expect(projects[0].title).toBe("Current name");
        expect(projects[0].chatSessions).toEqual([{ id: "private" }]);
        expect(projects[0].activeChatId).toBe("private");
        expect(projects[0].viewport).toEqual({ x: 10, y: 20, k: 2 });
        const outbox = scopedStorageKey(`lan-canvas:${roomId}:outbox`, getActiveUserScope());
        expect((await localForageInstance(SHARED_CATALOG_STORE_NAME).getItem<unknown[]>(outbox))!.length).toBeGreaterThan(0);
        publishFails = false; socket.onopen?.();
        await until(() => published.length > 0);
        for (const update of published) applyCollaborativeCanvasUpdate(remote, update);
        expect(readPortableSharedCanvasContent(remote)).toMatchObject({ title: "Current name", nodes: [{ id: "deleted", title: "Historic node" }] });
        updateProject("history-local", { nodes: projects[0].nodes.map((node) => ({ ...node, title: "Edit after restore" })) });
        await until(() => published.length > 1);
        for (const update of published) applyCollaborativeCanvasUpdate(remote, update);
        expect(readPortableSharedCanvasContent(remote).nodes[0]).toMatchObject({ title: "Edit after restore" });
    } finally {
        cleanup?.(); remote.destroy(); publishFails = false;
        Object.defineProperty(globalThis, "window", { configurable: true, value: originalWindow });
        Object.defineProperty(globalThis, "WebSocket", { configurable: true, value: originalSocket });
    }
});

test("adding another upload starts preparation before the previous file finishes", async () => {
    const originalWindow = globalThis.window;
    const originalSocket = globalThis.WebSocket;
    // No periodic polls: new uploads must be triggered by the edit itself.
    Object.defineProperty(globalThis, "window", { configurable: true, value: { setTimeout, clearTimeout, setInterval: () => 0, clearInterval: () => {} } });
    Object.defineProperty(globalThis, "WebSocket", { configurable: true, value: Socket });
    Socket.instances = []; published.length = 0; publishFails = false; resources = {};
    download = new Promise(() => {});
    const roomId = `immediate-upload-${crypto.randomUUID()}`;
    const content: SharedCanvasContent = { title: "Shared", nodes: [], connections: [], directorScenes: [] };
    const remote = createCollaborativeCanvasDocument(content);
    await localForageInstance(SHARED_CATALOG_STORE_NAME).setItem(scopedStorageKey(`lan-canvas:${roomId}:state`, getActiveUserScope()), encodeCollaborativeCanvasState(remote));
    projects = [{ ...content, id: roomId, sharedCanvasId: roomId, sharedCanvasAssetCatalogId: "catalog" } as CanvasProject];
    let release = () => {};
    const slow = new Promise<void>((resolve) => { release = resolve; });
    const preparations: string[] = [];
    prepareUpload = async (_id, refs, cache, onPrepared) => {
        const values = [...new Set(refs.map((ref) => ref.source?.value || ref.value))];
        preparations.push(...values);
        if (values.includes("resource:slow")) await slow;
        for (const value of values) {
            const name = value.slice("resource:".length);
            cache.set(value, `hash-${name}`);
            resources[`hash-${name}`] = name;
            onPrepared?.(`hash-${name}`, name);
        }
        return cache;
    };
    let cleanup: (() => void) | undefined;
    try {
        useLanCollaborativeCanvas({ project: projects[0], projectLoaded: true, containerRef: { current: null }, viewport: { x: 0, y: 0, k: 1 }, selectedNodeIds: new Set() });
        cleanup = effects.pop()!() || undefined;
        await until(() => Socket.instances.length === 1);
        Socket.instances[0].onopen?.();
        const node = (name: string): SharedCanvasContent["nodes"][number] => ({ id: name, type: "image", title: name, position: { x: 0, y: 0 }, width: 100, height: 100, metadata: { storageKey: `resource:${name}`, content: `blob:${name}` } });
        updateProject(roomId, { nodes: [node("slow")] });
        await until(() => preparations.includes("resource:slow"));
        updateProject(roomId, { nodes: [...projects[0].nodes, node("quick")] });
        await until(() => projects[0].nodes.find((item) => item.id === "quick")?.metadata?.content?.includes("/resources/quick/file") === true);
        for (const update of published) applyCollaborativeCanvasUpdate(remote, update);
        expect(readPortableSharedCanvasContent(remote).nodes.find((item) => item.id === "quick")).toMatchObject({ metadata: { storageKey: { contentHash: "hash-quick" } } });
        expect(preparations).toEqual(["resource:slow", "resource:quick"]);
        release();
        await until(() => projects[0].nodes[0].metadata?.content?.includes("/resources/slow/file") === true);
    } finally {
        release(); cleanup?.(); remote.destroy();
        Object.defineProperty(globalThis, "window", { configurable: true, value: originalWindow });
        Object.defineProperty(globalThis, "WebSocket", { configurable: true, value: originalSocket });
    }
});

test("late catalog notification wakes transfers without polling and new hashes do not wait for old downloads", async () => {
    const originalWindow = globalThis.window;
    const originalSocket = globalThis.WebSocket;
    Object.defineProperty(globalThis, "window", { configurable: true, value: { setTimeout, clearTimeout, setInterval: () => 0, clearInterval: () => {} } });
    Object.defineProperty(globalThis, "WebSocket", { configurable: true, value: Socket });
    Socket.instances = []; published.length = 0; publishFails = false; resources = {};
    const roomId = `immediate-download-${crypto.randomUUID()}`;
    const node = (name: string): SharedCanvasContent["nodes"][number] => ({ id: name, type: "image", title: name, position: { x: 0, y: 0 }, width: 100, height: 100, metadata: { storageKey: `resource:${name}`, content: `blob:${name}` } });
    const content: SharedCanvasContent = { title: "Shared", nodes: [node("slow")], connections: [], directorScenes: [] };
    const remote = createCollaborativeCanvasDocument(toPortableSharedCanvasContent(content, () => "hash-slow"));
    await localForageInstance(SHARED_CATALOG_STORE_NAME).setItem(scopedStorageKey(`lan-canvas:${roomId}:state`, getActiveUserScope()), encodeCollaborativeCanvasState(remote));
    projects = [{ ...content, nodes: [], id: roomId, sharedCanvasId: roomId, sharedCanvasAssetCatalogId: "catalog" } as CanvasProject];
    let releaseLookup = () => {};
    let releaseDownload = () => {};
    const lookup = new Promise<void>((resolve) => { releaseLookup = resolve; });
    const slow = new Promise<void>((resolve) => { releaseDownload = resolve; });
    const batches: string[][] = [];
    let statusQueries = 0;
    const originalQueryReadiness = queryReadiness;
    queryReadiness = async () => { statusQueries++; return new Promise(() => {}); };
    prepareDownload = async (_id, hashes) => {
        batches.push(hashes);
        if (batches.length === 1) { await lookup; return {}; }
        if (hashes.includes("hash-slow")) await slow;
        return Object.fromEntries(hashes.map((hash) => [hash, hash.slice(5)]));
    };
    let cleanup: (() => void) | undefined;
    try {
        useLanCollaborativeCanvas({ project: projects[0], projectLoaded: true, containerRef: { current: null }, viewport: { x: 0, y: 0, k: 1 }, selectedNodeIds: new Set() });
        cleanup = effects.pop()!() || undefined;
        await until(() => batches.length === 1 && Socket.instances.length === 1);
        expect(statusQueries).toBe(1);
        const socket = Socket.instances[0]; socket.onopen?.();
        socket.onmessage?.({ data: JSON.stringify({ type: "asset-catalog", canvasId: roomId, catalogId: "catalog" }) });
        // Notification arrived during an older empty lookup: it must be retained.
        releaseLookup();
        await until(() => batches.length === 2);
        let update = new Uint8Array();
        remote.on("update", (value: Uint8Array) => { update = value; });
        const portable = toPortableSharedCanvasContent({ ...content, nodes: [node("slow"), node("quick")] }, ({ localValue }) => localValue.includes("quick") ? "hash-quick" : "hash-slow");
        writeSharedCanvasContent(remote, portable);
        socket.onmessage?.({ data: JSON.stringify({ type: "update", originNodeId: "peer", updateBase64: sharing.encodeLanSharingUpdate(update) }) });
        await until(() => projects[0].nodes.find((item) => item.id === "quick")?.metadata?.storageKey === "resource:quick");
        expect(batches).toEqual([["hash-slow"], ["hash-slow"], ["hash-quick"]]);
        expect(projects[0].nodes[0].metadata?.storageKey).toBeUndefined();
        releaseDownload();
        await until(() => projects[0].nodes[0].metadata?.storageKey === "resource:slow");
        expect(projects[0].nodes[1].metadata?.storageKey).toBe("resource:quick");
    } finally {
        releaseLookup(); releaseDownload(); cleanup?.(); remote.destroy();
        prepareDownload = async () => download;
        queryReadiness = originalQueryReadiness;
        Object.defineProperty(globalThis, "window", { configurable: true, value: originalWindow });
        Object.defineProperty(globalThis, "WebSocket", { configurable: true, value: originalSocket });
    }
});
