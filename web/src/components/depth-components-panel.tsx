import { useCallback, useEffect, useState } from "react";
import { Button, Progress, type App } from "antd";
import { getDesktopAppBinding, type DepthComponentState } from "@/services/desktop-runtime";
import { navigateToSettings } from "@/lib/settings-navigation";

const gb = (bytes: number) => `${(bytes / 1024 ** 3).toFixed(1)} GB`;
const running = (state?: DepthComponentState) => state?.status === "downloading" || state?.status === "installing";
const statusText: Record<DepthComponentState["status"], string> = {
    not_installed: "尚未安装", downloading: "正在下载", installing: "正在解压或安装依赖", ready: "已安装，可以使用",
    paused: "下载已暂停", error: "安装未完成", unsupported: "当前设备暂不支持",
};

export function DepthComponentsPanel({ initialState, onReady, onCancel, onOpenSettings }: { initialState?: DepthComponentState; onReady?: () => void; onCancel?: () => Promise<void>; onOpenSettings?: () => void }) {
    const binding = getDesktopAppBinding();
    const [state, setState] = useState(initialState);
    const [busy, setBusy] = useState(false);
    const [error, setError] = useState("");
    const refresh = useCallback(async (force = false) => {
        try {
            if (!binding?.DepthComponentStatus) throw new Error("请在桌面版中管理本地组件");
            setState(await binding.DepthComponentStatus(force));
            setError("");
        } catch (cause) { setError(cause instanceof Error ? cause.message : String(cause)); }
    }, [binding]);
    useEffect(() => { if (!initialState) void refresh(); }, [initialState, refresh]);
    useEffect(() => {
        if (!running(state)) return;
        const timer = window.setInterval(() => void refresh(), 1000);
        return () => window.clearInterval(timer);
    }, [state?.status, refresh]);
    useEffect(() => { if (state?.status === "ready") onReady?.(); }, [state?.status, onReady]);
    const mutate = async (action?: () => Promise<DepthComponentState>) => {
        if (!action) return;
        setBusy(true);
        try { setState(await action()); setError(""); }
        catch (cause) { setError(cause instanceof Error ? cause.message : String(cause)); }
        finally { setBusy(false); }
    };
    const insufficient = Boolean(state && state.status !== "ready" && state.freeBytes < state.requiredBytes);
    const modelMirrorDefault = state?.variant === "cpu" || state?.variant === "cuda";
    const downloadLabel = state?.status === "paused" ? "继续下载深度捕捉组件/模型" : state?.status === "error" ? "重试下载深度捕捉组件/模型" : "下载深度捕捉组件/模型";
    return <div className="space-y-3 text-sm" data-canvas-no-zoom data-canvas-wheel-scroll>
        <p className="leading-6 text-foreground/70">桌面安装包不包含深度捕捉运行组件和模型。Windows 默认下载路径优先从 Hugging Face 国内镜像 hf-mirror.com 获取模型，官方 Hugging Face 为备用源；Python、PyTorch 和运行组件按需从默认上游软件源下载。自定义部署清单时遵循清单配置。</p>
        <p role="status">{state ? statusText[state.status] : error ? "组件信息暂时无法读取" : "正在检查组件信息…"}</p>
        {state && state.status !== "unsupported" ? <>
            <div className="space-y-1 leading-6">
                <p className="break-all">运行组件位置：{state.runtimeDir || state.dataDir}</p>
                {state.modelRuntime ? <p className="break-all">模型位置：{state.modelRuntime}</p> : null}
                {state.status !== "ready" ? <p>{state.estimated ? `预计下载约 ${gb(state.downloadBytes)}，预计需预留约 ${gb(state.requiredBytes)}（依赖解析后可能变化）` : `下载约 ${gb(state.downloadBytes)}，需预留 ${gb(state.requiredBytes)}`}，当前可用 {gb(state.freeBytes)}</p> : null}
                <p className="text-foreground/60">{state.variant === "cuda" ? "使用 NVIDIA 显卡加速" : state.variant === "cpu" ? "使用 CPU 处理，耗时可能较长" : "使用 Apple 芯片加速"}</p>
            </div>
            {running(state) ? <>
                {state.estimated ? <p role="status" className="text-foreground/60">已下载 {gb(state.downloadedBytes)}，下载与依赖安装中（大小为估算）。</p> : <>
                    <Progress percent={Math.min(99, Math.floor(state.downloadedBytes / Math.max(1, state.downloadBytes) * 100))} status="active" aria-label="组件下载进度" />
                    <p className="text-foreground/60">{gb(state.downloadedBytes)} / {gb(state.downloadBytes)}。可暂停后继续或重试；退出应用后不会自行恢复。</p>
                </>}
            </> : null}
            {insufficient && !running(state) ? <p role="alert" className="text-destructive">空间不足，请先更换目录或清理磁盘。</p> : null}
            <div className="flex flex-wrap gap-2">
                {running(state) ? <Button loading={busy} onClick={() => void mutate(binding?.PauseDepthComponentDownload)}>暂停下载</Button> : state.status !== "ready" ? <Button className="h-auto max-w-full whitespace-normal text-left leading-5" type="primary" loading={busy} disabled={insufficient} onClick={() => void mutate(binding?.DownloadDepthComponents)}>{modelMirrorDefault ? `${downloadLabel}（模型国内镜像优先）` : downloadLabel}</Button> : null}
                {onOpenSettings && !running(state) ? <Button onClick={onOpenSettings}>前往存储设置</Button> : null}
                {!onReady && !running(state) ? <Button disabled={busy} onClick={() => void refresh(true)}>重新检查</Button> : null}
                {onCancel ? <Button disabled={busy} onClick={async () => {
                    setBusy(true);
                    try { await onCancel(); } catch(cause) { setError(cause instanceof Error ? cause.message : String(cause)); setBusy(false); }
                }}>{running(state) ? "暂停并返回" : "暂不下载"}</Button> : null}
            </div>
        </> : null}
        {error || state?.error ? <p role="alert" className="break-words text-destructive">{error || state?.error}</p> : null}
        {!state && error ? <Button onClick={() => void refresh(true)}>重新检查</Button> : null}
    </div>;
}

type ModalAPI = ReturnType<typeof App.useApp>["modal"];

export async function confirmDepthComponents(modal: ModalAPI): Promise<boolean> {
    const binding = getDesktopAppBinding();
    if (!binding?.DepthComponentStatus) throw new Error("请先在桌面版的存储设置中安装深度捕捉组件");
    const state = await binding.DepthComponentStatus(true);
    if (state.status === "ready") return true;
    return new Promise<boolean>((resolve) => {
        let settled = false;
        let cancelRequested = false;
        const finish = (allowed: boolean) => { if (!settled) { settled = true; resolve(allowed); } };
        const cancel = async () => {
            // A download finishing during cancellation must not launch the task.
            cancelRequested = true;
            await binding.PauseDepthComponentDownload?.();
            dialog.destroy();
            finish(false);
        };
        const dialog = modal.confirm({
            title: "准备深度动作捕捉", centered: true, closable: true, maskClosable: false, width: 560, footer: null, icon: null,
            content: <DepthComponentsPanel initialState={state} onReady={() => { if (!cancelRequested) { dialog.destroy(); finish(true); } }} onCancel={cancel} onOpenSettings={() => { finish(false); dialog.destroy(); navigateToSettings({ section: "storage" }); }} />,
            onCancel: cancel,
        });
    });
}
