import { http } from "@/services/api/request";
import { invalidateAddedSkillsCache, type Skill, type SkillPackageFile } from "./skills";

export type SkillMetadata = { skillId?: string; name: string; description: string; versionLabel: string; license: string; sourceUrl: string; requirements: string[]; requirementNotes?: string; minClientVersion?: string; showcase: Array<{ url: string; type: string; caption: string }> };
export type MarketRelease = SkillMetadata & { skillId: string; releaseId: string; author: string; archiveBytes: number; contentHash: string; archiveSha256: string; files: SkillPackageFile[]; publishedAt: string };
export type MarketItem = SkillMetadata & { skillId: string; releaseId: string; author: string; compatibility: { status: string; reasons: string[] } };
export type InstalledVersion = { id: string; versionLabel: string; contentHash: string; createdAt: string };
const root = "/skills/market";
const id = encodeURIComponent;
const changed = <T>(promise: Promise<T>) => promise.finally(invalidateAddedSkillsCache);

export const searchSkillMarket = (q: string, page = 1) => http.get<{ items: MarketItem[]; total: number }>(`${root}/search`, { params: { q, page, pageSize: 20 } });
export const getMarketRelease = (skillId: string, releaseId: string) => http.get<MarketRelease>(`${root}/releases/${id(skillId)}/${id(releaseId)}`);
export const getMarketFile = (skillId: string, releaseId: string, path: string, offset = 0) => http.get<{ content: string; encoding: string; eof: boolean; nextOffset: number }>(`${root}/releases/${id(skillId)}/${id(releaseId)}/file`, { params: { path, offset } });
export const installMarketRelease = (skillId: string, releaseId: string) => changed(http.post<{ skill: Skill }>(`${root}/install`, { skillId, releaseId }));
export const getInstalledVersions = (skillId: string) => http.get<{ versions: InstalledVersion[] }>(`${root}/versions/${id(skillId)}`);
export const rollbackSkill = (skillId: string, versionId: string) => changed(http.post<{ skill: Skill }>(`${root}/rollback`, { skillId, versionId }));
export const forkSkill = (skillId: string) => changed(http.post<{ skill: Skill }>(`${root}/fork`, { skillId }));
