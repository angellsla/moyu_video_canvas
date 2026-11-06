import { getActiveUserScope } from "@/lib/user-scope";
import { localForageStorageForScope } from "@/lib/localforage-storage";
import { getLanSharingCatalogUpdates, addLanSharingCatalogAsset, createLanSharingCatalog, importLanSharingCatalogAsset, publishLanSharingCatalogUpdate, decodeLanSharingUpdate, type LanSharingAsset } from "@/services/api/lan-sharing";
import { getLanSharingPeers, type LanSharingPeer, type LanSharingTrustedPeer } from "@/services/api/lan-sharing";
import { getLanSharingCatalogs } from "@/services/api/lan-sharing";
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

async function bindResource(catalogId: string, contentHash: string, resourceId: string) {
    const store = localForageStorageForScope(getActiveUserScope());
    const next = await getBindings();
    next[catalogId] = { ...next[catalogId], [contentHash]: resourceId };
    await store.setItem(BINDINGS_KEY, JSON.stringify(next));
}

let retryQueueTail = Promise.resolve();

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
    await mutateAssetRetryQueue((queue) => {
        queue[catalogId] = [...new Set([...(queue[catalogId] || []), ...required])];
    });
}

export async function retryQueuedSharedCanvasAssets() {
    const scope = getActiveUserScope();
    const store = localForageStorageForScope(scope);
    const queue = readAssetRetryQueue(await store.getItem(ASSET_RETRY_KEY));
    for (const [catalogId, hashes] of Object.entries(queue)) {
        if (scope !== getActiveUserScope()) return;
        try {
            const result = await retrySharedCanvasAssetTransfer(catalogId, hashes);
            await mutateAssetRetryQueue((current) => {
                const remaining = (current[catalogId] || []).filter((hash) => result.missingHashes.includes(hash));
                if (remaining.length) current[catalogId] = remaining;
                else delete current[catalogId];
            });
        } catch {
            // Retain the queue until a holder or the local backend is reachable.
        }
    }
}

export function startSharedCanvasAssetRetryWorker() {
    let running = false;
    const tick = () => {
        if (running) return;
        running = true;
        void retryQueuedSharedCanvasAssets().finally(() => { running = false; });
    };
    tick();
    const timer = window.setInterval(tick, 10_000);
    return () => window.clearInterval(timer);
}

const openCatalogs = new Map<string, Promise<SharedCatalog>>();

async function connectCatalog(catalogId: string) {
    const key = `${getActiveUserScope()}:${catalogId}`;
    let opening = openCatalogs.get(key);
    if (!opening) {
        opening = openSharedCatalog(catalogId, { scope: getActiveUserScope() });
        openCatalogs.set(key, opening);
    }
    try {
        return await opening;
    } catch (error) {
        if (openCatalogs.get(key) === opening) openCatalogs.delete(key);
        throw error;
    }
}

async function publishPendingCatalogUpdates(catalogId: string, catalog: SharedCatalog) {
    for (const update of catalog.pendingLocalUpdates()) {
        await publishLanSharingCatalogUpdate(catalogId, update);
        await catalog.acknowledgeLocalUpdate(update);
    }
}

async function addLocalResourceToCatalog(catalogId: string, resourceId: string) {
    const bindings = await getBindings();
    const existingHash = Object.entries(bindings[catalogId] || {}).find(([, boundResourceId]) => boundResourceId === resourceId)?.[0];
    const catalog = await connectCatalog(catalogId);
    await publishPendingCatalogUpdates(catalogId, catalog);
    if (existingHash && await localResourceExists(resourceId)) return existingHash;

    const result = await addLanSharingCatalogAsset(catalogId, resourceId);
    catalog.upsertEntry(result.asset);
    await bindResource(catalogId, result.asset.contentHash, resourceId);
    await publishPendingCatalogUpdates(catalogId, catalog);
    return result.asset.contentHash;
}

