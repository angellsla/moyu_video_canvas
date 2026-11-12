import { expect, test } from "bun:test";
import * as Y from "yjs";

import {
    applyCollaborativeCanvasUpdate,
    applyCollaborativeCanvasLocalChanges,
    collectSharedCanvasAssetReferences,
    createCollaborativeCanvasDocument,
    deleteCollaborativeCanvasNode,
    encodeCollaborativeCanvasState,
    encodeCollaborativeCanvasStateVector,
    patchCollaborativeCanvasNode,
    patchCollaborativeCanvasTitle,
    readPortableSharedCanvasContent,
    readSharedCanvasContent,
    restoreLocalSharedCanvasContent,
    retainUnresolvedSharedCanvasAssets,
    retainLocalSharedCanvasAssets,
    restoreCollaborativeCanvasVersion,
    toPortableSharedCanvasContent,
    writeSharedCanvasContent,
    type SharedCanvasContent,
} from "@/services/collaborative-canvas-document";

test("upload status stays local and legacy upload flags cannot hide a ready image", () => {
    const local = content();
    local.nodes[0].type = "image";
    local.nodes[0].metadata = { content: "blob:pending", fileUpload: "uploading", fileUploadProgress: 30 };
    const portable = toPortableSharedCanvasContent(local, () => undefined);
    expect(portable.nodes[0]).toMatchObject({ metadata: {} });
    expect(JSON.stringify(portable)).not.toContain("fileUpload");
    const projected = restoreLocalSharedCanvasContent(portable, () => undefined);
    expect(retainLocalSharedCanvasAssets(projected, portable, local, portable).nodes[0].metadata).toEqual(local.nodes[0].metadata);
    const legacy = toPortableSharedCanvasContent(local, () => "hash");
    (legacy.nodes[0] as { metadata: object }).metadata = { ...(legacy.nodes[0] as { metadata: object }).metadata, fileUpload: "uploading", fileUploadProgress: 30 };
    expect(restoreLocalSharedCanvasContent(legacy, () => "/api/resources/local/file").nodes[0].metadata).toEqual({ content: "/api/resources/local/file" });
});

test("local pending media follows node IDs without undoing remote replacements or deletions", () => {
    const local = content();
    local.nodes[0].type = "image";
    local.nodes[0].metadata = { content: "blob:pending", storageKey: "resource:new" };
    const base = toPortableSharedCanvasContent(local, () => undefined);
    const reordered = structuredClone(base);
    reordered.nodes.unshift({ id: "remote", type: "text", title: "Remote" });
    const merged = retainLocalSharedCanvasAssets(restoreLocalSharedCanvasContent(reordered, () => undefined), base, local, reordered);
    expect(merged.nodes[1].metadata).toEqual(local.nodes[0].metadata);
    expect(merged.nodes[0].metadata).toBeUndefined();
    const replaced = toPortableSharedCanvasContent(local, () => "remote-hash");
    expect(retainLocalSharedCanvasAssets(restoreLocalSharedCanvasContent(replaced, () => "remote-file"), base, local, replaced).nodes[0].metadata?.content).toBe("remote-file");
    expect(retainLocalSharedCanvasAssets({ ...local, nodes: [] }, base, local, { ...base, nodes: [] }).nodes).toEqual([]);
});

test("explicit history restore revives deleted nodes and converges as a new Yjs update", () => {
    const doc = createCollaborativeCanvasDocument(content());
    const history = readPortableSharedCanvasContent(doc);
    const peer = createCollaborativeCanvasDocument();
    applyCollaborativeCanvasUpdate(peer, encodeCollaborativeCanvasState(doc));
    const id = content().nodes[0].id;
    deleteCollaborativeCanvasNode(doc, id);
    applyCollaborativeCanvasUpdate(peer, encodeCollaborativeCanvasState(doc));
    expect(readSharedCanvasContent(peer).nodes).toEqual([]);
    restoreCollaborativeCanvasVersion(doc, history);
    applyCollaborativeCanvasUpdate(peer, encodeCollaborativeCanvasState(doc));
    expect(readPortableSharedCanvasContent(peer)).toEqual(history);
    patchCollaborativeCanvasNode(peer, id, { title: "Editable again" });
    applyCollaborativeCanvasUpdate(doc, encodeCollaborativeCanvasState(peer));
    expect(readSharedCanvasContent(doc).nodes[0].title).toBe("Editable again");
    doc.destroy(); peer.destroy();
});

