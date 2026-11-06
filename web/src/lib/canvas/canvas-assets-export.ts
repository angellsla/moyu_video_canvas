import { canvasMediaNodeOrigin } from "@/lib/canvas/canvas-node-semantics";
import { loadCanvasDrawing, loadCanvasDrawingPreview, loadCanvasDrawingRender } from "@/lib/canvas/canvas-drawing-storage";
import { awaitCanvasExportStep, isArchiveStorageKey } from "@/lib/canvas/canvas-export";
import { sanitizeDownloadFileName } from "@/lib/canvas/canvas-media-download";
import { archiveFileExtension, assertUniqueArchiveNames, ExportIntegrityError, type MissingExportFile } from "@/lib/export-integrity";
import { createZip } from "@/lib/zip";
import { assertUserScope, captureUserScope } from "@/lib/user-scope-guard";
import { getImageBlob } from "@/services/image-storage";
import { getMediaBlob } from "@/services/file-storage";
import { getResource, ownedResourceIdFromMediaRef, resourceIdFromStorageKey, resourceStorageKey } from "@/services/api/resources";
import { decodeLanCollaborationCanvasUpdate, getLanCollaborationCanvasUpdates, nextLanCollaborationUpdateCursor } from "@/services/api/lan-collaboration";
import { prepareSharedCanvasLocalAssetValues, queueSharedCanvasAssetRetries } from "@/services/lan-collaboration-assets";
import {
    applyCollaborativeCanvasUpdate,
    collectSharedCanvasAssetReferences,
    createCollaborativeCanvasDocument,
    readPortableSharedCanvasContent,
    type PortableSharedCanvasContent,
    type SharedCanvasContent,
} from "@/services/collaborative-canvas-document";
import { saveOwnedNativeArchiveFiles, saveOwnedOrBrowserBlob, type OwnedMediaSaveResult } from "@/services/desktop-media-save";
import type { CanvasProject } from "@/stores/canvas/use-canvas-store";

type AssetReference = { value: string; field: string; path: string[]; sha256?: string };

export type CanvasAssetExportOptions = { includeResultVideos: boolean; signal?: AbortSignal };

const LOCAL_ASSET = /^(?:data:|blob:)/i;
const REMOTE_URL = /^https?:\/\//i;
const MEDIA_FIELDS = new Set(["content", "previewContent", "url", "dataUrl", "directImageUrl", "directorCoverUrl", "drawingPreviewUrl", "characterCoverUrl", "themeCover"]);
const PRIMARY_VIDEO_FIELDS = new Set(["content", "storageKey", "resourceId", "url", "dataUrl"]);

function canvasContent(project: CanvasProject): SharedCanvasContent {
    return { title: project.title, nodes: project.nodes, connections: project.connections, directorScenes: project.directorScenes, timeline: project.timeline };
}

function isMediaPath(path: string[], project: CanvasProject) {
    const field = path.at(-1) || "";
    if (path[0] === "timeline" && path[1] === "clips" && path[3] === "directMedia") return MEDIA_FIELDS.has(field);
    if (path[0] === "directorScenes") return MEDIA_FIELDS.has(field);
    if (path[0] !== "nodes" || path[2] !== "metadata") return false;
    const node = project.nodes[Number(path[1])];
    if (!node) return false;
    if (path.length === 4 && ["image", "video", "audio"].includes(node.type)) return MEDIA_FIELDS.has(field);
    if (["videoPreview", "videoTrimSource", "emotionEdit", "panoramaConfig", "folder", "imageLayer", "mediaConversion", "portraitTexture"].some((part) => path.includes(part))) return MEDIA_FIELDS.has(field);
    return field === "directorCoverUrl" || field === "drawingPreviewUrl" || field === "directImageUrl" || field === "characterCoverUrl";
}

