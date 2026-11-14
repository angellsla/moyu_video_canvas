import { useCallback, useEffect, useState } from "react";
import { App, Button, Input, Select, Tag } from "antd";
import { Check, LogOut, Plus, RefreshCw, Search, X } from "lucide-react";
import {
    actOnLanRoom,
    createLanRoom,
    decideLanRoomJoin,
    discoverLanRooms,
    getLanRoomRequests,
    getLanRooms,
    requestLanRoomJoin,
    getLanSharingStatus,
    startLanSharing,
    type LanRoom,
    type LanRoomJoin,
    type LanRoomLocation,
} from "@/services/api/lan-sharing";

function errorText(error: unknown) { return error instanceof Error ? error.message : "房间操作失败"; }

export function LanRoomsSection() {
    const { message } = App.useApp();
    const [rooms, setRooms] = useState<LanRoom[]>([]);
    const [requests, setRequests] = useState<LanRoomJoin[]>([]);
    const [outgoingRequests, setOutgoingRequests] = useState<LanRoomJoin[]>([]);
    const [locations, setLocations] = useState<LanRoomLocation[]>([]);
    const [name, setName] = useState("");
    const [code, setCode] = useState("");
    const [busy, setBusy] = useState("");
    const [localNodeId, setLocalNodeId] = useState("");

    const refresh = useCallback(async () => {
        try {
            const [roomResult, requestResult] = await Promise.all([getLanRooms(), getLanRoomRequests()]);
            const status = await getLanSharingStatus();
            setRooms(roomResult.rooms.filter((room) => room.joined && !room.dissolved));
            setRequests(requestResult.requests.filter((request) => request.status === "pending" && !request.outgoing));
            setOutgoingRequests(requestResult.requests.filter((request) => request.outgoing));
            setLocalNodeId(status.nodeId);
        } catch (error) { message.error(errorText(error)); }
    }, [message]);
    useEffect(() => { void refresh(); const timer = window.setInterval(() => void refresh(), 5000); return () => window.clearInterval(timer); }, [refresh]);

    const run = async (key: string, action: () => Promise<unknown>, success: string) => {
        setBusy(key);
        try { await action(); message.success(success); await refresh(); }
        catch (error) { message.error(errorText(error)); }
        finally { setBusy(""); }
    };
    const create = () => run("create", async () => { if (!name.trim()) throw new Error("请输入团队名称"); await startLanSharing(); await createLanRoom(name.trim()); setName(""); }, "团队已创建");
    const search = () => run("search", async () => { await startLanSharing(); const result = await discoverLanRooms(code.trim()); setLocations(result.rooms); if (!result.rooms.length) throw new Error("当前未发现匹配团队，请确认对方设备已开启局域网共享"); }, "已完成局域网搜索");

    return <section className="mb-6 border-b border-border pb-5" aria-label="局域网团队房间">
        <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
            <div><h2 className="text-base font-semibold">团队房间</h2><p className="text-xs text-foreground/60">8 位房间码只用于识别；成员权限平等，设备之间直接同步。</p></div>
            <Button size="small" icon={<RefreshCw className="size-3.5" />} onClick={() => void refresh()}>刷新</Button>
        </div>
        <div className="mb-4 flex flex-wrap gap-2">
            <Input className="max-w-xs" value={name} maxLength={80} placeholder="新团队名称" onChange={(event) => setName(event.target.value)} />
            <Button type="primary" icon={<Plus className="size-3.5" />} loading={busy === "create"} onClick={() => void create()}>创建团队</Button>
            <Input className="max-w-[180px]" value={code} maxLength={8} placeholder="8 位房间码" onChange={(event) => setCode(event.target.value.replace(/\D/gu, "").slice(0, 8))} />
            <Button icon={<Search className="size-3.5" />} loading={busy === "search"} onClick={() => void search()}>发现团队</Button>
        </div>
        {locations.length ? <div className="mb-4 rounded border border-border p-3"><div className="mb-2 text-sm font-medium">发现结果</div>{locations.map((location) => <div key={`${location.id}-${location.peerId}`} className="flex flex-wrap items-center justify-between gap-2 py-1.5 text-sm"><span>{location.name} · {location.code} · {location.address}:{location.port}</span><Button size="small" loading={busy === `join:${location.id}:${location.peerId}`} onClick={() => void run(`join:${location.id}:${location.peerId}`, async () => { await startLanSharing(); await requestLanRoomJoin({ roomId: location.id, peerId: location.peerId, address: location.address, port: location.port }); }, "已提交加入申请")}>申请加入</Button></div>)}</div> : null}
        {requests.length ? <div className="mb-4 rounded border border-amber-300/50 bg-amber-50/40 p-3"><div className="mb-2 text-sm font-medium">待处理加入申请</div>{requests.map((request) => <div key={request.id} className="flex flex-wrap items-center justify-between gap-2 py-1.5 text-sm"><span>{request.nickname || request.nodeId} · {request.roomId.slice(0, 8)}</span><span className="flex gap-1"><Button size="small" icon={<Check className="size-3.5" />} onClick={() => void run(`approve:${request.id}`, () => decideLanRoomJoin(request.id, true), "已批准成员加入")}>批准</Button><Button size="small" icon={<X className="size-3.5" />} onClick={() => void run(`reject:${request.id}`, () => decideLanRoomJoin(request.id, false), "已拒绝申请")}>拒绝</Button></span></div>)}</div> : null}
        {outgoingRequests.length ? <div className="mb-4 text-sm text-foreground/60">{outgoingRequests.map((request) => <div key={request.id}>加入「{request.roomId.slice(0, 8)}」：{request.status === "approved" ? "已批准，正在同步成员记录" : request.status === "rejected" ? "申请已拒绝" : "等待成员审批"}</div>)}</div> : null}
        <div className="space-y-3">{rooms.length ? rooms.map((room) => <RoomCard key={room.id} room={room} localNodeId={localNodeId} busy={busy} run={run} />) : <div className="text-sm text-foreground/60">尚未加入团队</div>}</div>
    </section>;
}

