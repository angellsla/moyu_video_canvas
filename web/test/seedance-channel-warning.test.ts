import { expect, test } from "bun:test";
import { seedanceReferenceRatioWarning } from "../src/lib/seedance-channel-warning";
import { seedanceSettingsConstraints } from "../src/lib/seedance-task-constraints";
import { explainGenerationError } from "../src/lib/generation-error";

const request = { model: "seedance-2.5", videoCount: 1, ratio: "16:9", operation: "reference_to_video" };
test("Seedance reference-video requests require informed consent only for affected models", () => {
    expect(seedanceReferenceRatioWarning(request)?.content).toContain("不会自动退款");
    expect(seedanceReferenceRatioWarning({ ...request, model: "seedance-2.0" })?.okText).toBe("接受风险并生成");
    for (const patch of [{ model: "seedance-2.0-fast" }, { model: "seedance-2.0-mini" }, { videoCount: 0 }, { ratio: "adaptive" }, { operation: "extend" }, { operation: "inpaint" }]) {
        expect(seedanceReferenceRatioWarning({ ...request, ...patch })).toBeUndefined();
    }
});

test("mode-specific controls keep free ratio in reference mode and unlock on model change", () => {
    const constrained = { taskTypeConstraints: true };
    const unconstrained = { taskTypeConstraints: false };
    expect(seedanceSettingsConstraints(constrained, 1, 0, 0).ratioLabel).toBe("随首帧");
    expect(seedanceSettingsConstraints(constrained, 1, 1, 1, "reference_to_video").ratioLabel).toBeUndefined();
    expect(seedanceSettingsConstraints(constrained, 1, 0, 0, "reference_to_video").ratioLabel).toBeUndefined();
    expect(seedanceSettingsConstraints(unconstrained, 1, 0, 0).ratioLabel).toBeUndefined();
    expect(seedanceSettingsConstraints(constrained, 0, 1, 0, "inpaint")).toEqual({ ratioLabel: "随原视频", durationLocked: true });
});

test("specific frame errors survive task-log serialization", () => {
    for (const reason of ["无法读取首帧图片尺寸", "首帧图片比例超出当前模型支持范围", "首帧图片比例与当前设置不一致"]) {
        const first = explainGenerationError({ code: "invalid_params", message: `${reason}。请更换首帧图片或调整画面比例后重试。` });
        expect(first.category).toBe("invalid_params");
        expect(first.reason).toBe(reason);
        expect(first.message).not.toContain("检查模型、尺寸");
    }
});
