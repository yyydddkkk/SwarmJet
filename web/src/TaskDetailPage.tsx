import { useCallback, useEffect, useState, type FormEvent } from "react";
import { api, getLog } from "./api";
import type { Task, TaskEvent, Execution, Project } from "./types";
import { canManageProject, canOperateProject } from "./permissions";
import { taskView, nextAction, taskPhase } from "./task-presentation";
import { TaskTerminal } from "./TaskTerminal";
import { Icon } from "./ConsoleIcon";
import { stageLabel, date, Status } from "./TaskStatus";
import { ConfirmDialog, Empty, ErrorBox, Field, Modal, Panel } from "./ui";
import ManualMergePanel from "./ManualMergePanel";
import { useToast } from "./ToastProvider";

export default function TaskDetail({ id, platformAdmin = false }: { id: string; platformAdmin?: boolean }) {
  const toast = useToast();
  const [task, setTask] = useState<Task | null>(null),
    [role, setRole] = useState<Project["myRole"]>(),
    [events, setEvents] = useState<TaskEvent[]>([]),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false),
    [pendingAutoMerge, setPendingAutoMerge] = useState<boolean | null>(null),
    [text, setText] = useState(""),
    [reject, setReject] = useState(false),
    [confirmCancel, setConfirmCancel] = useState(false),
    [log, setLog] = useState<{ name: string; body: string } | null>(null),
    [logBusy, setLogBusy] = useState(""),
    [detailView, setDetailView] = useState("delivery");
  const refresh = useCallback(async () => {
    try {
      const [t, e] = await Promise.all([
        api<Task>(`/tasks/${encodeURIComponent(id)}`),
        api<TaskEvent[] | null>(
          `/tasks/${encodeURIComponent(id)}/events?after=0`,
        ),
      ]);
      setTask(t);
      api<Project[] | null>("/projects").then(projects => setRole(projects?.find(project => project.id === t.project.id)?.myRole)).catch(() => setRole(undefined));
      setEvents(e || []);
    } catch (e) {
      setError((e as Error).message);
    }
  }, [id]);
  useEffect(() => {
    if (!task || pendingAutoMerge === null) return;
    const current = task.autoMergeOverride ?? task.project.autoMerge ?? true;
    if (current === pendingAutoMerge) {
      setPendingAutoMerge(null);
      toast.success(`此任务自动合并已${current ? "开启" : "关闭"}。`);
    } else if (["partial_merge", "finalizing", "completed", "cancelled"].includes(task.stage)) {
      setPendingAutoMerge(null);
      setError("合并流程已结束或部分合并，本次开关切换未生效。");
    }
  }, [task, pendingAutoMerge, toast]);
  useEffect(() => {
    void refresh();
    const timer = setInterval(() => {
      if (!document.hidden) void refresh();
    }, 3000);
    return () => clearInterval(timer);
  }, [refresh]);
  async function command(kind: string, message = "") {
    if (!task) return;
    setBusy(true);
    setError("");
    try {
      await api(`/tasks/${encodeURIComponent(id)}/commands`, "POST", {
        id: crypto.randomUUID(),
        kind,
        text: message,
        digest: kind === "approve" ? task.reviewDigest : "",
      });
      toast.info("指令已提交，任务状态将在执行后更新。");
      setText("");
      setReject(false);
      await refresh();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  async function setAutoMerge(value: boolean) {
    setBusy(true);
    setPendingAutoMerge(value);
    setError("");
    try {
      await api(`/tasks/${encodeURIComponent(id)}/commands`, "POST", {
        id: crypto.randomUUID(),
        kind: "set_auto_merge",
        autoMerge: value,
      });
      toast.info("自动合并切换已提交，待任务状态更新后生效。");
      await refresh();
    } catch (e) {
      setPendingAutoMerge(null);
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  async function showLog(name: string, execution: Execution) {
    setLogBusy(execution.id);
    try {
      const logArtifact = execution.artifacts?.find((ref) => ref.kind === "log");
      setLog({ name, body: await getLog(id, execution.id, undefined, logArtifact?.name) });
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setLogBusy("");
    }
  }
  if (!task)
    return (
      <>
        <ErrorBox error={error} retry={refresh} />
        <Empty>{error ? "无法读取任务。" : "正在加载任务…"}</Empty>
      </>
    );
  const terminal = ["completed", "cancelled"].includes(task.stage);
  const canOperate = canOperateProject(role, platformAdmin);
  const canManage = canManageProject(role, platformAdmin);
  const autoMergeEnabled = task.autoMergeOverride ?? task.project.autoMerge ?? true;
  const mergeStarted = terminal || ["merging", "manual_merging", "partial_merge", "finalizing"].includes(task.stage) || (task.workspace?.revisions || []).some((r) => r.merged) || !!task.executions?.merge?.id;
  return (
    <>
      <a className="back" href="#/tasks">
        ← 返回任务中心
      </a>
      <div className="page-head detail-heading">
        <div>
          <div className="eyebrow">
            {task.project.name} <span> / Issue #{task.issue.iid}</span>
            {task.round ? <span> / 第 {task.round} 轮</span> : null}
          </div>
          <h1>{task.issue.title}</h1>
          <p>
            <Status task={task} /> <span className="mono">{task.id}</span>
          </p>
        </div>
        <div className="task-detail-actions">
          {canOperate && <TaskTerminal task={task} />}
          {task.issue.url && (
            <a className="button" href={task.issue.url} target="_blank" rel="noreferrer">
              查看 Issue ↗
            </a>
          )}
        </div>
      </div>
      <ErrorBox
        error={error}
        retry={() => {
          setError("");
          void refresh();
        }}
      />
      <div className="detail-grid">
        <div className="main-column">
          <section className="task-summary">
            <div className="section-heading">
              <h2>执行进展</h2>
              <small>更新于 {date(task.updatedAt)}</small>
            </div>
            <ol className="stage-track" aria-label="任务流程">
              {[
                "准备",
                "开发",
                "验证",
                "发布与审查",
                "合并",
                task.stage === "completed" ? "完成" : "收尾",
              ].map((label, index) => (
                <li
                  key={label}
                  aria-current={taskPhase(task) === index ? "step" : undefined}
                  className={taskPhase(task) === index ? "current" : ""}
                >
                  <span />
                  {label}
                </li>
              ))}
            </ol>
            <div className="current-progress">
              <Status task={task} />
              <p>{task.message || stageLabel(task.stage, task.project.provider)}</p>
            </div>
            <small>返工 {task.reworks} 次 · 每 3 秒自动更新</small>
            {task.previousTaskId && <small>上一轮：<a href={`#/tasks/${encodeURIComponent(task.previousTaskId)}`}>{task.previousTaskId.slice(0, 12)}</a></small>}
            {task.triggerText && <p>本轮补充需求：{task.triggerText}</p>}
          </section>
          {taskView(task) === "attention" &&
            task.stage !== "waiting_approval" && (
              <section
                className={`task-guidance ${task.stage === "blocked" ? "is-blocked" : ""}`}
              >
                <Icon name="alert" />
                <div>
                  <h2>{task.paused ? "任务已暂停" : stageLabel(task.stage, task.project.provider)}</h2>
                  <p>{nextAction(task)}</p>
                </div>
              </section>
            )}
          {task.stage === "waiting_approval" && (
            <section className="approval">
              <div>
                <span className="eyebrow">需要你的决定</span>
                <h2>审查已完成，等待批准合并</h2>
                <p>批准将绑定下方审查版本。代码变更后需要重新审查。</p>
                <code className="digest">
                  {task.reviewDigest || "尚无审查摘要"}
                </code>
              </div>
              {canManage && <div className="actions">
                <button disabled={busy} onClick={() => setReject(true)}>
                  退回修改
                </button>
                <button
                  className="primary"
                  disabled={busy || !task.reviewDigest}
                  onClick={() => void command("approve")}
                >
                  {busy ? "正在提交…" : "批准此版本"}
                </button>
              </div>}
            </section>
          )}
          {canManage && task.project.provider !== "github" && (["waiting_manual_merge", "manual_merging", "partial_merge"].includes(task.stage) || task.acceptanceIncomplete) && <ManualMergePanel task={task} refresh={refresh} />}
          <div className="view-tabs detail-views" aria-label="任务详情视图">
            {[
              ["delivery", "交付结果"],
              ["execution", "执行记录"],
              ["context", "需求与说明"],
            ].map(([key, label]) => (
              <button
                key={key}
                aria-pressed={detailView === key}
                onClick={() => setDetailView(key)}
              >
                {label}
              </button>
            ))}
          </div>
          <div hidden={detailView !== "delivery"}>
            <Panel
              title="代码仓库与合并请求"
              extra={
                <span className="count">
                  {task.workspace?.revisions?.length || 0}
                </span>
              }
            >
              {task.workspace?.revisions?.length ? (
                task.workspace.revisions.map((r) => (
                  <article className="repo-revision" key={r.repositoryId}>
                    <div>
                      <strong>{r.repositoryId}</strong>
                      <span className={`status ${r.merged ? "completed" : ""}`}>
                        {r.merged ? "已合并" : r.changed ? "有变更" : "无变更"}
                      </span>
                    </div>
                    <p>
                      <code>{r.branch || "—"}</code>
                      <span className="muted"> → </span>
                      <code>{r.targetBranch}</code>
                    </p>
                    <small className="mono">
                      {r.baseSha?.slice(0, 12) || "—"} →{" "}
                      {r.headSha?.slice(0, 12) || "—"}
                    </small>
                    {r.mrUrl && (task.project.provider === "github" ? <a href={r.mrUrl} target="_blank" rel="noreferrer">PR #{r.mrIid} ↗</a> : <a href={`#/merge-requests/${encodeURIComponent(task.project.id)}/${encodeURIComponent(r.repositoryId)}/${r.mrIid}`}>MR !{r.mrIid} →</a>)}
                    {r.sensitive && (
                      <small className="warning-text">包含敏感路径变更</small>
                    )}
                    <details>
                      <summary>查看变更文件（{r.files?.length || 0}）</summary>
                      <ul>
                        {(r.files || []).map((f) => (
                          <li key={f}>
                            <code>{f}</code>
                          </li>
                        ))}
                      </ul>
                    </details>
                  </article>
                ))
              ) : (
                <Empty>工作区准备后，仓库版本会显示在这里。</Empty>
              )}
            </Panel>
            <Panel title="验证结果">
              {task.verification?.length ? (
                <div className="table-wrap">
                  <table>
                    <thead>
                      <tr>
                        <th>检查项</th>
                        <th>要求</th>
                        <th>结果</th>
                      </tr>
                    </thead>
                    <tbody>
                      {task.verification.map((v, i) => (
                        <tr key={i}>
                          <td>
                            <strong>{v.name}</strong>
                            {v.report && (
                              <details>
                                <summary>查看报告</summary>
                                <pre>{v.report}</pre>
                              </details>
                            )}
                          </td>
                          <td>{v.required ? "必需" : "可选"}</td>
                          <td>
                            <span
                              className={`status ${v.passed ? "completed" : "blocked"}`}
                            >
                              {v.passed ? "通过" : `未通过 · ${v.exitCode}`}
                            </span>
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              ) : (
                <Empty>{task.project.acceptanceStrategy === "none" ? "此项目未启用额外验收。" : "尚无验证结果。"}</Empty>
              )}
            </Panel>
          </div>
          <div hidden={detailView !== "execution"}>
            <Panel title="Agent 执行记录">
              {Object.entries(task.executions || {}).length ? (
                Object.entries(task.executions || {}).map(([role, e]) => (
                  <article className="execution" key={e.id}>
                    <div className="row-between">
                      <div>
                        <strong>
                          {role === "development"
                            ? "开发 Agent"
                            : role === "review"
                              ? "审查 Agent"
                              : role}
                        </strong>
                        <small>
                          {e.status} · 退出码 {e.exitCode}
                        </small>
                      </div>
                      <button
                        disabled={logBusy === e.id}
                        onClick={() => void showLog(role, e)}
                      >
                        {logBusy === e.id ? "读取中…" : "查看日志"}
                      </button>
                    </div>
                    {e.result && (
                      <>
                        <p>{e.result.summary}</p>
                        {e.result.needsHelp && (
                          <p className="warning-text">{e.result.needsHelp}</p>
                        )}
                        {(e.result.findings || []).map((f, i) => (
                          <p key={i}>
                            <code>
                              {f.repositoryId}/{f.path}
                            </code>
                            ：{f.message}
                          </p>
                        ))}
                      </>
                    )}
                    {e.artifacts?.some((ref) => ref.kind === "report") && (
                      <ul className="text-list">
                        {e.artifacts.filter((ref) => ref.kind === "report").map((ref) => (
                          <li key={`${ref.executionId}-${ref.name}`}>
                            <a href={`/api/tasks/${encodeURIComponent(task.id)}/artifacts?path=${encodeURIComponent(ref.name)}&kind=report`}>
                              下载报告：{ref.name}
                            </a>
                          </li>
                        ))}
                      </ul>
                    )}
                  </article>
                ))
              ) : (
                <Empty>Agent 启动后可在这里查看运行日志。</Empty>
              )}
            </Panel>
          </div>
          <div hidden={detailView !== "context"}>
            <Panel title="需求假设与补充说明">
              {task.issue.description && (
                <div className="issue-description">
                  {task.issue.description}
                </div>
              )}
              {task.assumptions?.length ? (
                <ul className="text-list">
                  {task.assumptions.map((a, i) => (
                    <li key={i}>{a}</li>
                  ))}
                </ul>
              ) : (
                <p className="panel-padding muted">暂无记录的需求假设。</p>
              )}
              {(task.notes || []).map((note, i) => (
                <div className="note" key={i}>
                  {note}
                </div>
              ))}
              {canOperate && <form
                className="panel-padding"
                onSubmit={(e) => {
                  e.preventDefault();
                  void command("note", text);
                }}
              >
                <Field
                  label="补充说明"
                  hint="为任务提供更多上下文，说明本身不会暂停任务。"
                >
                  <textarea
                    required
                    value={text}
                    onChange={(e) => setText(e.target.value)}
                    rows={3}
                    placeholder="记录需求澄清、约束或验收补充…"
                  />
                </Field>
                <button disabled={busy || !text.trim()} className="primary">
                  提交说明
                </button>
              </form>}
            </Panel>
          </div>
        </div>
        <aside className="context-column">
          <Panel title="任务操作">
            <div className="panel-padding">
              {canManage && <div className="task-merge-setting">
                <div>
                  <strong>此任务自动合并</strong>
                  <small>{mergeStarted ? "合并流程已开始，开关已锁定。" : autoMergeEnabled ? `开启时，符合合并条件后自动合并 ${task.project.provider === "github" ? "PR" : "MR"}。` : task.project.provider === "github" ? "关闭后继续开发与发布，合并时等待你在 GitHub 操作。" : "关闭后继续开发与发布，合并前在任务页确认验收并提交整轮合并。"}</small>
                </div>
                <button
                  type="button"
                  className={`task-merge-toggle ${autoMergeEnabled ? "on" : ""}`}
                  aria-pressed={autoMergeEnabled}
                  disabled={busy || pendingAutoMerge !== null || mergeStarted}
                  onClick={() => void setAutoMerge(!autoMergeEnabled)}
                >
                  {pendingAutoMerge !== null ? "切换中…" : autoMergeEnabled ? "已开启" : "已关闭"}
                </button>
              </div>}
              {canOperate && <div className="actions">
                {!terminal && (
                  <>
                    <button
                      disabled={busy}
                      onClick={() =>
                        void command(task.paused ? "resume" : "pause")
                      }
                    >
                      {task.paused ? "继续任务" : "暂停任务"}
                    </button>
                    {task.stage === "blocked" && (
                      <button
                        disabled={busy}
                        onClick={() => void command("retry")}
                      >
                        重试任务
                      </button>
                    )}
                    <button
                      className="danger-text"
                      disabled={busy}
                      onClick={() => setConfirmCancel(true)}
                    >
                      取消任务
                    </button>
                  </>
                )}
              </div>}

              <button
                className="execution-shortcut"
                onClick={() => setDetailView("execution")}
              >
                <Icon name="runtime" />
                查看执行记录
              </button>
            </div>
          </Panel>
          <Panel title="任务信息">
            <dl>
              <dt>创建时间</dt>
              <dd>{date(task.createdAt)}</dd>
              <dt>项目版本</dt>
              <dd>v{task.project.version}</dd>
              <dt>Meta-Repo 版本</dt>
              <dd className="mono">
                {task.workspace?.metaSha?.slice(0, 12) || "尚未准备"}
              </dd>
              <dt>Issue 标签</dt>
              <dd>
                {(task.issue.labels || []).map((l) => (
                  <span className="tag" key={l}>
                    {l}
                  </span>
                ))}
              </dd>
              <dt>前置依赖</dt>
              <dd>
                {task.dependencies?.length
                  ? task.dependencies.map((d) => (
                      <div key={`${d.projectId}-${d.iid}`}>
                        {d.projectId} / #{d.iid}
                      </div>
                    ))
                  : "无前置依赖"}
              </dd>
            </dl>
          </Panel>
          <Panel title="状态时间线">
            <ol className="timeline">
              {events.length ? (
                [...events].reverse().map((e) => (
                  <li key={e.id}>
                    <time>{date(e.createdAt)}</time>
                    <strong>{stageLabel(e.kind, task.project.provider)}</strong>
                    <p>{e.message}</p>
                  </li>
                ))
              ) : (
                <li>
                  <p>暂无事件记录。</p>
                </li>
              )}
            </ol>
          </Panel>
        </aside>
      </div>
      {reject && (
        <Modal title="退回修改" close={() => setReject(false)}>
          <form
            onSubmit={(e: FormEvent<HTMLFormElement>) => {
              e.preventDefault();
              void command(
                "reject",
                String(new FormData(e.currentTarget).get("reason")),
              );
            }}
          >
            <Field label="修改要求">
              <textarea name="reason" required autoFocus rows={5} />
            </Field>
            <div className="form-actions">
              <button type="button" onClick={() => setReject(false)}>
                取消
              </button>
              <button className="primary" disabled={busy}>
                提交修改要求
              </button>
            </div>
          </form>
        </Modal>
      )}
      {confirmCancel && (
        <ConfirmDialog
          title="取消任务"
          confirmLabel="取消任务"
          danger
          onCancel={() => setConfirmCancel(false)}
          onConfirm={() => {
            setConfirmCancel(false);
            void command("cancel");
          }}
        >
          <p>取消后任务将停止。已经合并的变更不会撤回。</p>
        </ConfirmDialog>
      )}
      {log && (
        <Modal title={`${log.name} · 运行日志`} close={() => setLog(null)}>
          <pre className="log">{log.body || "日志为空。"}</pre>
        </Modal>
      )}
    </>
  );
}