function RoomCard({ room, localNodeId, busy, run }: { room: LanRoom; localNodeId: string; busy: string; run: (key: string, action: () => Promise<unknown>, success: string) => Promise<void> }) {
    const members = Object.values(room.members);
    const pending = room.proposals.filter((proposal) => !proposal.passed);
    const proposeRemoval = (nodeId: string) => run(`remove:${room.id}:${nodeId}`, () => actOnLanRoom(room.id, "remove", nodeId), "已提交移除提案");
    return <div className="rounded border border-border p-3"><div className="flex flex-wrap items-center justify-between gap-2"><div><span className="font-medium">{room.name}</span><Tag className="ml-2">{room.code}</Tag><span className="ml-2 text-xs text-foreground/55">{members.length} 名成员</span></div><span className="flex gap-1"><Button size="small" onClick={() => void run(`dissolve:${room.id}`, () => actOnLanRoom(room.id, "dissolve"), "已提交解散提案")}>提议解散</Button><Button size="small" icon={<LogOut className="size-3.5" />} disabled={members.length === 1} title={members.length === 1 ? "最后一名成员请提议解散团队" : "离开团队"} onClick={() => void run(`leave:${room.id}`, () => actOnLanRoom(room.id, "leave"), "已离开团队")}>离开</Button></span></div><div className="mt-2 grid gap-1 text-sm">{members.map((member) => <div key={member.nodeId} className="flex items-center justify-between gap-2"><span className="truncate">{member.nickname || member.nodeId}</span>{member.nodeId !== localNodeId && !pending.some((proposal) => proposal.kind === "remove" && proposal.target === member.nodeId) ? <Button size="small" onClick={() => void proposeRemoval(member.nodeId)}>提议移除</Button> : null}</div>)}{pending.map((proposal) => <div key={proposal.id} className="mt-2 flex items-center justify-between rounded bg-amber-50/50 px-2 py-1"><span>待表决：{proposal.kind === "dissolve" ? "解散团队" : `移除 ${proposal.target?.slice(0, 8)}`} · {proposal.approvals.length}/{proposal.electorate.length}</span><Button type="text" size="small" icon={<Check className="size-3.5" />} onClick={() => void run(`vote:${room.id}:${proposal.id}`, () => actOnLanRoom(room.id, "vote", proposal.id), "已提交投票")}>投票</Button></div>)}</div></div>;
}
