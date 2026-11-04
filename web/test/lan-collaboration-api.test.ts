import { expect, test } from "bun:test";

import { apiClient } from "@/services/api/request";
import {
    createLanCollaborationStreamTicket,
    createLanCollaborationCanvas,
    deleteLanCollaborationCanvas,
    getLanCollaborationCanvasStreamUrl,
    getLanCollaborationCanvasUpdates,
    getLanCollaborationCanvases,
    nextLanCollaborationUpdateCursor,
    publishLanCollaborationCanvasUpdate,
    updateLanCollaborationCanvas,
} from "@/services/api/lan-collaboration";

test("empty update polls keep the durable cursor and invalid pagination fails", () => {
    expect(nextLanCollaborationUpdateCursor("cursor-9", { updates: [], cursor: "", hasMore: false })).toBe("cursor-9");
    expect(nextLanCollaborationUpdateCursor("cursor-9", { updates: [], cursor: "cursor-10", hasMore: false })).toBe("cursor-10");
    expect(() => nextLanCollaborationUpdateCursor("cursor-9", { updates: [], cursor: "", hasMore: true })).toThrow();
    expect(() => nextLanCollaborationUpdateCursor("cursor-9", { updates: [], cursor: "cursor-9", hasMore: true })).toThrow();
});

test("LAN collaboration canvas methods use the agreed local API contract", async () => {
    const original = apiClient.request;
    const calls: Array<{ method?: string; url?: string; data?: unknown; params?: unknown }> = [];
    apiClient.request = (async (config) => {
        calls.push({ method: config.method, url: config.url, data: config.data, params: config.params });
        return { data: { code: 0, data: {}, msg: "ok" }, status: 200, headers: {} };
    }) as typeof apiClient.request;

    try {
        await getLanCollaborationCanvases();
        await createLanCollaborationCanvas("Shared board", new Uint8Array([0, 127, 128, 255]), "catalog-a");
        await updateLanCollaborationCanvas("canvas/a", { archived: true });
        await deleteLanCollaborationCanvas("canvas/a");
        await getLanCollaborationCanvasUpdates("canvas/a", "cursor-1");
        await publishLanCollaborationCanvasUpdate("canvas/a", new Uint8Array([0, 127, 128, 255]));
        await createLanCollaborationStreamTicket("canvas/a");
    } finally {
        apiClient.request = original;
    }

    expect(calls.map(({ method, url }) => [method, url])).toEqual([
        ["get", "/lan-sharing/canvases"],
        ["post", "/lan-sharing/canvases"],
        ["patch", "/lan-sharing/canvases/canvas%2Fa"],
        ["delete", "/lan-sharing/canvases/canvas%2Fa"],
        ["get", "/lan-sharing/canvases/canvas%2Fa/updates"],
        ["post", "/lan-sharing/canvases/canvas%2Fa/updates"],
        ["post", "/lan-sharing/canvases/canvas%2Fa/stream-ticket"],
    ]);
    expect(calls[1].data).toEqual({ name: "Shared board", assetCatalogId: "catalog-a", initialUpdateBase64: "AH+A/w==" });
    expect(calls[2].data).toEqual({ archived: true });
    expect(calls[4].params).toEqual({ after: "cursor-1", limit: 200 });
    expect(calls[5].data).toEqual({ updateBase64: "AH+A/w==" });
});

test("collaboration stream URL follows the configured API origin and secure protocol", () => {
    const originalWindow = globalThis.window;
    Object.defineProperty(globalThis, "window", { configurable: true, value: { location: { href: "https://studio.local/workspace" } } });
    try {
        expect(getLanCollaborationCanvasStreamUrl("canvas/a", "one-time ticket")).toBe("wss://studio.local/api/lan-sharing/canvases/canvas%2Fa/stream?ticket=one-time+ticket");
    } finally {
        Object.defineProperty(globalThis, "window", { configurable: true, value: originalWindow });
    }
});
