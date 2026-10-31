import { Alert, App, Button, Form, Input, Popconfirm } from "antd";
import { Bug, KeyRound, Pencil, Plus, RefreshCw, Trash2, Workflow } from "lucide-react";
import { useEffect, useRef, useState, useSyncExternalStore, type ReactNode } from "react";

import { WorkspaceState } from "@/components/layout/workspace-state";
import { PageHeader } from "@/components/layout/workspace-page";
import { mergeFetchedChannelModelProfiles } from "@/lib/channel-model-catalog";
import { seedancePortraitModel } from "@/lib/seedance-portrait";
import { ensureModelProfilesWithUiDefaults } from "@/lib/model-protocols";
import { fetchChannelModels, type ChannelModelFetchResult } from "@/services/api/image";
import { channelHasGenerationCredential, encodeChannelModel, filterModelsByCapability, modelOptionsFromChannels, normalizeConfigSnapshot, useConfigStore, type AiConfig, type ModelChannel } from "@/stores/use-config-store";
import { ModelServiceEditor } from "./model-service-editor";
import { currentModelConnectionReceipt, useModelConnectionTests } from "@/stores/use-model-connection-tests";
import { ModelLogo } from "@/components/model-logo";
import { validateChannelHeaders } from "@/components/channel-headers-editor";
import { MODEL_SERVICE_PRESETS, isTopazUpscaleChannel, channelAllowsEmptyAPIKey, modelCatalogRequestURL, servicePresetFor } from "@/lib/model-service-presets";
import { workspaceCapabilities } from "@/services/workspace-mode";
import { commitModelConfig, flushModelConfig, getModelConfigPersistenceState, subscribeModelConfigPersistence, type ModelConfigPersistenceState } from "@/services/model-config-repository";
import { createChannelCatalogRequests } from "@/services/channel-catalog-requests";
import { getTransportFailureKind } from "@/services/api/request";
import { NEW_API_BASE_URL, syncNewAPIConnection } from "@/services/api/newapi-connection";
import { recordChannelDelete, recordChannelDeleteConfirmOpen } from "./model-settings-diagnostics";

type ChannelSettingsPaneProps = {
    onOpenDiagnostics?: () => void;
    onOpenRunningHub?: () => void;
};

