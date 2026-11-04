import { expect, test } from "bun:test";

import { resourceFileUrl } from "@/services/api/resources";
import { createSharedCanvasLocalAssetResolver, summarizeSharedCanvasAssetReadiness, validatedSharedCanvasLocalBindings } from "@/services/lan-collaboration-assets";

test("deleted local resource bindings are not treated as ready", async () => {
    const probes: string[] = [];
    const known = await validatedSharedCanvasLocalBindings(["hash-good", "hash-missing"], { "hash-good": "resource-good", "hash-missing": "resource-deleted" }, async (id) => {
        probes.push(id);
        return id === "resource-good";
    });
    expect(known).toEqual({ "hash-good": "resource-good" });
    expect(probes.sort()).toEqual(["resource-deleted", "resource-good"]);
});

test("a failed resource probe does not discard a binding as deleted", async () => {
    await expect(validatedSharedCanvasLocalBindings(["hash-a"], { "hash-a": "resource-a" }, async () => {
        throw new Error("backend unavailable");
    })).rejects.toThrow("backend unavailable");
});

test("shared asset hashes resolve to local field-compatible references", () => {
    const resolve = createSharedCanvasLocalAssetResolver({ "hash-image": "resource-a" });
    const path = ["nodes", "0", "metadata", "content"];

    expect(resolve({ field: "resourceId", contentHash: "hash-image", path })).toBe("resource-a");
    expect(resolve({ field: "storageKey", contentHash: "hash-image", path })).toBe("resource:resource-a");
    for (const field of ["content", "previewContent", "url", "dataUrl"] as const) {
        expect(resolve({ field, contentHash: "hash-image", path })).toBe(resourceFileUrl("resource-a"));
    }
    expect(resolve({ field: "content", contentHash: "missing", path })).toBeUndefined();
});

test("asset readiness distinguishes local mappings from trusted peer claims", () => {
    const summary = summarizeSharedCanvasAssetReadiness(
        ["hash-a", "hash-b", "hash-a"],
        { "hash-a": "local-resource-a" },
        [
            { contentHash: "hash-a", createdBy: "peer-a" },
            { contentHash: "hash-a", createdBy: "peer-a" },
            { contentHash: "hash-b", createdBy: "peer-b" },
            { contentHash: "hash-b", createdBy: "untrusted-peer" },
        ],
        [
            { peerId: "peer-a", nickname: "Editing Room", address: "192.168.1.20", port: 8080 },
            { peerId: "peer-b", nickname: "" },
        ],
        [{ peerId: "peer-a" }, { peerId: "untrusted-peer" }],
    );

    expect(summary).toEqual({
        requiredCount: 2,
        local: { mappedCount: 1, missingHashes: ["hash-b"] },
        peers: [
            { peerId: "peer-a", nickname: "Editing Room", discovered: true, claimedCount: 1, unconfirmedHashes: ["hash-b"] },
            { peerId: "peer-b", nickname: "peer-b", discovered: false, claimedCount: 1, unconfirmedHashes: ["hash-a"] },
        ],
    });
    expect(JSON.stringify(summary)).not.toContain("192.168.1.20");
    expect(JSON.stringify(summary)).not.toContain("untrusted-peer");
});

test("unadvertised assets remain unconfirmed rather than marked ready", () => {
    const summary = summarizeSharedCanvasAssetReadiness(["hash-a"], {}, [], [{ peerId: "peer-a", nickname: "Peer A" }], [{ peerId: "peer-a" }]);
    expect(summary.local.missingHashes).toEqual(["hash-a"]);
    expect(summary.peers[0]).toMatchObject({ discovered: true, claimedCount: 0, unconfirmedHashes: ["hash-a"] });
});
