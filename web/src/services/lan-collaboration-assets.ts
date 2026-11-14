import { getActiveUserScope } from "@/lib/user-scope";
import { assertUserScope, captureUserScope } from "@/lib/user-scope-guard";
import { localForageStorageForScope } from "@/lib/localforage-storage";
import { getLanSharingCatalogUpdates, addLanSharingCatalogAsset, createLanSharingCatalog, importLanSharingCatalogAsset, publishLanSharingCatalogUpdate, decodeLanSharingUpdate, type LanSharingAsset } from "@/services/api/lan-sharing";
import { getLanSharingPeers, type LanSharingPeer, type LanSharingTrustedPeer } from "@/services/api/lan-sharing";
import { getLanSharingCatalogs, getLanSharingCatalogAssetStatuses, type LanSharingAssetStatus } from "@/services/api/lan-sharing";
import { openSharedCatalog, type SharedCatalog, type SharedCatalogEntry } from "@/services/shared-catalog";
import { ownedResourceIdFromMediaRef, resourceFileUrl, resourceStorageKey, uploadResourceFile } from "@/services/api/resources";
import { getMediaBlob } from "@/services/file-storage";
import { getImageBlob } from "@/services/image-storage";
import { ApiError, http } from "@/services/api/request";
import { collectSharedCanvasAssetReferences, toPortableSharedCanvasContent, type SharedCanvasAssetField, type SharedCanvasContent, type SharedCanvasLocalAssetResolver, type PortableSharedCanvasContent } from "@/services/collaborative-canvas-document";

const BINDINGS_KEY = "lan-sharing:local-resource-bindings";
const ASSET_RETRY_KEY = "lan-sharing:asset-retry-queue";
type Bindings = Record<string, Record<string, string>>;
type AssetRetryQueue = Record<string, string[]>;

// Bound disk/hash work separately from transfers so a large download cannot
// hold up registration of newly added media (or consume unbounded RAM/I/O).
function assetWorkQueue(limit: number) {
    let active = 0;
    const waiting: Array<() => void> = [];
    return async <T>(work: () => Promise<T>): Promise<T> => {
        if (active >= limit) await new Promise<void>((resolve) => waiting.push(resolve));
        else active++;
        try { return await work(); }
        finally {
            const next = waiting.shift();
            if (next) next();
            else active--;
        }
    };
}

const runAssetPreparation = assetWorkQueue(3);
const runAssetDownload = assetWorkQueue(3);
const assetPreparations = new Map<string, Promise<{ resourceId: string; hash: string }>>();

function readAssetRetryQueue(value: string | null): AssetRetryQueue {
    if (!value) return {};
    try {
        const parsed = JSON.parse(value) as Record<string, unknown>;
        if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return {};
        return Object.fromEntries(Object.entries(parsed).filter(([, hashes]) => Array.isArray(hashes)).map(([id, hashes]) => [id, (hashes as unknown[]).filter((hash): hash is string => typeof hash === "string")])) as AssetRetryQueue;
    } catch {
        return {};
    }
}

export type SharedCanvasPeerAssetClaim = {
    peerId: string;
    nickname: string;
    discovered: boolean;
    claimedCount: number;
    unconfirmedHashes: string[];
};

export type SharedCanvasAssetReadiness = {
    requiredCount: number;
    local: { mappedCount: number; missingHashes: string[] };
    peers: SharedCanvasPeerAssetClaim[];
};

export type SharedCanvasAssetRetryResult = {
    localResources: Record<string, string>;
    missingHashes: string[];
};

export function summarizeSharedCanvasAssetReadiness(
    requiredHashes: string[],
    localResources: Record<string, string>,
    catalogEntries: Pick<SharedCatalogEntry, "contentHash" | "createdBy">[],
    trustedPeers: Pick<LanSharingTrustedPeer, "peerId" | "nickname">[],
    discoveredPeers: Pick<LanSharingPeer, "peerId">[],
): SharedCanvasAssetReadiness {
    const required = [...new Set(requiredHashes.filter(Boolean))];
    const discovered = new Set(discoveredPeers.map((peer) => peer.peerId));
    const claims = new Map<string, Set<string>>();
    for (const entry of catalogEntries) {
        if (!entry.contentHash || !entry.createdBy) continue;
        let hashes = claims.get(entry.createdBy);
        if (!hashes) {
            hashes = new Set<string>();
            claims.set(entry.createdBy, hashes);
        }
        hashes.add(entry.contentHash);
    }
    return {
        requiredCount: required.length,
        local: {
            mappedCount: required.filter((hash) => Boolean(localResources[hash])).length,
            missingHashes: required.filter((hash) => !localResources[hash]),
        },
        peers: trustedPeers.map((peer) => {
            const claimed = claims.get(peer.peerId) ?? new Set<string>();
            return {
                peerId: peer.peerId,
                nickname: peer.nickname || peer.peerId.slice(0, 8),
                discovered: discovered.has(peer.peerId),
                claimedCount: required.filter((hash) => claimed.has(hash)).length,
                unconfirmedHashes: required.filter((hash) => !claimed.has(hash)),
            };
        }),
    };
}

