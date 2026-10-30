import { App, Button, Input, Skeleton, Tag } from "antd";
import { useCallback, useEffect, useState } from "react";
import { PageHeader, PaginationBar, WorkspacePage } from "@/components/layout/workspace-page";
import { WorkspaceErrorState, WorkspaceState } from "@/components/layout/workspace-state";
import { AppModal } from "@/components/ui/product/app-modal";
import { addSkill, deleteSkill, getSkill, getSkillVersions, listSkills, removeSkill, rollbackSkillVersion, type Skill, type SkillVersion } from "@/services/api/skills";
import { SkillDetailModal } from "./skill-detail-drawer";
import { SkillEditorDrawer } from "./skill-editor-drawer";
import { SkillInstallModal } from "./skill-install-modal";
import "./marketplace.css";

export default function LocalSkillsPage() {
    const { message, modal } = App.useApp();
    const [skills, setSkills] = useState<Skill[]>([]);
    const [search, setSearch] = useState("");
    const [page, setPage] = useState(1);
    const [total, setTotal] = useState(0);
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState("");
    const [revision, setRevision] = useState(0);
    const [busy, setBusy] = useState("");
    const [detail, setDetail] = useState<Skill | null>(null);
    const [edit, setEdit] = useState<Skill | null>(null);
    const [editorOpen, setEditorOpen] = useState(false);
    const [installOpen, setInstallOpen] = useState(false);
    const [versions, setVersions] = useState<{ skill: Skill; items: SkillVersion[] } | null>(null);
    const [versionId, setVersionId] = useState("");
    const reload = useCallback(() => setRevision((value) => value + 1), []);

    useEffect(() => {
        let stale = false;
        setLoading(true);
        setError("");
        const timer = globalThis.setTimeout(() => {
            void listSkills({ scope: "mine", page, pageSize: 20, search: search.trim() || undefined, sort: "updated" })
                .then((value) => { if (!stale) { setSkills(value.skills); setTotal(value.totalCount); } })
                .catch((cause) => { if (!stale) setError(cause instanceof Error ? cause.message : "本机技能读取失败"); })
                .finally(() => { if (!stale) setLoading(false); });
        }, 180);
        return () => { stale = true; globalThis.clearTimeout(timer); };
    }, [page, revision, search]);

    const action = async (skillId: string, work: () => Promise<unknown>, success: string): Promise<boolean> => {
        setBusy(skillId);
        try { await work(); message.success(success); reload(); return true; }
        catch (cause) { message.error(cause instanceof Error ? cause.message : "操作未完成"); return false; }
        finally { setBusy(""); }
    };
    const openLocal = async (skill: Skill) => action(skill.skillId, async () => { setDetail((await getSkill(skill.skillId)).skill); }, "已读取本机技能文件");
    const openEditor = (skill?: Skill) => { setDetail(null); setEdit(skill || null); setEditorOpen(true); };
    const showVersions = async (skill: Skill) => action(skill.skillId, async () => {
        const value = await getSkillVersions(skill.skillId);
        setVersions({ skill, items: value.versions });
        setVersionId(skill.versionId);
    }, "已读取本机版本历史");
    const restoreVersion = () => {
        if (!versions || !versionId || versionId === versions.skill.versionId) return;
        const current = versions;
        modal.confirm({
            title: "恢复本机历史版本？",
            content: "恢复只切换本机技能的当前版本；历史版本和已有对话中固定的版本都会保留。",
            okText: "恢复此版本",
            cancelText: "取消",
            onOk: async () => {
                if (await action(current.skill.skillId, () => rollbackSkillVersion(current.skill.skillId, versionId), "已恢复本机版本")) setVersions(null);
            },
        });
    };
    const deleteLocal = (skill: Skill) => modal.confirm({
        title: `删除本机的“${skill.skillName}”？`,
        content: "删除本机技能时，其本机文件和版本历史也会删除。此操作仅在你确认后执行。",
        okText: "删除本机技能",
        cancelText: "取消",
        okButtonProps: { danger: true },
        onOk: async () => { await deleteSkill(skill.skillId); setDetail(null); reload(); },
    });

    return <WorkspacePage className="skill-market-page">
        <PageHeader title="本机技能" description="技能文件、导入内容和版本历史保存在本机工作区。" actions={<><Button onClick={() => openEditor()}>创建技能</Button><Button type="primary" onClick={() => setInstallOpen(true)}>导入文件</Button></>} />
        <div className="skill-market-toolbar"><Input.Search aria-label="搜索本机技能" placeholder="搜索名称或用途" allowClear value={search} onChange={(event) => { setSearch(event.target.value); setPage(1); }} /><Button onClick={reload}>刷新</Button></div>
        {error ? <WorkspaceErrorState compact description={error} onRetry={reload} /> : loading && !skills.length ? <Skeleton active paragraph={{ rows: 6 }} /> : skills.length ? <>
            {skills.map((skill) => <article className="skill-market-row" key={skill.skillId}>
                <div className="skill-market-copy">
                    <h2>{skill.skillName} <span>{skill.version || "1"}</span> {!skill.isAdded ? <Tag>未启用</Tag> : null}</h2>
                    <p>{skill.description}</p>
                    <div className="skill-market-meta"><span>本机存档</span><span>{skill.fileCount} 个文件</span><span>更新于 {skill.updatedAt.slice(0, 10)}</span></div>
                    <div className="skill-market-actions">
                        <Button size="small" onClick={() => void openLocal(skill)}>查看文件</Button>
                        <Button size="small" disabled={Boolean(busy)} onClick={() => void action(skill.skillId, () => skill.isAdded ? removeSkill(skill.skillId) : addSkill(skill.skillId), skill.isAdded ? "已从助手技能列表停用" : "已加入助手技能列表")}>{skill.isAdded ? "停用" : "启用"}</Button>
                        {skill.isOwner ? <><Button size="small" onClick={() => void showVersions(skill)}>本机版本历史</Button><Button size="small" onClick={() => void action(skill.skillId, async () => openEditor((await getSkill(skill.skillId)).skill), "已读取本机技能")}>编辑</Button><Button size="small" danger onClick={() => deleteLocal(skill)}>删除</Button></> : null}
                    </div>
                </div>
            </article>)}
            <div className="skill-market-actions"><span>共 {total} 项</span></div>
            <PaginationBar current={page} pageSize={20} total={total} pageSizeOptions={[20]} onChange={(nextPage) => setPage(nextPage)} />
            <p className="skill-market-help">在创作助手中选择已启用的技能；每段对话会固定所选版本。本机版本历史可单独恢复，不影响已有对话。</p>
        </> : <WorkspaceState compact className="min-h-[188px]" icon="skills" title={search ? "没有找到匹配的本机技能" : "还没有本机技能"} description={search ? "换个关键词试试。" : "导入一个 SKILL.md 或 ZIP 技能包，也可以创建单文件技能。"} action={search ? <Button onClick={() => setSearch("")}>清除搜索</Button> : <Button type="primary" onClick={() => setInstallOpen(true)}>导入技能</Button>} />}
        <SkillDetailModal fileOnly skill={detail} loading={false} mutating={Boolean(detail && busy === detail.skillId)} categories={[]} onClose={() => setDetail(null)} onEdit={openEditor} onAdd={() => {}} onLike={() => {}} onSync={() => {}} />
        <SkillEditorDrawer open={editorOpen} skill={edit} onClose={() => setEditorOpen(false)} onSaved={() => { setEditorOpen(false); setEdit(null); reload(); }} />
        <SkillInstallModal open={installOpen} onClose={() => setInstallOpen(false)} onManualCreate={() => { setInstallOpen(false); openEditor(); }} onInstalled={() => { setInstallOpen(false); reload(); }} />
        <AppModal open={Boolean(versions)} title="恢复本机历史版本" onCancel={() => setVersions(null)} footer={<><Button onClick={() => setVersions(null)}>取消</Button><Button type="primary" disabled={!versions || versionId === versions.skill.versionId} loading={Boolean(versions && busy === versions.skill.skillId)} onClick={restoreVersion}>恢复此版本</Button></>}>
            <p>{versions?.skill.skillName} · 选择本机保存的版本。</p>
            <div className="skill-history-list">{versions?.items.map((version) => <label key={version.id} className="skill-history-option"><input type="radio" name="skill-version" value={version.id} checked={versionId === version.id} onChange={() => setVersionId(version.id)} /><span>{version.versionLabel} · {version.createdAt.slice(0, 10)}{version.id === versions.skill.versionId ? "（当前）" : ""}</span></label>)}</div>
        </AppModal>
    </WorkspacePage>;
}