function isAssetValue(value: string, path: string[], project: CanvasProject) {
    const field = path.at(-1) || "";
    if (field === "sourceKey") return false;
    const storageField = /storageKey$/i.test(field);
    if (!storageField && !isMediaPath(path, project)) return false;
    if (isArchiveStorageKey(value) || LOCAL_ASSET.test(value) || ownedResourceIdFromMediaRef(undefined, value)) return true;
    return REMOTE_URL.test(value) && isMediaPath(path, project);
}

function shouldOmitResultVideo(path: string[], project: CanvasProject, includeResultVideos: boolean) {
    if (includeResultVideos || path[0] !== "nodes" || path[2] !== "metadata" || path.length !== 4 || !PRIMARY_VIDEO_FIELDS.has(path[3])) return false;
    const node = project.nodes[Number(path[1])];
    if (node?.type !== "video") return false;
    const origin = canvasMediaNodeOrigin(node);
    return origin === "generated" || origin === "derived";
}

function hasPreferredSibling(content: SharedCanvasContent | PortableSharedCanvasContent, reference: AssetReference) {
    const field = reference.path.at(-1);
    let sibling: string;
    if (field === "directorCoverUrl") sibling = "directorCoverStorageKey";
    else if (field === "drawingPreviewUrl") sibling = "drawingPreviewStorageKey";
    else if (field === "content" || field === "url" || field === "dataUrl") sibling = "storageKey";
    else return false;
    let parent: unknown = content;
    for (const part of reference.path.slice(0, -1)) {
        if (!parent || typeof parent !== "object") return false;
        parent = (parent as Record<string, unknown>)[part];
    }
    if (!parent || typeof parent !== "object") return false;
    const record = parent as Record<string, unknown>;
    const storageKey = record[sibling];
    if ((typeof storageKey === "string" && isArchiveStorageKey(storageKey)) || portableHash(storageKey)) return true;
    return sibling === "storageKey" && ((typeof record.resourceId === "string" && Boolean(record.resourceId.trim())) || Boolean(portableHash(record.resourceId)));
}

function portableHash(value: unknown) {
    if (!value || typeof value !== "object") return "";
    const record = value as Record<string, unknown>;
    return record.__beeftvSharedAsset === true && typeof record.contentHash === "string" ? record.contentHash : "";
}

function assetLocation(content: SharedCanvasContent | PortableSharedCanvasContent, path: string[]) {
    let owner = path[0];
    let entity: unknown;
    let fieldPath = path.slice(2);
    if (owner === "nodes") entity = content.nodes[Number(path[1])];
    else if (owner === "directorScenes") entity = content.directorScenes[Number(path[1])];
    else if (owner === "timeline" && path[1] === "clips") {
        owner = "timeline.clips";
        entity = (content.timeline as SharedCanvasContent["timeline"])?.clips[Number(path[2])];
        fieldPath = path.slice(3);
    }
    const id = entity && typeof entity === "object" && "id" in entity ? String(entity.id) : "";
    if (!id) return path.join(".");
    const last = fieldPath.at(-1);
    if (last && PRIMARY_VIDEO_FIELDS.has(last)) fieldPath[fieldPath.length - 1] = "primaryMedia";
    return `${owner}:${id}:${fieldPath.join(".")}`;
}

function collectPortableAssetHashes(content: PortableSharedCanvasContent, project: CanvasProject, includeResultVideos: boolean) {
    const portableProject = { ...project, nodes: content.nodes as unknown as CanvasProject["nodes"] };
    const hashes = new Set<string>();
    const locations = new Set<string>();
    const visit = (value: unknown, path: string[]) => {
        const hash = portableHash(value);
        if (value && typeof value === "object" && !Array.isArray(value) && (value as Record<string, unknown>).__beeftvSharedAsset === true && !hash) {
            throw new ExportIntegrityError([{ owner: project.title || project.id, reference: `协作素材引用 ${path.join(".")}` }]);
        }
        if (hash) {
            const reference = { field: path.at(-1) || "", path, value: hash };
            if (!shouldOmitResultVideo(path, portableProject, includeResultVideos) && !hasPreferredSibling(content, reference)) {
                hashes.add(hash);
                locations.add(assetLocation(content, path));
            }
            return;
        }
        if (Array.isArray(value)) value.forEach((item, index) => visit(item, [...path, String(index)]));
        else if (value && typeof value === "object") {
            for (const [key, item] of Object.entries(value)) visit(item, [...path, key]);
        }
    };
    visit(content, []);
    return { hashes: [...hashes], locations };
}