test("editing and reordering nodes with pending files preserves shared asset hashes", () => {
    const source = content();
    source.nodes[0].type = "image";
    source.nodes[0].metadata = { storageKey: "resource:image", content: "blob:preview" };
    const baseline = toPortableSharedCanvasContent(source, () => "hash-image");
    const local = restoreLocalSharedCanvasContent(baseline, () => undefined);
    const current = structuredClone(local);
    current.nodes[0].position.x = 700;
    current.nodes.unshift({ ...content().nodes[0], id: "new-node" });
    const next = retainUnresolvedSharedCanvasAssets(toPortableSharedCanvasContent(current, () => undefined), baseline, local, current);
    expect(next.nodes[1]).toMatchObject({ position: { x: 700 }, metadata: { storageKey: { contentHash: "hash-image" }, content: { contentHash: "hash-image" } } });
    expect(restoreLocalSharedCanvasContent(next, ({ field }) => field === "storageKey" ? "resource:downloaded" : "/api/resources/downloaded/file").nodes[1].metadata).toMatchObject({ storageKey: "resource:downloaded" });
    current.nodes = [];
    expect(retainUnresolvedSharedCanvasAssets(toPortableSharedCanvasContent(current, () => undefined), baseline, local, current).nodes).toEqual([]);
});

test("explicit media replacement and clearing are not undone by unavailable-asset preservation", () => {
    const source = content();
    source.nodes[0].type = "image";
    source.nodes[0].metadata = { storageKey: "resource:old", content: "blob:preview" };
    const baseline = toPortableSharedCanvasContent(source, () => "old-hash");
    const local = restoreLocalSharedCanvasContent(baseline, () => undefined);
    const current = structuredClone(local);
    current.nodes[0].metadata!.content = "resource:new";
    let next = retainUnresolvedSharedCanvasAssets(toPortableSharedCanvasContent(current, () => "new-hash"), baseline, local, current);
    expect(next.nodes[0]).toMatchObject({ metadata: { content: { contentHash: "new-hash" } } });
    expect((next.nodes[0] as { metadata: object }).metadata).not.toHaveProperty("storageKey");
    current.nodes[0].metadata!.content = "";
    next = retainUnresolvedSharedCanvasAssets(toPortableSharedCanvasContent(current, () => undefined), baseline, local, current);
    expect(next.nodes[0]).toMatchObject({ metadata: { content: "" } });
});

test("local delta preserves a remote addition made after the local snapshot", () => {
    const doc = createCollaborativeCanvasDocument(content());
    const base = readPortableSharedCanvasContent(doc);
    writeSharedCanvasContent(doc, { ...content(), nodes: [...content().nodes, { ...content().nodes[0], id: "node-remote", title: "Remote" }] });
    applyCollaborativeCanvasLocalChanges(doc, base, { ...base, nodes: [{ ...base.nodes[0] as Record<string, unknown>, title: "Local" }] });
    expect(readSharedCanvasContent(doc).nodes.map((node) => [node.id, node.title])).toEqual([["node-a", "Local"], ["node-remote", "Remote"]]);
    doc.destroy();
});

test("local nested delta preserves remote metadata fields added after the local snapshot", () => {
    const initial = content();
    initial.nodes[0].metadata = { pluginData: { baseline: true } };
    const doc = createCollaborativeCanvasDocument(initial);
    const base = readPortableSharedCanvasContent(doc);
    patchCollaborativeCanvasNode(doc, "node-a", { metadata: { pluginData: { baseline: true, remote: true } } });
    const localNode = structuredClone(base.nodes[0]) as Record<string, unknown>;
    (localNode.metadata as { pluginData: Record<string, boolean> }).pluginData.local = true;
    applyCollaborativeCanvasLocalChanges(doc, base, { ...base, nodes: [localNode] });
    expect(readSharedCanvasContent(doc).nodes[0].metadata?.pluginData).toEqual({ baseline: true, remote: true, local: true });
    doc.destroy();
});

