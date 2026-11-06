import { beforeEach, expect, mock, test } from "bun:test";
import { readZip } from "@/lib/zip";

import type { CanvasProject } from "@/stores/canvas/use-canvas-store";

const media = new Map<string, Blob>();
const reads: string[] = [];
let savedArchive: Blob | undefined;
const drawingDocuments = new Map<string, unknown>();
const drawingPreviews = new Map<string, Blob>();
const drawingRenders = new Map<string, { blob: Blob; mimeType: string }>();
let portableContent: unknown = { title: "", nodes: [], connections: [], directorScenes: [] };
let localSharedResources: Record<string, string> = {};
const sharedPulls: Array<{ catalogId: string; hashes: string[] }> = [];
const queuedRetries: Array<{ catalogId: string; hashes: string[] }> = [];
let sharedPullError: Error | undefined;
let onSharedPull: (() => void) | undefined;

beforeEach(() => {
    media.clear();
    reads.length = 0;
    savedArchive = undefined;
    drawingDocuments.clear();
    drawingPreviews.clear();
    drawingRenders.clear();
    portableContent = { title: "", nodes: [], connections: [], directorScenes: [] };
    localSharedResources = {};
    sharedPulls.length = 0;
    queuedRetries.length = 0;
    sharedPullError = undefined;
    onSharedPull = undefined;
});

mock.module("@/lib/canvas/canvas-drawing-storage", () => ({
    loadCanvasDrawing: async (_projectId: string, drawingId: string) => drawingDocuments.get(drawingId) ?? null,
    loadCanvasDrawingPreview: async (_projectId: string, drawingId: string) => drawingPreviews.get(drawingId) ?? null,
    loadCanvasDrawingRender: async (_projectId: string, drawingId: string) => drawingRenders.get(drawingId) ?? null,
}));
mock.module("@/lib/canvas/canvas-export", () => ({
    awaitCanvasExportStep: <T,>(promise: Promise<T>) => promise,
    isArchiveStorageKey: (value: string) => /^(image|video|audio|file|resource|model|video-reference|audio-reference):/.test(value),
}));
mock.module("@/lib/user-scope-guard", () => {
    class UserScopeAbandonedError extends Error {}
    return {
        UserScopeAbandonedError,
        captureUserScope: () => ({ userScope: "test", epoch: 0 }),
        assertUserScope: () => {},
        userScopeMatches: () => true,
        isUserScopeAbandonedError: (error: unknown) => error instanceof UserScopeAbandonedError,
        userScopeRetryWait: { initial: 0, maximum: 0, factor: 1 },
        waitForRetryDelay: async () => {},
    };
});
mock.module("@/services/image-storage", () => ({ getImageBlob: async (key: string) => media.get(key) }));
mock.module("@/services/file-storage", () => ({ getMediaBlob: async (key: string) => { reads.push(key); return media.get(key); } }));
mock.module("@/services/api/resources", () => ({
    getResource: async () => { throw new Error("unexpected server read"); },
    ownedResourceIdFromMediaRef: (key?: string) => key?.startsWith("resource:") ? key.slice("resource:".length) : "",
    resourceIdFromStorageKey: (key?: string) => key?.startsWith("resource:") ? key.slice("resource:".length) : "",
    resourceStorageKey: (id: string) => `resource:${id}`,
}));
mock.module("@/services/api/lan-collaboration", () => ({
    decodeLanCollaborationCanvasUpdate: () => new Uint8Array(),
    getLanCollaborationCanvasUpdates: async () => ({ updates: [], cursor: "", hasMore: false }),
    nextLanCollaborationUpdateCursor: () => "",
}));
mock.module("@/services/lan-collaboration-assets", () => ({
    prepareSharedCanvasLocalAssetValues: async (catalogId: string, hashes: string[]) => {
        sharedPulls.push({ catalogId, hashes });
        onSharedPull?.();
        if (sharedPullError) throw sharedPullError;
        return localSharedResources;
    },
    queueSharedCanvasAssetRetries: async (catalogId: string, hashes: string[]) => { queuedRetries.push({ catalogId, hashes }); },
}));
mock.module("@/services/collaborative-canvas-document", () => ({
    applyCollaborativeCanvasUpdate: () => {},
    collectSharedCanvasAssetReferences: (content: unknown) => {
        const references: Array<{ field: string; value: string; path: string[] }> = [];
        const visit = (value: unknown, path: string[]) => {
            if (Array.isArray(value)) value.forEach((item, index) => visit(item, [...path, String(index)]));
            else if (value && typeof value === "object") for (const [field, item] of Object.entries(value)) {
                const nextPath = [...path, field];
                if (field === "resourceId" && typeof item === "string") references.push({ field, value: item, path: nextPath });
                else visit(item, nextPath);
            }
        };
        visit(content, []);
        return references;
    },
    createCollaborativeCanvasDocument: () => ({ destroy: () => {} }),
    readPortableSharedCanvasContent: () => portableContent,
}));
mock.module("@/services/desktop-media-save", () => ({
    saveOwnedNativeArchiveFiles: async () => undefined,
    saveOwnedOrBrowserBlob: async (_name: string, blob: Blob) => { savedArchive = blob; return "saved"; },
}));

