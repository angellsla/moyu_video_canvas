import { useCallback, useEffect } from "react";
import { App } from "antd";

import { GENERATION_SUBMISSION_DELAY_MS, type GenerationSubmissionDelayRequest, registerGenerationSubmissionPrompt, type GenerationSubmissionDecision } from "@/services/generation-submission-delay";

function promptContent(request: GenerationSubmissionDelayRequest, seconds: number) {
    const kind = request.mode === "image" ? "图片" : "视频";
    const model = request.model?.trim() ? `模型：${request.model.trim()}。` : "";
    return `${model}${seconds} 秒后自动提交${kind}生成请求。你可以点击“取消”，或点击“立即提交”跳过等待。`;
}

export function GenerationSubmissionDelayBridge() {
    const { modal } = App.useApp();
    const prompt = useCallback(
        (request: GenerationSubmissionDelayRequest) =>
            new Promise<GenerationSubmissionDecision>((resolve) => {
                let settled = false;
                let dialog: { destroy: () => void; update: (config: { content: string }) => void } | undefined;
                let interval: ReturnType<typeof setInterval> | undefined;
                let timeout: ReturnType<typeof setTimeout> | undefined;

                const finish = (decision: GenerationSubmissionDecision) => {
                    if (settled) return;
                    settled = true;
                    if (interval) clearInterval(interval);
                    if (timeout) clearTimeout(timeout);
                    request.signal?.removeEventListener("abort", abort);
                    dialog?.destroy();
                    resolve(decision);
                };
                const abort = () => finish("cancel");
                if (request.signal?.aborted) return abort();

                const deadline = Date.now() + GENERATION_SUBMISSION_DELAY_MS;
                const updateCountdown = () => {
                    const remaining = Math.max(0, deadline - Date.now());
                    if (!remaining) {
                        finish("submit");
                        return;
                    }
                    dialog?.update({ content: promptContent(request, Math.ceil(remaining / 1000)) });
                };

                request.signal?.addEventListener("abort", abort, { once: true });
                dialog = modal.confirm({
                    title: request.mode === "image" ? "即将提交图片生成" : "即将提交视频生成",
                    content: promptContent(request, Math.ceil(GENERATION_SUBMISSION_DELAY_MS / 1000)),
                    okText: "立即提交",
                    cancelText: "取消",
                    centered: true,
                    maskClosable: false,
                    closable: false,
                    keyboard: false,
                    onOk: () => finish("submit"),
                    onCancel: () => finish("cancel"),
                    afterClose: () => finish("cancel"),
                });
                interval = setInterval(updateCountdown, 250);
                timeout = setTimeout(() => finish("submit"), GENERATION_SUBMISSION_DELAY_MS);
                updateCountdown();
                if (request.signal?.aborted) abort();
            }),
        [modal],
    );

    useEffect(() => registerGenerationSubmissionPrompt(prompt), [prompt]);
    return null;
}
