import { expect, test } from "bun:test";
import { videoModelContract } from "../src/lib/model-video-contracts";
import { createModelChannel, defaultConfig, normalizeConfigSnapshot, resolveModelRequestConfig } from "../src/stores/use-config-store";
import { defaultModelCapabilityConfig, modelCapabilityConfigFor } from "../src/lib/model-capabilities";
import { buildGenerationConfig } from "../src/lib/canvas/canvas-generation-config";
import { CanvasNodeType, type CanvasNodeData } from "../src/types/canvas";
import { backendProviderConfig } from "../src/services/api/generation-task";

test("Kling capability repair preserves profile defaults and explicit canvas audio choices through task config", () => {
    const model = "kling-video-v3";
    for (const audioDefault of [false, true]) {
        const capabilityConfig = defaultModelCapabilityConfig("newapi", model);
        capabilityConfig.video!.generateAudio = { supported: false, default: audioDefault };
        const channel = createModelChannel({ id: "newapi", baseUrl: "https://newapi.example.test", models: [model], modelProfiles: [{ model, capability: "video", protocol: "newapi", capabilityConfig }] });
        const snapshot = normalizeConfigSnapshot({ config: { ...defaultConfig, channels: [channel], videoModel: `newapi::${model}`, videoGenerateAudio: String(!audioDefault) } }).config;
        expect(modelCapabilityConfigFor(snapshot, `newapi::${model}`).video!.generateAudio).toEqual({ supported: true, default: audioDefault });
        for (const selected of [undefined, "false", "true"] as const) {
            const node: CanvasNodeData = { id: "video", type: CanvasNodeType.Video, title: "fixture", position: { x: 0, y: 0 }, width: 320, height: 180, metadata: { model: `newapi::${model}`, generateAudio: selected } };
            const config = resolveModelRequestConfig(buildGenerationConfig(snapshot, node, "video"), `newapi::${model}`);
            const expected = selected === undefined ? audioDefault : selected === "true";
            expect(config.videoGenerateAudio).toBe(String(expected));
            expect(backendProviderConfig(config, "video")).toMatchObject({ videoGenerateAudio: String(expected), videoSeconds: "5", model });
        }
    }
});

test("Wan uses shared protocol and supported reference limits across reloads", () => {
    const model = "wan3.0-video";
    const channel = createModelChannel({id: "newapi", baseUrl: "https://newapi.example.test", models: [model], modelProfiles: [{model, capability: "video", protocol: "openai-videos"}]});
    let config = {...defaultConfig, channels: [channel]};
    for (let i = 0; i < 2; i++) {
        config = normalizeConfigSnapshot({config}).config;
        expect(resolveModelRequestConfig(config, `newapi::${model}`).interfaceType).toBe("newapi-channel-2");
        const refs = modelCapabilityConfigFor(config, `newapi::${model}`).video!.references;
        expect([refs.maxImages, refs.maxVideos, refs.maxAudios]).toEqual([1, 0, 0]);
    }
});

test("new models preserve their catalog protocol without inheriting unverified inline support", () => {
    const model = "future-video";
    const channel = createModelChannel({id: "newapi", baseUrl: "https://newapi.example.test", models: [model], modelProfiles: [{model, capability: "video", protocol: "minimax-video"}]});
    const config = normalizeConfigSnapshot({config: {...defaultConfig, channels: [channel]}}).config;
    expect(resolveModelRequestConfig(config, `newapi::${model}`).interfaceType).toBe("minimax-video");
    expect(videoModelContract(model)).toBeUndefined();
    expect(videoModelContract("wan3.0-video-prime")).toBeUndefined();
});

test("saved New API grok video profile never keeps a text protocol", () => {
    const model = "grok-imagine-video-1.5";
    for (const savedCapability of ["text", "video"] as const) {
        const channel = createModelChannel({id: "newapi", baseUrl: "https://newapi.example.test", models: [model], modelProfiles: [{model, capability: savedCapability, protocol: "chat-completion"}]});
        const config = normalizeConfigSnapshot({config: {...defaultConfig, channels: [channel]}}).config;
        const profile = config.channels[0]!.modelProfiles!.find((item) => item.model === model)!;
        expect(profile.capability).toBe("video");
        expect(profile.protocol).toBe("newapi");
        expect(resolveModelRequestConfig(config, `newapi::${model}`).interfaceType).toBe("newapi");
    }
});

test("saved New API Seedance profiles still follow the shared video contract", () => {
    const model = "seedance-2.0-fast";
    const channel = createModelChannel({id: "newapi", baseUrl: "https://newapi.example.test", models: [model], modelProfiles: [{model, capability: "text", protocol: "chat-completion"}]});
    const config = normalizeConfigSnapshot({config: {...defaultConfig, channels: [channel]}}).config;
    const profile = config.channels[0]!.modelProfiles!.find((item) => item.model === model)!;
    expect(profile.capability).toBe("video");
    expect(profile.protocol).toBe("newapi");
    expect(resolveModelRequestConfig(config, `newapi::${model}`).interfaceType).toBe("newapi");
});

test("partial reference contracts preserve the limits of unspecified media kinds", () => {
    const model = "wan3.0-video";
    const contract = videoModelContract(model)!;
    const original = contract.maxReferences;
    try {
        contract.maxReferences = {image: 1};
        const capabilityConfig = defaultModelCapabilityConfig("newapi-channel-2", model);
        Object.assign(capabilityConfig.video!.references, {maxImages: 4, maxVideos: 2, maxAudios: 1});
        const channel = createModelChannel({id: "newapi", baseUrl: "https://newapi.example.test", models: [model], modelProfiles: [{model, capability: "video", protocol: "newapi-channel-2", capabilityConfig}]});
        const config = normalizeConfigSnapshot({config: {...defaultConfig, channels: [channel]}}).config;
        const refs = modelCapabilityConfigFor(config, `newapi::${model}`).video!.references;
        expect([refs.maxImages, refs.maxVideos, refs.maxAudios]).toEqual([1, 2, 1]);
    } finally { contract.maxReferences = original; }
});

test("custom channel profiles take their capability from the configured protocol", () => {
    const channel = createModelChannel({id: "studio", baseUrl: "https://api.example.com", apiKey: "synthetic-key", models: ["clip-x"], modelProfiles: [{model: "clip-x", capability: "text", protocol: "newapi-channel-2"}]});
    const config = normalizeConfigSnapshot({config: {...defaultConfig, channels: [channel]}}).config;
    const profile = config.channels[0]!.modelProfiles!.find((item) => item.model === "clip-x")!;
    expect(profile).toMatchObject({capability: "video", protocol: "newapi-channel-2"});
});