test("local nested delta does not restore a concurrently removed metadata field", () => {
    const initial = content();
    initial.nodes[0].metadata = { pluginData: { obsolete: true, baseline: true } };
    const doc = createCollaborativeCanvasDocument(initial);
    const base = readPortableSharedCanvasContent(doc);
    patchCollaborativeCanvasNode(doc, "node-a", { metadata: { pluginData: { baseline: true } } });
    const localNode = structuredClone(base.nodes[0]) as Record<string, unknown>;
    (localNode.metadata as { pluginData: Record<string, boolean> }).pluginData.local = true;
    applyCollaborativeCanvasLocalChanges(doc, base, { ...base, nodes: [localNode] });
    expect(readSharedCanvasContent(doc).nodes[0].metadata?.pluginData).toEqual({ baseline: true, local: true });
    doc.destroy();
});

test("local timeline clip delta preserves a remote nested media field", () => {
    const initial = content();
    initial.timeline!.clips[0].directMedia = { id: "media-a", kind: "video", title: "Source" };
    const doc = createCollaborativeCanvasDocument(initial);
    const base = readPortableSharedCanvasContent(doc);
    writeSharedCanvasContent(doc, {
        ...initial,
        timeline: {
            ...initial.timeline!,
            clips: [{ ...initial.timeline!.clips[0], directMedia: { ...initial.timeline!.clips[0].directMedia!, mimeType: "video/mp4" } }],
        },
    });
    const local = structuredClone(base);
    const localClip = (local.timeline as { clips: Array<{ directMedia: { title: string } }> }).clips[0];
    localClip.directMedia.title = "Local title";
    applyCollaborativeCanvasLocalChanges(doc, base, local);
    expect(readSharedCanvasContent(doc).timeline?.clips[0].directMedia).toMatchObject({ title: "Local title", mimeType: "video/mp4" });
    doc.destroy();
});

test("local delta leaves concurrently deleted nodes deleted", () => {
    const doc = createCollaborativeCanvasDocument(content());
    const base = readPortableSharedCanvasContent(doc);
    deleteCollaborativeCanvasNode(doc, "node-a");
    applyCollaborativeCanvasLocalChanges(doc, base, { ...base, nodes: [{ ...base.nodes[0] as Record<string, unknown>, title: "Late edit" }] });
    expect(readSharedCanvasContent(doc).nodes).toEqual([]);
    doc.destroy();
});

function content(title = "Shared board"): SharedCanvasContent {
    return {
        title,
        nodes: [{ id: "node-a", type: "text", title: "A", position: { x: 10, y: 20 }, width: 320, height: 180 }],
        connections: [{ id: "connection-a", fromNodeId: "node-a", toNodeId: "node-a" }],
        directorScenes: [],
        timeline: {
            version: 2,
            durationMs: 1000,
            tracks: [{ id: "track-a", kind: "video", label: "Video", order: 0 }],
            clips: [{ id: "clip-a", kind: "video", nodeId: "node-a", trackId: "track-a", startMs: 0, durationMs: 1000 }],
        },
    };
}

test("shared document import and export includes shared fields only", () => {
    const doc = createCollaborativeCanvasDocument(content());
    const exported = readSharedCanvasContent(doc);

    expect(exported).toEqual(content());
    expect(exported).not.toHaveProperty("viewport");
    expect(exported).not.toHaveProperty("chatSessions");
    doc.destroy();
});

test("title can be changed without reconciling entities", () => {
    const doc = createCollaborativeCanvasDocument(content());
    patchCollaborativeCanvasTitle(doc, "Renamed");
    expect(readSharedCanvasContent(doc)).toEqual({ ...content(), title: "Renamed" });
    doc.destroy();
});

