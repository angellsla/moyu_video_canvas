export const GENERATION_SUBMISSION_DELAY_MS = 5_000;

export type DelayedGenerationMode = "image" | "video";

export type GenerationSubmissionDelayRequest = {
    mode: DelayedGenerationMode;
    model?: string;
    signal?: AbortSignal;
};

export type GenerationSubmissionDecision = "submit" | "cancel";

export type GenerationSubmissionPrompt = (request: GenerationSubmissionDelayRequest) => Promise<GenerationSubmissionDecision>;

let submissionPrompt: GenerationSubmissionPrompt | undefined;

export class GenerationSubmissionCancelledError extends Error {
    readonly code = "submission_cancelled";

    constructor() {
        super("生成请求已取消，尚未提交");
        this.name = "GenerationSubmissionCancelledError";
    }
}

export function registerGenerationSubmissionPrompt(prompt: GenerationSubmissionPrompt) {
    submissionPrompt = prompt;
    return () => {
        if (submissionPrompt === prompt) submissionPrompt = undefined;
    };
}

export function generationModeForTaskInput(input: { input?: Record<string, unknown> }): DelayedGenerationMode | undefined {
    const mode = input.input?.mode;
    return mode === "image" || mode === "video" ? mode : undefined;
}

export function generationModeForTaskType(type: string | undefined): DelayedGenerationMode | undefined {
    if (type === "canvas_image") return "image";
    if (type === "canvas_video") return "video";
    return undefined;
}

export async function waitForGenerationSubmission(request: GenerationSubmissionDelayRequest) {
    if (request.signal?.aborted) throw new DOMException("Aborted", "AbortError");
    // The bridge is installed by the browser application. Keeping this a no-op
    // without a bridge preserves SSR and service-level tests, which have no UI.
    const prompt = submissionPrompt;
    if (!prompt) return;
    const decision = await prompt(request);
    if (request.signal?.aborted) throw new DOMException("Aborted", "AbortError");
    if (decision === "cancel") throw new GenerationSubmissionCancelledError();
}
