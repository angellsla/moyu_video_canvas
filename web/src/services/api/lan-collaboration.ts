import { apiBaseURL, http } from "@/services/api/request";
import { decodeLanSharingUpdate, encodeLanSharingUpdate } from "@/services/api/lan-sharing";

export type LanCollaborationCanvas = {
    id: string;
    name: string;
    createdByNodeId: string;
    archived: boolean;
    updateCount: number;
    assetCatalogId?: string;
    createdAt: string;
    updatedAt: string;
    archivedAt?: string;
};

export type LanCollaborationCanvasUpdate = {
    id: string;
    updateBase64: string;
    originNodeId: string;
    createdAt: string;
};

export type LanCollaborationCanvasUpdatePage = {
    updates: LanCollaborationCanvasUpdate[];
    cursor: string;
    hasMore: boolean;
};

export function nextLanCollaborationUpdateCursor(previous: string, page: LanCollaborationCanvasUpdatePage) {
    if (page.hasMore && (!page.cursor || page.cursor === previous)) throw new Error("协作画布更新游标无效");
    return page.cursor || previous;
}

export type LanCollaborationStreamTicket = {
    ticket: string;
    expiresInSeconds: number;
};

export type LanCollaborationClientFrame = { type: "update"; updateBase64: string } | { type: "presence"; state: unknown };

export type LanCollaborationServerFrame = { type: "update"; canvasId: string; updateBase64: string; originNodeId: string } | { type: "presence"; canvasId: string; state: unknown; peerId: string; expiresAt: string } | { type: "asset-catalog"; canvasId: string; catalogId: string } | { type: "error"; message: string };

const BASE_PATH = "/lan-sharing/canvases";

export function getLanCollaborationCanvases(signal?: AbortSignal) {
    return http.get<{ canvases: LanCollaborationCanvas[] }>(BASE_PATH, { signal });
}

export function createLanCollaborationCanvas(name: string, initialUpdate?: Uint8Array, assetCatalogId?: string) {
    return http.post<{ canvas: LanCollaborationCanvas }>(BASE_PATH, {
        name,
        ...(assetCatalogId ? { assetCatalogId } : {}),
        ...(initialUpdate?.byteLength ? { initialUpdateBase64: encodeLanSharingUpdate(initialUpdate) } : {}),
    });
}

export function updateLanCollaborationCanvas(canvasId: string, patch: { name?: string; archived?: boolean }) {
    return http.patch<{ canvas: LanCollaborationCanvas }>(`${BASE_PATH}/${encodeURIComponent(canvasId)}`, patch);
}

export function deleteLanCollaborationCanvas(canvasId: string) {
    return http.delete<{ canvasId: string }>(`${BASE_PATH}/${encodeURIComponent(canvasId)}`);
}

export function getLanCollaborationCanvasUpdates(canvasId: string, after = "", signal?: AbortSignal) {
    return http.get<LanCollaborationCanvasUpdatePage>(`${BASE_PATH}/${encodeURIComponent(canvasId)}/updates`, {
        params: { after, limit: 200 },
        signal,
    });
}

export function publishLanCollaborationCanvasUpdate(canvasId: string, update: Uint8Array) {
    return http.post<{ accepted: boolean }>(`${BASE_PATH}/${encodeURIComponent(canvasId)}/updates`, {
        updateBase64: encodeLanSharingUpdate(update),
    });
}

export function createLanCollaborationStreamTicket(canvasId: string) {
    return http.post<LanCollaborationStreamTicket>(`${BASE_PATH}/${encodeURIComponent(canvasId)}/stream-ticket`, {});
}

export function getLanCollaborationCanvasStreamUrl(canvasId: string, ticket: string) {
    const base = new URL(apiBaseURL, window.location.href);
    const path = `${base.pathname.replace(/\/$/, "")}${BASE_PATH}/${encodeURIComponent(canvasId)}/stream`;
    base.pathname = path;
    base.search = new URLSearchParams({ ticket }).toString();
    base.hash = "";
    base.protocol = base.protocol === "https:" ? "wss:" : "ws:";
    return base.toString();
}

export function decodeLanCollaborationCanvasUpdate(update: Pick<LanCollaborationCanvasUpdate, "updateBase64">) {
    return decodeLanSharingUpdate(update.updateBase64);
}