test("independent node field edits merge without overwriting one another", () => {
    const seed = createCollaborativeCanvasDocument(content());
    const left = new Y.Doc();
    const right = new Y.Doc();
    applyCollaborativeCanvasUpdate(left, encodeCollaborativeCanvasState(seed));
    applyCollaborativeCanvasUpdate(right, encodeCollaborativeCanvasState(seed));

    patchCollaborativeCanvasNode(left, "node-a", { title: "Left title" });
    patchCollaborativeCanvasNode(right, "node-a", { width: 640 });
    const leftUpdate = encodeCollaborativeCanvasState(left);
    const rightUpdate = encodeCollaborativeCanvasState(right);
    applyCollaborativeCanvasUpdate(left, rightUpdate);
    applyCollaborativeCanvasUpdate(right, leftUpdate);

    expect(readSharedCanvasContent(left).nodes[0]).toMatchObject({ title: "Left title", width: 640 });
    expect(readSharedCanvasContent(right).nodes[0]).toMatchObject({ title: "Left title", width: 640 });
    seed.destroy();
    left.destroy();
    right.destroy();
});

test("independent nested node metadata fields merge", () => {
    const initial = content();
    initial.nodes[0].metadata = { pluginId: "fixture", pluginData: { left: false, right: false } };
    const seed = createCollaborativeCanvasDocument(initial);
    const left = new Y.Doc();
    const right = new Y.Doc();
    applyCollaborativeCanvasUpdate(left, encodeCollaborativeCanvasState(seed));
    applyCollaborativeCanvasUpdate(right, encodeCollaborativeCanvasState(seed));

    patchCollaborativeCanvasNode(left, "node-a", { metadata: { ...initial.nodes[0].metadata, pluginData: { left: true, right: false } } });
    patchCollaborativeCanvasNode(right, "node-a", { metadata: { ...initial.nodes[0].metadata, pluginData: { left: false, right: true } } });
    const leftUpdate = encodeCollaborativeCanvasState(left);
    const rightUpdate = encodeCollaborativeCanvasState(right);
    applyCollaborativeCanvasUpdate(left, rightUpdate);
    applyCollaborativeCanvasUpdate(right, leftUpdate);

    expect(readSharedCanvasContent(left).nodes[0].metadata?.pluginData).toEqual({ left: true, right: true });
    expect(readSharedCanvasContent(right).nodes[0].metadata?.pluginData).toEqual({ left: true, right: true });
    seed.destroy();
    left.destroy();
    right.destroy();
});

test("snapshot reconciliation removes omitted optional entity fields", () => {
    const initial = content();
    initial.nodes[0].metadata = { content: "draft" };
    initial.connections[0].relation = "batch-output";
    const doc = createCollaborativeCanvasDocument(initial);
    writeSharedCanvasContent(doc, {
        ...initial,
        nodes: [{ ...initial.nodes[0], metadata: undefined }],
        connections: [{ id: "connection-a", fromNodeId: "node-a", toNodeId: "node-a" }],
    });

    expect(readSharedCanvasContent(doc).nodes[0].metadata).toBeUndefined();
    expect(readSharedCanvasContent(doc).connections[0].relation).toBeUndefined();
    doc.destroy();
});

