import { expect, test } from "bun:test";

import { resourceFileUrl } from "@/services/api/resources";
import { apiClient } from "@/services/api/request";
import { openSharedCatalog } from "@/services/shared-catalog";
import { getActiveUserScope } from "@/lib/user-scope";
import { createSharedCanvasLocalAssetResolver, getSharedCanvasAssetReadiness, getSharedCanvasLocalAssetBindings, makePortableSharedCanvasContent, prepareSharedCanvasLocalAssetValues, sharedCanvasPeerLocations, summarizeSharedCanvasAssetReadiness, validatedSharedCanvasLocalBindings } from "@/services/lan-collaboration-assets";
import type { SharedCanvasContent } from "@/services/collaborative-canvas-document";

async function withAssetAPI(run: (catalogId: string, calls: Array<{ url: string; data: unknown }>) => Promise<void>) {
    const originalRequest = apiClient.request;
    const originalWindow = globalThis.window;
    const catalogId = `asset-test-${crypto.randomUUID()}`;
    const calls: Array<{ url: string; data: unknown }> = [];
    Object.defineProperty(globalThis, "window", { configurable: true, value: originalWindow || {} });
    apiClient.request = (async (config) => {
        const url = config.url || "";
        calls.push({ url, data: config.data });
        let data: unknown = { accepted: true };
        if (url === "/resources") data = { resource: { id: "uploaded-blob", kind: "image", mimeType: "image/png", size: 7 } };
        else if (url.endsWith("/assets")) {
            const id = (config.data as { resourceId: string }).resourceId;
            data = { asset: { entryId: id, contentHash: `hash-${id}`, name: `${id}.png`, mimeType: "image/png", size: 7, createdBy: "local-peer", createdAt: new Date().toISOString() } };
        }
        return { status: 200, headers: {}, data: { code: 0, data, msg: "ok" } };
    }) as typeof apiClient.request;
    try {
        await run(catalogId, calls);
    } finally {
        await (await openSharedCatalog(catalogId, { scope: getActiveUserScope() })).close();
        apiClient.request = originalRequest;
        Object.defineProperty(globalThis, "window", { configurable: true, value: originalWindow });
    }
}

function mediaContent(type: "image" | "video" | "audio" = "image"): SharedCanvasContent {
    return { title: "Shared", nodes: [{ id: "a", type, title: "Upload", position: { x: 0, y: 0 }, width: 100, height: 100, metadata: { storageKey: "resource:original", content: "blob:expired-preview" } }], connections: [], directorScenes: [] };
}

test("canvas uploads share durable media instead of reading expired preview URLs", async () => {
    await withAssetAPI(async (id, calls) => {
        const cache = new Map<string, string>();
        for (const type of ["image", "video", "audio"] as const) {
            const portable = await makePortableSharedCanvasContent(id, mediaContent(type), cache);
            expect(portable.nodes[0]).toMatchObject({ metadata: {
                content: { __beeftvSharedAsset: true, contentHash: "hash-original", localField: "content" },
                storageKey: { __beeftvSharedAsset: true, contentHash: "hash-original", localField: "storageKey" },
            } });
            expect(JSON.stringify(portable)).not.toContain("blob:");
        }
        const registeredCalls = calls.length;
        const moved = mediaContent();
        moved.nodes[0].position.x = 200;
        await makePortableSharedCanvasContent(id, moved, cache);
        expect(calls.length).toBe(registeredCalls);
        expect(calls.filter((call) => call.url.endsWith("/assets"))).toHaveLength(1);
        expect(calls.filter((call) => call.url === "/resources")).toHaveLength(0);
        expect(await getSharedCanvasLocalAssetBindings(id)).toEqual({ "hash-original": "original" });
    });
});

test("blob-only uploads materialize and enter the shared catalog", async () => {
    const blobUrl = URL.createObjectURL(new Blob(["fixture"], { type: "image/png" }));
    try {
        await withAssetAPI(async (id, calls) => {
            const source = mediaContent();
            source.nodes[0].metadata = { content: blobUrl };
            const result = await makePortableSharedCanvasContent(id, source);
            expect(result.nodes[0]).toMatchObject({ metadata: { content: { contentHash: "hash-uploaded-blob" } } });
            const upload = calls.find((call) => call.url === "/resources")!.data as FormData;
            expect(await (upload.get("file") as Blob).text()).toBe("fixture");
        });
    } finally { URL.revokeObjectURL(blobUrl); }
});

test("video previews use their own durable resource and empty upload placeholders do not fail", async () => {
    await withAssetAPI(async (id, calls) => {
        const source = mediaContent("video");
        source.nodes[0].metadata!.videoPreview = { storageKey: "resource:thumbnail", content: "blob:expired-thumbnail" };
        const result = await makePortableSharedCanvasContent(id, source);
        expect(result.nodes[0]).toMatchObject({ metadata: { videoPreview: { content: { contentHash: "hash-thumbnail" }, storageKey: { contentHash: "hash-thumbnail" } } } });
        source.nodes[0].metadata = { content: "", storageKey: "" };
        await makePortableSharedCanvasContent(id, source);
        expect(calls.filter((call) => call.url.endsWith("/assets"))).toHaveLength(2);
    });
});

