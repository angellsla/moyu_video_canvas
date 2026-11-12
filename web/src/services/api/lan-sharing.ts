import type { RemoteResource } from "@/services/api/resources";
import { http } from "@/services/api/request";

export type LanSharingStatus = {
    enabled: boolean;
    nodeId: string;
    nickname: string;
    pairingCode?: string;
    pairingCodeExpiresAt?: string;
    endpoint?: string;
    endpoints?: string[];
    advertisedEndpoints?: string[];
};

export type LanSharingPeer = {
    peerId: string;
    address: string;
    port: number;
    nickname?: string;
    nodeId?: string;
};

export type LanSharingTrustedPeer = {
    peerId: string;
    nickname?: string;
    address?: string;
    port?: number;
    nodeId?: string;
    trustedAt?: string;
    lastSeenAt?: string;
};

export type LanSharingCatalog = {
    id: string;
    name: string;
    updateCount?: number;
    createdAt?: string;
    updatedAt?: string;
};

export type LanSharingAsset = {
    entryId: string;
    contentHash: string;
    name: string;
    mimeType: string;
    size: number;
    createdBy: string;
    createdAt: string;
    parentId?: string;
};

export type LanSharingAssetStatus = {
    contentHash: string;
    resourceId: string;
    availability: "present" | "missing" | "unknown";
    checkedAt?: string | null;
};

const BASE_PATH = "/lan-sharing";

export function getLanSharingStatus() {
    return http.get<LanSharingStatus>(`${BASE_PATH}/status`);
}

export function setLanSharingNickname(nickname: string) {
    return http.put<LanSharingStatus>(`${BASE_PATH}/nickname`, { nickname });
}

export function startLanSharing() {
    return http.post<LanSharingStatus>(`${BASE_PATH}/start`, {});
}

export function stopLanSharing() {
    return http.post<LanSharingStatus>(`${BASE_PATH}/stop`, {});
}

export function getLanSharingPeers() {
    return http.get<{ discovered: LanSharingPeer[]; trusted: LanSharingTrustedPeer[] }>(`${BASE_PATH}/peers`);
}

export function pairLanSharingPeer(input: {
    address: string;
    pairingCode: string;
    peerId?: string;
    port?: number;
    nickname?: string;
}) {
    return http.post<{ peer: LanSharingTrustedPeer }>(`${BASE_PATH}/pair`, input);
}

export function revokeLanSharingPeer(peerId: string) {
    return http.delete<{ peerId: string }>(`${BASE_PATH}/peers/${encodeURIComponent(peerId)}`);
}

export function getLanSharingCatalogs() {
    return http.get<{ catalogs: LanSharingCatalog[] }>(`${BASE_PATH}/catalogs`);
}

export function createLanSharingCatalog(name: string) {
    return http.post<{ catalog: LanSharingCatalog }>(`${BASE_PATH}/catalogs`, { name });
}

export function listLocalSharingResources() {
    return http.get<{ resources: RemoteResource[] }>("/resources", { params: { pageSize: 200 } });
}

export function addLanSharingCatalogAsset(catalogId: string, resourceId: string) {
    // Hashing and copying media can outlast the ordinary JSON-request deadline.
    return http.post<{ asset: LanSharingAsset }>(`${BASE_PATH}/catalogs/${encodeURIComponent(catalogId)}/assets`, { resourceId }, { timeout: 30 * 60_000 });
}

export function getLanSharingCatalogAssetStatuses(catalogId: string) {
    return http.get<{ assets: LanSharingAssetStatus[] }>(`${BASE_PATH}/catalogs/${encodeURIComponent(catalogId)}/assets/status`);
}

export function importLanSharingCatalogAsset(catalogId: string, input: {
    peerId: string;
    address: string;
    port: number;
    contentHash: string;
    name: string;
    mimeType: string;
    size: number;
}, signal?: AbortSignal) {
    return http.post<{ resource: RemoteResource; asset: LanSharingAsset }>(`${BASE_PATH}/catalogs/${encodeURIComponent(catalogId)}/assets/import`, input, { timeout: 30 * 60_000, signal });
}

export type LanSharingCatalogUpdate = {
    id: string;
    updateBase64: string;
    originNodeId: string;
    createdAt: string;
};

export function getLanSharingCatalogUpdates(catalogId: string, after = "") {
    return http.get<{ updates: LanSharingCatalogUpdate[]; cursor: string; hasMore: boolean }>(
        `${BASE_PATH}/catalogs/${encodeURIComponent(catalogId)}/updates`,
        { params: { after, limit: 200 } },
    );
}

export function encodeLanSharingUpdate(update: Uint8Array) {
    let binary = "";
    const chunkSize = 0x8000;
    for (let offset = 0; offset < update.byteLength; offset += chunkSize) {
        const chunk = update.subarray(offset, Math.min(update.byteLength, offset + chunkSize));
        binary += String.fromCharCode(...chunk);
    }
    return btoa(binary);
}

export function decodeLanSharingUpdate(updateBase64: string) {
    const binary = atob(updateBase64);
    return Uint8Array.from(binary, (character) => character.charCodeAt(0));
}

export function publishLanSharingCatalogUpdate(catalogId: string, update: Uint8Array) {
    return http.post<{ accepted: boolean }>(`${BASE_PATH}/catalogs/${encodeURIComponent(catalogId)}/updates`, {
        updateBase64: encodeLanSharingUpdate(update),
    });
}
