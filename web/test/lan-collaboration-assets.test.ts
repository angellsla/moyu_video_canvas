import { expect, test } from "bun:test";

import { resourceFileUrl } from "@/services/api/resources";
import { ApiError, apiClient } from "@/services/api/request";
import { openSharedCatalog } from "@/services/shared-catalog";
import { getActiveUserScope } from "@/lib/user-scope";
import { createSharedCanvasLocalAssetResolver, getSharedCanvasAssetReadiness, getSharedCanvasLocalAssetBindings, makePortableSharedCanvasContent, prepareSharedCanvasLocalAssetValues, queueSharedCanvasAssetRetries, retryQueuedSharedCanvasAssets, sharedCanvasPeerLocations, startSharedCanvasAssetRetryWorker, summarizeSharedCanvasAssetReadiness, validatedSharedCanvasLocalBindings } from "@/services/lan-collaboration-assets";
import { localForageStorageForScope } from "@/lib/localforage-storage";
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
        else if (url.endsWith("/assets/status")) data = { assets: [] };
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

async function untilAsset(predicate: () => boolean) {
    for (let i = 0; i < 100 && !predicate(); i++) await new Promise((resolve) => setTimeout(resolve, 5));
    expect(predicate()).toBe(true);
}

test("new files prepare concurrently with a three-file bound, shared jobs and per-file publication", async () => {
    await withAssetAPI(async (id) => {
        const original = apiClient.request;
        const started: string[] = [];
        let active = 0;
        let peak = 0;
        let releaseSlow = () => {};
        let releaseFast = () => {};
        const slow = new Promise<void>((resolve) => { releaseSlow = resolve; });
        const fast = new Promise<void>((resolve) => { releaseFast = resolve; });
        apiClient.request = (async (config) => {
            if (config.url?.endsWith("/assets")) {
                const resourceId = (config.data as { resourceId: string }).resourceId;
                started.push(resourceId);
                peak = Math.max(peak, ++active);
                try { await (resourceId === "slow" ? slow : fast); }
                finally { active--; }
            }
            return original(config);
        }) as typeof apiClient.request;
        const source = mediaContent();
        source.nodes = ["slow", "one", "two", "three", "four"].map((name) => ({ ...source.nodes[0], id: name, metadata: { storageKey: `resource:${name}`, content: `blob:${name}` } }));
        const prepared: string[] = [];
        const first = makePortableSharedCanvasContent(id, source, new Map(), (hash) => prepared.push(hash));
        const second = makePortableSharedCanvasContent(id, source);
        try {
            await untilAsset(() => started.length === 3);
            expect(peak).toBe(3);
            releaseFast();
            await untilAsset(() => prepared.includes("hash-four"));
            expect(prepared).not.toContain("hash-slow");
            expect(peak).toBeLessThanOrEqual(3);
            releaseSlow();
            await Promise.all([first, second]);
            expect(started).toHaveLength(5);
            expect(prepared).toHaveLength(5);
        } finally {
            releaseSlow(); releaseFast();
            await Promise.allSettled([first, second]);
            apiClient.request = original;
        }
    });
});

