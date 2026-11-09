import { useEffect, useId, useRef, useState } from "react";
import { Button, Popover } from "antd";
import { ChevronDown, Users } from "lucide-react";

import type { SharedCanvasAssetReadiness } from "@/services/lan-collaboration-assets";
import type { LanCanvasPresence } from "./use-lan-collaborative-canvas";

type Props = {
    members: LanCanvasPresence[];
    assetReadiness: SharedCanvasAssetReadiness | null;
};

export function CanvasCollaborationMemberList({ members, assetReadiness }: Props) {
    const claims = new Map(assetReadiness?.peers.map((peer) => [peer.peerId, peer]));
    if (!members.length) return <p className="py-2 text-xs text-muted-foreground">等待成员加入</p>;
    return (
        <ul aria-label="协作成员列表" className="m-0 max-h-64 list-none space-y-1 overflow-y-auto p-0" data-canvas-wheel-scroll>
            {members.map((member) => {
                const claim = claims.get(member.peerId);
                return (
                    <li key={member.peerId} className="flex items-start gap-2 rounded-sm px-1 py-2 text-xs">
                        <span aria-hidden="true" className={`mt-1 size-1.5 shrink-0 rounded-full ${member.collaborating ? "bg-emerald-500" : member.online ? "bg-amber-500" : "bg-foreground/35"}`} />
                        <div className="min-w-0 flex-1">
                            <div className="flex items-start justify-between gap-3">
                                <span className="min-w-0 break-all font-medium text-foreground">{member.nickname}</span>
                                <span className="shrink-0 text-muted-foreground">{member.collaborating ? "协作中" : member.online ? "局域网在线" : "离线"}</span>
                            </div>
                            {assetReadiness?.requiredCount && claim ? (
                                <p className="mt-1 text-muted-foreground" title="来自共享清单声明，尚未经该设备确认">
                                    素材声明 {claim.claimedCount}/{assetReadiness.requiredCount}
                                </p>
                            ) : null}
                        </div>
                    </li>
                );
            })}
        </ul>
    );
}

export function CanvasCollaborationMembers(props: Props) {
    const [open, setOpen] = useState(false);
    const listId = useId();
    const triggerRef = useRef<React.ComponentRef<typeof Button>>(null);
    useEffect(() => {
        if (!open) return;
        const closeOnEscape = (event: KeyboardEvent) => {
            if (event.key !== "Escape") return;
            event.preventDefault();
            event.stopPropagation();
            setOpen(false);
            triggerRef.current?.focus();
        };
        document.addEventListener("keydown", closeOnEscape, true);
        return () => document.removeEventListener("keydown", closeOnEscape, true);
    }, [open]);
    return (
        <Popover
            trigger="click"
            placement="bottomLeft"
            open={open}
            onOpenChange={setOpen}
            content={
                <div id={listId} data-canvas-no-zoom className="w-72 max-w-[calc(100vw-3rem)] whitespace-normal">
                    <p className="mb-2 text-sm font-medium text-foreground">协作成员（{props.members.length}）</p>
                    <CanvasCollaborationMemberList {...props} />
                </div>
            }
        >
            <Button
                ref={triggerRef}
                type="text"
                size="small"
                aria-label={`协作成员，${props.members.length} 人`}
                aria-expanded={open}
                aria-controls={open ? listId : undefined}
                icon={<Users className="size-3.5" />}
            >
                成员 {props.members.length}
                <ChevronDown aria-hidden="true" className={`size-3.5 ${open ? "rotate-180" : ""}`} />
            </Button>
        </Popover>
    );
}
