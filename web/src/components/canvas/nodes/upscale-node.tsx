import { useContext, useRef, useState } from "react";
import { Button } from "antd";
import { Image as ImageIcon, Video } from "lucide-react";
import type { CanvasTheme } from "@/lib/canvas-theme";
import { canRecoverUpscaleNode } from "@/lib/canvas/canvas-ai-upscale-task";
import { CanvasNodeType, type CanvasNodeData } from "@/types/canvas";
import { useCanvasNodeActions } from "../canvas-node-action-context";
import { CanvasNodeGraphContext, useUpstreamNodes } from "../canvas-node-graph-context";

const batchLabels = { waiting: "等待提交", submitting: "提交中", accepted: "已提交", rejected: "提交失败", unknown: "待确认" };

export function UpscaleNodeContent({ node, theme }: { node: CanvasNodeData; theme: CanvasTheme }) {
    const actions = useCanvasNodeActions();
    const { getNode } = useContext(CanvasNodeGraphContext);
    const upstream = useUpstreamNodes(node.id);
    const inputs = upstream.filter((item, index) => (item.type === CanvasNodeType.Image || item.type === CanvasNodeType.Video) && upstream.findIndex(other => other.id === item.id) === index);
    const imageCount = inputs.filter(item => item.type === CanvasNodeType.Image).length;
    const videoCount = inputs.length - imageCount;
    const [notice, setNotice] = useState("");
    const [busy, setBusy] = useState(false);
    const busyRef = useRef(false);
    const batch = node.metadata?.upscaleBatch;
    const run = async (result?: CanvasNodeData) => {
        if (busyRef.current) return;
        busyRef.current = true; setBusy(true); setNotice("");
        try {
            if (result) await actions.recoverUpscaleResult?.(result);
            else await actions.startUpscaleBatch?.(node);
        } catch (cause) { setNotice(cause instanceof Error ? cause.message : "处理失败，请查看原任务状态"); }
        finally { busyRef.current = false; setBusy(false); }
    };
    return <div className="relative flex h-full w-full min-h-0 min-w-0 flex-col gap-3 p-3 text-[var(--fs-tiny)]" style={{ color: theme.node.text }}>
        <div className="flex min-h-0 flex-1 flex-col gap-3">
            <div className="flex items-center gap-3" style={{ color: theme.node.muted }}><ImageIcon className="size-4" /><span>{imageCount} 张图片</span><Video className="ml-2 size-4" /><span>{videoCount} 个视频</span></div>
            {!inputs.length && !batch?.items.length ? <div className="flex flex-1 items-center justify-center text-center" style={{ color: theme.node.muted }}>从左侧连接图片或视频<br />可一次处理多个素材</div> : <div className="thin-scrollbar min-h-0 flex-1 space-y-2 overflow-y-auto" data-canvas-no-drag data-canvas-no-zoom onWheel={event => event.stopPropagation()}>{[
                ...inputs.map(input => ({ id: input.id, title: input.title || "未命名素材", type: input.type, item: batch?.items.find(item => item.sourceNodeId === input.id) })),
                ...(batch?.items || []).filter(item => !inputs.some(input => input.id === item.sourceNodeId)).map(item => ({ id: item.sourceNodeId, title: item.sourceTitle, type: item.mediaType === "video" ? CanvasNodeType.Video : CanvasNodeType.Image, item })),
            ].map(row => {
                const result = row.item ? getNode?.(row.item.outputNodeId) : undefined;
                const status = result?.metadata?.taskStatus;
                const label = !row.item ? "" : status === "succeeded" && result?.metadata?.status !== "error" ? "已完成" : status === "failed" || status === "cancelled" ? "失败" : status === "running" || status === "queued" ? "处理中" : batchLabels[row.item.status];
                return <div key={row.id} className="flex min-w-0 items-center gap-2">
                    <span className="shrink-0" style={{ color: theme.node.muted }}>{row.type === CanvasNodeType.Video ? <Video className="size-4" /> : <ImageIcon className="size-4" />}</span>
                    <span className="min-w-0 flex-1 truncate" title={row.title}>{row.title}</span>
                    {label ? <span className="w-16 shrink-0 whitespace-nowrap text-right" title={row.item?.error || result?.metadata?.errorDetails} style={{ color: theme.node.muted }}>{label}</span> : null}
                    {result && canRecoverUpscaleNode(result) ? <Button className="shrink-0" size="small" type="text" disabled={busy} onClick={() => void run(result)}>取回结果</Button> : null}
                </div>;
            })}</div>}

        </div>
        <div className="absolute inset-x-0 top-full z-20 mt-3 w-full space-y-3 rounded-[var(--r-lg)] border p-4 text-[var(--fs-tiny)] shadow-lg" style={{ background: theme.node.panel, borderColor: theme.node.edge }} data-canvas-no-drag data-canvas-no-zoom onPointerDown={event => event.stopPropagation()} onMouseDown={event => event.stopPropagation()} onWheel={event => event.stopPropagation()} onDoubleClick={event => event.stopPropagation()}>
            <p role="status" style={{ color: theme.node.muted }}>超分服务已下线；仅支持查询历史批次和取回已有结果。</p>
            {notice ? <p role="alert" className="break-words" style={{ color: "var(--status-error)" }}>{notice}</p> : null}
            {batch?.items.length ? <Button type="primary" block loading={busy} disabled={!actions.startUpscaleBatch} onClick={() => void run()}>查询历史批次</Button> : null}
        </div>
    </div>;
}