function readBindings(value: string | null): Bindings {
    if (!value) return {};
    try {
        const parsed = JSON.parse(value) as unknown;
        if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return {};
        const result: Bindings = {};
        for (const [catalogId, entries] of Object.entries(parsed)) {
            if (!entries || typeof entries !== "object" || Array.isArray(entries)) continue;
            result[catalogId] = Object.fromEntries(Object.entries(entries).filter((entry): entry is [string, string] => typeof entry[1] === "string" && Boolean(entry[1])));
        }
        return result;
    } catch {
        return {};
    }
}

async function getBindings(): Promise<Bindings> {
    return readBindings(await localForageStorageForScope(getActiveUserScope()).getItem(BINDINGS_KEY));
}

let bindingsTail = Promise.resolve();

async function bindResource(catalogId: string, contentHash: string, resourceId: string) {
    const store = localForageStorageForScope(getActiveUserScope());
    const operation = bindingsTail.catch(() => undefined).then(async () => {
        const next = readBindings(await store.getItem(BINDINGS_KEY));
        next[catalogId] = { ...next[catalogId], [contentHash]: resourceId };
        await store.setItem(BINDINGS_KEY, JSON.stringify(next));
    });
    bindingsTail = operation;
    await operation;
}

// Read mappings without downloading/probing media on the interactive projection path.
export async function getSharedCanvasLocalAssetBindings(catalogId: string) {
    return (await getBindings())[catalogId] || {};
}

let retryQueueTail = Promise.resolve();
const assetRetryListeners = new Set<() => void>();
const activeAssetRetries = new Set<string>();

async function mutateAssetRetryQueue(change: (queue: AssetRetryQueue) => void) {
    const scope = getActiveUserScope();
    const store = localForageStorageForScope(scope);
    const operation = retryQueueTail.catch(() => undefined).then(async () => {
        const queue = readAssetRetryQueue(await store.getItem(ASSET_RETRY_KEY));
        change(queue);
        await store.setItem(ASSET_RETRY_KEY, JSON.stringify(queue));
    });
    retryQueueTail = operation;
    await operation;
}

export async function queueSharedCanvasAssetRetries(catalogId: string, hashes: string[]) {
    const required = [...new Set(hashes.filter(Boolean))];
    if (!required.length) return;
    let added = false;
    await mutateAssetRetryQueue((queue) => {
        const previous = queue[catalogId] || [];
        queue[catalogId] = [...new Set([...previous, ...required])];
        added = queue[catalogId].length !== previous.length;
    });
    if (added) for (const listener of assetRetryListeners) listener();
}

export async function retryQueuedSharedCanvasAssets() {
    const scope = captureUserScope();
    const store = localForageStorageForScope(scope.userScope);
    const queue = readAssetRetryQueue(await store.getItem(ASSET_RETRY_KEY));
    assertUserScope(scope);
    await Promise.all(Object.entries(queue).map(async ([catalogId, queued]) => {
        const key = (hash: string) => JSON.stringify([scope.userScope, scope.epoch, catalogId, hash]);
        const hashes = queued.filter((hash) => !activeAssetRetries.has(key(hash)));
        if (!hashes.length) return;
        hashes.forEach((hash) => activeAssetRetries.add(key(hash)));
        try {
            const result = await retrySharedCanvasAssetTransfer(catalogId, hashes);
            assertUserScope(scope);
            await mutateAssetRetryQueue((current) => {
                const attempted = new Set(hashes);
                const remaining = (current[catalogId] || []).filter((hash) => !attempted.has(hash) || result.missingHashes.includes(hash));
                if (remaining.length) current[catalogId] = remaining;
                else delete current[catalogId];
            });
        } catch {
            // Retain the queue until a holder or the local backend is reachable.
        } finally {
            hashes.forEach((hash) => activeAssetRetries.delete(key(hash)));
        }
    }));
}