const { collectCanvasAssetReferences, exportCanvasAssetFiles } = await import("@/lib/canvas/canvas-assets-export");

function project(): CanvasProject {
    return {
        id: "canvas-a",
        title: "Scene",
        nodes: [
            { id: "generated", type: "video", title: "Generated", metadata: { nodeRole: "result", resultOrigin: "generated", storageKey: "resource:generated-video", content: "blob:stale-generated" } },
            { id: "derived", type: "video", title: "Derived", metadata: { nodeRole: "result", resultOrigin: "derived", storageKey: "resource:derived-video", content: "blob:stale-derived" } },
            { id: "uploaded", type: "video", title: "Uploaded", metadata: { nodeRole: "result", resultOrigin: "upload", storageKey: "resource:uploaded-video", content: "blob:stale-uploaded" } },
            { id: "image", type: "image", title: "Image", metadata: { storageKey: "resource:image", content: "blob:stale-image", drawingPreviewStorageKey: "resource:drawing-preview", drawingPreviewUrl: "blob:stale-drawing-preview", prompt: "https://example.invalid/prompt-image.png" } },
            { id: "character", type: "text", title: "Character", metadata: { characterCoverUrl: "https://example.invalid/character.png" } },
        ],
        directorScenes: [{ id: "scene", objects: [{ url: "https://example.invalid/scene-object.png" }], shots: [{ screenshots: [{ url: "https://example.invalid/shot.png" }] }] }],
        timeline: { version: 2, tracks: [], clips: [{ id: "clip", kind: "video", nodeId: "", trackId: "", startMs: 0, durationMs: 1000, directMedia: { id: "direct", kind: "video", title: "Direct", storageKey: "resource:timeline-video", url: "blob:stale-timeline" } }], durationMs: 1000 },
        connections: [],
    } as unknown as CanvasProject;
}

test("asset export omits only generated and derived result videos when disabled", () => {
    const withoutResults = collectCanvasAssetReferences(project(), false).map((reference) => reference.value);
    expect(withoutResults).not.toContain("resource:generated-video");
    expect(withoutResults).not.toContain("resource:derived-video");
    expect(withoutResults).toContain("resource:uploaded-video");
    expect(withoutResults).toContain("resource:timeline-video");

    const withResults = collectCanvasAssetReferences(project(), true).map((reference) => reference.value);
    expect(withResults).toContain("resource:generated-video");
    expect(withResults).toContain("resource:derived-video");
});

test("asset export prefers durable keys over transient sibling URLs", () => {
    const references = collectCanvasAssetReferences(project(), true).map((reference) => reference.value);
    expect(references).toContain("resource:image");
    expect(references).toContain("resource:drawing-preview");
    expect(references).not.toContain("blob:stale-image");
    expect(references).not.toContain("blob:stale-drawing-preview");
    expect(references).not.toContain("blob:stale-timeline");
    expect(references).not.toContain("https://example.invalid/prompt-image.png");
});

