import { expect, test } from "bun:test";
import * as Y from "yjs";
import { Awareness } from "y-protocols/awareness";

import { applyPeerLanAwareness, encodeLocalLanAwareness, isLanAwarenessFrame } from "@/services/lan-collaboration-awareness";

function peers() {
    const sourceDoc = new Y.Doc();
    sourceDoc.clientID = 1001;
    const receiverDoc = new Y.Doc();
    receiverDoc.clientID = 2002;
    const source = new Awareness(sourceDoc);
    const receiver = new Awareness(receiverDoc);
    const owners = new Map<number, string>();
    return { sourceDoc, receiverDoc, source, receiver, owners };
}

test("LAN presence carries a standard Yjs Awareness update", () => {
    const { sourceDoc, receiverDoc, source, receiver, owners } = peers();
    source.setLocalState({ online: true, x: 42, y: 21, selectionNodeIds: ["node-a"] });
    const frame = encodeLocalLanAwareness(source);
    expect(isLanAwarenessFrame(frame)).toBe(true);
    expect(applyPeerLanAwareness(receiver, "peer-a", frame, owners)).toBe(source.clientID);
    expect(receiver.getStates().get(source.clientID)).toEqual({ online: true, x: 42, y: 21, selectionNodeIds: ["node-a"] });
    expect(owners.get(source.clientID)).toBe("peer-a");

    source.setLocalState(null);
    expect(applyPeerLanAwareness(receiver, "peer-a", encodeLocalLanAwareness(source), owners)).toBe(source.clientID);
    expect(receiver.getStates().has(source.clientID)).toBe(false);
    expect(owners.has(source.clientID)).toBe(false);
    sourceDoc.destroy();
    receiverDoc.destroy();
});

test("Awareness updates cannot overwrite another peer or the local client", () => {
    const { sourceDoc, receiverDoc, source, receiver, owners } = peers();
    source.setLocalState({ online: true });
    const frame = encodeLocalLanAwareness(source);
    expect(applyPeerLanAwareness(receiver, "peer-a", frame, owners)).toBe(source.clientID);
    expect(applyPeerLanAwareness(receiver, "peer-b", frame, owners)).toBeNull();
    expect(owners.get(source.clientID)).toBe("peer-a");
    expect(applyPeerLanAwareness(receiver, "peer-a", { ...frame, clientId: receiver.clientID }, owners)).toBeNull();
    expect(applyPeerLanAwareness(receiver, "peer-a", { ...frame, clientId: 99 }, owners)).toBeNull();
    sourceDoc.destroy();
    receiverDoc.destroy();
});
