import { useCallback, useEffect, useMemo, useState } from "react";
import { App, Button, Input, Modal, Select, Tag } from "antd";
import { Archive, ExternalLink, Plus, RefreshCw, Trash2 } from "lucide-react";
import { nanoid } from "nanoid";
import { useNavigate } from "react-router";

import { ensureSharedCanvasCatalog, makePortableSharedCanvasContent, prepareSharedCanvasLocalAssetValues, createSharedCanvasLocalAssetResolver } from "@/services/lan-collaboration-assets";
import {
    createLanCollaborationCanvas,
    deleteLanCollaborationCanvas,
    getLanCollaborationCanvases,
    getLanCollaborationCanvasUpdates,
    nextLanCollaborationUpdateCursor,
    publishLanCollaborationCanvasUpdate,
    updateLanCollaborationCanvas,
    decodeLanCollaborationCanvasUpdate,
    type LanCollaborationCanvas,
} from "@/services/api/lan-collaboration";
import { getLanSharingStatus, startLanSharing } from "@/services/api/lan-sharing";
import {
    applyCollaborativeCanvasUpdate,
    createCollaborativeCanvasDocument,
    encodeCollaborativeCanvasState,
    readPortableSharedCanvasContent,
    restoreLocalSharedCanvasContent,
    patchCollaborativeCanvasTitle,
    encodeCollaborativeCanvasStateVector,
    type SharedCanvasContent,
} from "@/services/collaborative-canvas-document";
import * as Y from "yjs";
import { flushCanvasStorePersistence as flushCanvasStore, useCanvasStore, type CanvasProject } from "@/stores/canvas/use-canvas-store";

function projectContent(project?: CanvasProject): SharedCanvasContent {
    return {
        title: project?.title || "未命名协作画布",
        nodes: project?.nodes || [],
        connections: project?.connections || [],
        directorScenes: project?.directorScenes || [],
        timeline: project?.timeline,
    };
}

