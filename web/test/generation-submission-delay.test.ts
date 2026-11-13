import { expect, spyOn, test } from "bun:test";

import { GENERATION_SUBMISSION_DELAY_MS, GenerationSubmissionCancelledError, generationModeForTaskInput, generationModeForTaskType, registerGenerationSubmissionPrompt, waitForGenerationSubmission } from "@/services/generation-submission-delay";
import { submissionAcceptanceUnknown } from "@/services/api/generation-submission";
import { ApiError, http } from "@/services/api/request";
import { isGenerationTaskCancelled, runBackendGenerationTaskBatch, submitBackendGenerationTask } from "@/services/api/generation-task";
import { createModelChannel, defaultConfig, type AiConfig } from "@/stores/use-config-store";
import { createGenerationTask, retryGenerationTask } from "@/services/api/task-center";
import type { GenerationTask } from "@/services/api/task-center";

function imageConfig(): AiConfig {
    const channel = createModelChannel({
        id: "test-channel",
        baseUrl: "https://example.com",
        apiFormat: "openai",
        models: ["test-image"],
        modelProfiles: [{ model: "test-image", capability: "image", protocol: "openai-image" }],
    });
    return { ...defaultConfig, model: "test-channel::test-image", imageModel: "test-channel::test-image", models: ["test-channel::test-image"], imageModels: ["test-channel::test-image"], channels: [channel] };
}

test("only image and video task inputs use the submission delay", () => {
    expect(generationModeForTaskInput({ input: { mode: "image" } })).toBe("image");
    expect(generationModeForTaskInput({ input: { mode: "video" } })).toBe("video");
    expect(generationModeForTaskInput({ input: { mode: "text" } })).toBeUndefined();
    expect(generationModeForTaskInput({ input: { mode: "audio" } })).toBeUndefined();
    expect(generationModeForTaskInput({ input: { mode: "workflow" } })).toBeUndefined();
    expect(generationModeForTaskType("canvas_image")).toBe("image");
    expect(generationModeForTaskType("canvas_video")).toBe("video");
    expect(generationModeForTaskType("canvas_text")).toBeUndefined();
    expect(GENERATION_SUBMISSION_DELAY_MS).toBe(5_000);
});

test("the registered UI prompt receives the model and allows immediate submission", async () => {
    let seen: unknown;
    const dispose = registerGenerationSubmissionPrompt(async (request) => {
        seen = request;
        return "submit";
    });
    try {
        await waitForGenerationSubmission({ mode: "video", model: "seedance-2.0-gz" });
        expect(seen).toEqual({ mode: "video", model: "seedance-2.0-gz" });
    } finally {
        dispose();
    }
});

test("cancelling the prompt rejects before a task can be submitted", async () => {
    const dispose = registerGenerationSubmissionPrompt(async () => "cancel");
    try {
        await expect(waitForGenerationSubmission({ mode: "image", model: "gpt-image-2" })).rejects.toBeInstanceOf(GenerationSubmissionCancelledError);
        expect(submissionAcceptanceUnknown(new GenerationSubmissionCancelledError())).toBe(false);
    } finally {
        dispose();
    }
});

test("an already-aborted request never opens the prompt", async () => {
    let prompted = false;
    const dispose = registerGenerationSubmissionPrompt(async () => {
        prompted = true;
        return "submit";
    });
    const controller = new AbortController();
    controller.abort();
    try {
        await expect(waitForGenerationSubmission({ mode: "video", signal: controller.signal })).rejects.toMatchObject({ name: "AbortError" });
        expect(prompted).toBe(false);
    } finally {
        dispose();
    }
});

test("task creation waits before image submission and strips the internal skip flag", async () => {
    let prompts = 0;
    const dispose = registerGenerationSubmissionPrompt(async (request) => {
        prompts += 1;
        expect(request.mode).toBe("image");
        return "submit";
    });
    const post = spyOn(http, "post").mockResolvedValue({ id: "image-task" } as never);
    const input = { prompt: "draw", model: "test-image", input: { mode: "image" } };
    try {
        await createGenerationTask(input);
        await createGenerationTask(input, { skipGenerationSubmissionDelay: true });
        expect(prompts).toBe(1);
        expect(post).toHaveBeenCalledTimes(2);
        expect((post.mock.calls[0]?.[2] as Record<string, unknown> | undefined)?.skipGenerationSubmissionDelay).toBeUndefined();
        expect((post.mock.calls[1]?.[2] as Record<string, unknown> | undefined)?.skipGenerationSubmissionDelay).toBeUndefined();
    } finally {
        post.mockRestore();
        dispose();
    }
});

test("retrying an image or video task also waits before requeueing", async () => {
    let prompts = 0;
    const dispose = registerGenerationSubmissionPrompt(async (request) => {
        prompts += 1;
        expect(request.mode).toBe("video");
        expect(request.model).toBe("seedance-2.0");
        return "submit";
    });
    const post = spyOn(http, "post").mockResolvedValue({ id: "retry-task" } as never);
    try {
        await retryGenerationTask("task", undefined, { mode: "video", model: "seedance-2.0" });
        expect(prompts).toBe(1);
        expect(post).toHaveBeenCalledTimes(1);
    } finally {
        post.mockRestore();
        dispose();
    }
});

test("a batch shares one delay and retries reuse the completed delay", async () => {
    let prompts = 0;
    const dispose = registerGenerationSubmissionPrompt(async () => {
        prompts += 1;
        return "submit";
    });
    const configs: Array<Record<string, unknown> | undefined> = [];
    const task = { id: "task", type: "canvas_image", status: "queued", prompt: "draw", attempts: 0, createdAt: "now", updatedAt: "now" } as GenerationTask;
    try {
        const settled = await runBackendGenerationTaskBatch(
            { mode: "image", prompt: "draw", config: imageConfig(), count: 3 },
            {
                createTask: async (_input, config) => {
                    configs.push(config as Record<string, unknown>);
                    return task;
                },
                waitTask: async () => {
                    throw new Error("stop after admission");
                },
                createId: () => "batch-id",
            },
        );
        expect(settled).toHaveLength(3);
        expect(prompts).toBe(1);
        expect(configs).toHaveLength(3);
        expect(configs.every((config) => config?.skipGenerationSubmissionDelay === true)).toBe(true);

        let attempts = 0;
        const retryConfigs: Array<Record<string, unknown> | undefined> = [];
        await submitBackendGenerationTask(
            { projectId: "canvas", mode: "image", prompt: "draw", config: imageConfig() },
            {
                createTask: async (_input, config) => {
                    retryConfigs.push(config as Record<string, unknown>);
                    if (attempts++ === 0) throw new ApiError("temporary", { status: 504 });
                    return task;
                },
                waitTask: async () => {
                    throw new Error("must not wait");
                },
                createId: () => "retry-id",
                findTask: async () => undefined,
            },
        );
        expect(retryConfigs.map((config) => config?.skipGenerationSubmissionDelay)).toEqual([false, true]);
        expect(isGenerationTaskCancelled(new GenerationSubmissionCancelledError())).toBe(true);
    } finally {
        dispose();
    }
});
