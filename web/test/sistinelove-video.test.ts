import { expect, test } from "bun:test";
import { sistineloveSeedanceProtocol } from "../src/lib/sistinelove-video";
import { mergeFetchedChannelModelProfiles } from "../src/lib/channel-model-catalog";
import { createModelChannel, defaultConfig, normalizeConfigSnapshot, resolveModelRequestConfig } from "../src/stores/use-config-store";

const protocol = "sistinelove-seedance-video";
test("Seedance gateway uses documented JSON on refresh, reload and submission", () => {
    for (const model of ["seedance-2.0-gz", "tr_seedance-2.0", "seedance2.5-gz-1080", "SD-2.5-720p-15s"]) {
        for (const id of ["newapi", "custom-gateway"]) {
            let channel = createModelChannel({ id, baseUrl: "https://video.sistinelove.cn/v1", models: [model], modelProfiles: [{ model, capability: "video", protocol: "newapi" }] });
            expect(resolveModelRequestConfig({ ...defaultConfig, channels: [channel] }, `${id}::${model}`).interfaceType).toBe(protocol);
            for (let i = 0; i < 2; i++) {
                channel = { ...channel, modelProfiles: mergeFetchedChannelModelProfiles(channel, [{ id: model }]) };
                const config = normalizeConfigSnapshot({ config: { ...defaultConfig, channels: [channel] } }).config;
                channel = config.channels[0]!;
                expect(channel.modelProfiles![0]!.protocol).toBe(protocol);
                expect(resolveModelRequestConfig(config, `${id}::${model}`)).toMatchObject({ model, interfaceType: protocol, baseUrl: channel.baseUrl });
            }
        }
    }
});

test("other providers and non Seedance models keep their protocols", () => {
    for (const baseUrl of ["https://ark.cn-beijing.volces.com/api/v3", "https://video.sistinelove.cn.evil.example", "http://video.sistinelove.cn", "https://video.sistinelove.cn:444", "https://user@video.sistinelove.cn"]) {
        expect(sistineloveSeedanceProtocol(baseUrl, "seedance-2.0-gz")).toBeUndefined();
    }
    expect(sistineloveSeedanceProtocol("https://video.sistinelove.cn/v1", "sora-2")).toBeUndefined();
    expect(sistineloveSeedanceProtocol("https://video.sistinelove.cn:443/v1", "seedance-2.0-gz")).toBe(protocol);
});
