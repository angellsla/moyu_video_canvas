import * as Y from "yjs";

import { SHARED_CATALOG_STORE_NAME, localForageInstance } from "@/lib/localforage-storage";
import { getActiveUserScope, scopedStorageKey } from "@/lib/user-scope";

const ENTRIES_MAP_NAME = "entries";
const STORAGE_KEY_PREFIX = "shared-catalog:v1";
const REMOTE_UPDATE_ORIGIN = Symbol("shared-catalog-remote-update");
const LOCAL_UPDATE_ORIGIN = Symbol("shared-catalog-local-update");
const PERSISTENCE_UPDATE_ORIGIN = Symbol("shared-catalog-persistence-update");

export type SharedCatalogEntry = {
    entryId: string;
    contentHash: string;
    name: string;
    mimeType: string;
    size: number;
    createdBy: string;
    createdAt: string;
    parentId?: string;
};

export type SharedCatalogUpdate = {
    update: Uint8Array;
    remote: boolean;
};

export type OpenSharedCatalogOptions = {
    scope?: string;
};

export type SharedCatalog = {
    readonly roomId: string;
    getEntry(entryId: string): SharedCatalogEntry | undefined;
    listEntries(): SharedCatalogEntry[];
    findEntriesByContentHash(contentHash: string): SharedCatalogEntry[];
    upsertEntry(entry: SharedCatalogEntry): void;
    removeEntry(entryId: string): boolean;
    applyRemoteUpdate(update: Uint8Array): void;
    encodeStateAsUpdate(stateVector?: Uint8Array): Uint8Array;
    encodeStateVector(): Uint8Array;
    pendingLocalUpdates(): Uint8Array[];
    acknowledgeLocalUpdate(update: Uint8Array): Promise<void>;
    onUpdate(listener: (event: SharedCatalogUpdate) => void): () => void;
    flush(): Promise<void>;
    close(): Promise<void>;
};

const store = localForageInstance(SHARED_CATALOG_STORE_NAME);
const openedCatalogs = new Map<string, Promise<SharedCatalog>>();

function normalizeRequiredString(value: unknown, field: string) {
    if (typeof value !== "string" || !value.trim()) throw new Error(`${field} must be a non-empty string`);
    return value.trim();
}

function normalizeEntry(value: SharedCatalogEntry): SharedCatalogEntry {
    if (!value || typeof value !== "object") throw new Error("Catalog entry must be an object");
    const size = value.size;
    if (!Number.isSafeInteger(size) || size < 0) throw new Error("Catalog entry size must be a non-negative safe integer");

    const parentId = value.parentId === undefined ? undefined : normalizeRequiredString(value.parentId, "parentId");
    return {
        entryId: normalizeRequiredString(value.entryId, "entryId"),
        contentHash: normalizeRequiredString(value.contentHash, "contentHash"),
        name: normalizeRequiredString(value.name, "name"),
        mimeType: normalizeRequiredString(value.mimeType, "mimeType"),
        size,
        createdBy: normalizeRequiredString(value.createdBy, "createdBy"),
        createdAt: normalizeRequiredString(value.createdAt, "createdAt"),
        ...(parentId ? { parentId } : {}),
    };
}

function decodeEntry(value: unknown): SharedCatalogEntry | undefined {
    if (!(value instanceof Y.Map)) return undefined;
    try {
        return normalizeEntry({
            entryId: value.get("entryId") as string,
            contentHash: value.get("contentHash") as string,
            name: value.get("name") as string,
            mimeType: value.get("mimeType") as string,
            size: value.get("size") as number,
            createdBy: value.get("createdBy") as string,
            createdAt: value.get("createdAt") as string,
            ...(value.get("parentId") === undefined ? {} : { parentId: value.get("parentId") as string }),
        });
    } catch {
        return undefined;
    }
}

function copyUpdate(update: Uint8Array) {
    return new Uint8Array(update);
}

function encodeUpdate(update: Uint8Array) {
    let binary = "";
    const chunkSize = 0x8000;
    for (let offset = 0; offset < update.byteLength; offset += chunkSize) {
        binary += String.fromCharCode(...update.subarray(offset, Math.min(update.byteLength, offset + chunkSize)));
    }
    return btoa(binary);
}