export function ChannelSettingsPane({ onOpenDiagnostics, onOpenRunningHub }: ChannelSettingsPaneProps) {
    const { message } = App.useApp();
    const [newAPIForm] = Form.useForm<{ username: string; password: string }>();
    const config = useConfigStore((state) => state.config);
    const replaceConfig = useConfigStore((state) => state.replaceConfig);
    const localMode = workspaceCapabilities().local;
    const persistence = useSyncExternalStore(subscribeModelConfigPersistence, getModelConfigPersistenceState, getModelConfigPersistenceState);
    const [loadingChannelIds, setLoadingChannelIds] = useState<string[]>([]);
    const [serviceEditor, setServiceEditor] = useState<ModelChannel | null | undefined>(undefined);
    const [newAPIBusy, setNewAPIBusy] = useState(false);
    const [newAPIError, setNewAPIError] = useState("");
    const requests = useRef(createChannelCatalogRequests());
    const deleting = useRef(new Set<string>());
    const [deleteFailures, setDeleteFailures] = useState<Record<string, string>>({});
    useEffect(() => () => requests.current.cancelAll(), []);
    const cancelCatalog = (id: string) => {
        requests.current.cancel(id);
        setLoadingChannelIds((items) => items.filter((item) => item !== id));
    };

    const userChannels = config.channels.filter((channel) => channel.scope !== "system" && !isRetiredBeefChannel(channel));
    const runningHubReady = Boolean(config.runningHub.enabled && config.runningHub.baseUrl.trim() && config.runningHub.apiKey.trim() && config.runningHub.workflowId.trim());

    useEffect(() => {
        const retained = config.channels.filter((channel) => !isRetiredBeefChannel(channel));
        if (retained.length === config.channels.length) return;
        const next = withChannels(config, retained);
        replaceConfig(next);
        void commitModelConfig(next).catch((error) => {
            message.error(error instanceof Error ? error.message : "旧版渠道清理未保存");
        });
    }, [config, message, replaceConfig]);

    const updateChannels = (channels: ModelChannel[], baseConfig = config) => {
        replaceConfig(withChannels(baseConfig, channels));
    };

    const connectNewAPI = async ({ username, password }: { username: string; password: string }) => {
        setNewAPIBusy(true);
        setNewAPIError("");
        try {
            const response = await syncNewAPIConnection(username, password);
            const latest = useConfigStore.getState().config;
            const existing = latest.channels.find((channel) => channel.id === "newapi");
            const synced = response.channel;
            if (!synced || !Array.isArray(synced.models)) throw new Error("墨羽Canvas API 未返回有效模型渠道");
            const models = uniqueModels(synced.models);
            if (!models.length) throw new Error("墨羽Canvas API 未返回可用模型列表");
            const channel: ModelChannel = {
                ...synced,
                id: "newapi",
                name: "墨羽Canvas API",
                apiFormat: "openai",
                baseUrl: NEW_API_BASE_URL,
                apiKey: synced.apiKey?.trim() || "",
                models,
                modelProfiles: ensureModelProfilesWithUiDefaults(models, synced.modelProfiles || existing?.modelProfiles, [], "openai"),
            };
            if (!channel.apiKey) throw new Error("墨羽Canvas API 已同步，但响应未提供可用于模型调用的 API Key");
            const retained = latest.channels.filter((item) => !isRetiredBeefChannel(item));
            const channels = retained.some((item) => item.id === channel.id)
                ? retained.map((item) => item.id === channel.id ? channel : item)
                : [...retained, channel];
            const textModel = newAPITextModelAfterSync(models, latest.textModel);
            const normalized = normalizeConfigSnapshot({ config: { ...latest, channels, textModel } }).config;
            updateChannels(normalized.channels, normalized);
            await flushModelConfig();
            const saved = getModelConfigPersistenceState();
            if (saved.status === "error" || saved.dirty) throw new Error(saved.error || "墨羽Canvas API 渠道尚未保存，请重试");
            newAPIForm.resetFields();
            message.success(`墨羽Canvas API 已连接，已同步 ${models.length} 个模型`);
        } catch (error) {
            const detail = error instanceof Error ? error.message : "墨羽Canvas API 登录或模型同步失败";
            setNewAPIError(detail);
            message.error(detail);
        } finally {
            setNewAPIBusy(false);
        }
    };

    const addChannel = () => {
        setServiceEditor(null);
    };

    const saveService = async (channel: ModelChannel) => {
        cancelCatalog(channel.id);
        const latest = useConfigStore.getState().config;
        const channels = latest.channels.some((item) => item.id === channel.id)
            ? latest.channels.map((item) => item.id === channel.id ? channel : item)
            : [...latest.channels, channel];
        updateChannels(channels, latest);
        await flushModelConfig();
        const saved = getModelConfigPersistenceState();
        if (saved.status === "error" || saved.dirty) throw new Error(saved.error || "模型服务尚未保存，请重试");
        message.success("模型服务已保存，可在创作页选择模型");
    };

    const deleteChannel = async (id: string) => {
        if (deleting.current.has(id)) return;
        const latest = useConfigStore.getState().config;
        const channel = latest.channels.find((item) => item.id === id);
        if (channel && channel.scope === "system") {
            message.warning("系统渠道由管理员维护");
            return;
        }
        deleting.current.add(id);
        cancelCatalog(id);
        const next = withChannels(latest, latest.channels.filter((item) => item.id !== id));
        replaceConfig(next);
        try {
            await commitModelConfig(next);
            await flushModelConfig();
            const saved = getModelConfigPersistenceState();
            if (saved.status === "error" || saved.dirty) throw new Error(saved.error || "删除尚未保存");
            setDeleteFailures((items) => { const next = { ...items }; delete next[id]; return next; });
            recordChannelDelete({ scope: channel?.scope, before: latest.channels.length, after: next.channels.length });
            message.success("渠道已删除并保存");
        } catch (error) {
            const detail = error instanceof Error ? error.message : "保存失败";
            setDeleteFailures((items) => ({ ...items, [id]: `${channel?.name || "渠道"}删除尚未保存：${detail}。请重试，重新加载可能恢复原渠道。` }));
        } finally {
            deleting.current.delete(id);
        }
    };

    const setChannelLoading = (id: string, loading: boolean) => {
        setLoadingChannelIds((items) => (loading ? Array.from(new Set([...items, id])) : items.filter((item) => item !== id)));
    };

    const refreshChannelModels = async (channel: ModelChannel) => {
        if (isTopazUpscaleChannel(channel)) return;
        const connectionError = channelConnectionError(channel);
        if (connectionError) {
            message.error(`${channel.name || "当前渠道"}：${connectionError}`);
            return;
        }
        setChannelLoading(channel.id, true);
        const request = requests.current.start(channel.id);
        try {
            const result = await fetchChannelModels(channel, true, request.signal);
            if (!requests.current.current(channel.id, request)) return;
            if (!result.models.length) {
                message.warning(`${channel.name || "当前渠道"}未返回模型，已保留现有手工模型`);
                return;
            }
            const latestConfig = useConfigStore.getState().config;
            const latestChannel = latestConfig.channels.find((item) => item.id === channel.id);
            if (!latestChannel) return;
            if (channelConnectionSignature(latestChannel) !== channelConnectionSignature(channel)) {
                message.warning(`${latestChannel.name || "当前渠道"}的连接配置已改变，已忽略旧的拉取结果`);
                return;
            }
            updateChannels(
                latestConfig.channels.map((item) => (item.id === channel.id ? applyFetchedChannelModelCatalog(item, result) : item)),
                latestConfig,
            );
            await flushModelConfig();
            message.success(`${latestChannel.name || "当前渠道"}模型列表已更新`);
        } catch (error) {
            if (requests.current.current(channel.id, request)) message.error(channelModelFetchErrorMessage(error));
        } finally {
            if (requests.current.current(channel.id, request)) setChannelLoading(channel.id, false);
            requests.current.finish(channel.id, request);
        }
    };

    const refreshAllModels = async () => {
        const catalogChannels = userChannels.filter(channel => !isTopazUpscaleChannel(channel));
        const runnable = catalogChannels.filter((channel) => !channelConnectionError(channel));
        const skipped = catalogChannels.filter((channel) => channelConnectionError(channel));
        if (!runnable.length) {
            const detail = skipped.map((channel) => `${channel.name || "未命名渠道"}：${channelConnectionError(channel)}`).join("；");
            message.error(detail || "没有可拉取的个人模型渠道，请先填写有效 Base URL 和 API Key");
            return;
        }
        setChannelLoading("all", true);
        const batch = new Map<string, AbortController>();
        try {
            const results = await Promise.all(
                runnable.map(async (channel) => {
                    const request = requests.current.start(channel.id);
                    batch.set(channel.id, request);
                    try {
                        const result = await fetchChannelModels(channel, true, request.signal);
                        if (!requests.current.current(channel.id, request)) return { channel, result: { models: [], catalog: [] }, error: "", cancelled: true };
                        return { channel, result, error: "" };
                    } catch (error) {
                        if (!requests.current.current(channel.id, request)) return { channel, result: { models: [], catalog: [] }, error: "", cancelled: true };
                        return { channel, result: { models: [], catalog: [] }, error: channelModelFetchErrorMessage(error) };
                    }
                }),
            );
            const latestConfig = useConfigStore.getState().config;
            const successful = results.filter((item) => {
                const latestChannel = latestConfig.channels.find((channel) => channel.id === item.channel.id);
                return Boolean(item.result.models.length && latestChannel && requests.current.current(item.channel.id, batch.get(item.channel.id)!) && channelConnectionSignature(latestChannel) === channelConnectionSignature(item.channel));
            });
            const stale = results.filter((item) => {
                const latestChannel = latestConfig.channels.find((channel) => channel.id === item.channel.id);
                return Boolean(item.result.models.length && (!latestChannel || channelConnectionSignature(latestChannel) !== channelConnectionSignature(item.channel)));
            });
            const failed = results.filter((item) => !item.cancelled && !item.result.models.length);
            if (successful.length) {
                const resultMap = new Map(successful.map((item) => [item.channel.id, item.result] as const));
                const current = useConfigStore.getState().config;
                const nextChannels = current.channels.map((channel) => {
                    const fetched = resultMap.get(channel.id);
                    return fetched ? applyFetchedChannelModelCatalog(channel, fetched) : channel;
                });
                const updated = withChannels(current, nextChannels);
                replaceConfig(updated);
                await flushModelConfig();
                message.success(`已更新 ${successful.length} 个渠道的模型`);
            }
            const warnings = [
                ...failed.map((item) => `${item.channel.name || "未命名渠道"}：${item.error || "未返回模型"}`),
                ...stale.map((item) => `${item.channel.name || "未命名渠道"}：连接配置已改变，已忽略旧结果`),
                ...skipped.map((channel) => `${channel.name || "未命名渠道"}：${channelConnectionError(channel)}`),
            ];
            if (warnings.length) message.warning(`${warnings.join("；")}。未更新的渠道已保留原有模型列表`);
        } catch (error) {
            message.error(error instanceof Error ? error.message : "批量读取模型失败，原有模型列表未改动");
        } finally {
            for (const [id, request] of batch) requests.current.finish(id, request);
            setChannelLoading("all", false);
        }
    };

    return (
        <div>
            {Object.entries(deleteFailures).map(([id, detail]) => <Alert key={id} type="error" showIcon title={detail} action={<Button onClick={() => void deleteChannel(id)}>重试删除</Button>} />)}
            <PageHeader
                title={localMode ? "本地模型渠道" : "个人渠道"}
                actions={(
                    <div className="settings-pane-header-actions flex w-full gap-2 sm:w-auto sm:shrink-0">
                    {onOpenDiagnostics ? <Button className="h-10 flex-1 sm:h-8 sm:flex-none" icon={<Bug className="size-4" />} onClick={onOpenDiagnostics}>问题诊断</Button> : null}
                    <Button className="h-10 flex-1 sm:h-8 sm:flex-none" icon={<RefreshCw className="size-4" />} loading={loadingChannelIds.includes("all")} disabled={loadingChannelIds.some((id) => id !== "all") || !userChannels.some(channel => !isTopazUpscaleChannel(channel))} onClick={() => void refreshAllModels()}>
                        拉取全部渠道模型
                    </Button>
                    <Button type="primary" className="h-10 flex-1 sm:h-8 sm:flex-none" icon={<Plus className="size-4" />} onClick={addChannel}>
                        添加模型服务
                    </Button>
                    </div>
                )}
            />
            <section className="settings-section mb-3" aria-labelledby="newapi-login-title">
                <div className="mb-3">
                    <h3 id="newapi-login-title" className="flex items-center gap-2 text-sm font-semibold"><KeyRound className="size-4" />墨羽Canvas API</h3>
                    <p className="mt-1 text-xs text-foreground/55">登录后自动读取或创建 videoCanvas API Key，并同步全部可用模型。登录信息仅用于本次同步，不会保存在浏览器中。</p>
                </div>
                <Form form={newAPIForm} layout="vertical" requiredMark={false} onFinish={(values) => void connectNewAPI(values)}>
                    <div className="grid gap-3 sm:grid-cols-[1fr_1fr_auto] sm:items-end">
                        <Form.Item name="username" label="墨羽Canvas 用户名" className="mb-0" rules={[{ required: true, message: "请输入用户名" }]}>
                            <Input autoComplete="username" placeholder="输入墨羽Canvas 用户名" />
                        </Form.Item>
                        <Form.Item name="password" label="墨羽Canvas 密码" className="mb-0" rules={[{ required: true, message: "请输入密码" }]}>
                            <Input.Password autoComplete="current-password" placeholder="输入墨羽Canvas 密码" />
                        </Form.Item>
                        <Button className="h-10 sm:mb-0" type="primary" htmlType="submit" loading={newAPIBusy}>登录并同步模型</Button>
                    </div>
                </Form>
                {newAPIError ? <Alert className="mt-3" type="error" showIcon title={newAPIError} closable onClose={() => setNewAPIError("")} /> : null}
                <p className="mb-0 mt-2 text-xs text-foreground/50">服务地址：video.sistinelove.cn · API Key 名称：videoCanvas · 默认分组，不限制模型范围</p>
            </section>
            {onOpenRunningHub ? (
                <section className="settings-section mb-3">
                    <div className="mb-3">
                        <h3 className="text-sm font-semibold">个人工作流渠道</h3>
                        <p className="mt-1 text-xs text-foreground/55">RunningHub 使用独立的云端工作流参数与执行通道。</p>
                    </div>
                    <div className="grid gap-2 lg:grid-cols-2">
                        {onOpenRunningHub ? (
                            <WorkflowChannelEntry
                                icon={<Workflow className="size-4" />}
                                title="RunningHub"
                                description="云端工作流和 RunningHub App"
                                status={runningHubReady ? `${config.runningHub.workflows.length} 个工作流已配置` : config.runningHub.enabled ? "待完成连接和工作流配置" : "未启用"}
                                ready={runningHubReady}
                                onOpen={onOpenRunningHub}
                            />
                        ) : null}
                    </div>
                </section>
            ) : null}
            {userChannels.length ? (
                <div className="settings-channel-list space-y-2">
                    {userChannels.map((channel) => {
                        const topazUpscale = isTopazUpscaleChannel(channel);
                        return (
                            <section key={channel.id} aria-labelledby={`channel-${channel.id}-title`} className="settings-channel p-2.5 sm:p-3">
                                <div className="mb-2.5 flex flex-wrap items-start justify-between gap-2.5">
                                    <div className="min-w-0 flex-1 basis-52">
                                        <h3 id={`channel-${channel.id}-title`} className="flex items-center gap-2 text-sm font-semibold">
                                            <ModelLogo icon={MODEL_SERVICE_PRESETS.find((preset) => preset.id === servicePresetFor(channel))?.icon} size={20} />
                                            {channel.name || "未命名渠道"}
                                        </h3>
                                        <div className="mt-1 flex flex-wrap items-center gap-2 text-xs text-foreground/55">
                                            <span>{channelProtocolLabel(channel)} · 已保存 {channel.models.length} 个模型</span>
                                            <ChannelStatus channel={channel} persistence={persistence} />
                                        </div>
                                        {topazUpscale ? <p className="mt-1 text-xs text-foreground/55">从图片或视频节点上方的「AI超分」使用</p> : null}
                                        {!topazUpscale && <p className="mt-1 break-all text-xs text-foreground/50">模型目录地址：{modelCatalogRequestURL(channel)}</p>}
                                    </div>
                                    <div className="flex w-full flex-wrap justify-end gap-2 sm:w-auto sm:shrink-0">
                                        {!topazUpscale ? (
                                        <Button
                                            className="h-10 sm:h-8"
                                            size="small"
                                            icon={<RefreshCw className="size-3.5" />}
                                            loading={loadingChannelIds.includes(channel.id)}
                                            disabled={loadingChannelIds.includes("all")}
                                            onClick={() => void refreshChannelModels(channel)}
                                        >
                                            拉取模型
                                        </Button>
                                        ) : null}
                                        <Button
                                            size="small"
                                            icon={<Pencil className="size-3.5" />}
                                            onClick={() => {
                                                cancelCatalog(channel.id);
                                                setServiceEditor(channel);
                                            }}
                                        >
                                            编辑
                                        </Button>
                                            <Popconfirm classNames={{ root: "model-channel-delete-confirm" }} title="删除个人模型渠道？" description="该渠道关联的模型选择会同时移除。" okText="删除" cancelText="取消" okButtonProps={{ danger: true }} onOpenChange={recordChannelDeleteConfirmOpen} onConfirm={() => deleteChannel(channel.id)}>
                                                    <Button
                                                        title="删除渠道"
                                                        className="size-10 p-0 sm:size-8"
                                                        aria-label={`删除渠道 ${channel.name || "未命名渠道"}`}
                                                        size="small"
                                                        type="text"
                                                        danger
                                                        icon={<Trash2 className="size-3.5" />}
                                                    />
                                            </Popconfirm>
                                    </div>
                                </div>
                            </section>
                        );
                    })}
                </div>
            ) : (
                <WorkspaceState
                    icon="settings"
                    compact
                    title="连接你的第一个模型服务"
                    action={
                        <Button icon={<Plus className="size-4" />} onClick={addChannel}>
                            添加模型服务
                        </Button>
                    }
                />
            )}
            {serviceEditor !== undefined && <ModelServiceEditor initial={serviceEditor || undefined} onClose={() => setServiceEditor(undefined)} onSave={saveService} />}
        </div>
    );
}

