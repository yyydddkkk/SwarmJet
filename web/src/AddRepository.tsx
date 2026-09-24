import { useEffect, useRef, useState } from "react";
import { api } from "./api";
import type { Project } from "./types";
import { ErrorBox, Field } from "./ui";

type GitLabRepository = {
  id: number;
  name: string;
  pathWithNamespace: string;
  defaultBranch: string;
  httpUrlToRepo: string;
  archived: boolean;
};
type Page = { projects: GitLabRepository[]; hasNext: boolean };
type Change = {
  id: string;
  repository: { id: string; path: string };
  status: "pending" | "waiting" | "applied" | "closed" | "blocked";
  mergeRequestUrl?: string;
  error?: string;
  docsStale?: boolean;
};

export function AddRepository({ project, onApplied }: { project: Project; onApplied: (project: Project) => void }) {
  const [search, setSearch] = useState("");
  const [open, setOpen] = useState(false);
  const [page, setPage] = useState(1);
  const [results, setResults] = useState<GitLabRepository[]>([]);
  const [hasNext, setHasNext] = useState(false);
  const [loading, setLoading] = useState(false);
  const [chosen, setChosen] = useState<GitLabRepository | null>(null);
  const [alias, setAlias] = useState("");
  const [path, setPath] = useState("");
  const [targetBranch, setTargetBranch] = useState("");
  const [createFrom, setCreateFrom] = useState("");
  const [dependsOn, setDependsOn] = useState<string[]>([]);
  const [changes, setChanges] = useState<Change[]>([]);
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  const applied = useRef(new Set<string>());
  const endpoint = `/projects/${encodeURIComponent(project.id)}/repository-changes`;

  useEffect(() => {
    if (!open) return;
    let live = true;
    const timer = window.setTimeout(async () => {
      setLoading(true);
      try {
        const result = await api<Page>(`${endpoint}?available=true&search=${encodeURIComponent(search)}&page=${page}`);
        if (live) {
          setResults(result.projects || []);
          setHasNext(result.hasNext);
        }
      } catch (e) {
        if (live) setError((e as Error).message);
      } finally {
        if (live) setLoading(false);
      }
    }, 250);
    return () => { live = false; window.clearTimeout(timer); };
  }, [endpoint, search, page, open]);

  useEffect(() => {
    let live = true;
    async function refresh() {
      try {
        const list = await api<Change[]>(endpoint);
        if (!live) return;
        setChanges(list || []);
        const newlyApplied = (list || []).find((change) => change.status === "applied" && !applied.current.has(change.id));
        if (newlyApplied) {
          applied.current.add(newlyApplied.id);
          onApplied(await api<Project>(`/projects/${encodeURIComponent(project.id)}`));
        }
      } catch (e) {
        if (live) setError((e as Error).message);
      }
    }
    void refresh();
    const timer = window.setInterval(() => void refresh(), 5000);
    return () => { live = false; window.clearInterval(timer); };
  }, [endpoint, project.id]);

  const latest = changes[0];
  const active = latest && (latest.status === "pending" || latest.status === "waiting") ? latest : undefined;
  const blocked = latest?.status === "blocked" ? latest : undefined;
  const available = results.filter((repo) => !repo.archived && !(project.repositories || []).some((current) => current.projectId === repo.id));
  async function submit() {
    if (!chosen) return;
    setSaving(true);
    setError("");
    try {
      const change = await api<Change>(endpoint, "POST", {
        gitlabProjectId: chosen.id,
        alias: alias.trim(), path: path.trim(), targetBranch: targetBranch.trim(),
        createFrom: createFrom.trim(), dependsOn,
      });
      setChanges((old) => [change, ...old]);
      setChosen(null);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSaving(false);
    }
  }
  return <div className="add-repository" onKeyDown={(e) => { if (e.key === "Enter" && e.target instanceof HTMLInputElement) e.preventDefault(); }}>
    <h3>增加子仓库</h3>
    <p className="muted">选择 GitLab 仓库后，系统更新 Meta 清单。远端 Meta 会创建 MR；合并并校验后自动加入项目、开始按现有规则接单。远端变更需要连接 Token 具备 api 与 Meta 写入权限。</p>
    <ErrorBox error={error} />
    {active && <div className="notice" role="status">
      {active.status === "waiting" ? "等待 Meta 变更 MR 合并" : "正在准备 Meta 清单变更"}：{active.repository.id}
      {active.mergeRequestUrl && <> · <a href={active.mergeRequestUrl} target="_blank" rel="noreferrer">查看 MR</a></>}
      {active.error && <p>{active.error}</p>}
    </div>}
    {blocked && !active && <div className="setup-warning" role="status">新增 {blocked.repository.id} 暂时无法完成：{blocked.error}
      <button type="button" disabled={saving} onClick={async () => {
        setSaving(true); setError("");
        try { const retried = await api<Change>(`${endpoint}/${encodeURIComponent(blocked.id)}/retry`, "POST"); setChanges((old) => old.map((item) => item.id === retried.id ? retried : item)); }
        catch (e) { setError((e as Error).message); }
        finally { setSaving(false); }
      }}>重试此请求</button>
    </div>}
    {latest?.status === "closed" && <div className="setup-warning" role="status">{latest.error || "Meta 变更 MR 已关闭，仓库未加入项目"}</div>}
    {!active && <details className="setup-advanced" onToggle={(e) => setOpen(e.currentTarget.open)}>
      <summary>＋ 选择要加入的仓库</summary>
      <Field label="搜索 GitLab 仓库"><input value={search} onChange={(e) => { setSearch(e.target.value); setPage(1); }} placeholder="仓库名称" /></Field>
      <div className="repository-search-results">
        {loading && <p className="muted">正在读取…</p>}
        {available.map((repo) => <button type="button" key={repo.id} onClick={() => {
          const name = repo.pathWithNamespace.split("/").pop() || repo.name;
          setChosen(repo); setAlias(name); setPath(name); setTargetBranch(repo.defaultBranch); setCreateFrom(""); setDependsOn([]);
        }}>{repo.pathWithNamespace}</button>)}
        {page > 1 && <button type="button" onClick={() => setPage(page - 1)}>上一页</button>}
        {hasNext && <button type="button" onClick={() => setPage(page + 1)}>下一页</button>}
      </div>
      {chosen && <div className="form-grid">
        <strong>新增 {chosen.pathWithNamespace}</strong>
        <Field label="仓库标识"><input value={alias} onChange={(e) => setAlias(e.target.value)} /></Field>
        <Field label="Meta 中的路径"><input value={path} onChange={(e) => setPath(e.target.value)} /></Field>
        <Field label="目标分支"><input value={targetBranch} onChange={(e) => setTargetBranch(e.target.value)} /></Field>
        <Field label="创建起点（目标分支不存在时填写）"><input value={createFrom} onChange={(e) => setCreateFrom(e.target.value)} placeholder="现有分支" /></Field>
        <Field label="依赖仓库">
          <div>{(project.repositories || []).map((repo) => <label className="checkbox" key={repo.id}><input type="checkbox" checked={dependsOn.includes(repo.id)} onChange={(e) => setDependsOn((old) => e.target.checked ? [...old, repo.id] : old.filter((id) => id !== repo.id))} />{repo.id}</label>)}</div>
        </Field>
        <p className="muted">将登记 {alias || "仓库标识"} 到 {path || "Meta 路径"}，目标分支为 {targetBranch || "待填写"}。已有任务不受影响。</p>
        <button type="button" className="primary" disabled={saving || !alias.trim() || !path.trim() || !targetBranch.trim()} onClick={() => void submit()}>{saving ? "正在提交…" : "提交新增请求"}</button>
      </div>}
    </details>}
    {latest?.status === "applied" && latest.docsStale && <p className="muted">新增仓库已接单；现有项目说明可能需要增量复核。</p>}
  </div>;
}
