export const capabilityOptions = [{ value: "moyu.canvas", label: "墨羽Canvas 画布工具" }, { value: "beeftv.canvas", label: "墨羽Canvas 画布工具（旧版标识）" }, { value: "model.image", label: "图片理解模型" }, { value: "model.video", label: "视频理解模型" }, { value: "model.audio", label: "音频理解模型" }];
export function requirementLabel(value: string) {
    return capabilityOptions.find(item => item.value === value)?.label || `未确认的能力：${value}`;
}
export function unsupportedRequirements(values: string[]) {
    return values.filter(value => !capabilityOptions.some(option => option.value === value));
}