test("portable content replaces local references with hashes and restores device-local values", () => {
    const local = content();
    local.nodes[0].metadata = { storageKey: "cache/node-image", pluginData: { resourceId: "resource-node" } };
    local.timeline!.clips[0].directMedia = {
        id: "media-a",
        kind: "video",
        title: "Video",
        storageKey: "cache/timeline-video",
        dataUrl: "data:video/mp4;base64,local-only",
    };
    const hashes = new Map([
        ["cache/node-image", "hash-node-image"],
        ["resource-node", "hash-node-resource"],
        ["cache/timeline-video", "hash-timeline-video"],
        ["data:video/mp4;base64,local-only", "hash-video-data"],
    ]);
    const portable = toPortableSharedCanvasContent(local, ({ localValue }) => hashes.get(localValue));

    expect(JSON.stringify(portable)).not.toContain("cache/node-image");
    expect(JSON.stringify(portable)).not.toContain("resource-node");
    expect(JSON.stringify(portable)).not.toContain("data:video/mp4");
    expect(portable.nodes[0]).toHaveProperty("metadata.storageKey.contentHash", "hash-node-image");

    const source = createCollaborativeCanvasDocument(portable);
    const receiver = createCollaborativeCanvasDocument();
    applyCollaborativeCanvasUpdate(receiver, encodeCollaborativeCanvasState(source));
    const replicated = readPortableSharedCanvasContent(receiver);
    const restored = restoreLocalSharedCanvasContent(replicated, ({ field, contentHash }) => `${field}:${contentHash}`);
    expect(restored.nodes[0].metadata?.storageKey).toBe("storageKey:hash-node-image");
    expect(restored.nodes[0].metadata?.pluginData).toEqual({ resourceId: "resourceId:hash-node-resource" });
    expect(restored.timeline?.clips[0].directMedia).toMatchObject({
        storageKey: "storageKey:hash-timeline-video",
        dataUrl: "dataUrl:hash-video-data",
    });
    source.destroy();
    receiver.destroy();
});

test("portable media content is hashed without replacing text-node content", () => {
    const local = content();
    local.nodes[0].metadata = { content: "A text node body" };
    local.nodes.push({
        id: "image-a",
        type: "image",
        title: "Image",
        position: { x: 400, y: 20 },
        width: 320,
        height: 180,
        metadata: {
            content: "data:image/png;base64,local-image",
            previewContent: "blob:local-preview",
            videoPreview: { content: "resource:video-preview" },
        },
    });
    local.timeline!.clips[0].directMedia = {
        id: "media-a",
        kind: "video",
        title: "Video",
        url: "http://127.0.0.1/api/resources/video-a/file",
    };
    const hashes = new Map([
        ["data:image/png;base64,local-image", "hash-image"],
        ["blob:local-preview", "hash-preview"],
        ["resource:video-preview", "hash-video-preview"],
        ["http://127.0.0.1/api/resources/video-a/file", "hash-video"],
    ]);
    const portable = toPortableSharedCanvasContent(local, ({ localValue }) => hashes.get(localValue));

    expect(portable.nodes[0]).toHaveProperty("metadata.content", "A text node body");
    expect(portable.nodes[1]).toHaveProperty("metadata.content.contentHash", "hash-image");
    expect(portable.nodes[1]).toHaveProperty("metadata.previewContent.contentHash", "hash-preview");
    expect(portable.timeline?.clips[0]).toHaveProperty("directMedia.url.contentHash", "hash-video");
    expect(JSON.stringify(portable)).not.toContain("data:image/png");
    expect(JSON.stringify(portable)).not.toContain("127.0.0.1/api/resources");
});

test("local media refs are collected while text and remote URLs are preserved", () => {
    const local = content();
    local.nodes[0].metadata = { content: "A text body with data:image/png;base64,quoted" };
    local.nodes.push({
        id: "image-a",
        type: "image",
        title: "Image",
        position: { x: 400, y: 20 },
        width: 320,
        height: 180,
        metadata: { content: "data:image/png;base64,local-only", previewContent: "https://cdn.example.test/preview.jpg" },
    });
    local.timeline!.clips[0].directMedia = {
        id: "media-a",
        kind: "video",
        title: "Video",
        url: "/api/resources/video-a/file?size=large",
    };
    const references = collectSharedCanvasAssetReferences(local);
    expect(references.map(({ field, value }) => [field, value])).toEqual([
        ["content", "data:image/png;base64,local-only"],
        ["url", "/api/resources/video-a/file?size=large"],
    ]);

    const portable = toPortableSharedCanvasContent(local, ({ localValue }) => (localValue.includes("local-only") ? "image-hash" : undefined));
    expect(portable.nodes[0]).toHaveProperty("metadata.content", "A text body with data:image/png;base64,quoted");
    expect(portable.nodes[1]).toHaveProperty("metadata.previewContent", "https://cdn.example.test/preview.jpg");
    expect(portable.timeline?.clips[0]).not.toHaveProperty("directMedia.url");
    expect(JSON.stringify(portable)).not.toContain("/api/resources/video-a/file");
});

