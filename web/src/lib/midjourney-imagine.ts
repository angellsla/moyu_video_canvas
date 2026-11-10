export type MidjourneyImagineParams = {
    stylize?: number;
    chaos?: number;
    weird?: number;
    exp?: number;
    no?: string;
    seed?: number;
    raw?: boolean;
    tile?: boolean;
    repeat?: number;
    stop?: number;
    version?: string;
    niji?: string;
    sdHd?: "sd" | "hd";
    quality?: "1" | "2" | "4";
    draft?: boolean;
    speedMode?: "fast" | "relax" | "turbo";
    imageWeight?: number;
    sref?: string;
    sw?: number;
    sv?: string;
    profile?: string;
    oref?: string;
    ow?: number;
    cref?: string;
    cw?: number;
};

export function isMidjourneyImagineModel(model: string | undefined) {
    return (model || "").trim().split("::").at(-1)?.toLowerCase() === "mj_imagine";
}

export function midjourneyParamsForRequest(params: MidjourneyImagineParams = {}): MidjourneyImagineParams {
    const next = { ...params };
    const version = next.version || "";
    const isV8 = version.startsWith("8.");
    if (next.niji) delete next.version;
    if (isV8) {
        delete next.quality;
        delete next.draft;
        delete next.oref;
        delete next.ow;
        delete next.cref;
        delete next.cw;
        if (next.speedMode === "turbo") delete next.speedMode;
    } else {
        delete next.sdHd;
    }
    if (version && version !== "6") {
        delete next.cref;
        delete next.cw;
    }
    if (version && version !== "7") {
        delete next.oref;
        delete next.ow;
        delete next.draft;
    }
    if (next.niji || (version && version !== "6" && version !== "7")) delete next.quality;
    if (next.profile?.trim()) {
        delete next.sw;
        delete next.sv;
    }
    return next;
}

export function summarizeMidjourneyParams(params: MidjourneyImagineParams = {}) {
    const value = midjourneyParamsForRequest(params);
    const selected = [
        value.niji ? `Niji ${value.niji}` : value.version ? `V${value.version}` : "",
        value.stylize !== undefined ? `--s ${value.stylize}` : "",
        value.chaos !== undefined ? `--c ${value.chaos}` : "",
        value.sdHd?.toUpperCase() || "",
        value.quality ? `--q ${value.quality}` : "",
    ].filter(Boolean);
    return selected.slice(0, 2).join(" · ") || "自定义参数";
}
