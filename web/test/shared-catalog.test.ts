import { afterEach, expect, test } from "bun:test";

import { openSharedCatalog, type SharedCatalog, type SharedCatalogEntry } from "@/services/shared-catalog";

const opened: SharedCatalog[] = [];

async function open(roomId: string, scope: string) {
    const catalog = await openSharedCatalog(roomId, { scope });
    opened.push(catalog);
    return catalog;
}

function entry(entryId: string, contentHash = "sha256:abc123"): SharedCatalogEntry {
    return {
        entryId,
        contentHash,
        name: `${entryId}.png`,
        mimeType: "image/png",
        size: 1024,
        createdBy: "peer-a",
        createdAt: "2026-11-03T00:00:00.000Z",
    };
}

afterEach(async () => {
    await Promise.all(opened.splice(0).map((catalog) => catalog.close()));
});

test("catalog state persists per room and user scope", async () => {
    const roomId = `room-${crypto.randomUUID()}`;
    const first = await open(roomId, "user-a");
    first.upsertEntry(entry("entry-one"));

    expect(await openSharedCatalog(roomId, { scope: "user-a" })).toBe(first);
    const isolated = await open(roomId, "user-b");
    expect(isolated.listEntries()).toEqual([]);
    await isolated.close();

    await first.close();
    const restored = await open(roomId, "user-a");
    expect(restored.getEntry("entry-one")).toEqual(entry("entry-one"));
});

test("catalog exchanges full state and state-vector deltas with remote origin", async () => {
    const roomId = `room-${crypto.randomUUID()}`;
    const source = await open(roomId, "user-a");
    const receiver = await open(roomId, "user-b");
    const received: boolean[] = [];
    const sent: boolean[] = [];
    receiver.onUpdate(({ remote }) => received.push(remote));
    source.onUpdate(({ remote }) => sent.push(remote));

    source.upsertEntry(entry("entry-one"));
    expect(sent).toEqual([false]);
    receiver.applyRemoteUpdate(source.encodeStateAsUpdate());
    expect(receiver.getEntry("entry-one")).toEqual(entry("entry-one"));
    expect(received).toEqual([true]);

    const stateVector = receiver.encodeStateVector();
    source.upsertEntry(entry("entry-two", "sha256:def456"));
    receiver.applyRemoteUpdate(source.encodeStateAsUpdate(stateVector));
    expect(receiver.getEntry("entry-two")).toEqual(entry("entry-two", "sha256:def456"));
    expect(received).toEqual([true, true]);
});

test("locally published updates remain queued until acknowledged across reopen", async () => {
    const roomId = `room-${crypto.randomUUID()}`;
    const catalog = await open(roomId, "user-a");
    catalog.upsertEntry(entry("entry-one"));
    await catalog.flush();
    const [pending] = catalog.pendingLocalUpdates();
    expect(pending).toBeInstanceOf(Uint8Array);

    await catalog.close();
    const restored = await open(roomId, "user-a");
    expect(restored.getEntry("entry-one")).toEqual(entry("entry-one"));
    expect(restored.pendingLocalUpdates()).toEqual([pending]);

    await restored.acknowledgeLocalUpdate(pending);
    expect(restored.pendingLocalUpdates()).toEqual([]);
    await restored.close();

    const acknowledged = await open(roomId, "user-a");
    expect(acknowledged.pendingLocalUpdates()).toEqual([]);
});

test("entry identity is independent from content hash and presence is not durable metadata", async () => {
    const catalog = await open(`room-${crypto.randomUUID()}`, "user-a");
    const hash = "sha256:shared-content";
    catalog.upsertEntry(entry("entry-one", hash));
    catalog.upsertEntry({ ...entry("entry-two", hash), parentId: "entry-one" });

    expect(catalog.findEntriesByContentHash(hash)).toHaveLength(2);
    expect(catalog.listEntries().map(({ entryId }) => entryId)).toEqual(["entry-one", "entry-two"]);
    expect(catalog.getEntry("entry-two")).not.toHaveProperty("holders");
    expect(catalog.removeEntry("entry-one")).toBe(true);
    expect(catalog.removeEntry("entry-one")).toBe(false);
    expect(catalog.getEntry("entry-two")).toEqual({ ...entry("entry-two", hash), parentId: "entry-one" });
});

test("invalid entry metadata is rejected before changing the document", async () => {
    const catalog = await open(`room-${crypto.randomUUID()}`, "user-a");

    expect(() => catalog.upsertEntry({ ...entry("entry-one"), size: -1 })).toThrow("size");
    expect(() => catalog.upsertEntry({ ...entry(" ") })).toThrow("entryId");
    expect(catalog.listEntries()).toEqual([]);
});

test("update listener unsubscribe stops future notifications", async () => {
    const catalog = await open(`room-${crypto.randomUUID()}`, "user-a");
    const updates: SharedCatalogEntry[] = [];
    const unsubscribe = catalog.onUpdate(() => updates.push(...catalog.listEntries()));

    catalog.upsertEntry(entry("entry-one"));
    unsubscribe();
    catalog.upsertEntry(entry("entry-two"));

    expect(updates).toEqual([entry("entry-one")]);
});
