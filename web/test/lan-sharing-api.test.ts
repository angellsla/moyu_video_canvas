import { expect, test } from "bun:test";

import { apiClient } from "@/services/api/request";
import {
    createLanSharingCatalog,
    addLanSharingCatalogAsset,
    decodeLanSharingUpdate,
    encodeLanSharingUpdate,
    getLanSharingCatalogUpdates,
    getLanSharingCatalogAssetStatuses,
    getLanSharingCatalogs,
    getLanSharingPeers,
    getLanSharingStatus,
    importLanSharingCatalogAsset,
    listLocalSharingResources,
    pairLanSharingPeer,
    publishLanSharingCatalogUpdate,
    revokeLanSharingPeer,
    setLanSharingNickname,
    startLanSharing,
    stopLanSharing,
} from "@/services/api/lan-sharing";

test("LAN sharing API functions use the local JSON request contract", async () => {
    const original = apiClient.request;
    const calls: Array<{ method?: string; url?: string; data?: unknown }> = [];
    apiClient.request = (async (config) => {
        calls.push({ method: config.method, url: config.url, data: config.data });
        return { data: { code: 0, data: {}, msg: "ok" }, status: 200, headers: {} };
    }) as typeof apiClient.request;

    try {
        await getLanSharingStatus();
        await setLanSharingNickname("Studio Editor");
        await startLanSharing();
        await stopLanSharing();
        await getLanSharingPeers();
        await pairLanSharingPeer({ peerId: "peer/a", address: "127.0.0.1", port: 4096, pairingCode: "123456", nickname: "Editor" });
        await pairLanSharingPeer({ address: "192.168.1.20", pairingCode: "ONE-TIME" });
        await pairLanSharingPeer({ address: "192.168.1.20", port: 42123, pairingCode: "ONE-TIME" });
        await revokeLanSharingPeer("peer/a");
        await getLanSharingCatalogs();
        await createLanSharingCatalog("Shared shots");
        await listLocalSharingResources();
        await addLanSharingCatalogAsset("catalog/a", "resource/a");
        await importLanSharingCatalogAsset("catalog/a", { peerId: "peer-a", address: "192.168.1.2", port: 4096, contentHash: "sha256:abc", name: "shot.png", mimeType: "image/png", size: 123 });
        await getLanSharingCatalogUpdates("catalog/a", "cursor-1");
        await publishLanSharingCatalogUpdate("catalog/a", new Uint8Array([0, 127, 128, 255]));
    } finally {
        apiClient.request = original;
    }

    expect(calls.map(({ method, url }) => [method, url])).toEqual([
        ["get", "/lan-sharing/status"],
        ["put", "/lan-sharing/nickname"],
        ["post", "/lan-sharing/start"],
        ["post", "/lan-sharing/stop"],
        ["get", "/lan-sharing/peers"],
        ["post", "/lan-sharing/pair"],
        ["post", "/lan-sharing/pair"],
        ["post", "/lan-sharing/pair"],
        ["delete", "/lan-sharing/peers/peer%2Fa"],
        ["get", "/lan-sharing/catalogs"],
        ["post", "/lan-sharing/catalogs"],
        ["get", "/resources"],
        ["post", "/lan-sharing/catalogs/catalog%2Fa/assets"],
        ["post", "/lan-sharing/catalogs/catalog%2Fa/assets/import"],
        ["get", "/lan-sharing/catalogs/catalog%2Fa/updates"],
        ["post", "/lan-sharing/catalogs/catalog%2Fa/updates"],
    ]);
    expect(calls[1].data).toEqual({ nickname: "Studio Editor" });
    expect(calls[5].data).toEqual({ peerId: "peer/a", address: "127.0.0.1", port: 4096, pairingCode: "123456", nickname: "Editor" });
    expect(calls[6].data).toEqual({ address: "192.168.1.20", pairingCode: "ONE-TIME" });
    expect(calls[7].data).toEqual({ address: "192.168.1.20", port: 42123, pairingCode: "ONE-TIME" });
    expect(calls[10].data).toEqual({ name: "Shared shots" });
    expect(calls[12].data).toEqual({ resourceId: "resource/a" });
    expect(calls[13].data).toEqual({ peerId: "peer-a", address: "192.168.1.2", port: 4096, contentHash: "sha256:abc", name: "shot.png", mimeType: "image/png", size: 123 });
    expect(calls[15].data).toEqual({ updateBase64: "AH+A/w==" });
    expect(calls[14].method).toBe("get");
    expect(decodeLanSharingUpdate("AH+A/w==")).toEqual(new Uint8Array([0, 127, 128, 255]));
});

test("LAN update base64 encoding handles empty and chunked updates", () => {
    expect(encodeLanSharingUpdate(new Uint8Array())).toBe("");
    const update = new Uint8Array(0x8001).fill(0x61);
    expect(encodeLanSharingUpdate(update)).toBe(btoa("a".repeat(update.byteLength)));
});

test("LAN catalog asset status returns local file availability", async () => {
    const original = apiClient.request;
    const calls: Array<{ method?: string; url?: string }> = [];
    apiClient.request = (async (config) => {
        calls.push({ method: config.method, url: config.url });
        return {
            data: {
                code: 0,
                data: { assets: [
                    { contentHash: "sha256:present", resourceId: "resource-a", availability: "present", checkedAt: "2026-11-04T00:00:00Z" },
                    { contentHash: "sha256:missing", resourceId: "resource-b", availability: "missing", checkedAt: "2026-11-04T00:00:00Z" },
                    { contentHash: "sha256:unknown", resourceId: "resource-c", availability: "unknown" },
                ] },
                msg: "ok",
            },
            status: 200,
            headers: {},
        };
    }) as typeof apiClient.request;

    try {
        const response = await getLanSharingCatalogAssetStatuses("catalog/a");
        expect(calls).toEqual([{ method: "get", url: "/lan-sharing/catalogs/catalog%2Fa/assets/status" }]);
        expect(response.assets.map((asset) => asset.availability)).toEqual(["present", "missing", "unknown"]);
        expect(response.assets[1].resourceId).toBe("resource-b");
    } finally {
        apiClient.request = original;
    }
});
