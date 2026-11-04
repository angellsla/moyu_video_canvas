import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent } from "react";
import { App, Button, Input, InputNumber, Select, Switch, Tooltip } from "antd";
import { Plus, RefreshCw, Save, Unlink, UserRoundPlus } from "lucide-react";

import { PageHeader } from "@/components/layout/workspace-page";
import { localForageStorageForScope } from "@/lib/localforage-storage";
import { getActiveUserScope, subscribeUserScope } from "@/lib/user-scope";
import {
    addLanSharingCatalogAsset,
    createLanSharingCatalog,
    decodeLanSharingUpdate,
    getLanSharingCatalogUpdates,
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
    type LanSharingAsset,
    type LanSharingCatalog,
    type LanSharingPeer,
    type LanSharingStatus,
    type LanSharingTrustedPeer,
} from "@/services/api/lan-sharing";
import type { RemoteResource } from "@/services/api/resources";
import { openSharedCatalog, type SharedCatalog } from "@/services/shared-catalog";

const LOCAL_RESOURCE_BINDINGS_KEY = "lan-sharing:local-resource-bindings";
const POLL_INTERVAL_MS = 2_000;

type ResourceBindings = Record<string, Record<string, string>>;
type PeerLocation = { peerId: string; address: string; port: number; nickname?: string; discovered: boolean };
type ManualPairForm = { peerId: string; address: string; port?: number; pairingCode: string; nickname: string };
type CatalogEntry = LanSharingAsset;

const emptyManualPair: ManualPairForm = { peerId: "", address: "", pairingCode: "", nickname: "" };

function errorMessage(error: unknown) {
    return error instanceof Error ? error.message : "请求失败，请重试";
}

function pairingExpiry(value?: string) {
    if (!value) return "";
    const timestamp = Date.parse(value);
    return Number.isNaN(timestamp) ? "" : `有效期至 ${new Date(timestamp).toLocaleString()}`;
}

function formatBytes(bytes: number) {
    if (bytes < 1024) return `${bytes} B`;
    if (bytes < 1024 ** 2) return `${(bytes / 1024).toFixed(1)} KB`;
    if (bytes < 1024 ** 3) return `${(bytes / 1024 ** 2).toFixed(1)} MB`;
    return `${(bytes / 1024 ** 3).toFixed(1)} GB`;
}

function isPrivateIPv4(address: string) {
    const parts = address.trim().split(".").map(Number);
    if (parts.length !== 4 || parts.some((part, index) => !/^\d{1,3}$/u.test(address.trim().split(".")[index]) || part < 0 || part > 255)) return false;
    const [first, second] = parts;
    return first === 10 || (first === 172 && second >= 16 && second <= 31) || (first === 192 && second === 168);
}

function parseBindings(raw: string | null): ResourceBindings {
    if (!raw) return {};
    try {
        const value = JSON.parse(raw) as unknown;
        if (!value || typeof value !== "object" || Array.isArray(value)) return {};
        const result: ResourceBindings = {};
        for (const [catalogId, resources] of Object.entries(value)) {
            if (!resources || typeof resources !== "object" || Array.isArray(resources)) continue;
            const bindings: Record<string, string> = {};
            for (const [contentHash, resourceId] of Object.entries(resources)) {
                if (typeof resourceId === "string" && resourceId) bindings[contentHash] = resourceId;
            }
            if (Object.keys(bindings).length) result[catalogId] = bindings;
        }
        return result;
    } catch {
        return {};
    }
}

function decodePeerLocations(discovered: LanSharingPeer[], trusted: LanSharingTrustedPeer[]): PeerLocation[] {
    const peers = new Map<string, PeerLocation>();
    for (const peer of trusted) {
        if (!peer.address || !peer.port) continue;
        peers.set(peer.peerId, {
            peerId: peer.peerId,
            address: peer.address,
            port: peer.port,
            nickname: peer.nickname,
            discovered: false,
        });
    }
    for (const peer of discovered) {
        if (!peer.address || !peer.port) continue;
        peers.set(peer.peerId, {
            peerId: peer.peerId,
            address: peer.address,
            port: peer.port,
            nickname: peer.nickname,
            discovered: true,
        });
    }
    return [...peers.values()].sort((left, right) => (left.nickname || left.peerId).localeCompare(right.nickname || right.peerId));
}

function resourceLabel(resource: RemoteResource) {
    const timestamp = resource.createdAt ? new Date(resource.createdAt).toLocaleDateString() : "";
    return `${resource.kind} · ${resource.mimeType || "未知类型"} · ${formatBytes(resource.size)}${timestamp ? ` · ${timestamp}` : ""}`;
}

