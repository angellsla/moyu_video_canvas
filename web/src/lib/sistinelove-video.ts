import contract from "../../../backend/internal/providerpreset/sistinelove_video.json";

export function sistineloveSeedanceProtocol(baseUrl: string, model: string): string | undefined {
    try {
        const url = new URL(baseUrl);
        if (url.origin !== contract.generationOrigin || url.username || url.password) return undefined;
        return new RegExp(contract.modelPattern).test(model.trim().toLowerCase()) ? contract.protocol : undefined;
    } catch {
        return undefined;
    }
}
