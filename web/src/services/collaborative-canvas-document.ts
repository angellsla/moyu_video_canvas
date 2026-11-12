import * as Y from "yjs";

import type { CanvasConnection, CanvasNodeData } from "@/types/canvas";
import type { DirectorScene } from "@/types/director";
import type { TimelineClip, TimelineProject, TimelineTrack } from "@/types/timeline";

export type SharedCanvasContent = {
    title: string;
    nodes: CanvasNodeData[];
    connections: CanvasConnection[];
    directorScenes: DirectorScene[];
    timeline?: TimelineProject;
};

export type SharedCanvasAssetField = "storageKey" | "resourceId" | "dataUrl" | "content" | "previewContent" | "url";

export type SharedCanvasAssetReference = {
    __beeftvSharedAsset: true;
    contentHash: string;
    localField: SharedCanvasAssetField;
};

export type PortableValue = null | string | number | boolean | SharedCanvasAssetReference | PortableValue[] | { [key: string]: PortableValue | undefined };

export type PortableSharedCanvasContent = Omit<SharedCanvasContent, "nodes" | "connections" | "directorScenes" | "timeline"> & {
    nodes: PortableValue[];
    connections: PortableValue[];
    directorScenes: PortableValue[];
    timeline?: PortableValue;
};

export type SharedCanvasAssetSource = { field: SharedCanvasAssetField; value: string };
export type SharedCanvasAssetHashResolver = (input: { field: SharedCanvasAssetField; localValue: string; path: string[]; source?: SharedCanvasAssetSource }) => string | undefined;

export type SharedCanvasLocalAssetResolver = (input: { field: SharedCanvasAssetField; contentHash: string; path: string[] }) => string | undefined;

const ROOT = "sharedCanvas";
const PROPERTIES = "properties";
const NODES = "nodes";
const NODE_ORDER = "nodeOrder";
const CONNECTIONS = "connections";
const CONNECTION_ORDER = "connectionOrder";
const DIRECTOR_SCENES = "directorScenes";
const DIRECTOR_SCENE_ORDER = "directorSceneOrder";
const TIMELINE = "timeline";
const TIMELINE_TRACKS = "tracks";
const TIMELINE_TRACK_ORDER = "trackOrder";
const TIMELINE_CLIPS = "clips";
const TIMELINE_CLIP_ORDER = "clipOrder";
const DELETED = "__deleted";

type Entity = { id: string } & Record<string, unknown>;

function requireId(id: string, label: string) {
    if (!id.trim()) throw new Error(`${label} id must be a non-empty string`);
    return id;
}

function asMap(value: unknown): Y.Map<unknown> {
    if (!(value instanceof Y.Map)) throw new Error("Collaborative canvas document has an invalid structure");
    return value;
}

function readMap(value: unknown): Y.Map<unknown> {
    return value instanceof Y.Map ? (value as Y.Map<unknown>) : new Y.Map<unknown>();
}

function entityMap(parent: Y.Map<unknown>, key: string) {
    const existing = parent.get(key);
    if (existing instanceof Y.Map) return existing as Y.Map<unknown>;
    const created = new Y.Map<unknown>();
    parent.set(key, created);
    return created;
}

function orderArray(parent: Y.Map<unknown>, key: string) {
    const existing = parent.get(key);
    if (existing instanceof Y.Array) return existing as Y.Array<string>;
    const created = new Y.Array<string>();
    parent.set(key, created);
    return created;
}

function readOrder(value: unknown): Y.Array<string> {
    return value instanceof Y.Array ? (value as Y.Array<string>) : new Y.Array<string>();
}

function cloneJsonValue(value: unknown): unknown {
    if (value === undefined) return undefined;
    return JSON.parse(JSON.stringify(value)) as unknown;
}