export function applyFetchedChannelModelCatalog(channel: ModelChannel, result: ChannelModelFetchResult): ModelChannel {
    const profiles = mergeFetchedChannelModelProfiles(channel, result.catalog);
    const existing = new Map((channel.modelProfiles || []).map((profile) => [profile.model, profile]));
    // Refresh updates the catalog without silently enabling new models or removing manual ones.
    return {
        ...channel,
        models: channel.models.length ? channel.models : uniqueModels(result.models),
        modelProfiles: profiles.map((profile) => {
            const previous = existing.get(profile.model);
            if (!previous) return profile;
            if (seedancePortraitModel(profile.model)) return { ...previous, videoPricing: profile.videoPricing ?? null };
            return profile.videoPricing ? { ...previous, videoPricing: profile.videoPricing } : previous;
        }).concat((channel.modelProfiles || []).filter((profile) => !profiles.some((item) => item.model === profile.model))
            .map((profile) => seedancePortraitModel(profile.model) ? { ...profile, videoPricing: null } : profile)),
    };
}

export function newAPITextModelAfterSync(models: string[], current: string) {
    const preferredModel = "gpt-6.1-sol";
    return models.includes(preferredModel) ? encodeChannelModel("newapi", preferredModel) : current;
}

function WorkflowChannelEntry({ icon, title, description, status, ready, onOpen }: { icon: ReactNode; title: string; description: string; status: string; ready: boolean; onOpen?: () => void }) {
    return (
        <div className="settings-channel flex min-w-0 items-center justify-between gap-3 p-3">
            <div className="flex min-w-0 items-start gap-2.5">
                <span className="mt-0.5 shrink-0 text-[var(--workspace-accent)]" aria-hidden="true">
                    {icon}
                </span>
                <div className="min-w-0">
                    <h4 className="text-sm font-semibold">{title}</h4>
                    <p className="mt-0.5 truncate text-xs text-foreground/55">{description}</p>
                    <span className={`settings-channel-status mt-1.5 ${ready ? "is-ready" : "is-warning"}`}>
                        <i aria-hidden="true" />
                        {status}
                    </span>
                </div>
            </div>
            <Button size="small" onClick={onOpen} disabled={!onOpen}>
                配置
            </Button>
        </div>
    );
}