export function startSharedCanvasAssetRetryWorker() {
    let stopped = false;
    let scheduled: number | undefined;
    const tick = () => {
        if (stopped || scheduled !== undefined) return;
        scheduled = window.setTimeout(() => {
            scheduled = undefined;
            if (!stopped) void retryQueuedSharedCanvasAssets().catch(() => undefined);
        }, 0);
    };
    assetRetryListeners.add(tick);
    tick();
    const timer = window.setInterval(tick, 10_000);
    return () => {
        stopped = true;
        assetRetryListeners.delete(tick);
        if (scheduled !== undefined) window.clearTimeout(scheduled);
        window.clearInterval(timer);
    };
}

async function publishPendingCatalogUpdates(catalogId: string, catalog: SharedCatalog) {
    const scope = captureUserScope();
    for (const update of catalog.pendingLocalUpdates()) {
        assertUserScope(scope);
        await publishLanSharingCatalogUpdate(catalogId, update);
        await catalog.acknowledgeLocalUpdate(update);
    }
}

async function addLocalResourceToCatalog(catalogId: string, resourceId: string) {
    const scope = captureUserScope();
    const bindings = await getBindings();
    const existingHash = Object.entries(bindings[catalogId] || {}).find(([, boundResourceId]) => boundResourceId === resourceId)?.[0];
    const catalog = await openSharedCatalog(catalogId, { scope: scope.userScope });
    await publishPendingCatalogUpdates(catalogId, catalog);
    if (existingHash && await localResourceExists(resourceId)) return existingHash;

    assertUserScope(scope);
    const result = await addLanSharingCatalogAsset(catalogId, resourceId);
    assertUserScope(scope);
    catalog.upsertEntry(result.asset);
    await catalog.flush();
    assertUserScope(scope);
    await bindResource(catalogId, result.asset.contentHash, resourceId);
    assertUserScope(scope);
    await queueSharedCanvasAssetRetries(catalogId, [result.asset.contentHash]);
    assertUserScope(scope);
    await publishPendingCatalogUpdates(catalogId, catalog);
    return result.asset.contentHash;
}

async function materializeResource(value: string, field: SharedCanvasAssetField): Promise<string | undefined> {
    const scope = captureUserScope();
    const direct = field === "resourceId" ? value : ownedResourceIdFromMediaRef(value, value);
    if (direct) return direct;
    let blob: Blob | null | undefined;
    if (/^(?:data:|blob:)/i.test(value)) {
        const response = await fetch(value);
        if (!response.ok) throw new Error("协作素材读取失败");
        blob = await response.blob();
    } else {
        try {
            blob = await getImageBlob(value);
        } catch {
            blob = undefined;
        }
        if (!blob) {
            try {
                blob = await getMediaBlob(value);
            } catch {
                blob = undefined;
            }
        }
    }
    if (!blob?.size) return undefined;
    const mime = blob.type || "application/octet-stream";
    const kind = mime.startsWith("image/") ? "image" : mime.startsWith("video/") ? "video" : mime.startsWith("audio/") ? "audio" : "file";
    assertUserScope(scope);
    return (await uploadResourceFile(blob, kind, { fileName: `shared-canvas.${mime.split("/")[1] || "bin"}` })).id;
}

