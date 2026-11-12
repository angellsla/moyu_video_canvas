import { expect, test } from "bun:test";

import { mergeFetchedChannelModelProfiles } from "../src/lib/channel-model-catalog";
import { defaultModelCapabilityConfig } from "../src/lib/model-capabilities";
import { createModelChannel, defaultConfig, normalizeConfigSnapshot, resolveModelRequestConfig } from "../src/stores/use-config-store";

const model = "seedance-2.0-gz";

for (const channelId of ["newapi", "studio"]) {
    for (const protocol of ["video-generations-json", "newapi-channel-1", "newapi-channel-2", "volcengine-ark-video", "studio-multi-image"]) {
        test(`${channelId}: ${protocol} survives catalog refresh and config reload`, () => {
            const capabilityConfig = defaultModelCapabilityConfig(protocol, model);
            capabilityConfig.video!.references.maxImages = 5;
            const original = createModelChannel({
                id: channelId, baseUrl: "https://video.example/v1", models: [model],
                modelProfiles: [{ model, capability: "video", protocol, capabilityConfig }],
            });
            let channel = original;
            for (const catalog of [[{ id: model }], [{ id: model, supportedEndpointTypes: ["chat.completions"] }], [{ id: model, supportedEndpointTypes: ["openai-video"] }]]) {
                channel = { ...channel, modelProfiles: mergeFetchedChannelModelProfiles(channel, catalog) };
                const snapshot = normalizeConfigSnapshot({ config: { ...defaultConfig, channels: [channel] } }).config;
                channel = snapshot.channels[0]!;
                expect(resolveModelRequestConfig(snapshot, `${channelId}::${model}`)).toMatchObject({ model, interfaceType: protocol, baseUrl: original.baseUrl });
                expect(channel.modelProfiles?.[0]?.capabilityConfig?.video?.references.maxImages).toBe(5);
            }
            expect(original.modelProfiles?.[0]?.protocol).toBe(protocol);
        });
    }
}

test("known video IDs honor explicit catalog protocols when no protocol was configured", () => {
    const channel = createModelChannel({ id: "studio", models: [model] });
    expect(mergeFetchedChannelModelProfiles(channel, [{ id: model, supportedEndpointTypes: ["openai-video"] }])[0]).toMatchObject({
        model, capability: "video", protocol: "openai-videos",
    });
});
