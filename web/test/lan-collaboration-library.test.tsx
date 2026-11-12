import { expect, mock, test } from "bun:test";
import * as React from "react";
import { apiClient } from "@/services/api/request";
import { encodeLanSharingUpdate } from "@/services/api/lan-sharing";
import { createCollaborativeCanvasDocument, encodeCollaborativeCanvasState, toPortableSharedCanvasContent } from "@/services/collaborative-canvas-document";
import { localForageStorageForScope } from "@/lib/localforage-storage";
import { getActiveUserScope } from "@/lib/user-scope";
import type { CanvasProject } from "@/stores/canvas/use-canvas-store";
import type { LanCollaborationCanvas } from "@/services/api/lan-collaboration";

// Call the actual open-room handler with real Yjs, scoped storage and API
// adapters; only the component shell/store are replaced. Run in isolation.
let rooms: LanCollaborationCanvas[] = [];
let stateIndex = 0;
const errors: unknown[] = [];
mock.module("react", () => ({
    ...React,
    useState: (initial: unknown) => [stateIndex++ === 0 ? rooms : initial, () => {}],
    useMemo: (compute: () => unknown) => compute(),
    useCallback: (fn: unknown) => fn,
    useEffect: () => {},
}));
mock.module("antd", () => ({
    App: { useApp: () => ({ message: { error: (value: unknown) => errors.push(value), success: () => {} }, modal: {} }) },
    Button: "button", Input: "input", Modal: "dialog", Select: "select", Tag: "span",
}));
const navigations: string[] = [];
mock.module("react-router", () => ({ useNavigate: () => (url: string) => { navigations.push(url); } }));
const saved: Partial<CanvasProject>[] = [];
let persisted = false;
mock.module("@/stores/canvas/use-canvas-store", () => ({
    useCanvasStore: { getState: () => ({
        projects: [], createProject: () => "local-room", updateProject: (_id: string, content: Partial<CanvasProject>) => { saved.push(content); },
    }) },
    flushCanvasStorePersistence: async () => { persisted = true; },
}));

const { LanCollaborationLibrary } = await import("@/pages/canvas/lan-collaboration-library");
function findOpen(node: React.ReactNode): (() => void) | undefined {
    for (const child of React.Children.toArray(node)) {
        if (!React.isValidElement(child)) continue;
        const props = child.props as { children?: React.ReactNode; onClick?: () => void };
        if (props.children === "同步并打开") return props.onClick;
        const found = findOpen(props.children);
        if (found) return found;
    }
}

test("first open queues offline media without starting a transfer or losing the shared graph", async () => {
    const originalWindow = globalThis.window;
    Object.defineProperty(globalThis, "window", { configurable: true, value: originalWindow || {} });
    const catalogId = `open-${crypto.randomUUID()}`;
    const roomId = `room-${crypto.randomUUID()}`;
    rooms = [{ id: roomId, name: "Shared", createdByNodeId: "peer", assetCatalogId: catalogId, archived: false, updateCount: 1, createdAt: "", updatedAt: "" }];
    const doc = createCollaborativeCanvasDocument(toPortableSharedCanvasContent({
        title: "Shared", nodes: [{ id: "image", type: "image", title: "Uploaded", position: { x: 10, y: 20 }, width: 80, height: 80, metadata: { storageKey: "resource:remote", content: "blob:remote" } }], connections: [], directorScenes: [],
    }, () => "hash-image"));
    const original = apiClient.request;
    const calls: string[] = [];
    apiClient.request = (async (config) => {
        const url = config.url || "";
        calls.push(url);
        if (url !== `/lan-sharing/canvases/${roomId}/updates`) throw new Error(`first open tried to fetch media: ${url}`);
        return { status: 200, headers: {}, data: { code: 0, msg: "ok", data: {
            updates: [{ id: "update", updateBase64: encodeLanSharingUpdate(encodeCollaborativeCanvasState(doc)), createdAt: "", originNodeId: "peer" }], cursor: "update", hasMore: false,
        } } };
    }) as typeof apiClient.request;
    try {
        stateIndex = 0;
        const open = findOpen(LanCollaborationLibrary({ projects: [] }));
        expect(open).toBeDefined();
        open!();
        for (let i = 0; i < 100 && !navigations.length && !errors.length; i++) await new Promise((resolve) => setTimeout(resolve, 5));
        expect(errors).toEqual([]);
        expect(navigations).toEqual(["/canvas/local-room"]);
        expect(persisted).toBe(true);
        expect(saved[0]).toMatchObject({ sharedCanvasId: roomId, sharedCanvasAssetCatalogId: catalogId, nodes: [{ id: "image", position: { x: 10, y: 20 } }] });
        expect(JSON.stringify(saved)).not.toContain("blob:remote");
        const queued = JSON.parse(await localForageStorageForScope(getActiveUserScope()).getItem("lan-sharing:asset-retry-queue") || "{}");
        expect(queued[catalogId]).toEqual(["hash-image"]);
        expect(calls).toEqual([`/lan-sharing/canvases/${roomId}/updates`]);
    } finally {
        apiClient.request = original;
        doc.destroy();
        Object.defineProperty(globalThis, "window", { configurable: true, value: originalWindow });
    }
});