test("a small asset displays before an earlier slow download and its declared holder is tried first", async () => {
    await withAssetAPI(async (id) => {
        const catalog = await openSharedCatalog(id, { scope: getActiveUserScope() });
        for (const name of ["slow", "quick"]) catalog.upsertEntry({ entryId: name, contentHash: `hash-${name}`, name: `${name}.png`, mimeType: "image/png", size: 8, createdBy: "holder", createdAt: new Date().toISOString() });
        const original = apiClient.request;
        let release = () => {};
        const slow = new Promise<void>((resolve) => { release = resolve; });
        const started: string[] = [];
        const progress: Record<string, string>[] = [];
        apiClient.request = (async (config) => {
            const url = config.url || "";
            let data: unknown;
            if (url.endsWith("/updates") && config.method === "get") data = { updates: [], cursor: "", hasMore: false };
            else if (url.endsWith("/peers")) data = { trusted: [{ peerId: "unrelated", address: "192.168.1.30", port: 12047 }, { peerId: "holder", address: "192.168.1.20", port: 12047 }], discovered: [] };
            else if (url.endsWith("/import")) {
                const request = config.data as { contentHash: string; peerId: string };
                expect(request.peerId).toBe("holder");
                started.push(request.contentHash);
                if (request.contentHash === "hash-slow") await slow;
                const name = request.contentHash.slice(5);
                data = { resource: { id: name }, asset: { entryId: `local-${name}`, contentHash: request.contentHash, name: `${name}.png`, mimeType: "image/png", size: 8, createdBy: "local", createdAt: new Date().toISOString() } };
            } else return original(config);
            return { status: 200, headers: {}, data: { code: 0, data, msg: "ok" } };
        }) as typeof apiClient.request;
        const operation = prepareSharedCanvasLocalAssetValues(id, ["hash-slow", "hash-quick"], (ready) => progress.push(ready));
        try {
            await untilAsset(() => progress.some((ready) => ready["hash-quick"] === "quick"));
            expect(started).toEqual(["hash-slow", "hash-quick"]);
            expect(progress.at(-1)).not.toHaveProperty("hash-slow");
            release();
            expect(await operation).toEqual({ "hash-slow": "slow", "hash-quick": "quick" });
        } finally { release(); await operation.catch(() => {}); apiClient.request = original; }
    });
});

test("background retry dispatches new catalog and same-catalog assets while an old transfer is running", async () => {
    await withAssetAPI(async (id) => {
        const secondId = `${id}-second`;
        const firstCatalog = await openSharedCatalog(id, { scope: getActiveUserScope() });
        const secondCatalog = await openSharedCatalog(secondId, { scope: getActiveUserScope() });
        for (const [catalog, names] of [[firstCatalog, ["slow", "later"]], [secondCatalog, ["other"]]] as const) {
            for (const name of names) catalog.upsertEntry({ entryId: name, contentHash: `hash-${name}`, name: `${name}.png`, mimeType: "image/png", size: 8, createdBy: "peer", createdAt: new Date().toISOString() });
        }
        const original = apiClient.request;
        const originalWindow = globalThis.window;
        Object.defineProperty(globalThis, "window", { configurable: true, value: { setTimeout, clearTimeout, setInterval: () => 0, clearInterval: () => {} } });
        const imports: string[] = [];
        let release = () => {};
        const slow = new Promise<void>((resolve) => { release = resolve; });
        apiClient.request = (async (config) => {
            const url = config.url || "";
            if (url.includes("/catalogs/") && !url.includes(`/catalogs/${id}/`) && !url.includes(`/catalogs/${secondId}/`)) throw new Error("another test catalog is offline");
            let data: unknown;
            if (url.endsWith("/updates") && config.method === "get") data = { updates: [], cursor: "", hasMore: false };
            else if (url.endsWith("/peers")) data = { trusted: [{ peerId: "peer", address: "192.168.1.20", port: 12047 }], discovered: [] };
            else if (url.endsWith("/import")) {
                const hash = (config.data as { contentHash: string }).contentHash;
                imports.push(hash);
                if (hash === "hash-slow") await slow;
                const name = hash.slice(5);
                data = { resource: { id: name }, asset: { entryId: `local-${name}`, contentHash: hash, name: `${name}.png`, mimeType: "image/png", size: 8, createdBy: "local", createdAt: new Date().toISOString() } };
            } else return original(config);
            return { status: 200, headers: {}, data: { code: 0, data, msg: "ok" } };
        }) as typeof apiClient.request;
        const stop = startSharedCanvasAssetRetryWorker();
        try {
            await queueSharedCanvasAssetRetries(id, ["hash-slow"]);
            await untilAsset(() => imports.includes("hash-slow"));
            await queueSharedCanvasAssetRetries(secondId, ["hash-other"]);
            await queueSharedCanvasAssetRetries(id, ["hash-later"]);
            await untilAsset(() => imports.length === 3);
            expect(imports.filter((hash) => hash === "hash-slow")).toHaveLength(1);
            for (let i = 0; i < 100 && !(await getSharedCanvasLocalAssetBindings(id))["hash-later"]; i++) await new Promise((resolve) => setTimeout(resolve, 5));
            expect(await getSharedCanvasLocalAssetBindings(id)).toEqual({ "hash-later": "later" });
            expect(await getSharedCanvasLocalAssetBindings(secondId)).toEqual({ "hash-other": "other" });
        } finally {
            stop(); release();
            // Drain the request before restoring the mocked transport.
            for (let i = 0; i < 100; i++) {
                const queue = JSON.parse(await localForageStorageForScope(getActiveUserScope()).getItem("lan-sharing:asset-retry-queue") || "{}");
                if (!queue[id] && !queue[secondId]) break;
                await new Promise((resolve) => setTimeout(resolve, 5));
            }
            apiClient.request = original;
            await secondCatalog.close();
            Object.defineProperty(globalThis, "window", { configurable: true, value: originalWindow });
        }
    });
});

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