export function LanCollaborationLibrary({ projects }: { projects: CanvasProject[] }) {
    const { message, modal } = App.useApp();
    const navigate = useNavigate();
    const [canvases, setCanvases] = useState<LanCollaborationCanvas[]>([]);
    const [localNodeId, setLocalNodeId] = useState("");
    const [refreshing, setRefreshing] = useState(false);
    const [busy, setBusy] = useState("");
    const [createOpen, setCreateOpen] = useState(false);
    const [copyFromId, setCopyFromId] = useState<string>();
    const [name, setName] = useState("");
    const [showArchived, setShowArchived] = useState(false);

    const personalProjects = useMemo(() => projects.filter((project) => !project.sharedCanvasId), [projects]);

    const refresh = useCallback(async () => {
        setRefreshing(true);
        try {
            const [response, status] = await Promise.all([getLanCollaborationCanvases(), getLanSharingStatus()]);
            setCanvases(response.canvases);
            setLocalNodeId(status.nodeId);
        } catch {
            setCanvases([]);
        } finally {
            setRefreshing(false);
        }
    }, []);

    useEffect(() => { void refresh(); }, [refresh]);

    const ensureSharing = useCallback(async () => {
        const status = await getLanSharingStatus();
        if (!status.enabled) await startLanSharing();
        setLocalNodeId(status.nodeId);
    }, []);

    const saveLocalRoom = useCallback(async (room: LanCollaborationCanvas, content: SharedCanvasContent) => {
        const localId = useCanvasStore.getState().createProject(room.name);
        useCanvasStore.getState().updateProject(localId, {
            nodes: content.nodes,
            connections: content.connections,
            chatSessions: [],
            activeChatId: null,
            title: content.title || room.name,
            directorScenes: content.directorScenes,
            timeline: content.timeline,
            sharedCanvasId: room.id,
            sharedCanvasCreatorNodeId: room.createdByNodeId,
            sharedCanvasAssetCatalogId: room.assetCatalogId,
        });
        await flushCanvasStore();
        return localId;
    }, []);

    const create = async () => {
        const title = name.trim() || (copyFromId ? "协作副本" : "未命名协作画布");
        const source = personalProjects.find((project) => project.id === copyFromId);
        setBusy("create");
        try {
            await ensureSharing();
            const catalogId = await ensureSharedCanvasCatalog(`协作素材-${nanoid(8)}`);
            const content = { ...projectContent(source), title };
            const portable = await makePortableSharedCanvasContent(catalogId, content);
            const doc = createCollaborativeCanvasDocument(portable);
            const roomResponse = await createLanCollaborationCanvas(title, encodeCollaborativeCanvasState(doc), catalogId);
            doc.destroy();
            const localId = await saveLocalRoom(roomResponse.canvas, content);
            setCreateOpen(false);
            setName("");
            setCopyFromId(undefined);
            await refresh();
            navigate(`/canvas/${localId}`);
            message.success("协作画布已创建");
        } catch (error) {
            message.error(error instanceof Error ? error.message : "协作画布创建失败");
        } finally {
            setBusy("");
        }
    };

    const openRoom = async (room: LanCollaborationCanvas) => {
        const existing = useCanvasStore.getState().projects.find((project) => project.sharedCanvasId === room.id);
        if (existing) {
            navigate(`/canvas/${existing.id}`);
            return;
        }
        if (!room.assetCatalogId) {
            message.error("协作画布素材清单不可用");
            return;
        }
        setBusy(`open:${room.id}`);
        try {
            const doc = createCollaborativeCanvasDocument();
            let cursor = "";
            for (;;) {
                const page = await getLanCollaborationCanvasUpdates(room.id, cursor);
                page.updates.forEach((update) => applyCollaborativeCanvasUpdate(doc, decodeLanCollaborationCanvasUpdate(update)));
                cursor = nextLanCollaborationUpdateCursor(cursor, page);
                if (!page.hasMore) break;
            }
            const portable = readPortableSharedCanvasContent(doc);
            const hashes = extractHashes(portable);
            const bindings = await prepareSharedCanvasLocalAssetValues(room.assetCatalogId, hashes);
            const content = restoreLocalSharedCanvasContent(portable, createSharedCanvasLocalAssetResolver(bindings));
            const localId = await saveLocalRoom(room, content);
            doc.destroy();
            await flushCanvasStore();
            navigate(`/canvas/${localId}`);
        } catch (error) {
            message.error(error instanceof Error ? error.message : "协作画布打开失败");
        } finally {
            setBusy("");
        }
    };

    const rename = (room: LanCollaborationCanvas) => {
        let value = room.name;
        modal.confirm({
            title: "重命名协作画布",
            content: <Input defaultValue={value} maxLength={80} onChange={(event) => { value = event.target.value; }} />,
            okText: "保存",
            onOk: async () => {
                const title = value.trim();
                if (!title) throw new Error("请输入画布名称");
                await updateLanCollaborationCanvas(room.id, { name: title });
                const doc = createCollaborativeCanvasDocument();
                try {
                    let cursor = "";
                    for (;;) {
                        const page = await getLanCollaborationCanvasUpdates(room.id, cursor);
                        page.updates.forEach((update) => applyCollaborativeCanvasUpdate(doc, decodeLanCollaborationCanvasUpdate(update)));
                        if (!page.hasMore) break;
                        cursor = nextLanCollaborationUpdateCursor(cursor, page);
                    }
                    const vector = encodeCollaborativeCanvasStateVector(doc);
                    patchCollaborativeCanvasTitle(doc, title);
                    const delta = Y.encodeStateAsUpdate(doc, vector);
                    if (delta.byteLength > 2) await publishLanCollaborationCanvasUpdate(room.id, delta);
                    for (const local of useCanvasStore.getState().projects.filter((project) => project.sharedCanvasId === room.id)) {
                        useCanvasStore.getState().updateProject(local.id, { title });
                    }
                    await flushCanvasStore();
                    await refresh();
                } finally {
                    doc.destroy();
                }
            },
        });
    };

    const archive = async (room: LanCollaborationCanvas, archived: boolean) => {
        setBusy(`archive:${room.id}`);
        try { await updateLanCollaborationCanvas(room.id, { archived }); await refresh(); }
        catch (error) { message.error(error instanceof Error ? error.message : "协作画布状态更新失败"); }
        finally { setBusy(""); }
    };

    const remove = (room: LanCollaborationCanvas) => modal.confirm({
        title: `删除“${room.name}”？`,
        content: "这会从所有可信设备的协作清单中删除画布，不会删除各成员本地资源。",
        okText: "删除",
        okButtonProps: { danger: true },
        onOk: async () => {
            await deleteLanCollaborationCanvas(room.id);
            const localIds = useCanvasStore.getState().projects.filter((project) => project.sharedCanvasId === room.id).map((project) => project.id);
            if (localIds.length) {
                useCanvasStore.getState().deleteProjects(localIds);
                await flushCanvasStore();
            }
            setCanvases((current) => current.filter((item) => item.id !== room.id));
        },
    });

    const visible = canvases.filter((room) => showArchived || !room.archived);

    return (
        <section className="mb-6 border-b border-border pb-5" aria-label="局域网协作画布">
            <div className="mb-3 flex flex-wrap items-center justify-between gap-3">
                <div className="flex items-center gap-2">
                    <h2 className="text-base font-semibold">局域网协作</h2>
                    <Tag color="green">实时共享</Tag>
                </div>
                <div className="flex items-center gap-2">
                    <Button size="small" onClick={() => setShowArchived((value) => !value)}>{showArchived ? "隐藏归档" : "查看归档"}</Button>
                    <Button size="small" icon={<RefreshCw className="size-3.5" />} aria-label="刷新协作画布" loading={refreshing} onClick={() => void refresh()} />
                    <Button size="small" type="primary" icon={<Plus className="size-3.5" />} onClick={() => { setCopyFromId(undefined); setName(""); setCreateOpen(true); }}>新建协作画布</Button>
                    <Button size="small" onClick={() => { setCopyFromId(personalProjects[0]?.id); setName(""); setCreateOpen(true); }} disabled={!personalProjects.length}>从现有画布复制</Button>
                </div>
            </div>
            {visible.length ? (
                <div className="divide-y divide-border">
                    {visible.map((room) => {
                        const local = useCanvasStore.getState().projects.find((project) => project.sharedCanvasId === room.id);
                        const isOwner = room.createdByNodeId === localNodeId;
                        return (
                            <div key={room.id} className="flex min-w-0 flex-wrap items-center gap-3 py-2.5">
                                <div className="min-w-0 flex-1">
                                    <div className="truncate text-sm font-medium">{room.name}{room.archived ? <Tag className="ml-2">已归档</Tag> : null}</div>
                                    <div className="mt-0.5 text-xs text-foreground/60">{local ? "已在本机打开" : "所有已配对设备可访问"} · {room.updateCount} 次更新</div>
                                </div>
                                <Button size="small" type="primary" icon={<ExternalLink className="size-3.5" />} loading={busy === `open:${room.id}`} onClick={() => void openRoom(room)}>{local ? "打开" : "同步并打开"}</Button>
                                {isOwner ? <>
                                    <Button size="small" onClick={() => rename(room)}>重命名</Button>
                                    <Button size="small" icon={<Archive className="size-3.5" />} loading={busy === `archive:${room.id}`} onClick={() => void archive(room, !room.archived)}>{room.archived ? "取消归档" : "归档"}</Button>
                                    <Button size="small" danger icon={<Trash2 className="size-3.5" />} onClick={() => remove(room)} aria-label={`删除${room.name}`} />
                                </> : null}
                            </div>
                        );
                    })}
                </div>
            ) : <div className="py-5 text-sm text-foreground/60">还没有协作画布</div>}
            <Modal title={copyFromId !== undefined ? "从个人画布创建协作副本" : "新建协作画布"} open={createOpen} onCancel={() => setCreateOpen(false)} onOk={() => void create()} okText="创建" confirmLoading={busy === "create"}>
                {copyFromId !== undefined ? (
                    <div className="space-y-3">
                        <label className="block text-sm font-medium">来源画布</label>
                        <Select className="w-full" value={copyFromId} options={personalProjects.map((project) => ({ value: project.id, label: project.title }))} onChange={setCopyFromId} />
                        <label className="block text-sm font-medium">协作画布名称</label>
                        <Input value={name} maxLength={80} placeholder="输入名称" onChange={(event) => setName(event.target.value)} />
                    </div>
                ) : <Input value={name} maxLength={80} placeholder="输入名称" onChange={(event) => setName(event.target.value)} />}
            </Modal>
        </section>
    );
}

function extractHashes(value: unknown, result = new Set<string>()) {
    if (Array.isArray(value)) value.forEach((item) => extractHashes(item, result));
    else if (value && typeof value === "object") {
        const record = value as Record<string, unknown>;
        if (record.__beeftvSharedAsset === true && typeof record.contentHash === "string") result.add(record.contentHash);
        Object.values(record).forEach((item) => extractHashes(item, result));
    }
    return [...result];
}
