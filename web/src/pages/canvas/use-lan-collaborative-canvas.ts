import * as Y from "yjs";
import { Awareness, removeAwarenessStates } from "y-protocols/awareness";
import { useEffect, useMemo, useRef, useState } from "react";

import { scopedStorageKey, getActiveUserScope } from "@/lib/user-scope";
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
    type SharedCanvasContent,
    type PortableSharedCanvasContent,
} from "@/services/collaborative-canvas-document";
import { createSharedCanvasLocalAssetResolver, getSharedCanvasAssetReadiness, makePortableSharedCanvasContent, prepareSharedCanvasLocalAssetValues, queueSharedCanvasAssetRetries, retrySharedCanvasAssetTransfer, type SharedCanvasAssetReadiness } from "@/services/lan-collaboration-assets";
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
const PRESENCE_THROTTLE_MS = 250;
function stateKey(roomId: string) {
    return scopedStorageKey(`lan-canvas:${roomId}:state`, getActiveUserScope());
}

function outboxKey(roomId: string) {
    return scopedStorageKey(`lan-canvas:${roomId}:outbox`, getActiveUserScope());
}

function cursorKey(roomId: string) {
    return scopedStorageKey(`lan-canvas:${roomId}:cursor`, getActiveUserScope());
}

function localAssetRetryKey(roomId: string) {
    return scopedStorageKey(`lan-canvas:${roomId}:local-asset-retry`, getActiveUserScope());
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
        const catalogId = room.sharedCanvasAssetCatalogId;
        let disposed = false;
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
        let missingAssetHashes: string[] = [];
        let skipStoreWrite = false;
        let localChangePending = false;
        let localConversionsInFlight = 0;
        let syncVersion = 0;
        let remoteProjectionVersion = 0;
        let persistTail = Promise.resolve();
        let flushTail = Promise.resolve();
        const pendingUpdates = new Map<string, Uint8Array>();
        setSyncError("");
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
                await store.setItem(stateKey(roomId), state);
                await store.setItem(outboxKey(roomId), outbox);
                await store.setItem(cursorKey(roomId), savedCursor);
            });
            return persistTail;
        };

        const applyRemoteContent = async () => {
            if (localChangePending) return;
            const version = ++remoteProjectionVersion;
            const portable = readPortableSharedCanvasContent(doc);
            const hashes = collectContentHashes(portable);
            let localAssets: Record<string, string> = {};
            if (hashes.length) localAssets = await prepareSharedCanvasLocalAssetValues(catalogId, hashes);
            if (disposed || version !== remoteProjectionVersion) return;
            const nextMissing = hashes.filter((hash) => !localAssets[hash]);
            const previouslyMissing = missingAssetHashes;
            const assetsRecovered = previouslyMissing.some((hash) => !nextMissing.includes(hash));
            missingAssetHashes = nextMissing;
            setMissingAssets(nextMissing);
            if (nextMissing.length) void queueSharedCanvasAssetRetries(catalogId, nextMissing).catch(() => undefined);
            void getSharedCanvasAssetReadiness(catalogId, hashes).then((status) => {
                if (!disposed && version === remoteProjectionVersion) setAssetReadiness(status);
            }).catch(() => undefined);
            const content = restoreLocalSharedCanvasContent(portable, createSharedCanvasLocalAssetResolver(localAssets));
            const serialized = JSON.stringify(portable);
            const documentChanged = serialized !== lastPortable;
            lastPortable = serialized;
            projectedPortable = portable;
            if (!documentChanged && !assetsRecovered) return;
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

        const onDocUpdate = (update: Uint8Array, origin: unknown) => {
            if (origin !== ORIGIN_REMOTE) {
                const id = encodeLanSharingUpdate(update);
                pendingUpdates.set(id, new Uint8Array(update));
            }
            void persistDocument().catch((error) => setSyncError(error instanceof Error ? error.message : "协作画布本地缓存失败"));
            if (origin === ORIGIN_REMOTE) void applyRemoteContent().catch((error) => setSyncError(error instanceof Error ? error.message : "协作画布内容读取失败"));
            else void flushUpdates();
        };
        doc.on("update", onDocUpdate);

        function catchUp() {
            if (catchUpInFlight) return catchUpInFlight;
            const run = async () => {
                let after = cursor;
                for (;;) {
                    const page = await getLanCollaborationCanvasUpdates(roomId, after);
                    if (disposed) return;
                    for (const update of page.updates) applyCollaborativeCanvasUpdate(doc, decodeLanCollaborationCanvasUpdate(update), ORIGIN_REMOTE);
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
                    if (disposed) return;
                    try {
                        await publishLanCollaborationCanvasUpdate(roomId, update);
                        pendingUpdates.delete(id);
                        const socket = socketRef.current;
                        if (socket?.readyState === WebSocket.OPEN) socket.send(JSON.stringify({ type: "update", updateBase64: encodeLanSharingUpdate(update) }));
                    } catch (error) {
                        if (!disposed) setSyncError(error instanceof Error ? error.message : "协作更新暂未送达");
                        break;
                    }
                }
                await persistDocument();
            });
            return flushTail;
        }

        async function retryAssets() {
            if (localConversionsInFlight) return;
            if (missingAssetHashes.length) {
                await retrySharedCanvasAssetTransfer(catalogId, missingAssetHashes);
                await applyRemoteContent();
            }
            if (!retryLocalAssets) return;
            retryLocalAssets = false;
            await retrySharedCanvasAssetTransfer(catalogId, []);
            const current = useCanvasStore.getState().projects.find((item) => item.id === cachedProject?.id);
            if (!current) return;
            const portable = await makePortableSharedCanvasContent(catalogId, projectContent(current));
            const serialized = JSON.stringify(portable);
            if (serialized !== lastPortable) {
                applyCollaborativeCanvasLocalChanges(doc, projectedPortable, portable);
            }
            localChangePending = false;
            await applyRemoteContent();
            await store.setItem(localAssetRetryKey(roomId), false);
        }

        async function connect() {
            if (disposed) return;
            try {
                const [ticketResponse, status, peerState] = await Promise.all([
                    createLanCollaborationStreamTicket(roomId),
                    getLanSharingStatus(),
                    getLanSharingPeers(),
                ]);
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
            const stored = await store.getItem<Uint8Array>(stateKey(roomId));
            const pending = await store.getItem<Array<[string, number[]]>>(outboxKey(roomId));
            cursor = (await store.getItem<string>(cursorKey(roomId))) || "";
            retryLocalAssets = (await store.getItem<boolean>(localAssetRetryKey(roomId))) === true;
            for (const [id, bytes] of pending || []) pendingUpdates.set(id, new Uint8Array(bytes));
            if (stored) applyCollaborativeCanvasUpdate(doc, new Uint8Array(stored), ORIGIN_REMOTE);
            if (stored) {
                await applyRemoteContent();
            }
            initialized = true;
            if (!disposed) void connect();
        };

        void readStored().catch((error) => setSyncError(error instanceof Error ? error.message : "协作画布缓存读取失败"));
        pollTimer = window.setInterval(() => {
            if (!initialized) return;
            void catchUp().then(() => flushUpdates()).catch(() => undefined);
            void retryAssets().catch((error) => {
                retryLocalAssets = true;
                if (!disposed) setSyncError(error instanceof Error ? error.message : "协作素材暂未送达，后台将重试");
            });
        }, 5_000);
        cursorTimer = window.setInterval(() => {
            const socket = socketRef.current;
            const container = containerRef.current;
            if (!socket || socket.readyState !== WebSocket.OPEN || !container) return;
            const rect = container.getBoundingClientRect();
            const pointer = pointerRef.current;
            if (!pointer) return;
            const now = Date.now();
            if (now - lastPresenceAt < PRESENCE_THROTTLE_MS) return;
            lastPresenceAt = now;
            const currentViewport = viewportRef.current;
            awareness.setLocalState({ online: true, x: (pointer.x - rect.left - currentViewport.x) / currentViewport.k, y: (pointer.y - rect.top - currentViewport.y) / currentViewport.k, selectionNodeIds: [...selectedNodeIdsRef.current].slice(0, 100) });
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
            if (skipStoreWrite) return;
            const current = state.projects.find((item) => item.id === room.id);
            const before = previous.projects.find((item) => item.id === room.id);
            if (!current || !before || current === before) return;
            if (JSON.stringify(projectContent(current)) === JSON.stringify(projectContent(before))) return;
            localChangePending = true;
            retryLocalAssets = true;
            void store.setItem(localAssetRetryKey(roomId), true).catch(() => undefined);
            const version = ++syncVersion;
            window.setTimeout(() => {
                if (disposed || version !== syncVersion) return;
                const latest = useCanvasStore.getState().projects.find((item) => item.id === room.id);
                if (!latest) return;
                localConversionsInFlight += 1;
                void makePortableSharedCanvasContent(catalogId, projectContent(latest)).then((portable) => {
                    if (disposed || version !== syncVersion) return;
                    const serialized = JSON.stringify(portable);
                    localChangePending = false;
                    retryLocalAssets = false;
                    void store.setItem(localAssetRetryKey(roomId), false).catch(() => undefined);
                    if (serialized !== lastPortable) applyCollaborativeCanvasLocalChanges(doc, projectedPortable, portable);
                    void applyRemoteContent().catch((error) => setSyncError(error instanceof Error ? error.message : "协作画布内容读取失败"));
                }).catch((error) => {
                    retryLocalAssets = true;
                    void store.setItem(localAssetRetryKey(roomId), true).catch(() => undefined);
                    setSyncError(error instanceof Error ? error.message : "共享素材准备失败");
                }).finally(() => { localConversionsInFlight -= 1; });
            }, 120);
        });

        return () => {
            disposed = true;
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

    return { members, presence, connected, syncError, missingAssets, assetReadiness };
}
