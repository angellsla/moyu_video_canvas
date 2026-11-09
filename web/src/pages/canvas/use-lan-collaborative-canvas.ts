import * as Y from "yjs";
import { Awareness, removeAwarenessStates } from "y-protocols/awareness";
import { useEffect, useMemo, useRef, useState } from "react";

import { scopedStorageKey } from "@/lib/user-scope";
import { captureUserScope, userScopeMatches } from "@/lib/user-scope-guard";
import { localForageInstance, SHARED_CATALOG_STORE_NAME } from "@/lib/localforage-storage";
import {
    createLanCollaborationStreamTicket,
    decodeLanCollaborationCanvasUpdate,
    getLanCollaborationCanvasStreamUrl,
    getLanCollaborationCanvasUpdates,
    nextLanCollaborationUpdateCursor,
    publishLanCollaborationCanvasUpdate,
    type LanCollaborationServerFrame,
} from "@/services/api/lan-collaboration";
import { encodeLanSharingUpdate } from "@/services/api/lan-sharing";
import { applyPeerLanAwareness, encodeLocalLanAwareness, isLanAwarenessFrame } from "@/services/lan-collaboration-awareness";
import { getLanSharingPeers, getLanSharingStatus } from "@/services/api/lan-sharing";
import {
    applyCollaborativeCanvasUpdate,
    applyCollaborativeCanvasLocalChanges,
    createCollaborativeCanvasDocument,
    encodeCollaborativeCanvasState,
    readPortableSharedCanvasContent,
    restoreLocalSharedCanvasContent,
    retainUnresolvedSharedCanvasAssets,
    type SharedCanvasContent,
    type PortableSharedCanvasContent,
} from "@/services/collaborative-canvas-document";
import { createSharedCanvasLocalAssetResolver, getSharedCanvasLocalAssetBindings, getSharedCanvasAssetReadiness, makePortableSharedCanvasContent, prepareSharedCanvasLocalAssetValues, queueSharedCanvasAssetRetries, type SharedCanvasAssetReadiness } from "@/services/lan-collaboration-assets";
import { resourceFileUrl, resourceStorageKey } from "@/services/api/resources";
import { useCanvasStore, type CanvasProject } from "@/stores/canvas/use-canvas-store";
import type { ViewportTransform } from "@/types/canvas";

type PresenceState = { x?: number; y?: number; selectionNodeIds?: string[] };
export type LanCanvasPresence = { peerId: string; nickname: string; state: PresenceState; expiresAt?: string; online?: boolean; collaborating?: boolean };
type LanCanvasPeerStatus = { peerId: string; nickname: string; online: boolean };

type Options = {
    project: CanvasProject | undefined;
    projectLoaded: boolean;
    containerRef: React.RefObject<HTMLDivElement | null>;
    viewport: ViewportTransform;
    selectedNodeIds: Set<string>;
    onRemoteContent?: (content: SharedCanvasContent) => void;
};

const ORIGIN_REMOTE = Symbol("lan-collaborative-canvas-remote");
const PRESENCE_THROTTLE_MS = 80;
const EDIT_BATCH_MS = 40;
function stateKey(roomId: string, scope: string) {
    return scopedStorageKey(`lan-canvas:${roomId}:state`, scope);
}

function outboxKey(roomId: string, scope: string) {
    return scopedStorageKey(`lan-canvas:${roomId}:outbox`, scope);
}

function cursorKey(roomId: string, scope: string) {
    return scopedStorageKey(`lan-canvas:${roomId}:cursor`, scope);
}

function localAssetRetryKey(roomId: string, scope: string) {
    return scopedStorageKey(`lan-canvas:${roomId}:local-asset-retry`, scope);
}

function parsePresence(value: unknown): PresenceState | null {
    if (!value || typeof value !== "object") return null;
    const candidate = value as Record<string, unknown>;
    if (candidate.online === false) return null;
    const x = typeof candidate.x === "number" && Number.isFinite(candidate.x) ? candidate.x : undefined;
    const y = typeof candidate.y === "number" && Number.isFinite(candidate.y) ? candidate.y : undefined;
    const selectionNodeIds = Array.isArray(candidate.selectionNodeIds) ? candidate.selectionNodeIds.filter((item): item is string => typeof item === "string").slice(0, 100) : [];
    if (x === undefined && y === undefined && !selectionNodeIds.length && candidate.online !== true) return null;
    return { x, y, selectionNodeIds };
}