export function channelValidationError(channel: ModelChannel) {
    return channelConnectionError(channel) || validateChannelHeaders(channel.headers) || (!channel.models.length ? "请添加至少一个模型" : "");
}

export function isChannelReady(channel: ModelChannel) {
    return !channelValidationError(channel);
}

export function focusInvalidChannelField(channel: ModelChannel) {
    const baseUrlError = channelConnectionError({ ...channel, apiKey: "valid", secretKey: "valid" });
    const field = baseUrlError ? "base-url" : !channelHasGenerationCredential(channel) && !channelAllowsEmptyAPIKey(channel) ? "api-key" : requiresSecretKey(channel) && !channel.secretKey?.trim() ? "secret-key" : "models";
    requestAnimationFrame(() => {
        const element = document.getElementById(`channel-${channel.id}-${field}`);
        element?.scrollIntoView({ behavior: "smooth", block: "center" });
        element?.focus({ preventScroll: true });
    });
}

function ChannelStatus({ channel, persistence }: { channel: ModelChannel; persistence: ModelConfigPersistenceState }) {
    const receipts = useModelConnectionTests((state) => state.receipts);
    const tested = channel.models.map((model) => currentModelConnectionReceipt(receipts, channel, model));
    const passed = tested.filter((result) => result?.success).length;
    const failed = tested.filter((result) => result && !result.success).length;
    const error = channelValidationError(channel);
    const label = modelConfigChannelStatusLabel(channel, persistence);
    const testLabel = failed ? `${failed} 个模型测试失败` : passed ? `${passed}/${channel.models.length} 个模型测试通过` : label;
    return (
        <span className={`settings-channel-status ${error || failed ? "is-warning" : "is-ready"}`}>
            <i aria-hidden="true" />
            {error || (persistence.status === "saving" || persistence.status === "error" ? label : testLabel)}
        </span>
    );
}