export async function prepareSharedCanvasAssetHashes(catalogId: string, values: ReturnType<typeof collectSharedCanvasAssetReferences>, hashes = new Map<string, string>(), onPrepared?: (hash: string, resourceId: string) => void) {
    const scope = captureUserScope();
    const failures = new Map<string, unknown>();
    const sources = new Map<string, typeof values>();
    for (const reference of values) {
        const source = reference.source || reference;
        if (!source.value.trim()) continue;
        const cached = hashes.get(source.value);
        if (cached) { hashes.set(reference.value, cached); continue; }
        sources.set(source.value, [...(sources.get(source.value) || []), reference]);
    }
    await Promise.all([...sources].map(async ([value, references]) => {
        const source = references[0].source || references[0];
        const key = JSON.stringify([scope.userScope, scope.epoch, catalogId, source.field, value]);
        try {
            let preparation = assetPreparations.get(key);
            if (!preparation) {
                const operation = runAssetPreparation(async () => {
                    assertUserScope(scope);
                    const resourceId = await materializeResource(source.value, source.field);
                    assertUserScope(scope);
                    if (!resourceId) throw new Error("协作素材暂时无法读取，原稿已保留，请重试");
                    return { resourceId, hash: await addLocalResourceToCatalog(catalogId, resourceId) };
                }).finally(() => {
                    if (assetPreparations.get(key) === operation) assetPreparations.delete(key);
                });
                assetPreparations.set(key, operation);
                preparation = operation;
            }
            const { resourceId, hash } = await preparation;
            assertUserScope(scope);
            hashes.set(source.value, hash);
            for (const reference of references) hashes.set(reference.value, hash);
            hashes.set(resourceId, hash);
            hashes.set(resourceStorageKey(resourceId), hash);
            hashes.set(resourceFileUrl(resourceId), hash);
            onPrepared?.(hash, resourceId);
        } catch (error) {
            assertUserScope(scope);
            failures.set(source.value, error);
        }
    }));
    const firstFailure = failures.values().next().value;
    if (failures.size) throw new Error(`${failures.size} 项素材准备失败：${firstFailure instanceof Error ? firstFailure.message : "请稍后重试"}`);
    return hashes;
}

export async function retrySharedCanvasCatalogSync(catalogId: string) {
    const catalog = await openSharedCatalog(catalogId, { scope: getActiveUserScope() });
    await publishPendingCatalogUpdates(catalogId, catalog);
}

const catalogSyncStates = new WeakMap<SharedCatalog, { cursor: string; inFlight: Promise<SharedCatalog> | null }>();

async function syncSharedCanvasCatalog(catalogId: string) {
    const scope = captureUserScope();
    const catalog = await openSharedCatalog(catalogId, { scope: scope.userScope });
    let state = catalogSyncStates.get(catalog);
    if (!state) {
        state = { cursor: "", inFlight: null };
        catalogSyncStates.set(catalog, state);
    }
    if (state.inFlight) return state.inFlight;
    const current = state;
    current.inFlight = (async () => {
        for (;;) {
            assertUserScope(scope);
            const page = await getLanSharingCatalogUpdates(catalogId, current.cursor);
            assertUserScope(scope);
            for (const update of page.updates) catalog.applyRemoteUpdate(decodeLanSharingUpdate(update.updateBase64));
            if (page.hasMore && (!page.cursor || page.cursor === current.cursor)) throw new Error("共享素材清单游标无效");
            current.cursor = page.cursor || current.cursor;
            if (!page.hasMore) return catalog;
        }
    })().finally(() => { current.inFlight = null; });
    return current.inFlight;
}

export async function getSharedCanvasAssetReadiness(catalogId: string, hashes: string[]): Promise<SharedCanvasAssetReadiness> {
    const [bindings, catalog, peerResponse] = await Promise.all([getBindings(), syncSharedCanvasCatalog(catalogId), getLanSharingPeers()]);
    const localResources = await validatedSharedCanvasLocalBindings(hashes, bindings[catalogId] || {}, localResourceExists);
    return summarizeSharedCanvasAssetReadiness(hashes, localResources, catalog.listEntries(), peerResponse.trusted, peerResponse.discovered);
}

export async function retrySharedCanvasAssetTransfer(catalogId: string, hashes: string[]): Promise<SharedCanvasAssetRetryResult> {
    await retrySharedCanvasCatalogSync(catalogId);
    const localResources = await prepareSharedCanvasLocalAssetValues(catalogId, hashes);
    return {
        localResources,
        missingHashes: [...new Set(hashes.filter(Boolean))].filter((hash) => !localResources[hash]),
    };
}

export function sharedCanvasPeerLocations(
    trusted: Pick<LanSharingTrustedPeer, "peerId" | "address" | "port">[],
    discovered: Pick<LanSharingPeer, "peerId" | "address" | "port">[],
) {
    const result: { peerId: string; address: string; port: number }[] = [];
    for (const peer of trusted) {
        const active = discovered.filter((candidate) => candidate.peerId === peer.peerId && candidate.address && candidate.port);
        const saved = peer.address && peer.port ? { peerId: peer.peerId, address: peer.address, port: peer.port } : undefined;
        const savedIsActive = saved && active.some((candidate) => candidate.address === saved.address && candidate.port === saved.port);
        const candidates = [...(savedIsActive && saved ? [saved] : []), ...active, ...(saved ? [saved] : [])];
        const seen = new Set<string>();
        for (const candidate of candidates) {
            const key = `${candidate.address}:${candidate.port}`;
            if (seen.has(key)) continue;
            seen.add(key);
            result.push({ peerId: peer.peerId, address: candidate.address, port: candidate.port });
        }
    }
    return result;
}