function catalogCacheKey(scope: string, catalogId: string) {
    return `${scope}\0${catalogId}`;
}

export function LanSharingPane() {
    const { message, modal } = App.useApp();
    const [scope, setScope] = useState(getActiveUserScope);
    const [status, setStatus] = useState<LanSharingStatus>();
    const [discovered, setDiscovered] = useState<LanSharingPeer[]>([]);
    const [trusted, setTrusted] = useState<LanSharingTrustedPeer[]>([]);
    const [catalogs, setCatalogs] = useState<LanSharingCatalog[]>([]);
    const [resources, setResources] = useState<RemoteResource[]>([]);
    const [entriesByCatalog, setEntriesByCatalog] = useState<Record<string, CatalogEntry[]>>({});
    const [resourceBindings, setResourceBindings] = useState<ResourceBindings>({});
    const [resourceBindingsLoading, setResourceBindingsLoading] = useState(true);
    const [catalogName, setCatalogName] = useState("");
    const [resourceByCatalog, setResourceByCatalog] = useState<Record<string, string>>({});
    const [peerByEntry, setPeerByEntry] = useState<Record<string, string>>({});
    const [pairingPeer, setPairingPeer] = useState<PeerLocation>();
    const [manualPairOpen, setManualPairOpen] = useState(false);
    const [manualPair, setManualPair] = useState<ManualPairForm>(emptyManualPair);
    const [pairingCode, setPairingCode] = useState("");
    const [nickname, setNickname] = useState("");
    const [localDeviceName, setLocalDeviceName] = useState("");
    const [loading, setLoading] = useState(true);
    const [refreshing, setRefreshing] = useState(false);
    const [busy, setBusy] = useState("");
    const [loadError, setLoadError] = useState("");
    const [actionError, setActionError] = useState("");
    const catalogDocuments = useRef(new Map<string, SharedCatalog>());
    const catalogUnsubscribers = useRef(new Map<string, () => void>());
    const catalogOpenings = useRef(new Map<string, Promise<SharedCatalog>>());
    const publishingCatalogs = useRef(new Map<string, Promise<void>>());
    const catalogLifecycleGeneration = useRef(0);
    const scopeRef = useRef(scope);
    const catalogIdsKeyRef = useRef("");
    const mountedRef = useRef(false);
    const bindingsRef = useRef<ResourceBindings>({});

    scopeRef.current = scope;

    useEffect(() => {
        mountedRef.current = true;
        return () => { mountedRef.current = false; };
    }, []);

    useEffect(() => subscribeUserScope((epoch) => setScope(epoch.scope)), []);

    useEffect(() => {
        let active = true;
        bindingsRef.current = {};
        setResourceBindings({});
        setResourceBindingsLoading(true);
        void Promise.resolve(localForageStorageForScope(scope).getItem(LOCAL_RESOURCE_BINDINGS_KEY)).then((raw) => {
            if (!active) return;
            const bindings = parseBindings(raw);
            bindingsRef.current = bindings;
            setResourceBindings(bindings);
        }).catch((error) => {
            if (active) setActionError(errorMessage(error));
        }).finally(() => {
            if (active) setResourceBindingsLoading(false);
        });
        return () => { active = false; };
    }, [scope]);

    const refresh = useCallback(async (initial = false) => {
        if (initial) setLoading(true);
        else setRefreshing(true);
        setLoadError("");
        try {
            const [nextStatus, nextPeers, nextCatalogs, nextResources] = await Promise.all([
                getLanSharingStatus(),
                getLanSharingPeers(),
                getLanSharingCatalogs(),
                listLocalSharingResources(),
            ]);
            setStatus(nextStatus);
            if (initial) setLocalDeviceName(nextStatus.nickname);
            setDiscovered(nextPeers.discovered);
            setTrusted(nextPeers.trusted);
            setCatalogs(nextCatalogs.catalogs);
            setResources(nextResources.resources.filter((resource) => resource.status === "ready"));
        } catch (error) {
            setLoadError(errorMessage(error));
        } finally {
            setLoading(false);
            setRefreshing(false);
        }
    }, []);

    const publishPendingUpdates = useCallback((catalogId: string, catalog: SharedCatalog) => {
        const key = catalogCacheKey(scope, catalogId);
        const existing = publishingCatalogs.current.get(key);
        if (existing) return existing;
        const publishing = (async () => {
            await catalog.flush();
            for (const update of catalog.pendingLocalUpdates()) {
                if (scopeRef.current !== scope) return;
                await publishLanSharingCatalogUpdate(catalogId, update);
                await catalog.acknowledgeLocalUpdate(update);
            }
        })().finally(() => {
            if (publishingCatalogs.current.get(key) === publishing) publishingCatalogs.current.delete(key);
        });
        publishingCatalogs.current.set(key, publishing);
        return publishing;
    }, [scope]);

    useEffect(() => {
        void refresh(true);
    }, [refresh, scope]);

    useEffect(() => {
        if (!status?.enabled) return;
        let active = true;
        let inFlight = false;
        const refreshPeers = async () => {
            if (inFlight) return;
            inFlight = true;
            try {
                const [nextStatus, nextPeers] = await Promise.all([getLanSharingStatus(), getLanSharingPeers()]);
                if (!active) return;
                setStatus(nextStatus);
                setDiscovered(nextPeers.discovered);
                setTrusted(nextPeers.trusted);
            } catch (error) {
                if (active) setLoadError(errorMessage(error));
            } finally {
                inFlight = false;
            }
        };
        const timer = setInterval(() => void refreshPeers(), 5_000);
        return () => {
            active = false;
            clearInterval(timer);
        };
    }, [status?.enabled]);

    const persistLocalResourceBinding = useCallback(async (catalogId: string, contentHash: string, resourceId: string) => {
        const next: ResourceBindings = {
            ...bindingsRef.current,
            [catalogId]: { ...bindingsRef.current[catalogId], [contentHash]: resourceId },
        };
        bindingsRef.current = next;
        setResourceBindings(next);
        await localForageStorageForScope(scope).setItem(LOCAL_RESOURCE_BINDINGS_KEY, JSON.stringify(next));
    }, [scope]);

    const connectCatalog = useCallback(async (catalogId: string) => {
        const key = catalogCacheKey(scope, catalogId);
        let catalog = catalogDocuments.current.get(key);
        if (!catalog) {
            let opening = catalogOpenings.current.get(key);
            if (!opening) {
                opening = openSharedCatalog(catalogId, { scope });
                catalogOpenings.current.set(key, opening);
            }
            try {
                const openedCatalog = await opening;
                if (catalogOpenings.current.get(key) === opening) catalogOpenings.current.delete(key);
                const currentCatalogIds = catalogIdsKeyRef.current ? catalogIdsKeyRef.current.split("\0") : [];
                if (!mountedRef.current || scopeRef.current !== scope || !currentCatalogIds.includes(catalogId)) {
                    if (!catalogDocuments.current.has(key)) void openedCatalog.close().catch(() => undefined);
                    throw new Error("共享清单已关闭");
                }
                catalog = catalogDocuments.current.get(key) || openedCatalog;
                if (!catalogDocuments.current.has(key)) {
                    catalogDocuments.current.set(key, catalog);
                    setEntriesByCatalog((current) => ({ ...current, [catalogId]: catalog!.listEntries() }));
                }
            } catch (error) {
                if (catalogOpenings.current.get(key) === opening) catalogOpenings.current.delete(key);
                throw error;
            }
        }
        if (!catalogUnsubscribers.current.has(key)) {
            const connectedCatalog = catalog;
            const unsubscribe = connectedCatalog.onUpdate(({ remote }) => {
                if (scopeRef.current !== scope) return;
                setEntriesByCatalog((current) => ({ ...current, [catalogId]: connectedCatalog.listEntries() }));
                if (remote) return;
                void publishPendingUpdates(catalogId, connectedCatalog).catch((error) => setActionError(errorMessage(error)));
            });
            catalogUnsubscribers.current.set(key, unsubscribe);
        }
        return catalog;
    }, [publishPendingUpdates, scope]);

    const catalogIdsKey = useMemo(() => catalogs.map((catalog) => catalog.id).sort().join("\0"), [catalogs]);
    catalogIdsKeyRef.current = catalogIdsKey;

    const releaseCatalog = useCallback((catalogId: string, catalogScope: string) => {
        const key = catalogCacheKey(catalogScope, catalogId);
        catalogUnsubscribers.current.get(key)?.();
        catalogUnsubscribers.current.delete(key);
        const catalog = catalogDocuments.current.get(key);
        catalogDocuments.current.delete(key);
        if (catalog) void catalog.close().catch(() => undefined);
    }, []);

    useEffect(() => {
        let active = true;
        const generation = ++catalogLifecycleGeneration.current;
        const catalogScope = scope;
        const catalogIds = catalogIdsKey ? catalogIdsKey.split("\0") : [];
        void Promise.all(catalogIds.map(async (catalogId) => {
            try {
                await connectCatalog(catalogId);
            } catch (error) {
                if (active) setActionError(errorMessage(error));
            }
        }));
        return () => {
            active = false;
            const currentIds = catalogIdsKeyRef.current ? catalogIdsKeyRef.current.split("\0") : [];
            for (const catalogId of catalogIds) {
                if (scopeRef.current !== catalogScope || !currentIds.includes(catalogId)) {
                    releaseCatalog(catalogId, catalogScope);
                    continue;
                }
                queueMicrotask(() => {
                    if (catalogLifecycleGeneration.current === generation) releaseCatalog(catalogId, catalogScope);
                });
            }
        };
    }, [catalogIdsKey, connectCatalog, releaseCatalog, scope]);

    useEffect(() => {
        let active = true;
        let timer: ReturnType<typeof setInterval> | undefined;
        const cursors = new Map<string, string>();
        const polling = new Set<string>();
        const catalogIds = catalogIdsKey ? catalogIdsKey.split("\0") : [];

        const pollCatalog = async (catalogId: string) => {
            if (!active || !status?.enabled || polling.has(catalogId)) return;
            polling.add(catalogId);
            try {
                const catalog = await connectCatalog(catalogId);
                let hasMore = true;
                while (active && hasMore) {
                    const page = await getLanSharingCatalogUpdates(catalogId, cursors.get(catalogId) || "");
                    let skippedInvalidUpdate = false;
                    for (const item of page.updates) {
                        try {
                            catalog.applyRemoteUpdate(decodeLanSharingUpdate(item.updateBase64));
                        } catch {
                            skippedInvalidUpdate = true;
                        }
                    }
                    const lastUpdateId = page.updates[page.updates.length - 1]?.id;
                    cursors.set(catalogId, page.cursor || lastUpdateId || cursors.get(catalogId) || "");
                    if (skippedInvalidUpdate && active) setActionError("共享清单中有无法读取的同步记录，已跳过损坏更新");
                    hasMore = page.hasMore && page.updates.length > 0;
                }
                await publishPendingUpdates(catalogId, catalog);
            } catch (error) {
                if (active) setActionError(errorMessage(error));
            } finally {
                polling.delete(catalogId);
            }
        };

        if (status?.enabled) catalogIds.forEach((catalogId) => void pollCatalog(catalogId));
        if (status?.enabled && catalogIds.length) timer = setInterval(() => { catalogIds.forEach((catalogId) => void pollCatalog(catalogId)); }, POLL_INTERVAL_MS);

        return () => {
            active = false;
            if (timer) clearInterval(timer);
        };
    }, [catalogIdsKey, connectCatalog, publishPendingUpdates, status?.enabled]);

    const toggleSharing = async (enabled: boolean) => {
        setBusy("sharing");
        setActionError("");
        try {
            setStatus(enabled ? await startLanSharing() : await stopLanSharing());
            await refresh();
        } catch (error) {
            setActionError(errorMessage(error));
        } finally {
            setBusy("");
        }
    };

    const saveLocalDeviceName = async (event: FormEvent<HTMLFormElement>) => {
        event.preventDefault();
        setBusy("device-name");
        setActionError("");
        try {
            const nextStatus = await setLanSharingNickname(localDeviceName);
            setStatus(nextStatus);
            setLocalDeviceName(nextStatus.nickname);
            message.success("设备名称已更新");
        } catch (error) {
            setActionError(errorMessage(error));
        } finally {
            setBusy("");
        }
    };

    const completePairing = async (peer: PeerLocation, code: string, peerNickname: string) => {
        setBusy(`pair:${peer.peerId}`);
        setActionError("");
        try {
            await pairLanSharingPeer({
                peerId: peer.peerId.trim().toLowerCase(),
                address: peer.address.trim(),
                port: peer.port,
                pairingCode: code.trim(),
                nickname: peerNickname.trim() || peer.nickname || peer.peerId,
            });
            setPairingPeer(undefined);
            setManualPairOpen(false);
            setManualPair(emptyManualPair);
            setPairingCode("");
            setNickname("");
            message.success("设备已配对");
            await refresh();
        } catch (error) {
            setActionError(errorMessage(error));
        } finally {
            setBusy("");
        }
    };

    const submitPairing = async (event: FormEvent<HTMLFormElement>) => {
        event.preventDefault();
        if (!pairingPeer) return;
        await completePairing(pairingPeer, pairingCode, nickname);
    };

    const submitManualPairing = async (event: FormEvent<HTMLFormElement>) => {
        event.preventDefault();
        const peerId = manualPair.peerId.trim().toLowerCase();
        const address = manualPair.address.trim();
        const port = Number(manualPair.port);
        if (!/^[a-f0-9]{64}$/u.test(peerId)) {
            setActionError("设备指纹需要 64 位十六进制字符");
            return;
        }
        if (!isPrivateIPv4(address)) {
            setActionError("请输入 RFC1918 私有 IPv4 地址");
            return;
        }
        if (!Number.isInteger(port) || port < 1 || port > 65535 || !manualPair.pairingCode.trim()) {
            setActionError("请检查端口和一次性配对码");
            return;
        }
        await completePairing({ peerId, address, port, discovered: false }, manualPair.pairingCode, manualPair.nickname);
    };

    const confirmRevoke = (peer: LanSharingTrustedPeer) => {
        const label = peer.nickname || peer.peerId;
        modal.confirm({
            title: `移除受信任设备“${label}”？`,
            okText: "移除设备",
            cancelText: "取消",
            onOk: async () => {
                setBusy(`revoke:${peer.peerId}`);
                setActionError("");
                try {
                    await revokeLanSharingPeer(peer.peerId);
                    message.success("设备已移除");
                    await refresh();
                } catch (error) {
                    setActionError(errorMessage(error));
                    throw error;
                } finally {
                    setBusy("");
                }
            },
        });
    };

    const submitCatalog = async (event: FormEvent<HTMLFormElement>) => {
        event.preventDefault();
        const name = catalogName.trim();
        if (!name) return;
        setBusy("catalog");
        setActionError("");
        try {
            const result = await createLanSharingCatalog(name);
            setCatalogs((current) => [...current.filter((catalog) => catalog.id !== result.catalog.id), result.catalog]);
            setCatalogName("");
            message.success("清单已创建");
        } catch (error) {
            setActionError(errorMessage(error));
        } finally {
            setBusy("");
        }
    };

    const addResource = async (catalogId: string) => {
        const resourceId = resourceByCatalog[catalogId];
        if (!resourceId) return;
        setBusy(`add:${catalogId}`);
        setActionError("");
        try {
            const result = await addLanSharingCatalogAsset(catalogId, resourceId);
            const catalog = await connectCatalog(catalogId);
            catalog.upsertEntry(result.asset);
            await persistLocalResourceBinding(catalogId, result.asset.contentHash, resourceId);
            setResourceByCatalog((current) => ({ ...current, [catalogId]: "" }));
        } catch (error) {
            setActionError(errorMessage(error));
        } finally {
            setBusy("");
        }
    };

    const importEntry = async (catalogId: string, entry: CatalogEntry) => {
        const peerKey = `${catalogId}:${entry.entryId}`;
        const selectedPeerId = peerByEntry[peerKey] || peerLocations.find((item) => item.peerId === entry.createdBy)?.peerId;
        const peer = peerLocations.find((item) => item.peerId === selectedPeerId);
        if (!peer) return;
        setBusy(`import:${catalogId}:${entry.entryId}`);
        setActionError("");
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
            await persistLocalResourceBinding(catalogId, entry.contentHash, result.resource.id);
            setResources((current) => [result.resource, ...current.filter((resource) => resource.id !== result.resource.id)]);
            message.success("資產已匯入本機資源");
        } catch (error) {
            setActionError(errorMessage(error));
        } finally {
            setBusy("");
        }
    };

    const peerLocations = useMemo(() => decodePeerLocations(discovered, trusted), [discovered, trusted]);
    const resourceOptions = resources.map((resource) => ({ value: resource.id, label: `${resourceLabel(resource)} · ${resource.id}` }));

    return (
        <div className="settings-pane space-y-5">
            <PageHeader title="局域网共享" />

            <section className="settings-section space-y-4" aria-label="共享服务">
                <div className="settings-pane-header flex flex-wrap items-center justify-between gap-3">
                    <h2>共享服务</h2>
                    <Tooltip title="刷新共享状态">
                        <Button aria-label="刷新共享状态" icon={<RefreshCw className="size-4" />} onClick={() => void refresh()} loading={refreshing} disabled={loading} />
                    </Tooltip>
                </div>
                <div className="flex flex-wrap items-center justify-between gap-4 border-b border-border pb-4">
                    <div className="min-w-0">
                        <div className="font-medium">本机节点</div>
                        <div className="break-all text-sm text-foreground/60">{status?.nodeId || (loading ? "正在读取…" : "未连接")}</div>
                    </div>
                    <label className="flex shrink-0 items-center gap-3 text-sm">
                        <span>{status?.enabled ? "已启动" : "已停止"}</span>
                        <Switch
                            aria-label="启用局域网共享"
                            checked={Boolean(status?.enabled)}
                            disabled={!status || loading || Boolean(busy)}
                            loading={busy === "sharing"}
                            onChange={(enabled) => void toggleSharing(enabled)}
                        />
                    </label>
                </div>
                <form className="grid min-w-0 gap-3 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-end" onSubmit={saveLocalDeviceName}>
                    <label className="min-w-0 space-y-1 text-sm">
                        <span>本机设备名称</span>
                        <Input
                            value={localDeviceName}
                            onChange={(event) => setLocalDeviceName(event.target.value)}
                            maxLength={80}
                            autoComplete="off"
                            disabled={loading || Boolean(busy)}
                            aria-describedby="lan-device-name-hint"
                        />
                        <span id="lan-device-name-hint" className="block text-xs text-foreground/55">留空时使用系统设备名称</span>
                    </label>
                    <Button
                        type="primary"
                        htmlType="submit"
                        icon={<Save className="size-4" />}
                        loading={busy === "device-name"}
                        disabled={!status || loading || Boolean(busy) || localDeviceName === status.nickname}
                    >
                        保存名称
                    </Button>
                </form>
                {status?.endpoint ? <div className="break-all text-sm text-foreground/60">监听地址：{status.endpoint}</div> : null}
                {status?.enabled && status.pairingCode ? (
                    <div className="flex flex-wrap items-center gap-x-4 gap-y-2 rounded-[var(--r-sm)] border border-border px-3 py-2">
                        <span className="text-sm text-foreground/65">配对码</span>
                        <code className="select-all text-base font-semibold text-foreground">{status.pairingCode}</code>
                        {pairingExpiry(status.pairingCodeExpiresAt) ? <span className="text-sm text-foreground/55">{pairingExpiry(status.pairingCodeExpiresAt)}</span> : null}
                    </div>
                ) : null}
            </section>

            {loadError ? (
                <div className="flex flex-wrap items-center gap-3 text-sm text-destructive" role="alert">
                    <span>{loadError}</span>
                    <Button size="small" onClick={() => void refresh()}>重试</Button>
                </div>
            ) : null}
            {actionError ? <p className="text-sm text-destructive" role="alert">{actionError}</p> : null}

            <section className="settings-section space-y-3" aria-label="发现的设备">
                <div className="settings-pane-header flex items-center justify-between gap-3">
                    <h2>发现的设备</h2>
                    <span className="text-sm text-foreground/55">{discovered.length}</span>
                </div>
                {loading ? <p className="text-sm text-foreground/55" role="status">正在读取设备…</p> : discovered.length === 0 ? <p className="text-sm text-foreground/55">没有发现可配对设备</p> : null}
                {discovered.map((peer) => (
                    <div key={peer.peerId} className="border-b border-border py-3 last:border-b-0">
                        <div className="flex min-w-0 flex-wrap items-center justify-between gap-3">
                            <div className="min-w-0">
                                <div className="break-words font-medium">{peer.nickname || peer.peerId}</div>
                                <div className="break-all text-sm text-foreground/55">{peer.address}:{peer.port} · {peer.peerId}</div>
                            </div>
                            <Button icon={<UserRoundPlus className="size-4" />} disabled={!status?.enabled || Boolean(busy)} onClick={() => { setPairingPeer({ ...peer, discovered: true }); setPairingCode(""); setNickname(peer.nickname || ""); }}>
                                配對
                            </Button>
                        </div>
                        {pairingPeer?.peerId === peer.peerId ? (
                            <form className="mt-3 grid min-w-0 gap-3 sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto_auto]" onSubmit={submitPairing}>
                                <label className="min-w-0 space-y-1 text-sm">
                                    <span>对方配对码</span>
                                    <Input autoFocus value={pairingCode} onChange={(event) => setPairingCode(event.target.value)} autoComplete="off" required />
                                </label>
                                <label className="min-w-0 space-y-1 text-sm">
                                    <span>设备名称</span>
                                    <Input value={nickname} onChange={(event) => setNickname(event.target.value)} autoComplete="off" />
                                </label>
                                <Button type="primary" htmlType="submit" loading={busy === `pair:${peer.peerId}`} disabled={!pairingCode.trim() || Boolean(busy)} className="self-end">确认配对</Button>
                                <Button htmlType="button" onClick={() => setPairingPeer(undefined)} disabled={Boolean(busy)} className="self-end">取消</Button>
                            </form>
                        ) : null}
                    </div>
                ))}
                <div className="border-t border-border pt-3">
                    <Button type="link" className="px-0" onClick={() => setManualPairOpen((open) => !open)} aria-expanded={manualPairOpen} disabled={!status?.enabled || Boolean(busy)}>
                        {manualPairOpen ? "收起手动配对" : "手动配对设备"}
                    </Button>
                    {manualPairOpen ? (
                        <form className="mt-2 grid min-w-0 gap-3 sm:grid-cols-2 lg:grid-cols-3" onSubmit={submitManualPairing}>
                            <label className="min-w-0 space-y-1 text-sm">
                                <span>设备指纹</span>
                                <Input value={manualPair.peerId} onChange={(event) => setManualPair((current) => ({ ...current, peerId: event.target.value }))} maxLength={64} minLength={64} pattern="[A-Fa-f0-9]{64}" autoComplete="off" required />
                            </label>
                            <label className="min-w-0 space-y-1 text-sm">
                                <span>私有 IPv4</span>
                                <Input value={manualPair.address} onChange={(event) => setManualPair((current) => ({ ...current, address: event.target.value }))} inputMode="numeric" placeholder="192.168.1.20" required />
                            </label>
                            <label className="min-w-0 space-y-1 text-sm">
                                <span>端口</span>
                                <InputNumber className="w-full" value={manualPair.port} min={1} max={65535} onChange={(port) => setManualPair((current) => ({ ...current, port: port ?? undefined }))} required />
                            </label>
                            <label className="min-w-0 space-y-1 text-sm">
                                <span>一次性配对码</span>
                                <Input value={manualPair.pairingCode} onChange={(event) => setManualPair((current) => ({ ...current, pairingCode: event.target.value }))} autoComplete="off" required />
                            </label>
                            <label className="min-w-0 space-y-1 text-sm">
                                <span>设备名称</span>
                                <Input value={manualPair.nickname} onChange={(event) => setManualPair((current) => ({ ...current, nickname: event.target.value }))} autoComplete="off" />
                            </label>
                            <Button type="primary" htmlType="submit" loading={busy.startsWith("pair:")} disabled={Boolean(busy)} className="self-end">配对设备</Button>
                        </form>
                    ) : null}
                </div>
            </section>

            <section className="settings-section space-y-3" aria-label="受信任设备">
                <div className="settings-pane-header flex items-center justify-between gap-3">
                    <h2>受信任设备</h2>
                    <span className="text-sm text-foreground/55">{trusted.length}</span>
                </div>
                {loading ? <p className="text-sm text-foreground/55" role="status">正在读取设备…</p> : trusted.length === 0 ? <p className="text-sm text-foreground/55">暂无受信任设备</p> : null}
                {trusted.map((peer) => (
                    <div key={peer.peerId} className="flex min-w-0 flex-wrap items-center justify-between gap-3 border-b border-border py-3 last:border-b-0">
                        <div className="min-w-0">
                            <div className="break-words font-medium">{peer.nickname || peer.peerId}</div>
                            <div className="break-all text-sm text-foreground/55">{peer.address && peer.port ? `${peer.address}:${peer.port} · ` : ""}{peer.peerId}</div>
                        </div>
                        <Button danger icon={<Unlink className="size-4" />} disabled={Boolean(busy)} loading={busy === `revoke:${peer.peerId}`} onClick={() => confirmRevoke(peer)}>
                            移除信任
                        </Button>
                    </div>
                ))}
            </section>

            <section className="settings-section space-y-3" aria-label="共享清单">
                <div className="settings-pane-header flex items-center justify-between gap-3">
                    <h2>共享清单</h2>
                    <span className="text-sm text-foreground/55">{catalogs.length}</span>
                </div>
                <form className="flex min-w-0 flex-wrap gap-2" onSubmit={submitCatalog}>
                    <Input className="min-w-0 flex-1" aria-label="清单名称" value={catalogName} onChange={(event) => setCatalogName(event.target.value)} placeholder="清单名称" maxLength={80} />
                    <Button type="primary" htmlType="submit" icon={<Plus className="size-4" />} loading={busy === "catalog"} disabled={!catalogName.trim() || Boolean(busy)}>
                        新建清单
                    </Button>
                </form>
                {loading ? <p className="text-sm text-foreground/55" role="status">正在读取清单…</p> : catalogs.length === 0 ? <p className="text-sm text-foreground/55">暂无共享清单</p> : null}
                {catalogs.map((catalog) => {
                    const catalogEntries = entriesByCatalog[catalog.id] || [];
                    const heldResources = resourceBindings[catalog.id] || {};
                    return (
                        <div key={catalog.id} className="border-b border-border py-3 last:border-b-0">
                            <div className="flex min-w-0 flex-wrap items-center justify-between gap-3">
                                <div className="min-w-0">
                                    <div className="break-words font-medium">{catalog.name}</div>
                                    <div className="break-all text-sm text-foreground/55">{catalog.id}</div>
                                </div>
                                <span className="shrink-0 text-sm text-foreground/55">{catalogEntries.length} 项</span>
                            </div>
                            <div className="mt-3 flex min-w-0 flex-wrap gap-2">
                                <Select
                                    className="min-w-[220px] flex-1"
                                    aria-label={`选择要添加到${catalog.name}的本地资源`}
                                    placeholder="选择本地资源"
                                    value={resourceByCatalog[catalog.id] || undefined}
                                    options={resourceOptions}
                                    showSearch
                                    optionFilterProp="label"
                                    onChange={(value) => setResourceByCatalog((current) => ({ ...current, [catalog.id]: value }))}
                                    disabled={!status?.enabled || Boolean(busy)}
                                />
                                <Button icon={<Plus className="size-4" />} onClick={() => void addResource(catalog.id)} loading={busy === `add:${catalog.id}`} disabled={!status?.enabled || !resourceByCatalog[catalog.id] || Boolean(busy)}>
                                    添加本地资源
                                </Button>
                            </div>
                            {catalogEntries.length === 0 ? <p className="mt-3 text-sm text-foreground/55">此清单暂无资产</p> : (
                                <div className="mt-2 divide-y divide-border">
                                    {catalogEntries.map((entry) => {
                                        const resourceId = heldResources[entry.contentHash];
                                        const isHeld = Boolean(resourceId && resources.some((resource) => resource.id === resourceId));
                                        const peerKey = `${catalog.id}:${entry.entryId}`;
                                        const selectedPeerId = peerByEntry[peerKey] || peerLocations.find((peer) => peer.peerId === entry.createdBy)?.peerId;
                                        return (
                                            <div key={entry.entryId} className="flex min-w-0 flex-wrap items-center justify-between gap-3 py-3">
                                                <div className="min-w-0 flex-1">
                                                    <div className="break-words font-medium">{entry.name}</div>
                                                    <div className="mt-1 flex flex-wrap gap-x-3 gap-y-1 text-xs text-foreground/55">
                                                        <span>{entry.mimeType}</span>
                                                        <span>{formatBytes(entry.size)}</span>
                                                        <code className="break-all">{entry.contentHash}</code>
                                                    </div>
                                                </div>
                                                {isHeld ? (
                                                    <span className="shrink-0 text-sm text-foreground/60">本机已有</span>
                                                ) : peerLocations.length ? (
                                                    <div className="flex min-w-0 flex-wrap gap-2">
                                                        <Select
                                                            className="min-w-[180px]"
                                                            aria-label={`选择尝试导入${entry.name}的设备`}
                                                            placeholder="尝试设备"
                                                            value={selectedPeerId}
                                                            options={peerLocations.map((peer) => ({ value: peer.peerId, label: `${peer.nickname || peer.peerId} · ${peer.discovered ? "已发现" : "最近地址"}` }))}
                                                            onChange={(value) => setPeerByEntry((current) => ({ ...current, [peerKey]: value }))}
                                                        />
                                                        <Button onClick={() => void importEntry(catalog.id, entry)} loading={busy === `import:${catalog.id}:${entry.entryId}`} disabled={resourceBindingsLoading || !selectedPeerId || Boolean(busy)}>
                                                            导入
                                                        </Button>
                                                    </div>
                                                ) : (
                                                    <span className="shrink-0 text-sm text-foreground/55">暂无可尝试设备</span>
                                                )}
                                            </div>
                                        );
                                    })}
                                </div>
                            )}
                        </div>
                    );
                })}
            </section>
        </div>
    );
}
