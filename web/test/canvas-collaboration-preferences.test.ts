import { expect, test } from "bun:test";
import { persistRemoteCursorsVisible, readRemoteCursorsVisible } from "@/lib/canvas/canvas-collaboration-preferences";

test("remote cursor visibility persists on this client without changing another client", () => {
    const originalWindow = globalThis.window;
    const clientA = new Map<string, string>();
    const clientB = new Map<string, string>();
    const useClient = (storage: Map<string, string>) => Object.defineProperty(globalThis, "window", { configurable: true, value: { localStorage: {
        getItem: (key: string) => storage.get(key) ?? null,
        setItem: (key: string, value: string) => storage.set(key, value),
    } } });
    try {
        useClient(clientA);
        expect(readRemoteCursorsVisible()).toBe(true);
        persistRemoteCursorsVisible(false);
        expect(readRemoteCursorsVisible()).toBe(false);
        useClient(clientB);
        expect(readRemoteCursorsVisible()).toBe(true);
        useClient(clientA);
        expect(readRemoteCursorsVisible()).toBe(false);
        persistRemoteCursorsVisible(true);
        expect(readRemoteCursorsVisible()).toBe(true);
    } finally { Object.defineProperty(globalThis, "window", { configurable: true, value: originalWindow }); }
});

test("cursor preference has a usable default when local storage is unavailable", () => {
    const originalWindow = globalThis.window;
    Object.defineProperty(globalThis, "window", { configurable: true, value: {} });
    try {
        expect(readRemoteCursorsVisible()).toBe(true);
        expect(() => persistRemoteCursorsVisible(false)).not.toThrow();
    } finally { Object.defineProperty(globalThis, "window", { configurable: true, value: originalWindow }); }
});