type LocalAssetImport = { resourceId: string; announcementError?: string };
const assetImports = new Map<string, Promise<LocalAssetImport | undefined>>();

function ensureCatalogEntryLocal(catalogId: string, entry: SharedCanvasEntry, peers: ReturnType<typeof sharedCanvasPeerLocations>) {
    const scope = captureUserScope();
    const key = JSON.stringify([scope.userScope, scope.epoch, catalogId, entry.contentHash]);
    const existing = assetImports.get(key);
    if (existing) return existing;
    const operation = runAssetDownload(async () => {
        assertUserScope(scope);
        return importCatalogEntryLocal(catalogId, entry, peers);
    }).finally(() => {
        if (assetImports.get(key) === operation) assetImports.delete(key);
    });
    assetImports.set(key, operation);
    return operation;
}

async function importCatalogEntryLocal(catalogId: string, entry: SharedCanvasEntry, peers: ReturnType<typeof sharedCanvasPeerLocations>) {
    const scope = captureUserScope();
    let failure: unknown;
    let imported: Awaited<ReturnType<typeof importLanSharingCatalogAsset>> | undefined;
    for (const peer of peers) {
        assertUserScope(scope);
        try {
            imported = await importLanSharingCatalogAsset(catalogId, {
                peerId: peer.peerId,
                address: peer.address,
                port: peer.port,
                contentHash: entry.contentHash,
                name: entry.name,
                mimeType: entry.mimeType,
                size: entry.size,
            });
            assertUserScope(scope);
            break;
        } catch (error) {
            assertUserScope(scope);
            failure = error;
        }
    }
    if (!imported) {
        if (failure) throw failure;
        return undefined;
    }
    const catalog = await openSharedCatalog(catalogId, { scope: scope.userScope });
    assertUserScope(scope);
    catalog.upsertEntry(imported.asset);
    await catalog.flush();
    assertUserScope(scope);
    await bindResource(catalogId, entry.contentHash, imported.resource.id);
    assertUserScope(scope);
    await queueSharedCanvasAssetRetries(catalogId, [entry.contentHash]);
    assertUserScope(scope);
    try {
        await publishPendingCatalogUpdates(catalogId, catalog);
        return { resourceId: imported.resource.id };
    } catch (error) {
        assertUserScope(scope);
        // Bytes and the catalog outbox are durable. Do not redownload or hide
        // the file just because announcing this device's copy needs a retry.
        return { resourceId: imported.resource.id, announcementError: error instanceof Error ? error.message : "清单声明待重试" };
    }
}

async function localResourceExists(resourceId: string) {
    try {
        const response = await http.raw<ArrayBuffer>({
            method: "GET",
            url: `/resources/${encodeURIComponent(resourceId)}/file`,
            headers: { Range: "bytes=0-0", "Cache-Control": "no-cache" },
            responseType: "arraybuffer",
        });
        return response.status === 206 || (response.status === 200 && response.data.byteLength > 0);
    } catch (error) {
        if (error instanceof ApiError && (error.status === 404 || error.status === 410)) return false;
        throw error;
    }
}

export async function validatedSharedCanvasLocalBindings(hashes: string[], bindings: Record<string, string>, exists: (resourceId: string) => Promise<boolean>) {
    const known = { ...bindings };
    await Promise.all([...new Set(hashes)].map(async (hash) => {
        if (known[hash] && !await exists(known[hash])) delete known[hash];
    }));
    return known;
}

type SharedCanvasEntry = Pick<LanSharingAsset, "contentHash" | "name" | "mimeType" | "size" | "createdBy">;

