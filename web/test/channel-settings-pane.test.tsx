import { expect, test } from "bun:test";
import { newAPITextModelAfterSync, withChannels } from "../src/pages/settings/channel-settings-pane";
import { createModelChannel, defaultConfig, normalizeConfigSnapshot } from "../src/stores/use-config-store";

test("removing a channel clears its legacy and assistant selections", () => {
    const removed = createModelChannel({ id: "removed", apiKey: "dummy", models: ["chat"], modelProfiles: [{ model: "chat", capability: "text", protocol: "chat-completion" }] });
    const kept = createModelChannel({ id: "kept", apiKey: "dummy", models: ["other"], modelProfiles: [{ model: "other", capability: "text", protocol: "chat-completion" }] });
    const next = withChannels({ ...defaultConfig, channels: [removed, kept], model: "removed::chat", textModel: "removed::chat", assistantModel: "removed::chat" }, [kept]);
    expect(next.model).toBe("kept::other");
    expect(next.textModel).toBe("kept::other");
    expect(next.assistantModel).toBe("");
});

test("New API login selects gpt-6.1-sol as the default text model when it is available", () => {
    const models = ["gpt-6.1-sol", "gpt-image-2"];
    const channel = createModelChannel({
        id: "newapi",
        name: "墨羽Canvas API",
        baseUrl: "https://video.sistinelove.cn/v1",
        apiKey: "test-key",
        models,
        modelProfiles: [
            { model: "gpt-6.1-sol", capability: "text", protocol: "chat-completion" },
            { model: "gpt-image-2", capability: "image", protocol: "openai-image" },
        ],
    });
    const selected = newAPITextModelAfterSync(models, "other::text-model");
    const normalized = normalizeConfigSnapshot({ config: { ...defaultConfig, channels: [channel], textModel: selected } }).config;

    expect(selected).toBe("newapi::gpt-6.1-sol");
    expect(normalized.textModel).toBe("newapi::gpt-6.1-sol");
    expect(normalized.textModels).toContain("newapi::gpt-6.1-sol");
});

test("New API login preserves the current text model when gpt-6.1-sol is absent", () => {
    expect(newAPITextModelAfterSync(["gpt-5.6-sol"], "other::existing-model")).toBe("other::existing-model");
});