test("outgoing registration keeps catalog publication queued even after its resource hash is cached", async () => {
    await withAssetAPI(async (id, calls) => {
        const original = apiClient.request;
        let publishFails = true;
        apiClient.request = (async (config) => {
            if (config.url?.endsWith("/updates") && config.method === "post" && publishFails) throw new Error("catalog disconnected");
            if (config.url?.endsWith("/file")) return { status: 206, headers: {}, data: new Uint8Array([1]).buffer };
            return original(config);
        }) as typeof apiClient.request;
        try {
            await expect(makePortableSharedCanvasContent(id, mediaContent())).rejects.toThrow("catalog disconnected");
            expect(await getSharedCanvasLocalAssetBindings(id)).toEqual({ "hash-original": "original" });
            const catalog = await openSharedCatalog(id, { scope: getActiveUserScope() });
            expect(catalog.pendingLocalUpdates().length).toBeGreaterThan(0);
            publishFails = false;
            await retryQueuedSharedCanvasAssets();
            expect(catalog.pendingLocalUpdates()).toHaveLength(0);
            const queued = JSON.parse(await localForageStorageForScope(getActiveUserScope()).getItem("lan-sharing:asset-retry-queue") || "{}");
            expect(queued[id]).toBeUndefined();
            expect(calls.filter((call) => call.url.endsWith("/assets"))).toHaveLength(1);
        } finally { apiClient.request = original; }
    });
});

