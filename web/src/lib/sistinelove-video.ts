import contract from "../../../backend/internal/providerpreset/sistinelove_video.json";

const resolutionAliases: Record<string, string> = contract.resolutionAliases;

export function sistineloveSeedanceResolution(model: string): string {
    const match = new RegExp(contract.modelResolutionPattern, "i").exec(model.trim());
    return (match && resolutionAliases[match[1]!.toLowerCase().replace(/p$/, "")]) || contract.defaultResolution;
}

export function sistineloveSeedanceProtocol(baseUrl: string, model: string): string | undefined {
    try {
        const url = new URL(baseUrl);
        if (url.origin !== contract.generationOrigin || url.username || url.password) return undefined;
        return new RegExp(contract.modelPattern).test(model.trim().toLowerCase()) ? contract.protocol : undefined;
    } catch {
        return undefined;
    }
}