function decodeUpdate(encoded: string) {
    const binary = atob(encoded);
    return Uint8Array.from(binary, (character) => character.charCodeAt(0));
}

class SharedCatalogDocument implements SharedCatalog {
    private readonly doc = new Y.Doc();
    private readonly entries: Y.Map<unknown>;
    private readonly updateListeners = new Set<(event: SharedCatalogUpdate) => void>();
    private readonly pendingUpdates = new Map<string, Uint8Array>();
    private readonly updateHandler: (update: Uint8Array, origin: unknown) => void;
    private persistTail: Promise<void> = Promise.resolve();
    private changeVersion = 0;
    private persistedVersion = 0;
    private closed = false;
    private closing = false;
    private closePromise: Promise<void> | undefined;

    constructor(
        readonly roomId: string,
        private readonly storageKey: string,
        private readonly outboxKey: string,
        storedUpdate: Uint8Array | null,
        storedPendingUpdates: Uint8Array[],
        private readonly removeFromCache: () => void,
    ) {
        this.entries = this.doc.getMap(ENTRIES_MAP_NAME);
        if (storedUpdate) Y.applyUpdate(this.doc, storedUpdate, PERSISTENCE_UPDATE_ORIGIN);
        for (const update of storedPendingUpdates) {
            this.pendingUpdates.set(encodeUpdate(update), copyUpdate(update));
            Y.applyUpdate(this.doc, update, PERSISTENCE_UPDATE_ORIGIN);
        }
        this.updateHandler = (update, origin) => {
            if (origin === LOCAL_UPDATE_ORIGIN) this.pendingUpdates.set(encodeUpdate(update), copyUpdate(update));
            this.schedulePersistence();
            this.updateListeners.forEach((listener) => {
                try {
                    listener({ update: copyUpdate(update), remote: origin === REMOTE_UPDATE_ORIGIN });
                } catch (error) {
                    console.error("Shared catalog update listener failed", error);
                }
            });
        };
        this.doc.on("update", this.updateHandler);
    }

    getEntry(entryId: string) {
        const id = normalizeRequiredString(entryId, "entryId");
        const entry = decodeEntry(this.entries.get(id));
        return entry?.entryId === id ? entry : undefined;
    }

    listEntries() {
        const result: SharedCatalogEntry[] = [];
        for (const [id, value] of this.entries.entries()) {
            const entry = decodeEntry(value);
            if (entry?.entryId === id) result.push(entry);
        }
        return result.sort((left, right) => left.entryId.localeCompare(right.entryId));
    }

    findEntriesByContentHash(contentHash: string) {
        const hash = normalizeRequiredString(contentHash, "contentHash");
        return this.listEntries().filter((entry) => entry.contentHash === hash);
    }

    upsertEntry(input: SharedCatalogEntry) {
        this.assertOpen();
        const entry = normalizeEntry(input);
        this.doc.transact(() => {
            const existing = this.entries.get(entry.entryId);
            let record: Y.Map<unknown>;
            if (existing instanceof Y.Map) {
                record = existing;
            } else {
                record = new Y.Map<unknown>();
                this.entries.set(entry.entryId, record);
            }
            for (const [key, value] of Object.entries(entry)) record.set(key, value);
            for (const key of [...record.keys()]) {
                if (!(key in entry)) record.delete(key);
            }
        }, LOCAL_UPDATE_ORIGIN);
    }

    removeEntry(entryId: string) {
        this.assertOpen();
        const id = normalizeRequiredString(entryId, "entryId");
        if (!this.entries.has(id)) return false;
        this.doc.transact(() => this.entries.delete(id), LOCAL_UPDATE_ORIGIN);
        return true;
    }

    applyRemoteUpdate(update: Uint8Array) {
        this.assertOpen();
        if (!(update instanceof Uint8Array)) throw new Error("Remote catalog update must be a Uint8Array");
        Y.applyUpdate(this.doc, copyUpdate(update), REMOTE_UPDATE_ORIGIN);
    }

    encodeStateAsUpdate(stateVector?: Uint8Array) {
        this.assertOpen();
        return stateVector ? Y.encodeStateAsUpdate(this.doc, stateVector) : Y.encodeStateAsUpdate(this.doc);
    }

