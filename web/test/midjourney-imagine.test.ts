import { describe, expect, test } from "bun:test";

import { isMidjourneyImagineModel, midjourneyParamsForRequest, summarizeMidjourneyParams } from "../src/lib/midjourney-imagine.ts";
import { backendProviderConfig } from "../src/services/api/generation-task";
import { createModelChannel, defaultConfig } from "../src/stores/use-config-store";

describe("Midjourney Imagine parameters", () => {
    test("recognizes channel-qualified model IDs", () => {
        expect(isMidjourneyImagineModel("mj_imagine")).toBe(true);
        expect(isMidjourneyImagineModel("channel::mj_imagine")).toBe(true);
        expect(isMidjourneyImagineModel("gpt-image-2")).toBe(false);
    });

    test("keeps only options compatible with the selected version", () => {
        expect(midjourneyParamsForRequest({ version: "8.2", quality: "2", draft: true, oref: "https://assets.example/object.png", sdHd: "hd", speedMode: "turbo" })).toEqual({ version: "8.2", sdHd: "hd" });
        expect(summarizeMidjourneyParams({ niji: "7", version: "6", quality: "2" })).toBe("Niji 7");
    });

    test("passes dedicated parameters through the existing task config", () => {
        const params = { version: "7", stylize: 250, raw: true, sref: "https://assets.example/style.png" };
        const channel = createModelChannel({ id: "mj", name: "MJ", baseUrl: "https://mj.example/v1", apiFormat: "openai", interfaceType: "openai-image", models: ["mj_imagine"] });
        const config = { ...defaultConfig, channels: [channel], model: "mj::mj_imagine", imageModel: "mj::mj_imagine", midjourneyParams: params };
        expect(backendProviderConfig(config, "image").midjourneyParams).toEqual(params);
    });
});
