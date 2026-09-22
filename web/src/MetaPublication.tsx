import { useEffect, useState } from "react";
import { api } from "./api";
import type { Initialization } from "./types";

type Choice = { id: number; label: string };
export function MetaPublication({ projectId, init, onChange }: {
  projectId: string; init: Initialization; onChange: (value: Initialization) => void;
}) {
  const [open, setOpen] = useState(false);
  const [mode, setMode] = useState("create");
  const [path, setPath] = useState(projectId + "-meta");
  const [choices, setChoices] = useState<Choice[]>([]);
  const [target, setTarget] = useState("");
  const [targetLabel, setTargetLabel] = useState("");
  const [search, setSearch] = useState("");
  const [page, setPage] = useState(1);
  const [hasNext, setHasNext] = useState(false);
  const [busy, setBusy] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [query, setQuery] = useState("");
  const publication = init.publication;
  const base = `/projects/${encodeURIComponent(projectId)}/initialization/publication`;
  useEffect(() => {
    if (!open || publication) return;
    const abort = new AbortController();
    setLoading(true); setError("");
    const suffix = mode === "create" ? "namespaces" : "repositories";
    api<{ items?: {id: number; full_path: string}[]; projects?: {id: number; path_with_namespace: string}[]; hasNext: boolean }>(
      `${base}/${suffix}?search=${encodeURIComponent(query)}&page=${page}`, "GET", undefined, abort.signal,
    ).then((result) => {
      setChoices(mode === "create" ? (result.items || []).map((v) => ({id: v.id, label: v.full_path})) : (result.projects || []).map((v) => ({id: v.id, label: v.path_with_namespace})));
      setHasNext(result.hasNext);
    }).catch((e) => { if (!abort.signal.aborted) setError(e.message); })
      .finally(() => { if (!abort.signal.aborted) setLoading(false); });
    return () => abort.abort();
  }, [open, mode, page, query, base, publication]);
  async function publish() {
    setBusy(true); setError("");
    try {
      onChange(await api<Initialization>(base, "POST", publication ? {} : {
        mode, namespaceId: mode === "create" ? Number(target) : 0,
        path: path.trim(), existingProjectId: mode === "existing" ? Number(target) : 0,
      }));
    } catch (e) { setError((e as Error).message); }
    finally { setBusy(false); }
  }
  return <section className="meta-publication">
    <h3>发布 Meta 仓库到 GitLab（可选）</h3>
    <p>本地初始化已完成。发布仅上传 Git 仓库和说明，不安装依赖或运行项目代码。</p>
    {publication?.status === "published" ? <div className="notice" role="status">
      已发布：<a href={publication.url} target="_blank" rel="noreferrer">{publication.path}</a>
      <p>分支 {publication.ref} · 提交 <code>{publication.sha.slice(0, 12)}</code></p>
      <p>团队可在 GitLab 通过 MR 维护说明；平台不会自动覆盖远程修改。</p>
    </div> : publication?.status === "pending" ? <p role="status">正在发布到 {publication.path}，可离开页面，进度会保存。</p>
    : publication?.status === "failed" ? <>
      <div className="error" role="alert">{publication.error}</div>
      <p>目标：{publication.path}。本地成果已保留，重试会继续使用同一个目标。</p>
      <button type="button" disabled={busy} onClick={() => void publish()}>{busy ? "正在提交…" : "重试发布"}</button>
    </> : !open ? <button type="button" onClick={() => setOpen(true)}>发布到 GitLab</button> : <>
      <label className="field">发布方式<select value={mode} onChange={(e) => { setMode(e.target.value); setTarget(""); setTargetLabel(""); setChoices([]); setPage(1); setSearch(""); setQuery(""); }}>
        <option value="create">创建新的私有仓库</option><option value="existing">关联已有空仓库</option>
      </select></label>
      <form className="wizard-search" onSubmit={(e) => { e.preventDefault(); setQuery(search); setPage(1); }}>
        <input aria-label="搜索发布目标" placeholder={mode === "create" ? "搜索群组或个人空间" : "搜索已有空仓库"} value={search} onChange={(e) => setSearch(e.target.value)} />
        <button type="submit" disabled={loading}>搜索</button>
      </form>
      <label className="field">{mode === "create" ? "目标群组 / 个人空间" : "目标空仓库"}
        <select value={target} disabled={loading} onChange={(e) => {setTarget(e.target.value);setTargetLabel(choices.find((c) => String(c.id) === e.target.value)?.label || "");}}>
          <option value="">{loading ? "正在加载…" : "请选择"}</option>
          {target && !choices.some((v) => String(v.id) === target) && <option value={target}>{targetLabel}</option>}
          {choices.map((v) => <option key={v.id} value={v.id}>{v.label}</option>)}
        </select>
      </label>
      <div className="wizard-pager"><button type="button" disabled={page <= 1 || loading} onClick={() => setPage(page - 1)}>上一页</button><span>第 {page} 页</span><button type="button" disabled={!hasNext || loading} onClick={() => setPage(page + 1)}>下一页</button></div>
      {mode === "create" && <label className="field">仓库名<input value={path} onChange={(e) => setPath(e.target.value)} maxLength={80} pattern="[a-zA-Z0-9][a-zA-Z0-9_-]*" /></label>}
      <p>{mode === "create" ? `将创建私有仓库：${targetLabel || "请选择空间"}/${path}` : `将向 ${targetLabel || "请选择空仓库"} 发布；非空仓库会被拒绝。`}</p>
      <p>需要 Token 具备 API、仓库写入权限；新建仓库还需要目标空间的创建权限。</p>
      <div className="actions wizard-actions"><button type="button" onClick={() => setOpen(false)}>暂不发布</button><button type="button" className="primary" disabled={busy || loading || !target || (mode === "create" && !/^[a-zA-Z0-9][a-zA-Z0-9_-]{0,79}$/.test(path.trim()))} onClick={() => void publish()}>{busy ? "正在提交…" : "确认发布到 GitLab"}</button></div>
    </>}
    {error && <div className="error" role="alert">{error}</div>}
  </section>;
}