const ASSET_REFERENCE_FIELDS = new Set<SharedCanvasAssetField>(["storageKey", "resourceId", "dataUrl", "content", "previewContent", "url"]);
const RESOURCE_FILE_URL = /(?:^|\/)api\/resources\/[^/?#]+\/file(?:[?#]|$)/i;

function isLocalMediaReference(value: string) {
    return /^(?:data:|blob:|resource:)/i.test(value) || RESOURCE_FILE_URL.test(value);
}

function isLocalUploadField(key: string, record: Record<string, unknown>) {
    return key === "fileUpload" || key === "fileUploadProgress" || (key === "errorDetails" && Boolean(record.fileUpload));
}

function stripLocalUploadState(value: unknown): unknown {
    if (Array.isArray(value)) return value.map(stripLocalUploadState);
    if (!isRecord(value)) return value;
    return Object.fromEntries(Object.entries(value).filter(([key]) => !isLocalUploadField(key, value)).map(([key, item]) => [key, stripLocalUploadState(item)]));
}

function replaceWithPortableAssetReferences(value: unknown, path: string[], resolveHash: SharedCanvasAssetHashResolver, mediaAssetPaths: Set<string>): unknown {
    if (Array.isArray(value)) {
        return value.map((item, index) => replaceWithPortableAssetReferences(item, [...path, String(index)], resolveHash, mediaAssetPaths));
    }
    if (!value || typeof value !== "object") return value;
    const result: Record<string, unknown> = {};
    for (const [key, item] of Object.entries(value)) {
        if (isLocalUploadField(key, value as Record<string, unknown>)) continue;
        if (ASSET_REFERENCE_FIELDS.has(key as SharedCanvasAssetField) && typeof item === "string") {
            const field = key as SharedCanvasAssetField;
            const referencePath = [...path, key];
            const mediaReference = mediaAssetPaths.has(JSON.stringify(referencePath)) && isLocalMediaReference(item);
            const record = value as Record<string, unknown>;
            // Object URLs are ephemeral aliases of the durable media, not storage keys.
            // A previewContent thumbnail may differ from the original, so keep it separate.
            const sourceField = typeof record.storageKey === "string" && record.storageKey ? "storageKey" : "resourceId";
            const sourceValue = record[sourceField];
            const source = field !== "previewContent" && /^(?:blob:|data:)/i.test(item) && typeof sourceValue === "string" && sourceValue
                ? { field: sourceField, value: sourceValue } as SharedCanvasAssetSource : undefined;
            const contentHash = field === "storageKey" || field === "resourceId" || ((field === "dataUrl" || mediaReference) && isLocalMediaReference(item)) ? resolveHash({ field, localValue: item, path: referencePath, source }) : undefined;
            if (contentHash) {
                result[key] = { __beeftvSharedAsset: true, contentHash, localField: field } satisfies SharedCanvasAssetReference;
            } else if (!mediaReference && field !== "storageKey" && field !== "resourceId" && !(field === "dataUrl" && isLocalMediaReference(item))) result[key] = item;
            continue;
        }
        result[key] = replaceWithPortableAssetReferences(item, [...path, key], resolveHash, mediaAssetPaths);
    }
    return result;
}

function replaceWithLocalAssetReferences(value: unknown, path: string[], resolveLocalValue: SharedCanvasLocalAssetResolver): unknown {
    if (Array.isArray(value)) {
        return value.map((item, index) => replaceWithLocalAssetReferences(item, [...path, String(index)], resolveLocalValue));
    }
    if (!value || typeof value !== "object") return value;
    const result: Record<string, unknown> = {};
    for (const [key, item] of Object.entries(value)) {
        // Older peers may still publish upload UI state. It belongs to that device.
        if (isLocalUploadField(key, value as Record<string, unknown>)) continue;
        if (ASSET_REFERENCE_FIELDS.has(key as SharedCanvasAssetField) && isAssetReference(item)) {
            const localValue = resolveLocalValue({ field: item.localField, contentHash: item.contentHash, path: [...path, key] });
            if (localValue) result[key] = localValue;
            continue;
        }
        result[key] = replaceWithLocalAssetReferences(item, [...path, key], resolveLocalValue);
    }
    return result;
}

function isAssetReference(value: unknown): value is SharedCanvasAssetReference {
    if (!value || typeof value !== "object") return false;
    const candidate = value as Partial<SharedCanvasAssetReference>;
    return candidate.__beeftvSharedAsset === true && typeof candidate.contentHash === "string" && ASSET_REFERENCE_FIELDS.has(candidate.localField as SharedCanvasAssetField);
}

export function toPortableSharedCanvasContent(content: SharedCanvasContent, resolveHash: SharedCanvasAssetHashResolver): PortableSharedCanvasContent {
    const mediaAssetPaths = new Set<string>();
    const addPath = (path: string[]) => mediaAssetPaths.add(JSON.stringify(path));
    const mediaNodeTypes = new Set(["image", "video", "audio"]);
    content.nodes.forEach((node, index) => {
        if (!mediaNodeTypes.has(node.type)) return;
        const base = ["nodes", String(index), "metadata"];
        for (const field of ["content", "previewContent"]) addPath([...base, field]);
        if (node.metadata?.videoPreview) {
            addPath([...base, "videoPreview", "content"]);
            addPath([...base, "videoPreview", "storageKey"]);
        }
    });
    content.timeline?.clips.forEach((clip, index) => {
        if (!clip.directMedia) return;
        const base = ["timeline", "clips", String(index), "directMedia"];
        for (const field of ["content", "url", "dataUrl", "storageKey"]) addPath([...base, field]);
    });
    return replaceWithPortableAssetReferences(content, [], resolveHash, mediaAssetPaths) as PortableSharedCanvasContent;
}

export function collectSharedCanvasAssetReferences(content: SharedCanvasContent) {
    const references: Array<{ field: SharedCanvasAssetField; value: string; path: string[]; source?: SharedCanvasAssetSource }> = [];
    toPortableSharedCanvasContent(content, ({ field, localValue, path, source }) => {
        references.push({ field, value: localValue, path, ...(source ? { source } : {}) });
        return undefined;
    });
    return references;
}

export function restoreLocalSharedCanvasContent(content: PortableSharedCanvasContent, resolveLocalValue: SharedCanvasLocalAssetResolver): SharedCanvasContent {
    return replaceWithLocalAssetReferences(content, [], resolveLocalValue) as SharedCanvasContent;
}

// Keep only this device's pending media and upload UI while projecting remote
// edits. Match arrays by entity ID; a remote replacement or deletion wins.
export function retainLocalSharedCanvasAssets(next: SharedCanvasContent, before: PortableSharedCanvasContent, local: SharedCanvasContent, portable: PortableSharedCanvasContent): SharedCanvasContent {
    const match = (items: unknown, item: unknown, index: number) => {
        if (!Array.isArray(items)) return undefined;
        const id = isRecord(item) ? item.id : undefined;
        return id === undefined ? items[index] : items.find((value) => isRecord(value) && value.id === id);
    };
    const visit = (value: unknown, prior: unknown, previousLocal: unknown, shared: unknown): unknown => {
        if (Array.isArray(value)) return value.map((item, index) => visit(item, match(prior, item, index), match(previousLocal, item, index), match(shared, item, index)));
        if (!isRecord(value) || !isRecord(previousLocal)) return value;
        const old = isRecord(prior) ? prior : {};
        const current = isRecord(shared) ? shared : {};
        const result: Record<string, unknown> = { ...value };
        for (const [key, item] of Object.entries(previousLocal)) {
            if (isLocalUploadField(key, previousLocal)) {
                if (item !== undefined) result[key] = item;
            } else if (ASSET_REFERENCE_FIELDS.has(key as SharedCanvasAssetField) && typeof item === "string"
                && (key === "storageKey" || key === "resourceId" || isLocalMediaReference(item))
                && old[key] === undefined && current[key] === undefined) {
                result[key] = item;
            } else if (key in result) result[key] = visit(result[key], old[key], item, current[key]);
        }
        return result;
    };
    return visit(next, before, local, portable) as SharedCanvasContent;
}

// A missing local file projects as an absent field. Editing its node must not
// delete the shared hash; only an actual change to that local field replaces it.
export function retainUnresolvedSharedCanvasAssets(next: PortableSharedCanvasContent, previous: PortableSharedCanvasContent, previousLocal: SharedCanvasContent, currentLocal: SharedCanvasContent): PortableSharedCanvasContent {
    const visit = (portable: unknown, before: unknown, localBefore: unknown, localNow: unknown): unknown => {
        if (isAssetReference(before) && localBefore === localNow) return before;
        if (Array.isArray(portable)) {
            const prior = Array.isArray(before) ? before : [];
            const localPrior = Array.isArray(localBefore) ? localBefore : [];
            const localCurrent = Array.isArray(localNow) ? localNow : [];
            const match = (items: unknown[], item: unknown, index: number) => {
                const id = item && typeof item === "object" && "id" in item ? item.id : undefined;
                return id === undefined ? items[index] : items.find((value) => value && typeof value === "object" && "id" in value && value.id === id);
            };
            return portable.map((item, index) => visit(item, match(prior, item, index), match(localPrior, item, index), match(localCurrent, item, index)));
        }
        if (!portable || typeof portable !== "object" || !localNow || typeof localNow !== "object") return portable;
        const result = { ...portable } as Record<string, unknown>;
        const old = (before && typeof before === "object" ? before : {}) as Record<string, unknown>;
        const oldLocal = (localBefore && typeof localBefore === "object" ? localBefore : {}) as Record<string, unknown>;
        const current = localNow as Record<string, unknown>;
        const replacedHashes = new Set(Object.entries(old).flatMap(([key, value]) =>
            isAssetReference(value) && oldLocal[key] !== current[key] ? [value.contentHash] : []));
        for (const key of new Set([...Object.keys(result), ...Object.keys(old)])) {
            // Replacing an unresolved media URL also invalidates absent aliases
            // of the old file (otherwise its storageKey wins over the new URL).
            if (isAssetReference(old[key]) && replacedHashes.has(old[key].contentHash) && current[key] === undefined && oldLocal[key] === undefined) continue;
            const value = visit(result[key], old[key], oldLocal[key], current[key]);
            if (value !== undefined) result[key] = value;
        }
        return result;
    };
    return visit(next, previous, previousLocal, currentLocal) as PortableSharedCanvasContent;
}

function setSharedValue(parent: Y.Map<unknown>, key: string, value: unknown) {
    if (Array.isArray(value)) {
        let array = parent.get(key);
        if (!(array instanceof Y.Array)) {
            array = new Y.Array<unknown>();
            parent.set(key, array);
        }
        const sharedArray = array as Y.Array<unknown>;
        const next = value.map(encodeNestedValue);
        if (JSON.stringify(sharedArray.toArray().map(decodeNestedValue)) !== JSON.stringify(value)) {
            if (sharedArray.length) sharedArray.delete(0, sharedArray.length);
            if (next.length) sharedArray.insert(0, next);
        }
        return;
    }
    if (value && typeof value === "object" && !(value instanceof Uint8Array)) {
        let nested = parent.get(key);
        if (!(nested instanceof Y.Map)) {
            nested = new Y.Map<unknown>();
            parent.set(key, nested);
        }
        const sharedMap = nested as Y.Map<unknown>;
        const nextObject = value as Record<string, unknown>;
        for (const existingKey of [...sharedMap.keys()]) if (!(existingKey in nextObject)) sharedMap.delete(existingKey);
        for (const [nestedKey, nestedValue] of Object.entries(nextObject)) setSharedValue(sharedMap, nestedKey, nestedValue);
        return;
    }
    if (parent.get(key) !== value) parent.set(key, value);
}

function encodeNestedValue(value: unknown): unknown {
    if (Array.isArray(value)) {
        const result = new Y.Array<unknown>();
        const children = value.map(encodeNestedValue);
        if (children.length) result.insert(0, children);
        return result;
    }
    if (value && typeof value === "object" && !(value instanceof Uint8Array)) {
        const result = new Y.Map<unknown>();
        for (const [key, nestedValue] of Object.entries(value)) setSharedValue(result, key, nestedValue);
        return result;
    }
    return value;
}

function decodeNestedValue(value: unknown): unknown {
    if (value instanceof Y.Array) return value.toArray().map(decodeNestedValue);
    if (value instanceof Y.Map) {
        const result: Record<string, unknown> = {};
        for (const [key, nestedValue] of value.entries()) result[key] = decodeNestedValue(nestedValue);
        return result;
    }
    return value;
}

function writeEntity(map: Y.Map<unknown>, entity: Entity) {
    const id = requireId(entity.id, "Entity");
    let record = map.get(id);
    if (!(record instanceof Y.Map)) {
        record = new Y.Map<unknown>();
        map.set(id, record);
        (record as Y.Map<unknown>).set(DELETED, false);
    }
    const fields = record as Y.Map<unknown>;
    if (fields.get(DELETED) === true) return id;
    for (const key of [...fields.keys()]) {
        if (key !== DELETED && !(key in entity)) fields.delete(key);
    }
    for (const [key, value] of Object.entries(entity)) {
        if (key === "id") continue;
        if (value !== undefined) setSharedValue(fields, key, value);
        else fields.delete(key);
    }
    return id;
}

function patchEntity(map: Y.Map<unknown>, id: string, patch: Record<string, unknown>) {
    const entityId = requireId(id, "Entity");
    let record = map.get(entityId);
    if (!(record instanceof Y.Map)) {
        record = new Y.Map<unknown>();
        map.set(entityId, record);
        (record as Y.Map<unknown>).set(DELETED, false);
    }
    const fields = record as Y.Map<unknown>;
    if (fields.get(DELETED) === true) return false;
    for (const [key, value] of Object.entries(patch)) {
        if (key === "id" || key === DELETED) continue;
        if (value !== undefined) setSharedValue(fields, key, value);
        else fields.delete(key);
    }
    return true;
}

function isRecord(value: unknown): value is Record<string, unknown> {
    return value !== null && typeof value === "object" && !Array.isArray(value) && !(value instanceof Uint8Array);
}

function applySharedValueDelta(parent: Y.Map<unknown>, key: string, before: unknown, after: unknown) {
    if (after === undefined) {
        parent.delete(key);
        return;
    }
    if (!isRecord(after)) {
        setSharedValue(parent, key, after);
        return;
    }
    let current = parent.get(key);
    if (!(current instanceof Y.Map)) {
        current = new Y.Map<unknown>();
        parent.set(key, current);
    }
    const nested = current as Y.Map<unknown>;
    const previous = isRecord(before) ? before : {};
    for (const childKey of new Set([...Object.keys(previous), ...Object.keys(after)])) {
        if (JSON.stringify(previous[childKey]) === JSON.stringify(after[childKey])) continue;
        applySharedValueDelta(nested, childKey, previous[childKey], after[childKey]);
    }
}

function patchEntityDelta(map: Y.Map<unknown>, id: string, before: Entity, after: Entity) {
    const record = map.get(requireId(id, "Entity"));
    if (!(record instanceof Y.Map) || record.get(DELETED) === true) return;
    for (const key of new Set([...Object.keys(before), ...Object.keys(after)])) {
        if (key === "id" || key === DELETED) continue;
        if (JSON.stringify(before[key]) === JSON.stringify(after[key])) continue;
        applySharedValueDelta(record, key, before[key], after[key]);
    }
}

function deleteEntity(map: Y.Map<unknown>, id: string) {
    const record = map.get(requireId(id, "Entity"));
    if (!(record instanceof Y.Map) || record.get(DELETED) === true) return false;
    (record as Y.Map<unknown>).set(DELETED, true);
    return true;
}

function readEntity(map: Y.Map<unknown>, id: string): Entity | undefined {
    const record = map.get(id);
    if (!(record instanceof Y.Map) || record.get(DELETED) === true) return undefined;
    const result: Entity = { id };
    for (const [key, value] of record.entries()) {
        if (key !== DELETED && value !== undefined) result[key] = decodeNestedValue(value);
    }
    return result;
}

function listEntities<T extends Entity>(map: Y.Map<unknown>, order: Y.Array<string>): T[] {
    const seen = new Set<string>();
    const ids = [...order.toArray()];
    for (const id of [...map.keys()].sort()) if (!seen.has(id)) ids.push(id);
    const result: T[] = [];
    for (const id of ids) {
        if (seen.has(id)) continue;
        seen.add(id);
        const entity = readEntity(map, id);
        if (entity) result.push(entity as T);
    }
    return result;
}

function setOrder(order: Y.Array<string>, ids: string[]) {
    const currentLength = order.length;
    if (currentLength) order.delete(0, currentLength);
    if (ids.length) order.insert(0, ids);
}

function reconcileEntities(map: Y.Map<unknown>, order: Y.Array<string>, entities: Entity[]) {
    const nextIds = new Set<string>();
    const orderedIds: string[] = [];
    for (const entity of entities) {
        const id = writeEntity(map, entity);
        if (!nextIds.has(id)) orderedIds.push(id);
        nextIds.add(id);
    }
    for (const id of map.keys()) if (!nextIds.has(id)) deleteEntity(map, id);
    setOrder(order, orderedIds);
}

function applyLocalEntityChanges(map: Y.Map<unknown>, order: Y.Array<string>, before: Entity[], after: Entity[]) {
    const previous = new Map(before.map((entity) => [entity.id, entity]));
    const next = new Map(after.map((entity) => [entity.id, entity]));
    for (const [id, entity] of next) {
        const old = previous.get(id);
        if (!old) { writeEntity(map, entity); continue; }
        patchEntityDelta(map, id, old, entity);
    }
    for (const id of previous.keys()) if (!next.has(id)) deleteEntity(map, id);
    if (JSON.stringify(before.map((entity) => entity.id)) !== JSON.stringify(after.map((entity) => entity.id))) {
        const remoteIds = order.toArray().filter((id) => !previous.has(id) && !next.has(id));
        setOrder(order, [...after.map((entity) => entity.id), ...remoteIds]);
    }
}

function getRoot(doc: Y.Doc) {
    return doc.getMap<unknown>(ROOT);
}

function getTimeline(root: Y.Map<unknown>) {
    return entityMap(root, TIMELINE);
}

export function createCollaborativeCanvasDocument(content?: SharedCanvasContent | PortableSharedCanvasContent) {
    const doc = new Y.Doc();
    if (content) writeSharedCanvasContent(doc, content);
    return doc;
}

export function writeSharedCanvasContent(doc: Y.Doc, content: SharedCanvasContent | PortableSharedCanvasContent) {
    doc.transact(() => {
        const root = getRoot(doc);
        const properties = entityMap(root, PROPERTIES);
        properties.set("title", cloneJsonValue(content.title));
        reconcileEntities(entityMap(root, NODES), orderArray(root, NODE_ORDER), content.nodes as Entity[]);
        reconcileEntities(entityMap(root, CONNECTIONS), orderArray(root, CONNECTION_ORDER), content.connections as Entity[]);
        reconcileEntities(entityMap(root, DIRECTOR_SCENES), orderArray(root, DIRECTOR_SCENE_ORDER), content.directorScenes as Entity[]);

        const timeline = getTimeline(root);
        const timelineContent = content.timeline as TimelineProject | undefined;
        if (timelineContent) {
            timeline.set(DELETED, false);
            const fields = entityMap(timeline, "fields");
            const { tracks, clips, ...timelineFields } = timelineContent;
            for (const key of [...fields.keys()]) if (!(key in timelineFields)) fields.delete(key);
            for (const [key, value] of Object.entries(timelineFields)) {
                if (value !== undefined) setSharedValue(fields, key, value);
            }
            reconcileEntities(entityMap(timeline, TIMELINE_TRACKS), orderArray(timeline, TIMELINE_TRACK_ORDER), tracks as Entity[]);
            reconcileEntities(entityMap(timeline, TIMELINE_CLIPS), orderArray(timeline, TIMELINE_CLIP_ORDER), clips as Entity[]);
        } else {
            timeline.set(DELETED, true);
        }
    });
}

export function applyCollaborativeCanvasLocalChanges(doc: Y.Doc, before: PortableSharedCanvasContent, after: PortableSharedCanvasContent) {
    doc.transact(() => {
        const root = getRoot(doc);
        if (before.title !== after.title) entityMap(root, PROPERTIES).set("title", after.title);
        applyLocalEntityChanges(entityMap(root, NODES), orderArray(root, NODE_ORDER), before.nodes as Entity[], after.nodes as Entity[]);
        applyLocalEntityChanges(entityMap(root, CONNECTIONS), orderArray(root, CONNECTION_ORDER), before.connections as Entity[], after.connections as Entity[]);
        applyLocalEntityChanges(entityMap(root, DIRECTOR_SCENES), orderArray(root, DIRECTOR_SCENE_ORDER), before.directorScenes as Entity[], after.directorScenes as Entity[]);
        if (JSON.stringify(before.timeline) !== JSON.stringify(after.timeline)) {
            const timeline = getTimeline(root);
            if (!after.timeline) timeline.set(DELETED, true);
            else {
                timeline.set(DELETED, false);
                const previous = (before.timeline || {}) as Record<string, unknown>;
                const next = after.timeline as Record<string, unknown>;
                const fields = entityMap(timeline, "fields");
                for (const key of new Set([...Object.keys(previous), ...Object.keys(next)])) {
                    if (key === "tracks" || key === "clips") continue;
                    if (JSON.stringify(previous[key]) === JSON.stringify(next[key])) continue;
                    if (next[key] === undefined) fields.delete(key);
                    else setSharedValue(fields, key, next[key]);
                }
                applyLocalEntityChanges(entityMap(timeline, TIMELINE_TRACKS), orderArray(timeline, TIMELINE_TRACK_ORDER), (previous.tracks || []) as Entity[], (next.tracks || []) as Entity[]);
                applyLocalEntityChanges(entityMap(timeline, TIMELINE_CLIPS), orderArray(timeline, TIMELINE_CLIP_ORDER), (previous.clips || []) as Entity[], (next.clips || []) as Entity[]);
            }
        }
    });
}

// Explicit history restoration may revive tombstones. Ordinary stale edits
// still use applyCollaborativeCanvasLocalChanges and never resurrect deletions.
export function restoreCollaborativeCanvasVersion(doc: Y.Doc, content: PortableSharedCanvasContent) {
    content = stripLocalUploadState(content) as PortableSharedCanvasContent;
    doc.transact(() => {
        const root = getRoot(doc);
        const revive = (map: Y.Map<unknown>, entities: PortableValue[]) => {
            for (const entity of entities) {
                if (!isRecord(entity) || !("id" in entity) || typeof entity.id !== "string") continue;
                const record = map.get(entity.id);
                if (record instanceof Y.Map && record.get(DELETED) === true) record.set(DELETED, false);
            }
        };
        revive(entityMap(root, NODES), content.nodes);
        revive(entityMap(root, CONNECTIONS), content.connections);
        revive(entityMap(root, DIRECTOR_SCENES), content.directorScenes);
        if (isRecord(content.timeline) && "tracks" in content.timeline && "clips" in content.timeline) {
            const timeline = getTimeline(root);
            revive(entityMap(timeline, TIMELINE_TRACKS), (content.timeline.tracks || []) as PortableValue[]);
            revive(entityMap(timeline, TIMELINE_CLIPS), (content.timeline.clips || []) as PortableValue[]);
        }
        writeSharedCanvasContent(doc, content);
    });
}

export function readSharedCanvasContent(doc: Y.Doc): SharedCanvasContent {
    const root = getRoot(doc);
    const properties = readMap(root.get(PROPERTIES));
    const result: SharedCanvasContent = {
        title: String(properties.get("title") ?? ""),
        nodes: listEntities<CanvasNodeData & Entity>(readMap(root.get(NODES)), readOrder(root.get(NODE_ORDER))),
        connections: listEntities<CanvasConnection & Entity>(readMap(root.get(CONNECTIONS)), readOrder(root.get(CONNECTION_ORDER))),
        directorScenes: listEntities<DirectorScene & Entity>(readMap(root.get(DIRECTOR_SCENES)), readOrder(root.get(DIRECTOR_SCENE_ORDER))),
    };
    const timeline = root.get(TIMELINE);
    if (timeline instanceof Y.Map && timeline.get(DELETED) !== true) {
        const fields = asMap(timeline.get("fields"));
        const value: Record<string, unknown> = {};
        for (const [key, field] of fields.entries()) value[key] = decodeNestedValue(field);
        value.tracks = listEntities<TimelineTrack & Entity>(readMap(timeline.get(TIMELINE_TRACKS)), readOrder(timeline.get(TIMELINE_TRACK_ORDER)));
        value.clips = listEntities<TimelineClip & Entity>(readMap(timeline.get(TIMELINE_CLIPS)), readOrder(timeline.get(TIMELINE_CLIP_ORDER)));
        result.timeline = value as unknown as TimelineProject;
    }
    return result;
}

export function readPortableSharedCanvasContent(doc: Y.Doc): PortableSharedCanvasContent {
    return readSharedCanvasContent(doc) as unknown as PortableSharedCanvasContent;
}

export function patchCollaborativeCanvasNode(doc: Y.Doc, nodeId: string, patch: Partial<Omit<CanvasNodeData, "id">>) {
    return patchEntity(entityMap(getRoot(doc), NODES), nodeId, patch as Record<string, unknown>);
}

export function patchCollaborativeCanvasTitle(doc: Y.Doc, title: string) {
    entityMap(getRoot(doc), PROPERTIES).set("title", title);
}

export function deleteCollaborativeCanvasNode(doc: Y.Doc, nodeId: string) {
    return deleteEntity(entityMap(getRoot(doc), NODES), nodeId);
}

export function patchCollaborativeCanvasConnection(doc: Y.Doc, connectionId: string, patch: Partial<Omit<CanvasConnection, "id">>) {
    return patchEntity(entityMap(getRoot(doc), CONNECTIONS), connectionId, patch as Record<string, unknown>);
}

export function deleteCollaborativeCanvasConnection(doc: Y.Doc, connectionId: string) {
    return deleteEntity(entityMap(getRoot(doc), CONNECTIONS), connectionId);
}

export function applyCollaborativeCanvasUpdate(doc: Y.Doc, update: Uint8Array, origin?: unknown) {
    Y.applyUpdate(doc, update, origin);
}

export function encodeCollaborativeCanvasState(doc: Y.Doc) {
    return Y.encodeStateAsUpdate(doc);
}

export function encodeCollaborativeCanvasStateVector(doc: Y.Doc) {
    return Y.encodeStateVector(doc);
}