export function modelConfigChannelStatusLabel(channel: ModelChannel, persistence: ModelConfigPersistenceState) {
    if (!channelHasGenerationCredential(channel) && !channelAllowsEmptyAPIKey(channel)) return "待配置";
    if (persistence.status === "saving") return "保存中";
    if (persistence.status === "error") return "保存失败";
    if (persistence.status === "saved") return "已保存 · 尚未测试";
    return "尚未测试";
}

export function withChannels(config: AiConfig, channels: ModelChannel[]): AiConfig {
    const models = modelOptionsFromChannels(channels);
    const imageModels = filterModelsByCapability(models, "image", channels);
    const videoModels = filterModelsByCapability(models, "video", channels);
    const textModels = filterModelsByCapability(models, "text", channels);
    const audioModels = filterModelsByCapability(models, "audio", channels);
    return {
        ...config,
        channels,
        models,
        model: normalizeDefaultModel(config.model, models),
        assistantModel: config.channels.some((channel) => !channels.some((item) => item.id === channel.id) && config.assistantModel?.startsWith(`${channel.id}::`)) ? "" : config.assistantModel,
        mediaReviewModel: config.channels.some((channel) => !channels.some((item) => item.id === channel.id) && config.mediaReviewModel?.startsWith(`${channel.id}::`)) ? "" : config.mediaReviewModel,
        baseUrl: channels[0]?.baseUrl || config.baseUrl,
        apiKey: channels[0]?.apiKey || config.apiKey,
        apiFormat: channels[0]?.apiFormat || config.apiFormat,
        imageModels,
        videoModels,
        textModels,
        audioModels,
        imageModel: normalizeDefaultModel(config.imageModel, imageModels),
        videoModel: normalizeDefaultModel(config.videoModel, videoModels),
        textModel: normalizeDefaultModel(config.textModel, textModels),
        audioModel: normalizeDefaultModel(config.audioModel, audioModels),
    };
}