    encodeStateVector() {
        this.assertOpen();
        return Y.encodeStateVector(this.doc);
    }

    pendingLocalUpdates() {
        this.assertOpen();
        return [...this.pendingUpdates.values()].map(copyUpdate);
    }

    async acknowledgeLocalUpdate(update: Uint8Array) {
        this.assertOpen();
        if (this.pendingUpdates.delete(encodeUpdate(update))) {
            this.schedulePersistence();
            await this.flushPending();
        }
    }

    onUpdate(listener: (event: SharedCatalogUpdate) => void) {
        this.assertOpen();
        this.updateListeners.add(listener);
        return () => this.updateListeners.delete(listener);
    }

    async flush() {
        this.assertOpen();
        return this.flushPending();
    }

    close() {
        if (this.closed) return Promise.resolve();
        if (this.closePromise) return this.closePromise;
        this.closing = true;
        this.closePromise = this.flushPending().then(() => {
            this.closed = true;
            this.closing = false;
            this.doc.off("update", this.updateHandler);
            this.updateListeners.clear();
            this.doc.destroy();
            this.removeFromCache();
        }).catch((error) => {
            this.closing = false;
            this.closePromise = undefined;
            throw error;
        });
        return this.closePromise;
    }

    private async flushPending() {
        await this.persistTail.catch(() => undefined);
        if (this.persistedVersion < this.changeVersion) {
            this.persistTail = this.persistTail.catch(() => undefined).then(() => this.persistCurrentState());
        }
        await this.persistTail;
    }

    private schedulePersistence() {
        this.changeVersion += 1;
        this.persistTail = this.persistTail.catch(() => undefined).then(async () => {
            if (this.persistedVersion >= this.changeVersion) return;
            await this.persistCurrentState();
        });
    }

    private async persistCurrentState() {
        const version = this.changeVersion;
        await store.setItem(this.outboxKey, [...this.pendingUpdates.keys()]);
        await store.setItem(this.storageKey, Y.encodeStateAsUpdate(this.doc));
        this.persistedVersion = Math.max(this.persistedVersion, version);
    }

    private assertOpen() {
        if (this.closed || this.closing) throw new Error("Shared catalog is closed");
    }
}

export function openSharedCatalog(roomId: string, options: OpenSharedCatalogOptions = {}) {
    const normalizedRoomId = normalizeRequiredString(roomId, "roomId");
    const scope = normalizeRequiredString(options.scope ?? getActiveUserScope(), "scope");
    const cacheKey = `${scope}\0${normalizedRoomId}`;
    const existing = openedCatalogs.get(cacheKey);
    if (existing) return existing;

    const storageKey = scopedStorageKey(`${STORAGE_KEY_PREFIX}:${encodeURIComponent(normalizedRoomId)}`, scope);
    const outboxKey = `${storageKey}:outbox`;
    let catalogPromise: Promise<SharedCatalog>;
    catalogPromise = (async () => {
        const [storedUpdate, storedOutbox] = await Promise.all([
            store.getItem<Uint8Array>(storageKey),
            store.getItem<string[]>(outboxKey),
        ]);
        if (storedUpdate && !(storedUpdate instanceof Uint8Array)) throw new Error("Stored shared catalog update is invalid");
        if (storedOutbox !== null && (!Array.isArray(storedOutbox) || storedOutbox.some((item) => typeof item !== "string"))) {
            throw new Error("Stored shared catalog outbox is invalid");
        }
        const pendingUpdates = (storedOutbox || []).map((item) => decodeUpdate(item));
        const catalog = new SharedCatalogDocument(normalizedRoomId, storageKey, outboxKey, storedUpdate, pendingUpdates, () => {
            if (openedCatalogs.get(cacheKey) === catalogPromise) openedCatalogs.delete(cacheKey);
        });
        return catalog;
    })().catch((error) => {
        if (openedCatalogs.get(cacheKey) === catalogPromise) openedCatalogs.delete(cacheKey);
        throw error;
    });
    openedCatalogs.set(cacheKey, catalogPromise);
    return catalogPromise;
}
