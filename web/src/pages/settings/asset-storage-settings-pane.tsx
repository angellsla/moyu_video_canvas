import { useEffect, useState } from "react";
import { App, Button, Input } from "antd";
import { FolderOpen } from "lucide-react";
import { getDesktopAppBinding, type DesktopAssetStorageUsage } from "@/services/desktop-runtime";
import { flushModelConfig, getModelConfigPersistenceState } from "@/services/model-config-repository";
import { saveRemoteUserDataNow } from "@/services/local-workspace-sync";
import { recordDiagnosticEvent } from "@/services/diagnostics/client-diagnostics";
import { startOperation } from "@/stores/use-operation-progress";
import { createTransferSampler } from "@/lib/operation-progress";

const size = (bytes: number) => `${(bytes / 1024 ** 3).toFixed(1)} GB`;

export function AssetStorageSettingsPane() {
    const { message, modal } = App.useApp();
    const binding = getDesktopAppBinding();
    const [usage, setUsage] = useState<DesktopAssetStorageUsage>();
    const [target, setTarget] = useState("");
    const [busy, setBusy] = useState(false);
    const [error, setError] = useState("");
    const [cleanupInterrupted, setCleanupInterrupted] = useState(false);
    const [errorSummary, ...errorDetails] = error.split("\n\n");

    useEffect(() => {
        let active = true;
        void binding?.AssetStorageSettings?.().then((value) => {
            if (active) setUsage(value);
        }).catch(() => {
            if (active) setError("素材存储信息暂时无法读取，请重试");
        });
        return () => { active = false; };
    }, [binding]);

    if (!binding?.AssetStorageSettings || !binding.MigrateAssetStorage) return null;

    const choose = async () => {
        try {
            const path = await binding.ChooseAssetStorageDirectory?.();
            if (path) setTarget(path);
        } catch (error) {
            message.error(error instanceof Error ? error.message : String(error));
        }
    };

    const migrate = async () => {
        setBusy(true);
        setError("");
        let phase = "save_workspace";
        const progress = startOperation("迁移素材存储", { detail: "正在保存工作区" });
        const sample = createTransferSampler();
        let stopped = false;
        let pollTimer: ReturnType<typeof setTimeout> | undefined;
        const poll = async () => {
            try {
                const state = await binding.StorageMigrationStatus?.();
                if (!stopped && state?.phase) progress.update({ ...sample(state.loadedBytes, state.totalBytes), ...state });
            } catch { /* The local API is restarted as part of the directory switch. */ }
            if (!stopped) pollTimer = setTimeout(poll, 500);
        };
        recordDiagnosticEvent({ level: "info", category: "action", code: "asset_storage_migration_started", message: "开始准备素材存储迁移" });
        try {
            await Promise.all([flushModelConfig(), saveRemoteUserDataNow()]);
            const state = getModelConfigPersistenceState();
            if (state.dirty || state.status === "error") throw new Error("模型配置尚未保存，请先解决保存错误再迁移");
            phase = "migrate_assets";
            void poll();
            await binding.MigrateAssetStorage!(target.trim());
            progress.done("迁移完成，正在重新载入");
            window.location.reload();
        } catch (error) {
            progress.fail("迁移未完成，请查看下方错误详情");
            const details = error instanceof Error ? error.message : String(error);
            recordDiagnosticEvent({ level: "error", category: "action", code: "asset_storage_migration_failed", reason: phase, message: "素材存储迁移失败" });
            setError(details);
            setBusy(false);
        } finally {
            stopped = true;
            if (pollTimer) clearTimeout(pollTimer);
        }
    };

    const cleanup = () => modal.confirm({
        title: "清理旧素材目录",
        centered: true,
        okText: "确认并清理",
        cancelText: "取消",
        content: <p className="break-words">旧目录 {usage?.previousDir} 中约有 {size(usage?.previousBytes || 0)}。程序会先核对迁移记录；若文件发生变化，将停止清理并保留目录。</p>,
        onOk: async () => {
            setBusy(true);
            setError("");
            try {
                setUsage(await binding.CleanupPreviousAssetStorage!());
                setCleanupInterrupted(false);
                message.success("旧素材目录已清理");
            } catch (error) {
                setCleanupInterrupted(true);
                setError(error instanceof Error ? error.message : String(error));
                try { setUsage(await binding.AssetStorageSettings!()); } catch { /* Keep the last known usage. */ }
            } finally {
                setBusy(false);
            }
        },
    });

    return (
        <section className="settings-section storage-settings-block space-y-4" aria-label="素材文件存储">
            <div className="settings-pane-header"><div className="min-w-0"><h2>素材文件存储</h2></div></div>
            <div className="flex flex-wrap items-baseline justify-between gap-x-6 gap-y-2 text-sm">
                <div className="min-w-0 break-all">当前目录：{usage?.resourceDir || "正在读取…"}</div>
                {usage ? <p className="shrink-0 text-foreground/60">占用 {size(usage.usedBytes)} · 磁盘剩余 {size(usage.freeBytes)}</p> : null}
            </div>
            <label className="flex min-w-0 flex-wrap items-center gap-2">
                <span className="shrink-0 text-sm">新素材目录：</span>
                <div className="flex min-w-0 flex-1 flex-wrap gap-2">
                    <Input className="min-w-0 flex-1" aria-label="新素材目录" value={target} onChange={(event) => setTarget(event.target.value)} placeholder="例如 D:\\MoyuCanvasAssets" disabled={busy} />
                    <Button icon={<FolderOpen className="size-4" />} onClick={choose} disabled={busy}>选择文件夹</Button>
                </div>
            </label>
            <p className="text-sm leading-6 text-foreground/60">本设置管理素材库内的文件，包括上传素材、局域网拉取的素材及已入库的图片和视频生成结果。迁移会复制并校验现有文件，完成后自动切换保存位置；通过另存或导出对话框保存到其他位置的副本不受影响。目标须为空文件夹；通过“选择文件夹”选中非空目录时，会使用其下的 MoyuCanvasAssets 子目录。数据库、配置和任务记录留在原工作区。</p>
            <p className="text-sm leading-6 text-foreground/60">原素材副本会保留，确认新目录正常后可清理。</p>
            {error ? (
                <div role="alert" className="break-words text-sm text-destructive">
                    <p>{errorSummary}</p>
                    {errorDetails.length > 0 ? <details className="mt-2"><summary className="cursor-pointer">错误详情</summary><p className="mt-2 break-all whitespace-pre-wrap">{errorDetails.join("\n\n")}</p></details> : null}
                </div>
            ) : null}
            <Button type="primary" onClick={migrate} loading={busy} disabled={!usage || !target.trim() || target.trim() === usage.resourceDir}>
                {busy ? "正在迁移并校验素材…" : "迁移素材并使用新目录"}
            </Button>
            {usage?.previousDir && binding.CleanupPreviousAssetStorage ? (
                <div className="space-y-2 border-t border-border pt-4">
                    <p className="break-all text-sm">待清理的旧素材目录：{usage.previousDir}。</p>
                    {cleanupInterrupted ? <p className="text-sm">清理未完成，旧目录已变化或无法访问；当前素材目录不受影响。</p> : null}
                    <Button danger disabled={busy} onClick={cleanup}>清理旧素材目录，释放磁盘空间</Button>
                </div>
            ) : null}
        </section>
    );
}