async function materializeResource(value: string, field: SharedCanvasAssetField): Promise<string | undefined> {
    const direct = field === "resourceId" ? value : ownedResourceIdFromMediaRef(value, value);
    if (direct) return direct;
    let blob: Blob | null | undefined;
    if (value.startsWith("data:")) {
        try {
            blob = await fetch(value).then((response) => response.blob());
        } catch {
            blob = undefined;
        }
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
    try {
        return (await uploadResourceFile(blob, kind, { fileName: `shared-canvas.${mime.split("/")[1] || "bin"}` })).id;
    } catch {
        return undefined;
    }
}

export async function prepareSharedCanvasAssetHashes(catalogId: string, values: Array<{ field: SharedCanvasAssetField; value: string }>) {
    const hashes = new Map<string, string>();
    const pending = [...new Set(values.map(({ value }) => value))];
    for (const value of pending) {
        const resourceId = await materializeResource(value, values.find((item) => item.value === value)?.field || "storageKey");
        if (!resourceId) throw new Error("协作素材暂时无法读取，原稿已保留，请重试");
        hashes.set(value, await addLocalResourceToCatalog(catalogId, resourceId));
    }
    return hashes;
}

export async function retrySharedCanvasCatalogSync(catalogId: string) {
    const catalog = await connectCatalog(catalogId);
    await publishPendingCatalogUpdates(catalogId, catalog);
}

async function syncSharedCanvasCatalog(catalogId: string) {
    const catalog = await connectCatalog(catalogId);
    let cursor = "";
    for (;;) {
        const page = await getLanSharingCatalogUpdates(catalogId, cursor);
        for (const update of page.updates) catalog.applyRemoteUpdate(decodeLanSharingUpdate(update.updateBase64));
        if (!page.hasMore) return catalog;
        if (!page.cursor || page.cursor === cursor) throw new Error("共享素材清单游标无效");
        cursor = page.cursor;
    }
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

async function ensureCatalogEntryLocal(catalogId: string, entry: SharedCanvasEntry, peers: ReturnType<typeof sharedCanvasPeerLocations>) {
    const bindings = await getBindings();
    const existingId = bindings[catalogId]?.[entry.contentHash];
    if (existingId && await localResourceExists(existingId)) return existingId;
    for (const peer of peers) {
        try {
            const result = await importLanSharingCatalogAsset(catalogId, {
                peerId: peer.peerId,
                address: peer.address,
                port: peer.port,
                contentHash: entry.contentHash,
                name: entry.name,
                mimeType: entry.mimeType,
                size: entry.size,
            });
            const localAsset = await addLanSharingCatalogAsset(catalogId, result.resource.id);
            const catalog = await connectCatalog(catalogId);
            catalog.upsertEntry(localAsset.asset);
            await bindResource(catalogId, entry.contentHash, result.resource.id);
            await publishPendingCatalogUpdates(catalogId, catalog);
            return result.resource.id;
        } catch {
            // Another online holder or a later retry may complete this asset.
        }
    }
    return undefined;
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

export async function prepareSharedCanvasLocalAssetValues(catalogId: string, hashes: string[]) {
    const bindings = await getBindings();
    const required = [...new Set(hashes)];
    const known = await validatedSharedCanvasLocalBindings(required, bindings[catalogId] || {}, localResourceExists);
    const missing = required.filter((hash) => !known[hash]);
    if (!missing.length) return known;
    const [catalog, peerResponse] = await Promise.all([syncSharedCanvasCatalog(catalogId), getLanSharingPeers()]);
    const entries = catalog.listEntries();
    const peers = sharedCanvasPeerLocations(peerResponse.trusted, peerResponse.discovered);
    for (const hash of missing) {
        const entry = entries.find((candidate) => candidate.contentHash === hash);
        if (!entry) continue;
        const resourceId = await ensureCatalogEntryLocal(catalogId, entry, peers);
        if (resourceId) known[hash] = resourceId;
    }
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

export async function makePortableSharedCanvasContent(catalogId: string, content: SharedCanvasContent): Promise<PortableSharedCanvasContent> {
    const references = collectSharedCanvasAssetReferences(content);
    const hashes = await prepareSharedCanvasAssetHashes(catalogId, references);
    return toPortableSharedCanvasContent(content, ({ localValue }) => hashes.get(localValue));
}

export async function ensureSharedCanvasCatalog(name: string) {
    const response = await getLanSharingCatalogs();
    const existing = response.catalogs.find((catalog) => catalog.name === name);
    if (existing) return existing.id;
    const created = await createLanSharingCatalog(name);
    return created.catalog.id;
}

export function sharedCanvasResourceUrl(resourceId: string) {
    return resourceFileUrl(resourceId);
}