export async function prepareSharedCanvasLocalAssetValues(catalogId: string, hashes: string[], onProgress?: (resources: Record<string, string>) => void) {
    const scope = captureUserScope();
    const bindings = await getBindings();
    const required = [...new Set(hashes)];
    const known = { ...bindings[catalogId] };
    const uncertain = new Set<string>();
    const probe = async (hash: string, resourceId: string) => {
        delete known[hash];
        try {
            if (await localResourceExists(resourceId)) {
                known[hash] = resourceId;
                uncertain.delete(hash);
            }
        } catch {
            // Keep the durable binding, but do not claim an unverified file is ready.
            uncertain.add(hash);
        }
    };
    await Promise.all(required.map(async (hash) => {
        if (known[hash]) await probe(hash, known[hash]);
    }));
    assertUserScope(scope);
    const unmapped = new Set(required.filter((hash) => !known[hash]));
    if (unmapped.size) {
        let nativeAssets: LanSharingAssetStatus[] = [];
        try {
            const { assets } = await getLanSharingCatalogAssetStatuses(catalogId);
            nativeAssets = assets;
        } catch {
            // A failed mapping lookup does not prevent trying online holders.
        }
        assertUserScope(scope);
        await Promise.all(nativeAssets.map(async (asset) => {
            if (unmapped.has(asset.contentHash) && asset.availability !== "missing" && asset.resourceId !== bindings[catalogId]?.[asset.contentHash]) {
                await probe(asset.contentHash, asset.resourceId);
            }
        }));
    }
    const failures = [...uncertain].map(() => "本机素材检查失败，稍后重试");
    assertUserScope(scope);
    for (const hash of unmapped) {
        assertUserScope(scope);
        if (known[hash]) await bindResource(catalogId, hash, known[hash]);
    }
    assertUserScope(scope);
    onProgress?.({ ...known });
    const missing = required.filter((hash) => !known[hash]);
    if (!missing.length) return known;
    const [catalog, peerResponse] = await Promise.all([syncSharedCanvasCatalog(catalogId), getLanSharingPeers()]);
    const entries = catalog.listEntries();
    const peers = sharedCanvasPeerLocations(peerResponse.trusted, peerResponse.discovered);
    await Promise.all(missing.map(async (hash) => {
        assertUserScope(scope);
        if (uncertain.has(hash)) return;
        const entry = entries.find((candidate) => candidate.contentHash === hash);
        if (!entry) return;
        const holders = new Set(entries.filter((candidate) => candidate.contentHash === hash).map((candidate) => candidate.createdBy));
        const preferredPeers = [...peers].sort((left, right) => Number(holders.has(right.peerId)) - Number(holders.has(left.peerId)));
        try {
            const imported = await ensureCatalogEntryLocal(catalogId, entry, preferredPeers);
            assertUserScope(scope);
            if (imported) {
                known[hash] = imported.resourceId;
                onProgress?.({ ...known });
                if (imported.announcementError) failures.push(`${entry.name} 文件已就绪，清单声明待重试：${imported.announcementError}`);
            }
        } catch (error) {
            assertUserScope(scope);
            failures.push(`${entry.name}：${error instanceof Error ? error.message : "传输失败"}`);
        }
    }));
    if (failures.length) throw new Error(`${failures.length} 项素材待重试：${failures[0]}`);
    return known;
}

export function createSharedCanvasLocalAssetResolver(resources: Record<string, string>): SharedCanvasLocalAssetResolver {
    return ({ field, contentHash }) => {
        const resourceId = resources[contentHash];
        if (!resourceId) return undefined;
        if (field === "resourceId") return resourceId;
        if (field === "storageKey") return resourceStorageKey(resourceId);
        return resourceFileUrl(resourceId);
    };
}

export async function makePortableSharedCanvasContent(catalogId: string, content: SharedCanvasContent, cache = new Map<string, string>(), onPrepared?: (hash: string, resourceId: string) => void): Promise<PortableSharedCanvasContent> {
    const references = collectSharedCanvasAssetReferences(content);
    const hashes = await prepareSharedCanvasAssetHashes(catalogId, references, cache, onPrepared);
    return toPortableSharedCanvasContent(content, ({ localValue, source }) => hashes.get(source?.value || localValue));
}

export async function ensureSharedCanvasCatalog(name: string, roomId?: string) {
    const response = await getLanSharingCatalogs();
    const existing = response.catalogs.find((catalog) => catalog.name === name && (catalog.roomId || "") === (roomId || ""));
    if (existing) return existing.id;
    const created = await createLanSharingCatalog(name, roomId);
    return created.catalog.id;
}

export function sharedCanvasResourceUrl(resourceId: string) {
    return resourceFileUrl(resourceId);
}
