import { useEffect, useRef, useState } from "react";
import {
  useDataAPI,
  useProjectState,
  type Agent,
  type HiveOverview,
  type Message,
  type ProjectStore,
} from "../../data";
import type { StarsSlotProps } from "../slots";
import type { ObjectSelection } from "../ui";
import { useFocus, Panel, ErrorNotice } from "./primitives";
import { AgentUsagePanel } from "./UsagePanel";
import Methodology from "./MethodologyPage";
import { useResource } from "./use-resource";
const states: Record<string, string> = {
  active: "工作中",
  creating: "正在准备",
  parked: "空闲",
  hibernated: "已休眠",
  completed: "已完成",
  failed: "失败",
  cancelled: "已取消",
  disabled: "已停用",
};
const address = (value: string) =>
  value === "queen"
    ? "主 Agent"
    : value === "hive" || value === "system"
      ? "系统"
      : value.startsWith("local:")
        ? "你"
        : value.replace(/^forager:/, "Agent ");
const trustName: Record<string, string> = {
  user: "来自管理员",
  agent: "来自 Agent",
  external: "外部内容，未经核实",
  system: "来自系统",
};
export function ObjectPanel({
  ui,
  store,
  selection,
}: { store: ProjectStore; selection: ObjectSelection } & StarsSlotProps) {
  const live = useProjectState(store);
  const api = useDataAPI();
  const ref = useFocus();
  const [body, setBody] = useState("");
  const [reply, setReply] = useState<Message>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [attempt, setAttempt] = useState(0);
  const [messages, setMessages] = useState<Message[]>([]);
  const [loading, setLoading] = useState(false);
  const lock = useRef(false);
  const detail = useResource(
    (signal) =>
      selection.kind === "agent"
        ? api.getForager(selection.id, signal)
        : Promise.resolve(undefined),
    [selection.id, selection.kind, live.revision],
  );
  const snapshot = live.snapshot;
  const agent =
    detail.data?.forager ?? snapshot?.agents.find((a) => a.id === selection.id);
  const local = snapshot?.localAddress;
  const messageRevision = snapshot?.messages.map((m) => m.id).join(",");
  useEffect(() => {
    if (!snapshot || !["queen", "agent", "signal"].includes(selection.kind))
      return;
    let valid = true;
    const controller = new AbortController();
    setLoading(true);
    const addresses =
      selection.kind === "signal"
        ? ["hive"]
        : [
            ...new Set([
              "queen",
              "hive",
              local,
              ...snapshot.agents.map((a) => `forager:${a.id}`),
            ]),
          ].filter((a): a is string => !!a);
    void Promise.all(
      addresses.map(async (to) => {
        const result: Message[] = [];
        let after: string | undefined;
        do {
          const page = await api.listMessages(
            selection.hiveId,
            { to, after, limit: 200 },
            controller.signal,
          );
          result.push(...page.messages);
          if (!page.truncated || !page.cursor || page.cursor === after) break;
          after = page.cursor;
        } while (!controller.signal.aborted);
        return result;
      }),
    )
      .then((pages) => {
        if (!valid) return;
        const all = [
          ...new Map(pages.flat().map((m) => [m.id, m])).values(),
        ].sort(
          (a, b) =>
            Date.parse(a.createdAt) - Date.parse(b.createdAt) ||
            a.id.localeCompare(b.id),
        );
        const target =
          selection.kind === "queen" ? "queen" : `forager:${selection.id}`;
        setMessages(
          all.filter(
            (m) =>
              (selection.kind === "signal" && m.id === selection.id) ||
              m.from === target ||
              m.to === target ||
              (selection.kind === "queen" && m.to === local),
          ),
        );
        setError("");
      })
      .catch((cause) => {
        if (valid)
          setError(String(cause instanceof Error ? cause.message : cause));
      })
      .finally(() => {
        if (valid) setLoading(false);
      });
    return () => {
      valid = false;
      controller.abort();
    };
  }, [
    api,
    selection.id,
    selection.kind,
    selection.hiveId,
    local,
    messageRevision,
    attempt,
  ]);
  const act = async (fn: () => Promise<unknown>, text: string) => {
    if (lock.current) return;
    lock.current = true;
    setBusy(true);
    setError("");
    try {
      await fn();
      setNotice(text);
      setBody("");
      setReply(undefined);
      setAttempt((n) => n + 1);
      await store.refresh();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setBusy(false);
      lock.current = false;
    }
  };
  if (!snapshot)
    return (
      <div
        ref={ref}
        className="pc-sheet pc-right"
        role="region"
        aria-label="对象详情"
      >
        <button onClick={() => ui.closeObject()}>关闭详情</button>正在读取…
      </div>
    );
  const target =
    selection.kind === "queen"
      ? "主 Agent"
      : selection.kind === "pr"
        ? `PR #${selection.number}`
        : selection.kind === "signal"
          ? "待处理事件"
          : agent?.roleName || "Agent";
  return (
    <div
      ref={ref}
      className="pc-sheet pc-right"
      role="region"
      aria-label="对象详情"
    >
      <button
        className="pc-close"
        aria-label="关闭对象详情"
        onClick={() => ui.closeObject()}
      >
        ×
      </button>
      <p className="pc-muted">{snapshot.hive.name}</p>
      <h2>{target}</h2>
      {error && (
        <ErrorNotice message={error} onRetry={() => setAttempt((n) => n + 1)} />
      )}{" "}
      {notice && <p role="status">{notice}</p>}
      {selection.kind === "pr" ? (
        <PRPanel
          key={`${selection.hiveId}:${selection.number}`}
          store={store}
          number={selection.number!}
        />
      ) : selection.kind === "signal" ? (
        <>
          {snapshot.signals
            .filter((s) => s.messageId === selection.id)
            .map((signal) => (
              <section key={signal.messageId}>
                <h3>{signal.title}</h3>
                {messages
                  .filter((m) => m.id === signal.messageId)
                  .map((m) => (
                    <div key={m.id}>
                      <small>{trustName[m.trust || "agent"] || m.trust}</small>
                      <time>{new Date(m.createdAt).toLocaleString()}</time>
                      <p className="pc-message-body">{m.body}</p>
                    </div>
                  ))}
                {loading && <p role="status">正在读取事件正文…</p>}
                <a href={signal.url} target="_blank" rel="noreferrer">
                  在 GitHub 查看
                </a>
                {signal.claimed ? (
                  <p>已由 {signal.claimedBy} 处理</p>
                ) : (
                  <button
                    disabled={busy}
                    onClick={() =>
                      void act(
                        () => api.assignIssue(snapshot.hive.id, signal.number),
                        "已请主 Agent 处理。",
                      )
                    }
                  >
                    请主 Agent 处理 Issue #{signal.number}
                  </button>
                )}
              </section>
            ))}
        </>
      ) : (
        <>
          <Panel title="此刻在做什么">
            <p>
              {selection.kind === "queen"
                ? states[snapshot.queen.state]
                : agent
                  ? states[agent.state]
                  : "正在读取 Agent…"}
            </p>
            <p>
              {agent?.task ||
                (selection.kind === "queen" ? "负责协调这个项目。" : "")}
            </p>
            {agent && <AgentFacts agent={agent} />}
            <p className="pc-muted">
              最近的工具调用：平台暂未提供；下方显示真实消息。
            </p>
          </Panel>
          {detail.error && (
            <ErrorNotice message={detail.error} onRetry={detail.reload} />
          )}
          {selection.kind === "queen" &&
            (!snapshot.methodology || snapshot.methodology.builtin) && (
              <>
                <p>先告诉主 Agent 你的目标，再选择方法论。</p>
                <Methodology hiveId={snapshot.hive.id} />
              </>
            )}
          {selection.kind === "queen" && !snapshot.queen.enabled && (
            <>
              <p>主 Agent 已停用，打开后即可对话。</p>
              <button onClick={() => ui.openProject("settings")}>
                打开项目设置
              </button>
            </>
          )}
          <Panel title="对话">
            {loading && <p role="status">正在读取完整对话…</p>}
            {!loading && !messages.length && <p>还没有对话。</p>}
            <ol className="pc-messages">
              {messages.map((m) => (
                <li key={m.id}>
                  <header>
                    <strong>
                      {address(m.from)} 对 {address(m.to)}
                    </strong>
                    <time>{new Date(m.createdAt).toLocaleString()}</time>
                  </header>
                  <small>
                    {trustName[m.trust || "agent"] || m.trust}{" "}
                    {m.kind === "question" ? "提问" : ""}
                  </small>
                  <p>{m.body}</p>
                  {m.kind === "question" &&
                    m.to === local &&
                    (m.from === "queen" || m.from.startsWith("forager:")) && (
                      <button
                        type="button"
                        onClick={() => {
                          setReply(m);
                          ref.current
                            ?.querySelector<HTMLTextAreaElement>("textarea")
                            ?.focus();
                        }}
                      >
                        回复这条提问
                      </button>
                    )}
                  {m.artifacts?.map((a, i) => (
                    <p key={i}>
                      <code>{a.ref}</code>
                    </p>
                  ))}
                </li>
              ))}
            </ol>
          </Panel>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              if (!body.trim()) return;
              void act(
                () =>
                  reply
                    ? api.sendMessage(snapshot.hive.id, {
                        to: reply.from,
                        body: body.trim(),
                        kind: "reply",
                        chainId: reply.chainId,
                      })
                    : selection.kind === "queen"
                      ? api.talkToQueen(snapshot.hive.id, body.trim())
                      : api.interject(
                          snapshot.hive.id,
                          selection.id,
                          body.trim(),
                          agent?.chainId,
                        ),
                "消息已发出。",
              );
            }}
          >
            {reply && (
              <p>
                正在回复 {address(reply.from)} 的提问。
                <button type="button" onClick={() => setReply(undefined)}>
                  取消回复
                </button>
              </p>
            )}
            <label>
              {" "}
              {selection.kind === "queen" ? "对主 Agent 说" : "插话"}
              <textarea
                aria-label={
                  selection.kind === "queen" ? "对主 Agent 说" : "插话"
                }
                value={body}
                onChange={(e) => setBody(e.target.value)}
                required
              />
            </label>
            <button
              disabled={
                busy || (selection.kind === "queen" && !snapshot.queen.enabled)
              }
            >
              {busy ? "正在发送…" : "发送"}
            </button>
          </form>
          {agent && (
            <>
              <details>
                <summary>交接摘要</summary>
                {agent.handoff ? (
                  <>
                    <dl>
                      {(
                        [
                          ["goal", "目标"],
                          ["done", "已完成"],
                          ["remaining", "未完成"],
                          ["risks", "风险"],
                          ["next", "下一步"],
                        ] as const
                      ).map(([key, label]) => (
                        <div key={key}>
                          <dt>{label}</dt>
                          <dd>{agent.handoff?.[key]}</dd>
                        </div>
                      ))}
                    </dl>
                    {agent.handoff.artifacts?.map((a, i) => (
                      <pre key={i}>
                        {a.kind}: {a.ref}
                      </pre>
                    ))}
                  </>
                ) : (
                  <p>还没有交接摘要。</p>
                )}
              </details>
              <details>
                <summary>验证与合并</summary>
                {agent.prNumber ? (
                  <PRPanel
                    key={`${snapshot.hive.id}:${agent.prNumber}`}
                    store={store}
                    number={agent.prNumber}
                  />
                ) : (
                  <p>还没有关联 PR。</p>
                )}
              </details>
              <details>
                <summary>花费</summary>
                <AgentUsagePanel
                  hiveId={snapshot.hive.id}
                  agentId={agent.id}
                  roleName={agent.roleName}
                />
              </details>
              {["Diff", "终端", "时间线"].map((label) => (
                <details key={label}>
                  <summary>{label}</summary>
                  <p>平台暂未提供{label}数据。</p>
                </details>
              ))}
              {!["completed", "failed", "cancelled"].includes(agent.state) && (
                <button
                  disabled={busy}
                  onClick={() =>
                    void act(
                      () => api.cancelForager(agent.id),
                      "已发送取消指令。",
                    )
                  }
                >
                  取消 Agent
                </button>
              )}
            </>
          )}
        </>
      )}
    </div>
  );
}
function AgentFacts({ agent }: { agent: Agent }) {
  return (
    <dl className="pc-facts">
      {[
        ["编号", agent.id],
        ["分支", agent.branch],
        ["父 Agent", agent.parentId],
        ["检出提交", agent.revision],
        ["对话链", agent.chainId],
        ["Issue", agent.issueNumber ? `#${agent.issueNumber}` : undefined],
      ]
        .filter(([, v]) => v)
        .map(([k, v]) => (
          <div key={k}>
            <dt>{k}</dt>
            <dd>
              <code>{v}</code>
            </dd>
          </div>
        ))}
    </dl>
  );
}
export function PRPanel({
  store,
  number,
}: {
  store: ProjectStore;
  number: number;
}) {
  const api = useDataAPI();
  const state = useProjectState(store);
  const id = state.snapshot?.hive.id ?? "";
  const live = state.snapshot?.prs.find((p) => p.number === number);
  const details = useResource(
    (signal) => api.getPRStatus(id, number, signal),
    [id, number, live?.headSha, live?.ready, live?.merged],
  );
  const pr = details.data;
  // The submitted head belongs to these loaded checks and attestations, never
  // to a newer lightweight event that has not been reviewed in this panel.
  const observed = pr?.headSha;
  const changedHead =
    !!live?.headSha && !!observed && live.headSha !== observed;
  const canMerge =
    !!observed &&
    !!pr?.ready &&
    !pr.merged &&
    !live?.merged &&
    live?.ready !== false &&
    !changedHead &&
    !details.loading &&
    !details.error;
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const lock = useRef(false);
  return (
    <section className="pc-pr">
      <h3>PR #{number}</h3>
      {details.error && (
        <ErrorNotice message={details.error} onRetry={details.reload} />
      )}
      {changedHead && (
        <div role="alert">
          <p>PR 有新提交，请重新查看</p>
          <p>
            当前提交：<code>{live?.headSha}</code>
            ；下方检查与验证对应已读取提交。
          </p>
          <button onClick={details.reload}>重新查看</button>
        </div>
      )}
      {details.loading && <p role="status">正在读取提交的检查与验证…</p>}
      <p>
        {live?.merged || pr?.merged
          ? "已合并"
          : canMerge
            ? "等你决定"
            : "尚未满足合并条件"}
      </p>
      <p>
        已读取提交：
        <code data-testid="reviewed-pr-head">{observed ?? "尚未读取"}</code>
      </p>
      {(pr?.reasons ?? []).map((reason) => (
        <p key={reason}>{reason}</p>
      ))}
      {(pr?.url || live?.url) && (
        <p>
          <a href={pr?.url || live?.url} target="_blank" rel="noreferrer">
            在 GitHub 查看
          </a>
        </p>
      )}
      <h4>检查结果</h4>
      {pr?.checks?.length ? (
        pr.checks.map((c, i) => (
          <p key={i}>
            {c.name}：{c.conclusion || c.status}
            {c.url && <a href={c.url}> 查看</a>}
            {c.output && <pre>{c.output}</pre>}
          </p>
        ))
      ) : (
        <p>还没有检查结果。</p>
      )}
      <h4>验证结论历史</h4>
      {(pr?.attestations ?? []).map((a) => (
        <article className="pc-attestation" key={a.id}>
          <p>
            {
              {
                pass: "通过",
                pass_notes: "通过，有建议",
                fail: "未通过",
                blocked: "受阻",
              }[a.result]
            }
            ：{a.summary}
          </p>
          <p>
            {a.attesterId} {a.isAuthor ? "作者" : "独立验证"}{" "}
            {a.superseded ? "已作废" : "有效"}
          </p>
          <code>{a.headSha}</code>
          <time>{a.createdAt}</time>
          {a.findings?.map((f, i) => (
            <p key={i}>
              {f.severity} {f.location} {f.finding} {f.evidence}
            </p>
          ))}
          {a.commentUrl && <a href={a.commentUrl}>查看评论</a>}
        </article>
      ))}
      {!pr?.attestations?.length && <p>还没有验证结论。</p>}
      <button
        disabled={busy || !canMerge}
        onClick={() => {
          if (lock.current || !canMerge || !observed) return;
          // A store event may arrive before React commits the next render.
          // Recheck synchronously and retain the exact displayed head.
          const latest = store
            .getSnapshot()
            .snapshot?.prs.find((p) => p.number === number);
          if (
            (latest?.headSha && latest.headSha !== observed) ||
            latest?.ready === false ||
            latest?.merged
          ) {
            setError("PR 有新提交或合并条件已变化，请重新查看");
            details.reload();
            return;
          }
          lock.current = true;
          setBusy(true);
          setError("");
          void store
            .mergePR(number, observed)
            .then((result) => {
              setNotice(
                result.merged
                  ? "已合并。"
                  : result.queued
                    ? "已进入合并队列，等待平台处理。"
                    : "合并请求已提交，请重新读取确认状态。",
              );
              details.reload();
            })
            .catch((cause) => {
              setError(cause instanceof Error ? cause.message : String(cause));
              details.reload();
            })
            .finally(() => {
              lock.current = false;
              setBusy(false);
            });
        }}
      >
        {busy ? "正在合并…" : "确认合并"}
      </button>
      {error && <p role="alert">{error}</p>}
      {notice && <p role="status">{notice}</p>}
    </section>
  );
}
export function Ledger({ projects }: { projects: HiveOverview[] }) {
  return (
    <>
      <h3>账簿</h3>
      <p>按项目查看今天与累计花费。本周花费暂时无法获取。</p>
      {projects.map((p) => (
        <LedgerRow key={p.hive.id} project={p} />
      ))}
    </>
  );
}
function LedgerRow({ project }: { project: HiveOverview }) {
  const api = useDataAPI();
  const data = useResource(
    async (signal) =>
      Promise.all([
        api.getUsage({ hive: project.hive.id, period: "today" }, signal),
        api.getUsage({ hive: project.hive.id, period: "all" }, signal),
      ]),
    [project.hive.id],
  );
  return (
    <Panel title={project.hive.name}>
      {data.error ? (
        <ErrorNotice message={data.error} onRetry={data.reload} />
      ) : data.data ? (
        data.data.map((usage, i) => (
          <p key={i}>
            {i === 0 ? "今天" : "累计"}：${usage.totals.costUsd.toFixed(4)}
            {usage.totals.unknownPriceRequests > 0
              ? `（有 ${usage.totals.unknownPriceRequests} 次调用价格未知，金额不完整）`
              : ""}
          </p>
        ))
      ) : (
        <p>正在读取…</p>
      )}
    </Panel>
  );
}
