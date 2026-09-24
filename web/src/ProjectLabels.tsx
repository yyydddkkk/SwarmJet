import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "./api";
import { ErrorBox } from "./ui";

type LabelStatus = { name: string; color: string; existingColor?: string; exists: boolean; archived?: boolean };
type RepositoryStatus = { repositoryId: string; projectId: number; labels: LabelStatus[]; error?: string };
type LabelReport = { repositories: RepositoryStatus[] };

export function ProjectLabels({ projectId }: { projectId: string }) {
  const [report, setReport] = useState<LabelReport | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const request = useRef(0);
  const path = `/projects/${encodeURIComponent(projectId)}/labels`;
  const refresh = useCallback(async () => {
    const current = ++request.current;
    try {
      const result = await api<LabelReport>(path);
      if (current === request.current) { setReport(result); setError(""); }
    } catch (e) {
      if (current === request.current) setError((e as Error).message);
    }
  }, [path]);
  useEffect(() => { void refresh(); }, [refresh]);
  const sync = async () => {
    const current = ++request.current;
    setBusy(true);
    try {
      const result = await api<LabelReport>(path, "POST");
      if (current === request.current) { setReport(result); setError(""); }
    } catch (e) {
      if (current === request.current) setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };
  return <section className="panel panel-padding project-labels" aria-label="GitLab 标签管理">
    <div className="row-between">
      <div><h2>GitLab 标签</h2><p>为已登记仓库补齐接单和执行器标签。已有标签的颜色保持原样。</p></div>
      <button type="button" onClick={sync} disabled={busy}>{busy ? "正在补齐…" : "检查并补齐"}</button>
    </div>
    <ErrorBox error={error} retry={refresh} />
    {!report && !error && <p>正在检查仓库标签…</p>}
    {report?.repositories.map((repo) => <div className="project-label-repo" key={repo.projectId}>
      <strong>{repo.repositoryId}</strong>
      {repo.error ? <span role="alert">同步失败：{repo.error}</span> : <span>{repo.labels.some((label) => label.archived) ? "存在已归档标签，请在 GitLab 取消归档" : repo.labels.some((label) => !label.exists) ? "存在待补齐标签" : "标签已就绪"}</span>}
      <div className="project-label-list">{repo.labels.map((label) => <span className={label.exists && !label.archived ? "" : "missing"} key={label.name}>
        <i style={{ background: label.existingColor || label.color }} />{label.name}{label.archived ? " · 已归档" : !label.exists && " · 待创建"}
      </span>)}</div>
    </div>)}
  </section>;
}