test("failed asset preparation is retryable and does not poison the session hash cache", async () => {
    await withAssetAPI(async (id) => {
        const fetchBefore = globalThis.fetch;
        let attempts = 0;
        globalThis.fetch = (async () => {
            attempts++;
            return attempts === 1 ? new Response("expired", { status: 404 }) : new Response(new Blob(["fixture"], { type: "image/png" }));
        }) as typeof fetch;
        try {
            const source = mediaContent();
            source.nodes[0].metadata = { content: "blob:retry" };
            const cache = new Map<string, string>();
            await expect(makePortableSharedCanvasContent(id, source, cache)).rejects.toThrow();
            expect(cache.size).toBe(0);
            expect(await makePortableSharedCanvasContent(id, source, cache)).toMatchObject({ nodes: [{ metadata: { content: { contentHash: "hash-uploaded-blob" } } }] });
        } finally { globalThis.fetch = fetchBefore; }
    });
});

test("concurrent asset registrations preserve both local bindings", async () => {
    await withAssetAPI(async (id) => {
        const first = mediaContent();
        const second = mediaContent();
        second.nodes[0].metadata = { storageKey: "resource:second", content: "blob:second" };
        await Promise.all([makePortableSharedCanvasContent(id, first), makePortableSharedCanvasContent(id, second)]);
        expect(await getSharedCanvasLocalAssetBindings(id)).toEqual({ "hash-original": "original", "hash-second": "second" });
    });
});

test("readiness polls share in-flight catalog reads and retain the incremental cursor", async () => {
    await withAssetAPI(async (id) => {
        const originalRequest = apiClient.request;
        const cursors: string[] = [];
        apiClient.request = (async (config) => {
            let data: unknown;
            if (config.url?.endsWith("/updates")) {
                cursors.push(String(config.params?.after || ""));
                await new Promise((resolve) => setTimeout(resolve, 5));
                data = { updates: [], cursor: cursors.length === 1 ? "cursor-1" : "", hasMore: false };
            } else data = { trusted: [], discovered: [] };
            return { status: 200, headers: {}, data: { code: 0, data, msg: "ok" } };
        }) as typeof apiClient.request;
        try {
            await Promise.all([getSharedCanvasAssetReadiness(id, []), getSharedCanvasAssetReadiness(id, [])]);
            expect(cursors).toEqual([""]);
            await getSharedCanvasAssetReadiness(id, []);
            await getSharedCanvasAssetReadiness(id, []);
            expect(cursors).toEqual(["", "cursor-1", "cursor-1"]);
        } finally { apiClient.request = originalRequest; }
    });
});

test("shared asset pulls try every discovered address of trusted peers", () => {
    expect(sharedCanvasPeerLocations(
        [
            { peerId: "peer-a", address: "192.168.1.20", port: 8080 },
            { peerId: "peer-b", address: "10.0.0.8", port: 8080 },
            { peerId: "peer-c" },
        ],
        [
            { peerId: "peer-a", address: "10.0.0.20", port: 8080 },
            { peerId: "peer-a", address: "192.168.1.20", port: 8080 },
            { peerId: "peer-a", address: "10.0.0.20", port: 8080 },
            { peerId: "peer-b", address: "172.16.0.8", port: 8080 },
            { peerId: "peer-c", address: "192.168.2.8", port: 8080 },
            { peerId: "untrusted", address: "192.168.2.9", port: 8080 },
        ],
    )).toEqual([
        { peerId: "peer-a", address: "192.168.1.20", port: 8080 },
        { peerId: "peer-a", address: "10.0.0.20", port: 8080 },
        { peerId: "peer-b", address: "172.16.0.8", port: 8080 },
        { peerId: "peer-b", address: "10.0.0.8", port: 8080 },
        { peerId: "peer-c", address: "192.168.2.8", port: 8080 },
    ]);
});