function normalizeDefaultModel(value: string, options: string[]) {
    return options.includes(value) ? value : options[0] || "";
}

function uniqueModels(models: string[]) {
    return Array.from(new Set(models.map((model) => model.trim()).filter(Boolean)));
}

function isRetiredBeefChannel(channel: ModelChannel) {
	// Do not remove a user-owned channel merely because it reused the old ID or
	// is pinned. Match the backend migration predicate using the managed marker
	// or the exact former BeefTV service origins.
	if (channel.id !== "beefapi") return false;
	if (channel.credentialRef === "beefapi-enterprise") return true;
	try {
		const host = new URL(channel.baseUrl.trim()).hostname.toLowerCase();
		return host === "beeftv.app" || host === "enterprise.beefapi.com";
	} catch {
		return false;
	}
}

function channelModelFetchErrorMessage(error: unknown) {
    if (getTransportFailureKind(error) === "timeout") return "读取模型目录超时，请检查地址和网络后重试；也可以手动添加模型 ID";
    const detail = error instanceof Error ? error.message : "读取模型失败";
    if (detail.includes("不允许访问本机") || detail.includes("不允许访问保留地址")) return `${detail}；可信私网服务需由部署管理员配置 CANVAS_ALLOWED_PRIVATE_UPSTREAM_HOSTS`;
    return `${detail}；也可以直接在模型列表中手动输入模型名`;
}

