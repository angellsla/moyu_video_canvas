import { expect, test } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import { CanvasCollaborationMemberList, CanvasCollaborationMembers } from "@/pages/canvas/canvas-collaboration-members";
import type { LanCanvasPresence } from "@/pages/canvas/use-lan-collaborative-canvas";

const members: LanCanvasPresence[] = [
    { peerId: "a", nickname: "Editing PC", state: {}, collaborating: true, online: true },
    { peerId: "b", nickname: "LAN PC", state: {}, online: true },
    { peerId: "c", nickname: "Offline PC", state: {}, online: false },
];

test("member control is collapsed by default without flattening names in the status bar", () => {
    const html = renderToStaticMarkup(<CanvasCollaborationMembers members={members} assetReadiness={null} />);
    expect(html).toContain('aria-expanded="false"');
    expect(html).toContain("成员 3");
    expect(html).not.toContain("Editing PC");
    expect(html).not.toContain("Offline PC");
});

test("expanded member content is a scrollable list with per-member status and claims", () => {
    const html = renderToStaticMarkup(<CanvasCollaborationMemberList members={members} assetReadiness={{
        requiredCount: 4,
        local: { mappedCount: 4, missingHashes: [] },
        peers: [{ peerId: "b", nickname: "LAN PC", discovered: true, claimedCount: 2, unconfirmedHashes: ["pending-a", "pending-b"] }],
    }} />);
    expect(html.match(/<li /g)).toHaveLength(3);
    for (const text of ["Editing PC", "LAN PC", "Offline PC", "协作中", "局域网在线", "离线", "素材声明 2/4"]) expect(html).toContain(text);
    expect(html).toContain("overflow-y-auto");
    expect(html).toContain("data-canvas-wheel-scroll");
});

test("an empty member list has a readable waiting state", () => {
    expect(renderToStaticMarkup(<CanvasCollaborationMemberList members={[]} assetReadiness={null} />)).toContain("等待成员加入");
});