test("asset export includes director-scene URLs and timeline direct media", () => {
    const references = collectCanvasAssetReferences(project(), false).map((reference) => reference.value);
    expect(references).toContain("https://example.invalid/scene-object.png");
    expect(references).toContain("https://example.invalid/shot.png");
    expect(references).toContain("resource:timeline-video");
    expect(references).toContain("https://example.invalid/character.png");
});

test("asset export resolves bare resourceId fields to local resource storage", async () => {
    media.set("resource:asset-1", new Blob(["asset bytes"], { type: "image/png" }));
    const canvas = {
        id: "canvas-resource-id",
        title: "Resource ID",
        nodes: [{ id: "asset", type: "image", title: "Asset", metadata: { resourceId: "asset-1" } }],
        connections: [],
    } as unknown as CanvasProject;

    await expect(exportCanvasAssetFiles(canvas, { includeResultVideos: true })).resolves.toBe("saved");
    expect(reads).toEqual(["resource:asset-1"]);
    expect(savedArchive).toBeInstanceOf(Blob);
    const archive = await readZip(savedArchive!);
    const manifest = JSON.parse(await archive.get("canvas-assets.json")!.text());
    expect(manifest.assets).toHaveLength(1);
    expect(await archive.get(manifest.assets[0].path)!.text()).toBe("asset bytes");
});

test("asset export includes drawing documents, previews, and rendered pages", async () => {
    drawingDocuments.set("drawing-1", { version: 2, engine: "excalidraw", snapshot: { elements: [] }, revision: 3 });
    drawingPreviews.set("drawing-1", new Blob(["preview bytes"], { type: "image/png" }));
    drawingRenders.set("drawing-1", { blob: new Blob(["render bytes"], { type: "image/png" }), mimeType: "image/png" });
    const canvas = {
        id: "canvas-drawing",
        title: "Drawing",
        nodes: [{ id: "drawing", type: "drawing", title: "Storyboard", metadata: { drawingId: "drawing-1" } }],
        connections: [],
    } as unknown as CanvasProject;

    await expect(exportCanvasAssetFiles(canvas, { includeResultVideos: false })).resolves.toBe("saved");
    const archive = await readZip(savedArchive!);
    const manifest = JSON.parse(await archive.get("canvas-assets.json")!.text());
    expect(manifest.assets.map((asset: { path: string }) => asset.path)).toHaveLength(3);
    const drawingPaths = [...archive.keys()].filter((path) => path.startsWith("drawings/"));
    const documentPath = drawingPaths.find((path) => path.endsWith(".json"))!;
    const previewPath = drawingPaths.find((path) => path.includes("-preview."))!;
    const renderPath = drawingPaths.find((path) => path.includes("-render."))!;
    expect(drawingPaths).toHaveLength(3);
    expect(JSON.parse(await archive.get(documentPath)!.text()).drawingId).toBe("drawing-1");
    expect(await archive.get(previewPath)!.text()).toBe("preview bytes");
    expect(await archive.get(renderPath)!.text()).toBe("render bytes");
});

async function sharedFixture(hash: string, options: { nodeType?: string; resultOrigin?: string; duplicate?: boolean } = {}) {
    const marker = { __beeftvSharedAsset: true, contentHash: hash, localField: "storageKey" };
    const node = (id: string) => ({ id, type: options.nodeType || "image", title: id, metadata: { nodeRole: "result", resultOrigin: options.resultOrigin || "upload", storageKey: marker } });
    portableContent = { title: "Shared", nodes: options.duplicate ? [node("asset"), node("asset-2")] : [node("asset")], connections: [], directorScenes: [] };
    return {
        id: "local-shared",
        title: "Shared",
        sharedCanvasId: "room-1",
        sharedCanvasAssetCatalogId: "catalog-1",
        nodes: [{ id: "asset", type: options.nodeType || "image", title: "Old snapshot", metadata: { storageKey: "resource:stale" } }],
        connections: [],
    } as unknown as CanvasProject;
}

