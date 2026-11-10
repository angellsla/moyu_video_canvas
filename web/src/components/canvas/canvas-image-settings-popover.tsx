import { useEffect, useRef, useState, type RefObject } from "react";
import { createPortal } from "react-dom";
import { Settings2 } from "lucide-react";
import { Button } from "antd";

import { ImageSettingsPanel, imageQualityLabel, imageSizeLabel } from "@/components/image-settings-panel";
import { MidjourneyImagineSettings } from "@/components/midjourney-imagine-settings";
import { canvasThemes } from "@/lib/canvas-theme";
import { canvasPopoverLayer } from "@/lib/canvas/canvas-popover-layer";
import { modelCapabilityConfigFor, normalizeImageValue } from "@/lib/model-capabilities";
import { useActiveTheme } from "@/stores/canvas/use-canvas-theme-store";
import type { AiConfig } from "@/stores/use-config-store";
import { seedreamFamily } from "@/lib/seedream-image";
import { isMidjourneyImagineModel, summarizeMidjourneyParams, type MidjourneyImagineParams } from "@/lib/midjourney-imagine";

type CanvasImageSettingsPopoverProps = {
    config: AiConfig;
    onConfigChange: (key: keyof AiConfig, value: string | MidjourneyImagineParams) => void;
    onMissingConfig?: () => void;
    onOpenChange?: (open: boolean) => void;
    buttonClassName?: string;
    getPopupContainer?: (triggerNode: HTMLElement) => HTMLElement;
    placement?: "topLeft" | "top" | "topRight" | "bottomLeft" | "bottom" | "bottomRight";
    autoAdjustOverflow?: boolean;
    showCount?: boolean;
    summaryOverride?: string;
};

export function CanvasImageSettingsPopover({ config, onConfigChange, onOpenChange, buttonClassName, placement = "topLeft", showCount = true, summaryOverride }: CanvasImageSettingsPopoverProps) {
    const theme = canvasThemes[useActiveTheme()];
    const buttonRef = useRef<HTMLSpanElement>(null);
    const panelRef = useRef<HTMLDivElement>(null);
    const [open, setOpen] = useState(false);
    const [buttonRect, setButtonRect] = useState<DOMRect | null>(null);
    const profile = modelCapabilityConfigFor(config, config.model || config.imageModel).image!;
    const normalized = normalizeImageValue(profile, config);
    const seedream = seedreamFamily(config.model || config.imageModel);
    const midjourney = isMidjourneyImagineModel(config.model || config.imageModel);
    const summaryParts = [
        ...(seedream && config.seedreamMode === "layers" ? ["图层拆分"] : []),
        ...(profile.size.parameter !== "none" ? [imageSizeLabel(normalized.size, profile)] : []),
        ...(profile.quality.supported ? [imageQualityLabel(normalized.quality)] : []),
        ...(seedream && seedream !== "pro" && seedream !== "flash" && Number(config.seedreamMaxImages) > 1 ? [`每批最多 ${config.seedreamMaxImages} 张`] : []),
        ...(showCount && profile.maxOutputs > 1 ? [`${normalized.count} ${seedream ? "批" : "张"}`] : []),
        ...(profile.transparentBackground.supported && normalized.transparentBackground === "true" ? ["透明"] : []),
    ];
    const summary = summaryOverride || (midjourney ? `MJ 参数 · ${imageSizeLabel(config.size || normalized.size, profile)} · ${summarizeMidjourneyParams(config.midjourneyParams)}` : summaryParts.join(" · "));
    const hasSettings = midjourney || profile.size.parameter !== "none" || profile.quality.supported || profile.transparentBackground.supported || (showCount && profile.maxOutputs > 1);
    const updateOpen = (nextOpen: boolean) => {
        setOpen(nextOpen);
        onOpenChange?.(nextOpen);
    };

    useEffect(() => {
        if (!open) return;
        const syncPosition = () => setButtonRect(buttonRef.current?.getBoundingClientRect() || null);
        const closeOnOutsidePointer = (event: PointerEvent) => {
            const target = event.target;
            if (!(target instanceof Node)) return;
            if (buttonRef.current?.contains(target) || panelRef.current?.contains(target)) return;
            if (target instanceof Element && target.closest(".ant-select-dropdown")) return;
            if (document.activeElement instanceof HTMLElement && panelRef.current?.contains(document.activeElement)) document.activeElement.blur();
            setOpen(false);
            onOpenChange?.(false);
        };

        syncPosition();
        window.addEventListener("resize", syncPosition);
        window.addEventListener("scroll", syncPosition, true);
        window.addEventListener("pointerdown", closeOnOutsidePointer, true);
        return () => {
            window.removeEventListener("resize", syncPosition);
            window.removeEventListener("scroll", syncPosition, true);
            window.removeEventListener("pointerdown", closeOnOutsidePointer, true);
        };
    }, [onOpenChange, open]);

    const panel = open && buttonRect ? <ImageSettingsPortal layer={canvasPopoverLayer(buttonRef.current)} buttonRect={buttonRect} panelRef={panelRef} placement={placement} theme={theme} config={config} showCount={showCount} onConfigChange={onConfigChange} /> : null;

    if (!hasSettings) return null;

    return (
        <>
            <span ref={buttonRef} className="inline-flex min-w-0">
                <Button size="small" type="text" className={`canvas-generation-settings-trigger ${buttonClassName || "!h-8 !max-w-[180px] !justify-start !rounded-full !px-2.5"}`} style={{ background: theme.node.fill, color: theme.node.text }} icon={<Settings2 className="size-3.5" />} aria-expanded={open} aria-label={`高级设置：${summary}`} title={`高级设置 · ${summary}`} onClick={() => updateOpen(!open)}>
                    <span className="truncate">{summary}</span>
                </Button>
            </span>
            {panel}
        </>
    );
}

