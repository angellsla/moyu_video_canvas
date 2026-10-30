import { expect, test } from "bun:test";
import { withChannels } from "../src/pages/settings/channel-settings-pane";
import { createModelChannel, defaultConfig } from "../src/stores/use-config-store";

test("removing a channel clears its legacy and assistant selections", () => {
    const removed = createModelChannel({ id: "removed", apiKey: "dummy", models: ["chat"], modelProfiles: [{ model: "chat", capability: "text", protocol: "chat-completion" }] });
    const kept = createModelChannel({ id: "kept", apiKey: "dummy", models: ["other"], modelProfiles: [{ model: "other", capability: "text", protocol: "chat-completion" }] });
    const next = withChannels({ ...defaultConfig, channels: [removed, kept], model: "removed::chat", textModel: "removed::chat", assistantModel: "removed::chat" }, [kept]);
    expect(next.model).toBe("kept::other");
    expect(next.textModel).toBe("kept::other");
    expect(next.assistantModel).toBe("");
});