async function digest(value: string) {
    const bytes = new TextEncoder().encode(value);
    return Array.from(new Uint8Array(await crypto.subtle.digest("SHA-256", bytes)), (byte) => byte.toString(16).padStart(2, "0")).join("");
}

test("shared export pulls hash-backed bytes and ignores a stale local resource ID", async () => {
    const hash = await digest("downloaded bytes");
    const canvas = await sharedFixture(hash, { duplicate: true });
    localSharedResources = { [hash]: "downloaded" };
    media.set("resource:downloaded", new Blob(["downloaded bytes"], { type: "image/png" }));

    await expect(exportCanvasAssetFiles(canvas, { includeResultVideos: false })).resolves.toBe("saved");
    expect(sharedPulls).toEqual([{ catalogId: "catalog-1", hashes: [hash] }]);
    expect(reads).toEqual(["resource:downloaded"]);
    const archive = await readZip(savedArchive!);
    const manifest = JSON.parse(await archive.get("canvas-assets.json")!.text());
    expect(manifest.assets).toHaveLength(1);
    expect(await archive.get(manifest.assets[0].path)!.text()).toBe("downloaded bytes");
});

test("shared export queues unavailable bytes and does not save a partial ZIP", async () => {
    const hash = await digest("offline asset");
    const canvas = await sharedFixture(hash);

    await expect(exportCanvasAssetFiles(canvas, { includeResultVideos: false })).rejects.toThrow(`协作素材 ${hash.slice(0, 12)}`);
    expect(sharedPulls).toEqual([{ catalogId: "catalog-1", hashes: [hash] }]);
    expect(queuedRetries).toEqual([{ catalogId: "catalog-1", hashes: [hash] }]);
    expect(savedArchive).toBeUndefined();
});

test("shared export queues a failed LAN pull and does not save a partial ZIP", async () => {
    const hash = await digest("unreachable asset");
    const canvas = await sharedFixture(hash);
    canvas.nodes = [];
    sharedPullError = new Error("LAN peer unavailable");

    await expect(exportCanvasAssetFiles(canvas, { includeResultVideos: false })).rejects.toThrow("LAN peer unavailable");
    expect(queuedRetries).toEqual([{ catalogId: "catalog-1", hashes: [hash] }]);
    expect(savedArchive).toBeUndefined();
});

test("cancelled shared export stops after the LAN pull without saving or queueing", async () => {
    const hash = await digest("cancelled asset");
    const canvas = await sharedFixture(hash);
    const controller = new AbortController();
    onSharedPull = () => controller.abort();

    await expect(exportCanvasAssetFiles(canvas, { includeResultVideos: false, signal: controller.signal })).rejects.toThrow();
    expect(sharedPulls).toEqual([{ catalogId: "catalog-1", hashes: [hash] }]);
    expect(queuedRetries).toEqual([]);
    expect(savedArchive).toBeUndefined();
});

test("shared result video is pulled only when selected", async () => {
    const hash = await digest("result video");
    const canvas = await sharedFixture(hash, { nodeType: "video", resultOrigin: "generated" });
    canvas.nodes = [];

    await expect(exportCanvasAssetFiles(canvas, { includeResultVideos: false })).resolves.toBe("saved");
    expect(sharedPulls).toEqual([]);
    savedArchive = undefined;
    localSharedResources = { [hash]: "result-video" };
    media.set("resource:result-video", new Blob(["result video"], { type: "video/mp4" }));
    await expect(exportCanvasAssetFiles(canvas, { includeResultVideos: true })).resolves.toBe("saved");
    expect(sharedPulls).toEqual([{ catalogId: "catalog-1", hashes: [hash] }]);
});

test("shared export rejects a local byte stream with the wrong content hash", async () => {
    const hash = await digest("expected bytes");
    const canvas = await sharedFixture(hash);
    canvas.nodes = [];
    localSharedResources = { [hash]: "corrupted" };
    media.set("resource:corrupted", new Blob(["wrong bytes"], { type: "image/png" }));

    await expect(exportCanvasAssetFiles(canvas, { includeResultVideos: false })).rejects.toThrow("协作素材");
    expect(savedArchive).toBeUndefined();
});