function ImageSettingsPortal({
    layer,
    buttonRect,
    panelRef,
    placement,
    theme,
    config,
    showCount,
    onConfigChange,
}: {
    layer: number | string;
    buttonRect: DOMRect;
    panelRef: RefObject<HTMLDivElement | null>;
    placement: CanvasImageSettingsPopoverProps["placement"];
    theme: (typeof canvasThemes)[keyof typeof canvasThemes];
    config: AiConfig;
    showCount: boolean;
    onConfigChange: (key: keyof AiConfig, value: string | MidjourneyImagineParams) => void;
}) {
    const gap = 8;
    const margin = 12;
    const width = Math.min(420, window.innerWidth - margin * 2);
    const alignRight = placement?.endsWith("Right");
    const alignCenter = placement === "top" || placement === "bottom";
    const left = alignCenter ? buttonRect.left + buttonRect.width / 2 - width / 2 : alignRight ? buttonRect.right - width : buttonRect.left;
    const preferTop = placement?.startsWith("top") || false;
    const spaceAbove = Math.max(0, buttonRect.top - margin - gap);
    const spaceBelow = Math.max(0, window.innerHeight - buttonRect.bottom - margin - gap);
    const topPlacement = preferTop ? spaceAbove >= 220 || spaceAbove >= spaceBelow : spaceBelow < 220 && spaceAbove > spaceBelow;
    const maxHeight = Math.max(0, Math.min(560, window.innerHeight - margin * 2, topPlacement ? spaceAbove : spaceBelow));
    const style = {
        position: "fixed",
        zIndex: layer,
        width,
        left: Math.max(margin, Math.min(window.innerWidth - width - margin, left)),
        ...(topPlacement ? { bottom: window.innerHeight - buttonRect.top + gap } : { top: buttonRect.bottom + gap }),
        maxHeight,
        background: theme.canvas.background,
        border: `1px solid ${theme.toolbar.border}`,
        borderRadius: 10,
        boxShadow: `0 24px 72px ${theme.spatial.shadow}`,
        padding: 12,
        overflowY: "auto",
        color: theme.node.text,
    } as const;

    return createPortal(
        <div
            ref={panelRef}
            className="canvas-image-settings-popover aceternity-floating-panel backdrop-blur-2xl"
            style={style}
            onPointerDown={(event) => event.stopPropagation()}
            onMouseDown={(event) => event.stopPropagation()}
            onClick={(event) => event.stopPropagation()}
        >
            {isMidjourneyImagineModel(config.model || config.imageModel) ? (
                <MidjourneyImagineSettings config={config} theme={theme} onConfigChange={onConfigChange} />
            ) : (
                <ImageSettingsPanel config={config} onConfigChange={(key, value) => onConfigChange(key, value)} theme={theme} showCount={showCount} quickCount={3} className="space-y-3" />
            )}
        </div>,
        document.body,
    );
}
