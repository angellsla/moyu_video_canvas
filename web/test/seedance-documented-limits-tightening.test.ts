import { expect, test } from "bun:test";
import { defaultModelCapabilityConfig, modelCapabilityConfigFor, type ModelCapabilityConfig } from "../src/lib/model-capabilities";
import { http } from "../src/services/api/request";
import { assertVideoCapability } from "../src/services/api/video-validation";
import { normalizeModelCapabilityDraft } from "../src/services/api/workspace";

const image = (id: string) => ({ id, name: id, type: "image/png", dataUrl: "", width: 512, height: 512 });
const audio = (id: string) => ({ id, name: id, type: "audio/mpeg", url: `https://example.com/${id}.mp3`, durationMs: 3000 });

function backendTightenedProfile(model: string, mutate: (config: ModelCapabilityConfig) => void, baseUrl = "https://enterprise.beefapi.com", protocol: "newapi" | "volcengine-ark-video" = "newapi") {
    const capabilityConfig = defaultModelCapabilityConfig(protocol, model);
    capabilityConfig.video!.operations = ["text_to_video", "image_to_video", "reference_to_video"];
    capabilityConfig.video!.references.maxVideos = 3;
    capabilityConfig.video!.references.maxAudios = 3;
    capabilityConfig.video!.references.minVideoDurationSeconds = 2;
    capabilityConfig.video!.references.minAudioDurationSeconds = 2;
    mutate(capabilityConfig);
    const channel = { id: "test", models: [model], baseUrl, modelProfiles: [{ model, protocol, capability: "video" as const, capabilityConfig }] };
    return modelCapabilityConfigFor({ channels: [channel] }, `test::${model}`).video!;
}

test("normalizeModelCapabilityDraft 请求后端能力规范化接口并返回收紧后的 capabilityConfig", async () => {
    const originalPost = http.post;
    let capturedUrl = "";
    let capturedBody: unknown = null;
    const backendReturned: ModelCapabilityConfig = {
        ...defaultModelCapabilityConfig("newapi", "seedance-2.0"),
        video: {
            ...defaultModelCapabilityConfig("newapi", "seedance-2.0").video!,
            taskTypeConstraints: false,
            duration: { selection: "enum", values: [-1, 4, 5], default: 5 },
            references: {
                ...defaultModelCapabilityConfig("newapi", "seedance-2.0").video!.references,
                minVideoDurationSeconds: 2,
                minAudioDurationSeconds: 2,
                minVideoPixels: 409600,
                maxVideoPixels: 927408,
            },
        },
    };
    http.post = (async (url: string, data?: unknown) => {
        capturedUrl = url;
        capturedBody = data;
        return { capabilityConfig: backendReturned };
    }) as typeof http.post;

    try {
        const result = await normalizeModelCapabilityDraft({
            channelId: "beefapi",
            baseUrl: "https://enterprise.beefapi.com",
            capability: "video",
            protocol: "newapi",
            model: "seedance-2.0",
        });
        expect(capturedUrl).toBe("/workspace/model-config/capability");
        expect(capturedBody).toEqual({
            channelId: "beefapi",
            baseUrl: "https://enterprise.beefapi.com",
            capability: "video",
            protocol: "newapi",
            model: "seedance-2.0",
        });
        expect(result.capabilityConfig?.video?.duration.values).toEqual([-1, 4, 5]);
        expect(result.capabilityConfig?.video?.references.minAudioDurationSeconds).toBe(2);
    } finally {
        http.post = originalPost;
    }
});

test("后端收紧后的枚举时长配置保留自动时长并拒绝低于下限的值", () => {
    const enumProfile = backendTightenedProfile("seedance-2.0", (config) => {
        config.video!.duration = { selection: "enum", values: [-1, 4, 5], default: 5 };
    });
    expect(enumProfile.duration.values).toEqual([-1, 4, 5]);
    expect(enumProfile.duration.default).toBe(5);
    expect(() => assertVideoCapability(enumProfile, [image("i0")], [], [], "1")).toThrow("输出时长为 1 秒");
    expect(() => assertVideoCapability(enumProfile, [image("i0")], [], [], "4")).not.toThrow();
});

test("后端收紧后的连续时长配置校验 4 秒下限", () => {
    const continuous = backendTightenedProfile("seedance-2.0", (config) => {
        config.video!.duration = { selection: "range", min: 4, fractionalMin: 4, max: 15, step: 1, default: 5, continuous: true };
    });
    expect(continuous.duration.min).toBe(4);
    expect(continuous.duration.fractionalMin).toBe(4);
    expect(() => assertVideoCapability(continuous, [image("i0")], [], [], "1")).toThrow("输出时长为 1 秒");
    expect(() => assertVideoCapability(continuous, [image("i0")], [], [], "4")).not.toThrow();
});

test("后端返回的参考音频下限被保留并作用于参考音频校验", () => {
    const strict = backendTightenedProfile("seedance-2.0", (config) => { config.video!.references.minAudioDurationSeconds = 5; });
    expect(strict.references.minAudioDurationSeconds).toBe(5);
    const standard = backendTightenedProfile("seedance-2.0", (config) => { config.video!.references.minAudioDurationSeconds = 2; });
    expect(standard.references.minAudioDurationSeconds).toBe(2);
    expect(() => assertVideoCapability(standard, [image("i0")], [], [audio("a0"), { ...audio("a1"), durationMs: 1800 }], "5")).toThrow("1.80");
});

test("枚举里没有任何合法时长时拒绝提交，而不是退回低于 4 秒的值", () => {
    const profile = backendTightenedProfile("seedance-2.0", (config) => {
        config.video!.duration = { selection: "enum", values: [], default: 1 };
    });
    expect(profile.duration.values).toEqual([]);
    for (const seconds of ["1", "2", "3", "4"]) {
        expect(() => assertVideoCapability(profile, [image("i0")], [], [], seconds)).toThrow("当前模型没有可用的输出时长");
    }
});