test("concurrent deletion wins over an independent field edit on the same node", () => {
    const seed = createCollaborativeCanvasDocument(content());
    const deleter = new Y.Doc();
    const editor = new Y.Doc();
    applyCollaborativeCanvasUpdate(deleter, encodeCollaborativeCanvasState(seed));
    applyCollaborativeCanvasUpdate(editor, encodeCollaborativeCanvasState(seed));

    deleteCollaborativeCanvasNode(deleter, "node-a");
    patchCollaborativeCanvasNode(editor, "node-a", { title: "Edited concurrently" });
    const deleteUpdate = encodeCollaborativeCanvasState(deleter);
    const editUpdate = encodeCollaborativeCanvasState(editor);
    applyCollaborativeCanvasUpdate(deleter, editUpdate);
    applyCollaborativeCanvasUpdate(editor, deleteUpdate);

    expect(readSharedCanvasContent(deleter).nodes).toEqual([]);
    expect(readSharedCanvasContent(editor).nodes).toEqual([]);
    seed.destroy();
    deleter.destroy();
    editor.destroy();
});

test("a stale full-content snapshot does not resurrect a concurrently deleted node", () => {
    const initial = content();
    const seed = createCollaborativeCanvasDocument(initial);
    const deleter = new Y.Doc();
    const staleEditor = new Y.Doc();
    const seedUpdate = encodeCollaborativeCanvasState(seed);
    applyCollaborativeCanvasUpdate(deleter, seedUpdate);
    applyCollaborativeCanvasUpdate(staleEditor, seedUpdate);

    deleteCollaborativeCanvasNode(deleter, "node-a");
    writeSharedCanvasContent(staleEditor, { ...initial, nodes: [{ ...initial.nodes[0], title: "Stale edit" }] });
    const deleteUpdate = encodeCollaborativeCanvasState(deleter);
    const staleUpdate = encodeCollaborativeCanvasState(staleEditor);
    applyCollaborativeCanvasUpdate(deleter, staleUpdate);
    applyCollaborativeCanvasUpdate(staleEditor, deleteUpdate);
    expect(readSharedCanvasContent(deleter).nodes).toEqual([]);
    expect(readSharedCanvasContent(staleEditor).nodes).toEqual([]);

    writeSharedCanvasContent(staleEditor, initial);
    expect(readSharedCanvasContent(staleEditor).nodes).toEqual([]);
    seed.destroy();
    deleter.destroy();
    staleEditor.destroy();
});

test("updates can be applied repeatedly and state-vector sync sends only missing state", () => {
    const source = createCollaborativeCanvasDocument(content());
    const receiver = createCollaborativeCanvasDocument();
    const fullUpdate = encodeCollaborativeCanvasState(source);
    let eventCount = 0;
    receiver.on("update", () => {
        eventCount += 1;
    });
    applyCollaborativeCanvasUpdate(receiver, fullUpdate);
    const afterFirstApply = eventCount;
    applyCollaborativeCanvasUpdate(receiver, fullUpdate);
    expect(eventCount).toBe(afterFirstApply);
    expect(readSharedCanvasContent(receiver)).toEqual(content());

    const stateVector = encodeCollaborativeCanvasStateVector(receiver);
    writeSharedCanvasContent(source, { ...content("Renamed"), nodes: [{ ...content().nodes[0], title: "Changed" }] });
    const delta = Y.encodeStateAsUpdate(source, stateVector);
    applyCollaborativeCanvasUpdate(receiver, delta);
    expect(readSharedCanvasContent(receiver).title).toBe("Renamed");
    expect(readSharedCanvasContent(receiver).nodes[0].title).toBe("Changed");
    source.destroy();
    receiver.destroy();
});