async function preflightCollaborativeAssets(project: CanvasProject, options: CanvasAssetExportOptions, step: <T>(promise: Promise<T>) => Promise<T>, check: () => void) {
    if (!project.sharedCanvasId) return { references: [] as AssetReference[], locations: new Set<string>() };
    const catalogId = project.sharedCanvasAssetCatalogId;
    if (!catalogId) throw new ExportIntegrityError([{ owner: project.title || project.id, reference: "协作素材清单缺失" }]);
    const doc = createCollaborativeCanvasDocument();
    try {
        let cursor = "";
        for (;;) {
            check();
            const page = await step(getLanCollaborationCanvasUpdates(project.sharedCanvasId, cursor));
            check();
            for (const update of page.updates) applyCollaborativeCanvasUpdate(doc, decodeLanCollaborationCanvasUpdate(update));
            cursor = nextLanCollaborationUpdateCursor(cursor, page);
            if (!page.hasMore) break;
        }
        const { hashes, locations } = collectPortableAssetHashes(readPortableSharedCanvasContent(doc), project, options.includeResultVideos);
        if (!hashes.length) return { references: [] as AssetReference[], locations };
        let localResources: Record<string, string>;
        try {
            localResources = await step(prepareSharedCanvasLocalAssetValues(catalogId, hashes));
        } catch (error) {
            check();
            await step(queueSharedCanvasAssetRetries(catalogId, hashes));
            throw error;
        }
        check();
        const missingHashes = hashes.filter((hash) => !localResources[hash]);
        if (missingHashes.length) {
            await step(queueSharedCanvasAssetRetries(catalogId, missingHashes));
            throw new ExportIntegrityError(missingHashes.map((hash) => ({ owner: project.title || project.id, reference: `协作素材 ${hash.slice(0, 12)}` })));
        }
        return {
            references: hashes.map((hash) => ({ field: "storageKey", value: resourceStorageKey(localResources[hash]), path: ["sharedCanvas", hash], sha256: hash })),
            locations,
        };
    } finally {
        doc.destroy();
    }
}

/** Collect only persisted media fields; prompt text and remote links elsewhere are not files. */
export function collectCanvasAssetReferences(project: CanvasProject, includeResultVideos: boolean): AssetReference[] {
    const content = canvasContent(project);
    const references = new Map<string, AssetReference>();
    const add = (reference: AssetReference) => {
        if (shouldOmitResultVideo(reference.path, project, includeResultVideos)) return;
        if (hasPreferredSibling(content, reference)) return;
        references.set(JSON.stringify(reference.path), reference);
    };
    for (const reference of collectSharedCanvasAssetReferences(content)) {
        if (reference.field === "resourceId" || isAssetValue(reference.value, reference.path, project)) add(reference);
    }
    const visit = (value: unknown, path: string[]) => {
        if (Array.isArray(value)) {
            value.forEach((item, index) => visit(item, [...path, String(index)]));
        } else if (value && typeof value === "object") {
            for (const [field, item] of Object.entries(value)) {
                const nextPath = [...path, field];
                if (typeof item === "string" && isAssetValue(item, nextPath, project)) add({ value: item, field, path: nextPath });
                else visit(item, nextPath);
            }
        }
    };
    visit(content, []);
    return [...references.values()];
}

function referenceIdentity(reference: AssetReference) {
    const id = reference.field === "resourceId"
        ? resourceIdFromStorageKey(reference.value) || reference.value
        : ownedResourceIdFromMediaRef(reference.value, reference.value);
    return id ? resourceStorageKey(id) : reference.value;
}

