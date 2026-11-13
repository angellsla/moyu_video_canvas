import { expect, test } from "bun:test";
import { sistineloveSeedanceProtocol, sistineloveSeedanceResolution } from "../src/lib/sistinelove-video";
import { mergeFetchedChannelModelProfiles } from "../src/lib/channel-model-catalog";
import { defaultModelCapabilityConfig, modelCapabilityConfigFor, normalizeVideoValue } from "../src/lib/model-capabilities";
import { createModelChannel, defaultConfig, normalizeConfigSnapshot, resolveModelRequestConfig } from "../src/stores/use-config-store";

const protocol = "sistinelove-seedance-video";

test("Seedance gateway identifies resolution tokens without confusing versions, dates or durations", () => {
    for (const [model, resolution] of [
        ["seedance-2.0-gz", "720p"],
        ["tr_seedance-2.0", "720p"],
        ["seedance-2.5-20261080-15s", "720p"],
        ["seedance-2.0-1080s", "720p"],
        ["seedance-2.0-10800", "720p"],
        ["seedance-2.0-480", "480p"],
        ["SD-2.5-720p-15s", "720p"],
        ["seedance2.5-gz-1080", "1080p"],
        ["SEEDANCE-2.0_[1080P]", "1080p"],
        ["seedance-2.0-1440p", "1440p"],
        ["seedance-2.0-2160", "2160p"],
        ["seedance-2.0-2K", "1440p"],
        ["seedance-2.0-4k", "2160p"],
    ] as const) {
        expect(sistineloveSeedanceResolution(model)).toBe(resolution);
    }
});

test("Seedance gateway defaults and old saved selections use the model resolution", () => {
    for (const [model, resolution] of [
        ["seedance-2.0-gz", "720p"],
        ["seedance-2.5-gz", "720p"],
        ["seedance-2.0-480p", "480p"],
        ["seedance2.5-gz-1080", "1080p"],
    ] as const) {
        const initial = defaultModelCapabilityConfig(protocol, model).video!;
        expect(initial.resolutions).toEqual([resolution]);
        expect(initial.defaultResolution).toBe(resolution);
        const stale = defaultModelCapabilityConfig(protocol, model);
        stale.video!.resolutions = ["480p", "720p", "1080p"];
        stale.video!.defaultResolution = "1080p";
        stale.video!.references.maxImages = 7;
        const channel = createModelChannel({ id: "newapi", baseUrl: "https://video.sistinelove.cn/v1", models: [model], modelProfiles: [{ model, capability: "video", protocol, capabilityConfig: stale }] });
        const config = normalizeConfigSnapshot({ config: { ...defaultConfig, channels: [channel] } }).config;
        const video = modelCapabilityConfigFor(config, `newapi::${model}`).video!;
        expect(video.resolutions).toEqual([resolution]);
        expect(video.defaultResolution).toBe(resolution);
        expect(video.references.maxImages).toBe(7);
        expect(normalizeVideoValue(video, { seconds: "5", ratio: "16:9", resolution: "1080" }).resolution).toBe(resolution);
        expect(stale.video!.defaultResolution).toBe("1080p");
    }
});
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