test("returning from sharing settings reopens the catalog instead of reusing a closed document", async () => {
    await withAssetAPI(async (id) => {
        await makePortableSharedCanvasContent(id, mediaContent());
        const closed = await openSharedCatalog(id, { scope: getActiveUserScope() });
        await closed.close();
        const next = mediaContent();
        next.nodes[0].metadata = { storageKey: "resource:next", content: "blob:next" };
        expect(await makePortableSharedCanvasContent(id, next)).toMatchObject({ nodes: [{ metadata: { content: { contentHash: "hash-next" } } }] });
        expect(await getSharedCanvasLocalAssetBindings(id)).toEqual({ "hash-original": "original", "hash-next": "next" });
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

test("an existing asset probe does not prevent the next import and concurrent workers share its transfer", async () => {
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
                await new Promise((resolve) => setTimeout(resolve, 5));
                data = { resource: { id: "second" }, asset: { entryId: "local-second", contentHash: "hash-second", name: "second.jpg", mimeType: "image/jpeg", size: 8, createdBy: "local", createdAt: new Date().toISOString() } };
            } else return originalRequest(config);
            return { status: 200, headers: {}, data: { code: 0, data, msg: "ok" } };
        }) as typeof apiClient.request;
        try {
            const hashes = ["hash-original", "hash-second"];
            const results = await Promise.all([prepareSharedCanvasLocalAssetValues(id, hashes), prepareSharedCanvasLocalAssetValues(id, hashes)]);
            for (const result of results) expect(result).toEqual({ "hash-original": "original", "hash-second": "second" });
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

test("downloaded bytes remain usable when catalog publication fails, and retry only publishes the receipt", async () => {
    await withAssetAPI(async (id, calls) => {
        const catalog = await openSharedCatalog(id, { scope: getActiveUserScope() });
        catalog.upsertEntry({ entryId: "remote", contentHash: "hash-image", name: "image.png", mimeType: "image/png", size: 8, createdBy: "peer", createdAt: new Date().toISOString() });
        for (const update of catalog.pendingLocalUpdates()) await catalog.acknowledgeLocalUpdate(update);
        const original = apiClient.request;
        let publishFails = true;
        let imports = 0;
        const progress: Record<string, string>[] = [];
        apiClient.request = (async (config) => {
            const url = config.url || "";
            if (url.endsWith("/file")) return { status: 206, headers: {}, data: new Uint8Array([1]).buffer };
            let data: unknown;
            if (url.endsWith("/updates")) {
                if (config.method === "post" && publishFails) throw new Error("catalog disconnected");
                data = config.method === "get" ? { updates: [], cursor: "", hasMore: false } : { accepted: true };
            } else if (url.endsWith("/peers")) data = { trusted: [
                { peerId: "peer", address: "192.168.1.20", port: 12047 },
                { peerId: "other", address: "192.168.1.21", port: 12047 },
            ], discovered: [] };
            else if (url.endsWith("/import")) {
                imports++;
                data = { resource: { id: "downloaded" }, asset: { entryId: "local", contentHash: "hash-image", name: "image.png", mimeType: "image/png", size: 8, createdBy: "local", createdAt: new Date().toISOString() } };
            } else return original(config);
            return { status: 200, headers: {}, data: { code: 0, data, msg: "ok" } };
        }) as typeof apiClient.request;
        const store = localForageStorageForScope(getActiveUserScope());
        const queued = async () => JSON.parse(await store.getItem("lan-sharing:asset-retry-queue") || "{}");
        try {
            await expect(prepareSharedCanvasLocalAssetValues(id, ["hash-image"], (ready) => progress.push(ready))).rejects.toThrow("清单声明待重试");
            expect(progress.at(-1)).toEqual({ "hash-image": "downloaded" });
            expect(await getSharedCanvasLocalAssetBindings(id)).toEqual({ "hash-image": "downloaded" });
            expect(catalog.getEntry("local")?.createdBy).toBe("local");
            expect(catalog.pendingLocalUpdates().length).toBeGreaterThan(0);
            expect((await queued())[id]).toEqual(["hash-image"]);
            await retryQueuedSharedCanvasAssets();
            expect((await queued())[id]).toEqual(["hash-image"]);
            publishFails = false;
            await retryQueuedSharedCanvasAssets();
            expect((await queued())[id]).toBeUndefined();
            expect(catalog.pendingLocalUpdates()).toHaveLength(0);
            expect(imports).toBe(1);
            expect(calls.some((call) => call.url.endsWith("/assets"))).toBe(false);
        } finally { apiClient.request = original; }
    });
});

for (const staleBinding of [false, true]) test(`native mapping recovers ${staleBinding ? "stale" : "absent"} browser bindings while holders are offline`, async () => {
    await withAssetAPI(async (id) => {
        if (staleBinding) await makePortableSharedCanvasContent(id, mediaContent());
        const original = apiClient.request;
        const probes: string[] = [];
        apiClient.request = (async (config) => {
            const url = config.url || "";
            if (url.endsWith("/assets/status")) return { status: 200, data: { code: 0, data: { assets: [{ contentHash: "hash-original", resourceId: "native-existing", availability: "present" }] }, msg: "ok" }, headers: {} };
            if (url.endsWith("/file")) {
                probes.push(url);
                if (url === "/resources/original/file") throw new ApiError("gone", { status: 404 });
                return { status: 206, data: new Uint8Array([1]).buffer, headers: {} };
            }
            throw new Error(`unexpected peer or transfer request: ${url}`);
        }) as typeof apiClient.request;
        try {
            expect(await prepareSharedCanvasLocalAssetValues(id, ["hash-original"])).toEqual({ "hash-original": "native-existing" });
            expect(await getSharedCanvasLocalAssetBindings(id)).toEqual({ "hash-original": "native-existing" });
            expect(probes).toContain("/resources/native-existing/file");
        } finally { apiClient.request = original; }
    });
});

test("a native present status with missing bytes stays pending rather than binding a missing file", async () => {
    await withAssetAPI(async (id) => {
        const original = apiClient.request;
        let peerQueries = 0;
        apiClient.request = (async (config) => {
            const url = config.url || "";
            let data: unknown;
            if (url.endsWith("/assets/status")) data = { assets: [{ contentHash: "hash-gone", resourceId: "missing", availability: "present" }] };
            else if (url.endsWith("/file")) throw new ApiError("gone", { status: 404 });
            else if (url.endsWith("/updates")) data = { updates: [], cursor: "", hasMore: false };
            else if (url.endsWith("/peers")) { peerQueries++; data = { trusted: [], discovered: [] }; }
            else throw new Error(`unexpected request: ${url}`);
            return { status: 200, data: { code: 0, data, msg: "ok" }, headers: {} };
        }) as typeof apiClient.request;
        try {
            expect(await prepareSharedCanvasLocalAssetValues(id, ["hash-gone"])).toEqual({});
            expect(await getSharedCanvasLocalAssetBindings(id)).toEqual({});
            expect(peerQueries).toBe(1);
        } finally { apiClient.request = original; }
    });
});

test("a failed probe and failed file do not stop later imports, while diagnostics and bindings survive", async () => {
    await withAssetAPI(async (id) => {
        await makePortableSharedCanvasContent(id, mediaContent());
        const catalog = await openSharedCatalog(id, { scope: getActiveUserScope() });
        for (const name of ["broken", "second"]) catalog.upsertEntry({ entryId: name, contentHash: `hash-${name}`, name: `${name}.png`, mimeType: "image/png", size: 8, createdBy: "peer", createdAt: new Date().toISOString() });
        const original = apiClient.request;
        const imports: string[] = [];
        const progress: Record<string, string>[] = [];
        apiClient.request = (async (config) => {
            const url = config.url || "";
            if (url === "/resources/original/file") throw new Error("probe temporarily interrupted");
            let data: unknown;
            if (url.endsWith("/updates") && config.method === "get") data = { updates: [], hasMore: false, cursor: "" };
            else if (url.endsWith("/peers")) data = { trusted: [{ peerId: "peer", address: "192.168.1.20", port: 12047 }], discovered: [] };
            else if (url.endsWith("/import")) {
                expect(config.timeout).toBeGreaterThan(4_000);
                const hash = (config.data as { contentHash: string }).contentHash;
                imports.push(hash);
                if (hash === "hash-broken") throw new Error("hash verification failed");
                data = { resource: { id: "second" }, asset: { entryId: "local-second", contentHash: "hash-second", name: "second.png", mimeType: "image/png", size: 8, createdBy: "local", createdAt: new Date().toISOString() } };
            } else {
                if (url.endsWith("/assets")) expect(config.timeout).toBeGreaterThan(4_000);
                return original(config);
            }
            return { status: 200, headers: {}, data: { code: 0, data, msg: "ok" } };
        }) as typeof apiClient.request;
        try {
            await expect(prepareSharedCanvasLocalAssetValues(id, ["hash-original", "hash-broken", "hash-second"], (ready) => progress.push(ready))).rejects.toThrow("2 项素材待重试");
            expect(imports).toEqual(["hash-broken", "hash-second"]);
            expect(progress.at(-1)).toEqual({ "hash-second": "second" });
            expect(await getSharedCanvasLocalAssetBindings(id)).toEqual({ "hash-original": "original", "hash-second": "second" });
        } finally { apiClient.request = original; }
    });
});

test("one unreadable outgoing file does not prevent preparing the other files", async () => {
    await withAssetAPI(async (id) => {
        const source = mediaContent();
        source.nodes.unshift({ ...source.nodes[0], id: "broken", metadata: { content: "blob:expired" } });
        const originalFetch = globalThis.fetch;
        globalThis.fetch = (async () => new Response("gone", { status: 404 })) as typeof fetch;
        const cache = new Map<string, string>();
        const prepared: string[] = [];
        try {
            await expect(makePortableSharedCanvasContent(id, source, cache, (hash, resourceId) => prepared.push(`${hash}:${resourceId}`))).rejects.toThrow("1 项素材准备失败");
            expect(cache.get("resource:original")).toBe("hash-original");
            expect(cache.has("blob:expired")).toBe(false);
            expect(prepared).toEqual(["hash-original:original"]);
            expect(await getSharedCanvasLocalAssetBindings(id)).toEqual({ "hash-original": "original" });
        } finally { globalThis.fetch = originalFetch; }
    });
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