function referenceLabel(reference: AssetReference, project: CanvasProject) {
    if (reference.path[0] === "sharedCanvas") return `协作素材 ${reference.path[1]?.slice(0, 12) || ""}`;
    if (reference.path[0] === "nodes") return project.nodes[Number(reference.path[1])]?.title || "画布节点";
    if (reference.path[0] === "timeline") return "时间线素材";
    if (reference.path[0] === "directorScenes") return "导演场景素材";
    return "画布素材";
}

async function readReferenceBlob(reference: AssetReference, signal?: AbortSignal): Promise<Blob | null | undefined> {
    const value = reference.value;
    if (LOCAL_ASSET.test(value) || REMOTE_URL.test(value)) {
        const response = await fetch(value, { signal, credentials: "omit" });
        if (!response.ok) throw new Error(`HTTP ${response.status}`);
        return response.blob();
    }
    const image = await getImageBlob(value);
    return image || getMediaBlob(value);
}

/** Saves asset bytes only; this is separate from the importable canvas backup. */
export async function exportCanvasAssetFiles(project: CanvasProject, options: CanvasAssetExportOptions): Promise<OwnedMediaSaveResult> {
    const scope = captureUserScope();
    const check = () => { options.signal?.throwIfAborted(); assertUserScope(scope); };
    const step = <T,>(promise: Promise<T>) => awaitCanvasExportStep(promise, options.signal);
    check();
    const shared = await preflightCollaborativeAssets(project, options, step, check);
    const localReferences = collectCanvasAssetReferences(project, options.includeResultVideos)
        .filter((reference) => !shared.locations.has(assetLocation(canvasContent(project), reference.path)));
    const byIdentity = new Map<string, { reference: AssetReference; labels: Set<string>; sha256?: string }>();
    for (const reference of [...localReferences, ...shared.references]) {
        const identity = referenceIdentity(reference);
        const label = referenceLabel(reference, project);
        const existing = byIdentity.get(identity);
        if (existing) {
            if (existing.sha256 && reference.sha256 && existing.sha256 !== reference.sha256) throw new Error("协作素材映射冲突，导出已停止");
            existing.labels.add(label);
            existing.sha256 ||= reference.sha256;
        } else byIdentity.set(identity, { reference, labels: new Set([label]), sha256: reference.sha256 });
    }
    const nativeArchive = typeof window !== "undefined" && Boolean(window.go?.main?.DesktopApp?.SaveOwnedArchive);
    const files: Array<{ name: string; data: BlobPart; resourceId?: string; sha256?: string }> = [];
    const manifestAssets: Array<{ path: string; mimeType: string; bytes: number; usedBy: string[] }> = [];
    const missing: MissingExportFile[] = [];
    for (const { reference, labels, sha256 } of byIdentity.values()) {
        check();
        const identity = referenceIdentity(reference);
        const resourceId = resourceIdFromStorageKey(identity);
        let blob: Blob | null | undefined;
        let mimeType = "application/octet-stream";
        let bytes = 0;
        try {
            if (resourceId && nativeArchive) {
                const resource = await step(getResource(resourceId, { expectedScope: scope, signal: options.signal }));
                check();
                if (resource.status !== "ready" || resource.size <= 0) throw new Error("文件尚未就绪");
                if (resource.provider === "local") {
                    mimeType = resource.mimeType || mimeType;
                    bytes = resource.size;
                } else {
                    blob = await step(getMediaBlob(resourceStorageKey(resourceId)));
                }
            } else if (resourceId) {
                blob = await step(getMediaBlob(resourceStorageKey(resourceId)));
            } else {
                blob = await step(readReferenceBlob(reference, options.signal));
            }
            check();
            if (!resourceId || !nativeArchive || blob) {
                if (!blob?.size) throw new Error("文件为空或不存在");
                if (sha256) {
                    const digest = await step(crypto.subtle.digest("SHA-256", await step(blob.arrayBuffer())));
                    const actual = Array.from(new Uint8Array(digest), (byte) => byte.toString(16).padStart(2, "0")).join("");
                    if (actual !== sha256) throw new Error("共享素材内容哈希不匹配");
                }
                mimeType = blob.type || mimeType;
                bytes = blob.size;
            }
        } catch {
            check();
            missing.push({ owner: project.title || project.id, reference: `${[...labels].join("、")} (${reference.path.join(".")})` });
            continue;
        }
        const index = files.length + 1;
        const label = sanitizeDownloadFileName([...labels][0], "素材");
        const path = `assets/${String(index).padStart(4, "0")}-${label}.${archiveFileExtension(mimeType, "bin")}`;
        files.push({ name: path, data: blob || new Blob(), resourceId: nativeArchive && !blob ? resourceId || undefined : undefined, sha256 });
        manifestAssets.push({ path, mimeType, bytes, usedBy: [...labels] });
    }
    const drawingIds = new Set<string>();
    for (const node of project.nodes) {
        if (node.type !== "drawing" || !node.metadata?.drawingId || drawingIds.has(node.metadata.drawingId)) continue;
        const drawingId = node.metadata.drawingId;
        drawingIds.add(drawingId);
        check();
        try {
            const [document, preview, render] = await step(Promise.all([
                loadCanvasDrawing(project.id, drawingId, scope),
                loadCanvasDrawingPreview(project.id, drawingId, scope),
                loadCanvasDrawingRender(project.id, drawingId, scope),
            ]));
            check();
            if (!document) throw new Error("画板文档不存在");
            const base = `drawings/${String(drawingIds.size).padStart(4, "0")}-${sanitizeDownloadFileName(node.title || drawingId, "画板")}`;
            const appendDrawingFile = (path: string, blob: Blob) => {
                if (!blob.size) throw new Error("画板文件为空");
                files.push({ name: path, data: blob });
                manifestAssets.push({ path, mimeType: blob.type || "application/octet-stream", bytes: blob.size, usedBy: [node.title || drawingId] });
            };
            appendDrawingFile(`${base}.json`, new Blob([JSON.stringify({ drawingId, ...document })], { type: "application/json" }));
            if (preview) appendDrawingFile(`${base}-preview.${archiveFileExtension(preview.type, "png")}`, preview);
            if (render) appendDrawingFile(`${base}-render.${archiveFileExtension(render.mimeType || render.blob.type, "png")}`, render.blob);
        } catch {
            check();
            missing.push({ owner: project.title || project.id, reference: `画板 ${node.title || drawingId}` });
        }
    }
    if (missing.length) throw new ExportIntegrityError(missing);
    const manifest = {
        app: "beeftv", kind: "canvas-assets", version: 1, canvasId: project.id, canvasTitle: project.title,
        exportedAt: new Date().toISOString(), includeResultVideos: options.includeResultVideos, assets: manifestAssets,
    };
    const archiveFiles = [{ name: "canvas-assets.json", data: JSON.stringify(manifest, null, 2) }, ...files];
    assertUniqueArchiveNames(archiveFiles.map((file) => file.name));
    check();
    const fileName = `${sanitizeDownloadFileName(project.title || "画布")}-资产.zip`;
    const nativeResult = await saveOwnedNativeArchiveFiles(fileName, archiveFiles, scope, { signal: options.signal });
    if (nativeResult !== undefined) return nativeResult;
    if (nativeArchive) {
        for (const file of files) {
            if (!file.resourceId) continue;
            const blob = await step(getMediaBlob(resourceStorageKey(file.resourceId)));
            check();
            if (!blob?.size) throw new ExportIntegrityError([{ owner: project.title || project.id, reference: file.name }]);
            file.data = blob;
            file.resourceId = undefined;
        }
    }
    const archive = await step(createZip(archiveFiles));
    check();
    return saveOwnedOrBrowserBlob(fileName, archive);
}
