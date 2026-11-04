import * as decoding from "lib0/decoding";
import { Awareness, applyAwarenessUpdate, encodeAwarenessUpdate } from "y-protocols/awareness";

import { decodeLanSharingUpdate, encodeLanSharingUpdate } from "@/services/api/lan-sharing";

export type LanAwarenessFrame = {
    online: true;
    clientId: number;
    awarenessUpdateBase64: string;
};

export function encodeLocalLanAwareness(awareness: Awareness): LanAwarenessFrame {
    return {
        online: true,
        clientId: awareness.clientID,
        awarenessUpdateBase64: encodeLanSharingUpdate(encodeAwarenessUpdate(awareness, [awareness.clientID])),
    };
}

export function isLanAwarenessFrame(value: unknown): value is LanAwarenessFrame {
    if (!value || typeof value !== "object") return false;
    const frame = value as Partial<LanAwarenessFrame>;
    return frame.online === true && Number.isInteger(frame.clientId) && frame.clientId! >= 0 && frame.clientId! <= 0xffffffff && typeof frame.awarenessUpdateBase64 === "string";
}

export function applyPeerLanAwareness(awareness: Awareness, peerId: string, frame: LanAwarenessFrame, owners: Map<number, string>): number | null {
    try {
        const update = decodeLanSharingUpdate(frame.awarenessUpdateBase64);
        const decoder = decoding.createDecoder(update);
        if (decoding.readVarUint(decoder) !== 1) return null;
        const clientId = decoding.readVarUint(decoder);
        if (clientId !== frame.clientId || clientId === awareness.clientID) return null;
        decoding.readVarUint(decoder);
        const state = JSON.parse(decoding.readVarString(decoder)) as unknown;
        if (decoding.hasContent(decoder) || (state !== null && (typeof state !== "object" || Array.isArray(state)))) return null;
        if (owners.has(clientId) && owners.get(clientId) !== peerId) return null;
        applyAwarenessUpdate(awareness, update, peerId);
        if (awareness.getStates().has(clientId)) owners.set(clientId, peerId);
        else owners.delete(clientId);
        return clientId;
    } catch {
        return null;
    }
}
