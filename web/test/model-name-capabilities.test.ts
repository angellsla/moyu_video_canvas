import { expect, test } from "bun:test";

import { applyFetchedChannelModelCatalog } from "../src/pages/settings/channel-settings-pane";
import { createModelChannel, defaultConfig, normalizeConfigSnapshot } from "../src/stores/use-config-store";

const textModels = ["gpt-6.1-sol"];

const imageModels = [
    "gpt-image-2.5-sunburst-k",
    "gemini-3.1-flash-image-preview",
    "gpt-image-2.5-flare-k",
    "mj_imagine",
    "gpt-image-2.5-sunburst-4k-k",
    "gpt-image-2-4k",
    "gpt-image-2-4k-k",
    "gpt-image-2-k",
    "gpt-image-2.5-flare-4k-k",
];

const videoModels = [
    "wan3-pro-1",
    "SD-2.5-720p-15s",
    "seedance-2.0-nt-720",
    "grok-imagine-video-1.5",
    "gemini-nano-banana-2.1",
    "dola-2.5-720p",
    "seedance-2.0-gz",
    "SD-2.5-1080p",
    "seedance2.5-mrt-720p",
    "gpt-image-2.5-sunburst",
    "seedance2.5-gz-1080",
    "yk-seedance25-1080",
    "seedance-2.5-gz",
    "seedance-2.5-r4",
    "seedance-2.5-l2-720p",
    "seedance-2.5-pa-1080p",
    "seedance-2.5-pa-720p",
    "tr_seedance-2.0",
    "seedance-2.5-yy-720p",
    "wan3.0-video",
    "gpt-image-2.5-flare",
    "NTT-720p",
    "sd2.5-1080p",
    "2.0-933-720p",
    "seedance2.5-30s",
    "SD-2.0-720p",
    "tr_seedance-2.0_faceok",
    "seedance-2.0-nt-480",
    "wan3.0-smart2",
    "sd2.5-pp-720p",
    "wan3-pro",
];

const allModels = [...textModels, ...imageModels, ...videoModels];

test("New API model sync classifies the supplied text, image, and video IDs", () => {
    const channel = createModelChannel({
        id: "newapi",
        models: allModels,
        modelProfiles: allModels.map((model) => ({ model, capability: "text", protocol: "chat-completion" })),
    });
    const config = normalizeConfigSnapshot({ config: { ...defaultConfig, channels: [channel] } }).config;

    expect(config.textModels).toEqual(["newapi::gpt-6.1-sol"]);
    expect(config.imageModels).toEqual(imageModels.map((model) => `newapi::${model}`));
    expect(config.videoModels).toEqual(videoModels.map((model) => `newapi::${model}`));
});

test("New API catalog refresh corrects stale text metadata for explicitly classified IDs", () => {
    const channel = createModelChannel({
        id: "newapi",
        models: allModels,
        modelProfiles: allModels.map((model) => ({ model, capability: "text", protocol: "chat-completion" })),
    });
    const refreshed = applyFetchedChannelModelCatalog(channel, {
        models: allModels,
        catalog: allModels.map((id) => ({ id, modelType: "text", supportedEndpointTypes: ["chat.completions"] })),
    });
    const profileByModel = new Map(refreshed.modelProfiles?.map((profile) => [profile.model, profile]));

    expect(profileByModel.get(textModels[0]!)).toMatchObject({ capability: "text", protocol: "chat-completion" });
    for (const model of imageModels) {
        expect(profileByModel.get(model)).toMatchObject({ capability: "image", protocol: "openai-image" });
    }
    for (const model of videoModels) {
        expect(profileByModel.get(model)).toMatchObject({ capability: "video", protocol: "newapi" });
    }
});