function channelConnectionError(channel: ModelChannel) {
    const baseUrl = channel.baseUrl.trim();
    if (!baseUrl) return "请填写 Base URL";
    try {
        const parsed = new URL(baseUrl);
        if (parsed.protocol !== "http:" && parsed.protocol !== "https:") return "Base URL 只支持 HTTP 或 HTTPS";
    } catch {
        return "Base URL 格式不正确";
    }
    if (!channelHasGenerationCredential(channel) && !channelAllowsEmptyAPIKey(channel)) return "请填写 API Key / Access Key";
    if (requiresSecretKey(channel) && !channel.secretKey?.trim()) return "当前协议需要填写 Secret Key";
    return "";
}

function channelConnectionSignature(channel: ModelChannel) {
    return [channel.baseUrl.trim(), channel.referenceAssetOrigin?.trim() || "", channel.apiKey.trim(), channel.secretKey?.trim() || "", channel.apiFormat, JSON.stringify(channel.headers || [])].join("\n");
}

function channelProtocolLabel(channel: ModelChannel) {
    if (isTopazUpscaleChannel(channel)) return "fal · Topaz 专用超分";
    return channel.apiFormat === "gemini" ? "Gemini 原生" : "OpenAI 兼容";
}

function requiresSecretKey(channel: ModelChannel) {
    return channel.modelProfiles?.some((item) => item.protocol?.startsWith("volcengine-jimeng-")) === true;
}