function collectContentHashes(value: unknown, output = new Set<string>()) {
    if (Array.isArray(value)) {
        value.forEach((item) => collectContentHashes(item, output));
    } else if (value && typeof value === "object") {
        const marker = value as Record<string, unknown>;
        if (marker.__beeftvSharedAsset === true && typeof marker.contentHash === "string") output.add(marker.contentHash);
        for (const nested of Object.values(value)) collectContentHashes(nested, output);
    }
    return [...output];
}

function projectContent(project: CanvasProject): SharedCanvasContent {
    return {
        title: project.title,
        nodes: project.nodes,
        connections: project.connections,
        directorScenes: project.directorScenes || [],
        timeline: project.timeline,
    };
}

export function useLanCollaborativeCanvas({ project, projectLoaded, containerRef, viewport, selectedNodeIds, onRemoteContent }: Options) {
    const [presence, setPresence] = useState<LanCanvasPresence[]>([]);
    const [peerInventory, setPeerInventory] = useState<LanCanvasPeerStatus[]>([]);
    const [connected, setConnected] = useState(false);
    const [syncError, setSyncError] = useState("");
    const [localAssetError, setLocalAssetError] = useState("");
    const [transferError, setTransferError] = useState("");
    const [missingAssets, setMissingAssets] = useState<string[]>([]);
    const [assetReadiness, setAssetReadiness] = useState<SharedCanvasAssetReadiness | null>(null);
    const socketRef = useRef<WebSocket | null>(null);
    const localNodeIdRef = useRef("");
    const nicknamesRef = useRef(new Map<string, string>());
    const localNicknameRef = useRef("本机");
    const onRemoteContentRef = useRef(onRemoteContent);
    onRemoteContentRef.current = onRemoteContent;
    const selectedNodeIdsRef = useRef(selectedNodeIds);
    const viewportRef = useRef(viewport);
    selectedNodeIdsRef.current = selectedNodeIds;
    viewportRef.current = viewport;
    const room = project?.sharedCanvasId ? project : undefined;

    const members = useMemo(() => {
        const active = new Map(presence.map((member) => [member.peerId, member]));
        const result = peerInventory.map((peer) => ({
            peerId: peer.peerId,
            nickname: active.get(peer.peerId)?.nickname || peer.nickname,
            state: active.get(peer.peerId)?.state || {},
            online: peer.online || active.has(peer.peerId),
            collaborating: active.has(peer.peerId),
        }));
        if (localNodeIdRef.current) result.unshift({
            peerId: localNodeIdRef.current,
            nickname: localNicknameRef.current,
            state: {},
            online: true,
            collaborating: connected,
        });
        return result;
    }, [peerInventory, presence, connected]);

    useEffect(() => {
        if (!room || !projectLoaded || !room.sharedCanvasAssetCatalogId) return;
        const roomId = room.sharedCanvasId!;
        const localProjectId = room.id;
        const catalogId = room.sharedCanvasAssetCatalogId;
        const scope = captureUserScope();
        let disposed = false;
        const alive = () => !disposed && userScopeMatches(scope);
        let reconnectTimer: number | undefined;
        let pollTimer: number | undefined;
        let cursorTimer: number | undefined;
        let peerTimer: number | undefined;
        let presenceHeartbeatTimer: number | undefined;
        let lastPresenceAt = 0;
        let initialized = false;
        let catchUpInFlight: Promise<void> | null = null;
        let cursor = "";
        let retryLocalAssets = false;
        let lastPortable = "";
        let projectedPortable: PortableSharedCanvasContent = { title: "", nodes: [], connections: [], directorScenes: [] };
        let skipStoreWrite = false;
        let localChangePending = false;
        let localConversionRunning = false;
        let localSyncTimer: number | undefined;
        let syncVersion = 0;
        let assetsInFlight: Promise<void> | null = null;
        let requestedAssets = "";
        let localAssets: Record<string, string> = {};
        const assetHashCache = new Map<string, string>();
        let persistTail = Promise.resolve();
        let flushTail = Promise.resolve();
        const pendingUpdates = new Map<string, Uint8Array>();
        setSyncError("");
        setLocalAssetError("");
        setTransferError("");
        setPresence([]);
        setPeerInventory([]);
        setConnected(false);
        setMissingAssets([]);
        setAssetReadiness(null);
        const store = localForageInstance(SHARED_CATALOG_STORE_NAME);
        const doc = createCollaborativeCanvasDocument();
        const awareness = new Awareness(doc);
        awareness.setLocalState(null);
        const awarenessOwners = new Map<number, string>();
        const peerAwarenessClients = new Map<string, Map<number, number>>();
        const cachedProject = useCanvasStore.getState().projects.find((item) => item.id === room.id);
        let projectedLocal = projectContent(cachedProject || room);

        const rememberLocalAssets = (resources: Record<string, string>) => {
            localAssets = resources;
            for (const [hash, id] of Object.entries(resources)) {
                assetHashCache.set(id, hash);
                assetHashCache.set(resourceStorageKey(id), hash);
                assetHashCache.set(resourceFileUrl(id), hash);
            }
        };

        const refreshPeerPresence = (peerId: string, expiresAt?: string, fallback?: PresenceState | null) => {
            let state = fallback || null;
            const clients = peerAwarenessClients.get(peerId);
            if (clients) {
                for (const clientId of clients.keys()) {
                    const clientState = parsePresence(awareness.getStates().get(clientId));
                    if (clientState) state = clientState;
                }
            }
            setPresence((current) => {
                const next = current.filter((member) => member.peerId !== peerId);
                if (state) next.push({ peerId, nickname: nicknamesRef.current.get(peerId) || peerId.slice(0, 8), state, expiresAt });
                return next;
            });
        };

        const onAwarenessUpdate = (_changes: unknown, origin: unknown) => {
            if (origin !== "local") return;
            const socket = socketRef.current;
            if (socket?.readyState === WebSocket.OPEN) socket.send(JSON.stringify({ type: "presence", state: encodeLocalLanAwareness(awareness) }));
        };
        awareness.on("update", onAwarenessUpdate);

        const persistDocument = () => {
            const state = encodeCollaborativeCanvasState(doc);
            const outbox = [...pendingUpdates.entries()].map(([id, update]) => [id, Array.from(update)] as [string, number[]]);
            const savedCursor = cursor;
            persistTail = persistTail.catch(() => undefined).then(async () => {
                await store.setItem(stateKey(roomId, scope.userScope), state);
                await store.setItem(outboxKey(roomId, scope.userScope), outbox);
                await store.setItem(cursorKey(roomId, scope.userScope), savedCursor);
            });
            return persistTail;
        };

        const applyRemoteContent = async () => {
            if (!alive() || localChangePending) return;
            const portable = readPortableSharedCanvasContent(doc);
            const hashes = collectContentHashes(portable);
            const nextMissing = hashes.filter((hash) => !localAssets[hash]);
            setMissingAssets(nextMissing);
            if (JSON.stringify(hashes.sort()) !== requestedAssets) void refreshAssets();
            const content = restoreLocalSharedCanvasContent(portable, createSharedCanvasLocalAssetResolver(localAssets));
            const serialized = JSON.stringify(portable);
            const documentChanged = serialized !== lastPortable;
            lastPortable = serialized;
            projectedPortable = portable;
            const localChanged = JSON.stringify(content) !== JSON.stringify(projectedLocal);
            projectedLocal = content;
            if (!documentChanged && !localChanged) return;
            skipStoreWrite = true;
            try {
                useCanvasStore.getState().updateProject(room.id, {
                    title: content.title || room.title,
                    nodes: content.nodes,
                    connections: content.connections,
                    directorScenes: content.directorScenes,
                    timeline: content.timeline,
                });
                onRemoteContentRef.current?.(content);
            } finally {
                skipStoreWrite = false;
            }
        };

        // Network probes and file transfers never hold up node/content projection.
        function refreshAssets(): Promise<void> {
            if (assetsInFlight) return assetsInFlight;
            const hashes = collectContentHashes(readPortableSharedCanvasContent(doc)).sort();
            requestedAssets = JSON.stringify(hashes);
            const run = async () => {
                if (!alive()) return;
                const missing = hashes.filter((hash) => !localAssets[hash]);
                if (missing.length) await queueSharedCanvasAssetRetries(catalogId, missing);
                const resources = await prepareSharedCanvasLocalAssetValues(catalogId, hashes);
                if (!alive()) return;
                rememberLocalAssets(resources);
                await applyRemoteContent();
                const readiness = await getSharedCanvasAssetReadiness(catalogId, hashes);
                if (alive()) {
                    setTransferError("");
                    if (requestedAssets === JSON.stringify(collectContentHashes(readPortableSharedCanvasContent(doc)).sort())) setAssetReadiness(readiness);
                }
            };
            assetsInFlight = run().catch((error) => {
                if (alive()) setTransferError(error instanceof Error ? error.message : "协作素材暂未送达，后台将重试");
            }).finally(() => {
                assetsInFlight = null;
                if (alive() && requestedAssets !== JSON.stringify(collectContentHashes(readPortableSharedCanvasContent(doc)).sort())) void refreshAssets();
            });
            return assetsInFlight;
        }

        const onDocUpdate = (update: Uint8Array, origin: unknown) => {
            if (origin !== ORIGIN_REMOTE) {
                const id = encodeLanSharingUpdate(update);
                pendingUpdates.set(id, new Uint8Array(update));
            }
            void persistDocument().catch((error) => setSyncError(error instanceof Error ? error.message : "协作画布本地缓存失败"));
            if (origin === ORIGIN_REMOTE) void applyRemoteContent().catch((error) => setSyncError(error instanceof Error ? error.message : "协作画布内容读取失败"));
            else void flushUpdates().catch((error) => {
                if (alive()) setSyncError(error instanceof Error ? error.message : "协作画布本地缓存失败");
            });
        };
        doc.on("update", onDocUpdate);

        function catchUp() {
            if (catchUpInFlight) return catchUpInFlight;
            const run = async () => {
                let after = cursor;
                for (;;) {
                    if (!alive()) return;
                    const page = await getLanCollaborationCanvasUpdates(roomId, after);
                    if (!alive()) return;
                    if (page.updates.length) applyCollaborativeCanvasUpdate(doc, Y.mergeUpdates(page.updates.map(decodeLanCollaborationCanvasUpdate)), ORIGIN_REMOTE);
                    after = nextLanCollaborationUpdateCursor(after, page);
                    cursor = after;
                    await persistDocument();
                    if (!page.hasMore) break;
                }
                if (readPortableSharedCanvasContent(doc).nodes.length || readPortableSharedCanvasContent(doc).title) await applyRemoteContent();
            };
            catchUpInFlight = run().finally(() => { catchUpInFlight = null; });
            return catchUpInFlight;
        }

        async function flushUpdates() {
            flushTail = flushTail.catch(() => undefined).then(async () => {
                for (const [id, update] of [...pendingUpdates]) {
                    if (!alive()) return;
                    try {
                        await publishLanCollaborationCanvasUpdate(roomId, update);
                        pendingUpdates.delete(id);
                    } catch (error) {
                        if (!disposed) setSyncError(error instanceof Error ? error.message : "协作更新暂未送达");
                        break;
                    }
                }
                await persistDocument();
                if (alive() && !pendingUpdates.size) setSyncError("");
            });
            return flushTail;
        }

        async function syncLocalContent() {
            if (!alive() || !initialized || localConversionRunning) return;
            const current = useCanvasStore.getState().projects.find((item) => item.id === localProjectId);
            if (!current) return;
            const version = syncVersion;
            const currentContent = projectContent(current);
            const basePortable = projectedPortable;
            const baseLocal = projectedLocal;
            localConversionRunning = true;
            try {
                const prepared = await makePortableSharedCanvasContent(catalogId, currentContent, assetHashCache);
                const resources = await getSharedCanvasLocalAssetBindings(catalogId);
                if (!alive()) return;
                rememberLocalAssets(resources);
                const portable = retainUnresolvedSharedCanvasAssets(prepared, basePortable, baseLocal, currentContent);
                // Commit even during continuous dragging; subsequent edits use this
                // local baseline, not remote fields the user has not seen yet.
                applyCollaborativeCanvasLocalChanges(doc, basePortable, portable);
                projectedPortable = portable;
                projectedLocal = currentContent;
                if (version !== syncVersion) return;
                localChangePending = false;
                retryLocalAssets = false;
                await store.setItem(localAssetRetryKey(roomId, scope.userScope), false);
                if (version === syncVersion) await applyRemoteContent();
                if (alive()) setLocalAssetError("");
            } catch (error) {
                if (alive()) setLocalAssetError(error instanceof Error ? error.message : "共享素材准备失败");
            } finally {
                localConversionRunning = false;
                if (alive() && version !== syncVersion) scheduleLocalSync();
            }
        }

        function scheduleLocalSync() {
            if (localSyncTimer !== undefined || localConversionRunning || !initialized) return;
            localSyncTimer = window.setTimeout(() => {
                localSyncTimer = undefined;
                void syncLocalContent();
            }, EDIT_BATCH_MS);
        }

        async function connect() {
            if (!alive()) return;
            try {
                const [ticketResponse, status, peerState] = await Promise.all([
                    createLanCollaborationStreamTicket(roomId),
                    getLanSharingStatus(),
                    getLanSharingPeers(),
                ]);
                if (!alive()) return;
                localNodeIdRef.current = status.nodeId;
                localNicknameRef.current = status.nickname || "本机";
                nicknamesRef.current.clear();
                const discoveredById = new Map(peerState.discovered.map((peer) => [peer.peerId, peer]));
                for (const peer of peerState.trusted) nicknamesRef.current.set(peer.peerId, discoveredById.get(peer.peerId)?.nickname || peer.nickname || peer.peerId.slice(0, 8));
                for (const peer of peerState.discovered) if (peer.nickname) nicknamesRef.current.set(peer.peerId, peer.nickname);
                setPeerInventory(peerState.trusted.map((peer) => ({
                    peerId: peer.peerId,
                    nickname: nicknamesRef.current.get(peer.peerId) || peer.peerId.slice(0, 8),
                    online: discoveredById.has(peer.peerId),
                })));
                if (disposed) return;
                const socket = new WebSocket(getLanCollaborationCanvasStreamUrl(roomId, ticketResponse.ticket));
                socketRef.current = socket;
                socket.onopen = () => {
                    if (disposed) return;
                    setConnected(true);
                    setSyncError("");
                    awareness.setLocalState({ online: true, selectionNodeIds: [...selectedNodeIdsRef.current].slice(0, 100) });
                    void catchUp().then(() => flushUpdates()).catch((error) => setSyncError(error instanceof Error ? error.message : "协作画布同步失败"));
                };
                socket.onmessage = (event) => {
                    if (!alive()) return;
                    let frame: LanCollaborationServerFrame;
                    try { frame = JSON.parse(String(event.data)) as LanCollaborationServerFrame; } catch { return; }
                    if (frame.type === "error") { setSyncError(frame.message); return; }
                    if (frame.type === "update") {
                        if (frame.originNodeId !== localNodeIdRef.current) applyCollaborativeCanvasUpdate(doc, decodeLanCollaborationCanvasUpdate(frame), ORIGIN_REMOTE);
                    } else if (frame.type === "presence" && frame.peerId !== localNodeIdRef.current) {
                        const peerId = frame.peerId;
                        const state = frame.state;
                        if (state && typeof state === "object" && "online" in state && state.online === false) {
                            const clients = peerAwarenessClients.get(peerId);
                            if (clients) removeAwarenessStates(awareness, [...clients.keys()], "peer-offline");
                            for (const clientId of clients?.keys() || []) awarenessOwners.delete(clientId);
                            peerAwarenessClients.delete(peerId);
                            refreshPeerPresence(peerId);
                        } else if (isLanAwarenessFrame(state)) {
                            const clientId = applyPeerLanAwareness(awareness, peerId, state, awarenessOwners);
                            if (clientId === null) return;
                            let clients = peerAwarenessClients.get(peerId);
                            if (!clients) {
                                clients = new Map<number, number>();
                                peerAwarenessClients.set(peerId, clients);
                            }
                            if (awareness.getStates().has(clientId)) clients.set(clientId, Date.parse(frame.expiresAt));
                            else clients.delete(clientId);
                            if (!clients.size) peerAwarenessClients.delete(peerId);
                            refreshPeerPresence(peerId, frame.expiresAt);
                        } else {
                            refreshPeerPresence(peerId, frame.expiresAt, parsePresence(state));
                        }
                    }
                };
                socket.onclose = () => {
                    if (socketRef.current === socket) socketRef.current = null;
                    if (!disposed) {
                        awareness.setLocalState(null);
                        setConnected(false);
                        setPresence([]);
                        for (const [peerId, clients] of peerAwarenessClients) {
                            removeAwarenessStates(awareness, [...clients.keys()], "disconnect");
                            peerAwarenessClients.delete(peerId);
                        }
                        awarenessOwners.clear();
                        reconnectTimer = window.setTimeout(() => void connect(), 1_500);
                    }
                };
                socket.onerror = () => setConnected(false);
            } catch (error) {
                if (!disposed) {
                    setSyncError(error instanceof Error ? error.message : "协作连接失败");
                    reconnectTimer = window.setTimeout(() => void connect(), 2_500);
                }
            }
        }

        const readStored = async () => {
            rememberLocalAssets(await getSharedCanvasLocalAssetBindings(catalogId));
            const stored = await store.getItem<Uint8Array>(stateKey(roomId, scope.userScope));
            const pending = await store.getItem<Array<[string, number[]]>>(outboxKey(roomId, scope.userScope));
            cursor = (await store.getItem<string>(cursorKey(roomId, scope.userScope))) || "";
            retryLocalAssets = (await store.getItem<boolean>(localAssetRetryKey(roomId, scope.userScope))) === true || retryLocalAssets;
            if (!alive()) return;
            localChangePending = localChangePending || retryLocalAssets;
            for (const [id, bytes] of pending || []) pendingUpdates.set(id, new Uint8Array(bytes));
            if (stored) applyCollaborativeCanvasUpdate(doc, new Uint8Array(stored), ORIGIN_REMOTE);
            if (stored) {
                if (localChangePending) {
                    projectedPortable = readPortableSharedCanvasContent(doc);
                    projectedLocal = restoreLocalSharedCanvasContent(projectedPortable, createSharedCanvasLocalAssetResolver(localAssets));
                }
                await applyRemoteContent();
            }
            initialized = true;
            if (localChangePending) scheduleLocalSync();
            if (!disposed) void connect();
        };

        void readStored().catch((error) => setSyncError(error instanceof Error ? error.message : "协作画布缓存读取失败"));
        pollTimer = window.setInterval(() => {
            if (!alive() || !initialized) return;
            void catchUp().then(() => flushUpdates()).catch(() => undefined);
            void refreshAssets();
            if (retryLocalAssets) scheduleLocalSync();
        }, 5_000);
        cursorTimer = window.setInterval(() => {
            if (!alive()) return;
            const socket = socketRef.current;
            const container = containerRef.current;
            if (!socket || socket.readyState !== WebSocket.OPEN || !container) return;
            const rect = container.getBoundingClientRect();
            const pointer = pointerRef.current;
            const now = Date.now();
            if (now - lastPresenceAt < PRESENCE_THROTTLE_MS) return;
            lastPresenceAt = now;
            const currentViewport = viewportRef.current;
            const nextState = {
                online: true,
                ...(pointer ? { x: (pointer.x - rect.left - currentViewport.x) / currentViewport.k, y: (pointer.y - rect.top - currentViewport.y) / currentViewport.k } : {}),
                selectionNodeIds: [...selectedNodeIdsRef.current].slice(0, 100),
            };
            if (JSON.stringify(nextState) !== JSON.stringify(awareness.getLocalState())) awareness.setLocalState(nextState);
        }, PRESENCE_THROTTLE_MS);
        peerTimer = window.setInterval(() => {
            void getLanSharingPeers().then((peerState) => {
                const discoveredById = new Map(peerState.discovered.map((peer) => [peer.peerId, peer]));
                for (const peer of peerState.trusted) nicknamesRef.current.set(peer.peerId, discoveredById.get(peer.peerId)?.nickname || peer.nickname || peer.peerId.slice(0, 8));
                setPeerInventory(peerState.trusted.map((peer) => ({
                    peerId: peer.peerId,
                    nickname: nicknamesRef.current.get(peer.peerId) || peer.peerId.slice(0, 8),
                    online: discoveredById.has(peer.peerId),
                })));
            }).catch(() => undefined);
        }, 10_000);
        presenceHeartbeatTimer = window.setInterval(() => {
            const socket = socketRef.current;
            if (socket?.readyState === WebSocket.OPEN) {
                awareness.setLocalState({ ...awareness.getLocalState(), online: true, selectionNodeIds: [...selectedNodeIdsRef.current].slice(0, 100) });
            }
        }, 15_000);
        const presenceExpiryTimer = window.setInterval(() => {
            const now = Date.now();
            for (const [peerId, clients] of peerAwarenessClients) {
                let expired = false;
                for (const [clientId, expiresAt] of clients) {
                    if (expiresAt > now && awareness.getStates().has(clientId)) continue;
                    removeAwarenessStates(awareness, [clientId], "presence-expired");
                    clients.delete(clientId);
                    awarenessOwners.delete(clientId);
                    expired = true;
                }
                if (!expired) continue;
                if (!clients.size) peerAwarenessClients.delete(peerId);
                const nextExpiry = clients.size ? new Date(Math.max(...clients.values())).toISOString() : undefined;
                refreshPeerPresence(peerId, nextExpiry);
            }
            setPresence((current) => current.filter((member) => !member.expiresAt || Date.parse(member.expiresAt) > now));
        }, 1_000);
        const pointerRef = { current: null as { x: number; y: number } | null };
        const onPointerMove = (event: PointerEvent) => { pointerRef.current = { x: event.clientX, y: event.clientY }; };
        const onPointerLeave = () => {
            pointerRef.current = null;
            const socket = socketRef.current;
            if (socket?.readyState === WebSocket.OPEN) awareness.setLocalState({ online: true, selectionNodeIds: [...selectedNodeIdsRef.current].slice(0, 100) });
        };
        containerRef.current?.addEventListener("pointermove", onPointerMove);
        containerRef.current?.addEventListener("pointerleave", onPointerLeave);

        const unsubscribeStore = useCanvasStore.subscribe((state, previous) => {
            if (!alive() || skipStoreWrite) return;
            const current = state.projects.find((item) => item.id === room.id);
            const before = previous.projects.find((item) => item.id === room.id);
            if (!current || !before || current === before) return;
            if (JSON.stringify(projectContent(current)) === JSON.stringify(projectContent(before))) return;
            localChangePending = true;
            retryLocalAssets = true;
            void store.setItem(localAssetRetryKey(roomId, scope.userScope), true).catch(() => undefined);
            syncVersion += 1;
            scheduleLocalSync();
        });

        return () => {
            disposed = true;
            if (localSyncTimer !== undefined) window.clearTimeout(localSyncTimer);
            if (reconnectTimer) window.clearTimeout(reconnectTimer);
            if (pollTimer) window.clearInterval(pollTimer);
            if (cursorTimer) window.clearInterval(cursorTimer);
            if (peerTimer) window.clearInterval(peerTimer);
            if (presenceHeartbeatTimer) window.clearInterval(presenceHeartbeatTimer);
            window.clearInterval(presenceExpiryTimer);
            containerRef.current?.removeEventListener("pointermove", onPointerMove);
            containerRef.current?.removeEventListener("pointerleave", onPointerLeave);
            unsubscribeStore();
            void persistDocument().catch(() => undefined);
            awareness.setLocalState(null);
            awareness.off("update", onAwarenessUpdate);
            awareness.destroy();
            socketRef.current?.close();
            socketRef.current = null;
            setConnected(false);
            setPresence([]);
            setPeerInventory([]);
            setMissingAssets([]);
            setAssetReadiness(null);
            doc.off("update", onDocUpdate);
            doc.destroy();
        };
    }, [room?.id, room?.sharedCanvasId, room?.sharedCanvasAssetCatalogId, projectLoaded, containerRef]);

    return { members, presence, connected, syncError: syncError || localAssetError || transferError, missingAssets, assetReadiness };
}
