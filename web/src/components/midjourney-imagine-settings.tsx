import { ConfigProvider, Input, InputNumber, Select } from "antd";
import type { ReactNode } from "react";

import type { CanvasTheme } from "@/lib/canvas-theme";
import { isMidjourneyImagineModel, type MidjourneyImagineParams } from "@/lib/midjourney-imagine";
import type { AiConfig } from "@/stores/use-config-store";
import { Switch } from "@/components/ui/base/switch";

type Change = (key: keyof AiConfig, value: string | MidjourneyImagineParams) => void;

export function MidjourneyImagineSettings({ config, theme, onConfigChange }: { config: AiConfig; theme: CanvasTheme; onConfigChange: Change }) {
    const params = config.midjourneyParams || {};
    const version = params.version || "";
    const isV8 = version.startsWith("8.");
    const allowQuality = !params.niji && (!version || version === "6" || version === "7");
    const allowSDHD = version === "8.1" || version === "8.2";
    const allowDraft = !params.niji && (!version || version === "7");
    const allowOref = !params.niji && (!version || version === "7");
    const allowCref = !params.niji && (!version || version === "6");
    const allowMoodboardWeights = !params.profile?.trim();
    const update = <K extends keyof MidjourneyImagineParams>(key: K, value: MidjourneyImagineParams[K] | null) => {
        const next = { ...params };
        if (value === null || value === undefined || value === "" || value === false) delete next[key];
        else next[key] = value;
        if (key === "version" && value) {
            delete next.niji;
            if (value !== "6") { delete next.cref; delete next.cw; }
            if (value !== "7") { delete next.oref; delete next.ow; delete next.draft; }
            if (value !== "6" && value !== "7") delete next.quality;
            if (value !== "8.1" && value !== "8.2") delete next.sdHd;
            if (String(value).startsWith("8.") && next.speedMode === "turbo") delete next.speedMode;
        }
        if (key === "niji" && value) {
            delete next.version;
            delete next.quality;
            delete next.draft;
            delete next.sdHd;
            delete next.oref;
            delete next.ow;
            delete next.cref;
            delete next.cw;
        }
        if (key === "profile" && value) { delete next.sw; delete next.sv; }
        onConfigChange("midjourneyParams", next);
    };
    const numberField = (label: string, key: "stylize" | "chaos" | "weird" | "exp" | "seed" | "repeat" | "stop" | "imageWeight" | "sw" | "ow" | "cw", min: number, max?: number, disabled = false) => (
        <Field label={label}>
            <InputNumber
                aria-label={label}
                size="small"
                min={min}
                max={max}
                step={key === "imageWeight" ? 0.1 : 1}
                value={params[key] as number | undefined}
                disabled={disabled}
                controls
                className="!w-full"
                onChange={(value) => update(key, value == null ? null : Number(value) as never)}
            />
        </Field>
    );
    const textField = (label: string, key: "no" | "sref" | "sv" | "profile" | "oref" | "cref", disabled = false, placeholder?: string) => (
        <Field label={label}>
            <Input aria-label={label} size="small" value={params[key]} disabled={disabled} placeholder={placeholder} onChange={(event) => update(key, event.target.value as never)} />
        </Field>
    );
    const selectField = <K extends "version" | "niji" | "sdHd" | "quality" | "speedMode">(label: string, key: K, options: Array<{ value: string; label: string; disabled?: boolean }>, disabled = false) => (
        <Field label={label}>
            <Select
                aria-label={label}
                size="small"
                allowClear
                value={params[key]}
                disabled={disabled}
                placeholder="默认"
                options={options}
                onChange={(value) => update(key, (value || null) as never)}
            />
        </Field>
    );
    const toggle = (label: string, key: "raw" | "tile" | "draft", disabled = false) => (
        <div className="flex min-h-8 items-center justify-between gap-3 py-1">
            <span className="text-xs">{label}</span>
            <Switch size="sm" checked={Boolean(params[key])} disabled={disabled} onChange={(checked) => update(key, checked as never)} aria-label={label} />
        </div>
    );

    if (!isMidjourneyImagineModel(config.model || config.imageModel)) return null;

    return (
        <ConfigProvider theme={{ token: { colorBgContainer: theme.canvas.background, colorBgElevated: theme.canvas.background, colorBorder: theme.node.stroke, colorPrimary: theme.node.activeStroke, colorText: theme.node.text, zIndexPopupBase: 1200 } }}>
            <div className="space-y-4 text-sm" style={{ color: theme.node.text }}>
                <div>
                    <div className="text-sm font-semibold">Midjourney Imagine</div>
                    <div className="mt-1 text-xs" style={{ color: theme.node.muted }}>生成参数将按 Midjourney 语法附加到提示词。</div>
                </div>

                <Section title="画幅与风格">
                    <Field label="画幅比例 --ar">
                        <Input
                            aria-label="画幅比例 --ar"
                            size="small"
                            value={config.size || "1:1"}
                            placeholder="例如 16:9 或 1920:1080"
                            onChange={(event) => onConfigChange("size", event.target.value)}
                        />
                    </Field>
                    <div className="grid grid-cols-2 gap-2">
                        {numberField("风格化 --stylize", "stylize", 0, 1000)}
                        {numberField("混乱度 --chaos", "chaos", 0, 100)}
                        {numberField("怪异度 --weird", "weird", 0, 3000)}
                        {numberField("实验性 --exp", "exp", 0, 100)}
                        {numberField("种子 --seed", "seed", 0, 4294967295)}
                        {textField("负面提示 --no", "no", false, "逗号分隔")}
                    </div>
                    <div className="grid grid-cols-2 gap-x-3 gap-y-1">
                        {toggle("Raw 风格", "raw")}
                        {toggle("无缝平铺 --tile", "tile")}
                        {toggle("草稿模式 --draft", "draft", !allowDraft)}
                    </div>
                    <div className="grid grid-cols-2 gap-2">
                        {numberField("重复次数 --repeat", "repeat", 1)}
                        {numberField("提前停止 --stop (%)", "stop", 0, 100)}
                    </div>
                </Section>

                <Section title="模型与输出">
                    <div className="grid grid-cols-2 gap-2">
                        {selectField("模型版本 --v", "version", ["1", "2", "3", "4", "5", "6", "7", "8.1", "8.2"].map((value) => ({ value, label: `V${value}` })))}
                        {selectField("动漫模型 --niji", "niji", ["6", "7"].map((value) => ({ value, label: `Niji ${value}` })))}
                        {selectField("输出档位", "sdHd", [{ value: "sd", label: "SD · 1024px" }, { value: "hd", label: "HD · 2048px" }], !allowSDHD)}
                        {selectField("质量 --q", "quality", ["1", "2", "4"].map((value) => ({ value, label: value })), !allowQuality)}
                        {selectField("速度模式", "speedMode", [{ value: "fast", label: "Fast" }, { value: "relax", label: "Relax" }, { value: "turbo", label: "Turbo", disabled: isV8 }])}
                    </div>
                </Section>

                <Section title="参考图与个性化">
                    <div className="grid grid-cols-2 gap-2">
                        {numberField("图片权重 --iw", "imageWeight", 0, 3)}
                        {numberField("风格权重 --sw", "sw", 0, 1000, !allowMoodboardWeights)}
                        {numberField("物体权重 --ow", "ow", 0, 1000, !allowOref)}
                        {numberField("角色权重 --cw", "cw", 0, 100, !allowCref)}
                        {textField("风格参考 --sref", "sref", false, "URL、随机或权重")}
                        {textField("风格版本 --sv", "sv", !allowMoodboardWeights, "例如 4 或 6")}
                        {textField("个性化 --profile", "profile", false, "Profile / Moodboard ID")}
                        {textField("物体参考 --oref", "oref", !allowOref, "V7")}
                        {textField("角色参考 --cref", "cref", !allowCref, "V6")}
                    </div>
                </Section>
            </div>
        </ConfigProvider>
    );
}

function Section({ title, children }: { title: string; children: ReactNode }) {
    return <section className="space-y-2 border-t pt-3" style={{ borderColor: "var(--border)" }}><h3 className="m-0 text-xs font-semibold" style={{ color: "var(--muted-foreground)" }}>{title}</h3>{children}</section>;
}

function Field({ label, children }: { label: string; children: ReactNode }) {
    return <label className="block min-w-0 space-y-1 text-xs"><span className="block truncate">{label}</span>{children}</label>;
}