test("an existing asset probe does not prevent importing the next shared asset", async () => {
    await withAssetAPI(async (id) => {
        await makePortableSharedCanvasContent(id, mediaContent());
        const catalog = await openSharedCatalog(id, { scope: getActiveUserScope() });
        catalog.upsertEntry({ entryId: "second", contentHash: "hash-second", name: "second.jpg", mimeType: "image/jpeg", size: 8, createdBy: "peer", createdAt: new Date().toISOString() });
        const originalRequest = apiClient.request;
        const probes: string[] = [];
        const imports: string[] = [];
        apiClient.request = (async (config) => {
            const url = config.url || "";
            if (url.endsWith("/file")) {
                expect(config.headers).toMatchObject({ Range: "bytes=0-0", "Cache-Control": "no-cache" });
                expect(config.responseType).toBe("arraybuffer");
                probes.push(url);
                return { status: 206, headers: {}, data: new Uint8Array([1]).buffer };
            }
            let data: unknown;
            if (url.endsWith("/updates") && config.method === "get") data = { updates: [], cursor: "", hasMore: false };
            else if (url.endsWith("/peers")) data = { trusted: [{ peerId: "peer", address: "192.168.1.20", port: 12047 }], discovered: [] };
            else if (url.endsWith("/import")) {
                imports.push((config.data as { contentHash: string }).contentHash);
                data = { resource: { id: "second" } };
            } else return originalRequest(config);
            return { status: 200, headers: {}, data: { code: 0, data, msg: "ok" } };
        }) as typeof apiClient.request;
        try {
            const hashes = ["hash-original", "hash-second"];
            expect(await prepareSharedCanvasLocalAssetValues(id, hashes)).toEqual({ "hash-original": "original", "hash-second": "second" });
            const readiness = await getSharedCanvasAssetReadiness(id, hashes);
            expect(readiness.local).toEqual({ mappedCount: 2, missingHashes: [] });
            expect(imports).toEqual(["hash-second"]);
            expect(probes).toContain("/resources/original/file");
            expect(probes).toContain("/resources/second/file");
        } finally { apiClient.request = originalRequest; }
    });
});

test("deleted local resource bindings are not treated as ready", async () => {
    const probes: string[] = [];
    const known = await validatedSharedCanvasLocalBindings(["hash-good", "hash-missing"], { "hash-good": "resource-good", "hash-missing": "resource-deleted" }, async (id) => {
        probes.push(id);
        return id === "resource-good";
    });
    expect(known).toEqual({ "hash-good": "resource-good" });
    expect(probes.sort()).toEqual(["resource-deleted", "resource-good"]);
});

test("a failed resource probe does not discard a binding as deleted", async () => {
    await expect(validatedSharedCanvasLocalBindings(["hash-a"], { "hash-a": "resource-a" }, async () => {
        throw new Error("backend unavailable");
    })).rejects.toThrow("backend unavailable");
});

test("shared asset hashes resolve to local field-compatible references", () => {
    const resolve = createSharedCanvasLocalAssetResolver({ "hash-image": "resource-a" });
    const path = ["nodes", "0", "metadata", "content"];

    expect(resolve({ field: "resourceId", contentHash: "hash-image", path })).toBe("resource-a");
    expect(resolve({ field: "storageKey", contentHash: "hash-image", path })).toBe("resource:resource-a");
    for (const field of ["content", "previewContent", "url", "dataUrl"] as const) {
        expect(resolve({ field, contentHash: "hash-image", path })).toBe(resourceFileUrl("resource-a"));
    }
    expect(resolve({ field: "content", contentHash: "missing", path })).toBeUndefined();
});

test("asset readiness distinguishes local mappings from trusted peer claims", () => {
    const summary = summarizeSharedCanvasAssetReadiness(
        ["hash-a", "hash-b", "hash-a"],
        { "hash-a": "local-resource-a" },
        [
            { contentHash: "hash-a", createdBy: "peer-a" },
            { contentHash: "hash-a", createdBy: "peer-a" },
            { contentHash: "hash-b", createdBy: "peer-b" },
            { contentHash: "hash-b", createdBy: "untrusted-peer" },
        ],
        [
            { peerId: "peer-a", nickname: "Editing Room", address: "192.168.1.20", port: 8080 },
            { peerId: "peer-b", nickname: "" },
        ],
        [{ peerId: "peer-a" }, { peerId: "untrusted-peer" }],
    );

    expect(summary).toEqual({
        requiredCount: 2,
        local: { mappedCount: 1, missingHashes: ["hash-b"] },
        peers: [
            { peerId: "peer-a", nickname: "Editing Room", discovered: true, claimedCount: 1, unconfirmedHashes: ["hash-b"] },
            { peerId: "peer-b", nickname: "peer-b", discovered: false, claimedCount: 1, unconfirmedHashes: ["hash-a"] },
        ],
    });
    expect(JSON.stringify(summary)).not.toContain("192.168.1.20");
    expect(JSON.stringify(summary)).not.toContain("untrusted-peer");
});

test("unadvertised assets remain unconfirmed rather than marked ready", () => {
    const summary = summarizeSharedCanvasAssetReadiness(["hash-a"], {}, [], [{ peerId: "peer-a", nickname: "Peer A" }], [{ peerId: "peer-a" }]);
    expect(summary.local.missingHashes).toEqual(["hash-a"]);
    expect(summary.peers[0]).toMatchObject({ discovered: true, claimedCount: 0, unconfirmedHashes: ["hash-a"] });
});
