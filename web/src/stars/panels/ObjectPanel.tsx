import { useEffect, useRef, useState } from "react";
import {
  useDataAPI,
  useProjectState,
  oneLineSummary, prSummary, weightedTruncate, uniqueLongTexts, shortAgentId, shortSha, relativeTime, localizedReason, agentStateLabel, addressLabel, messageTrustLabel, attestationStateLabel,
  type Agent,
  type HiveOverview,
  type Message,
  type ProjectStore,
} from "../../data";
import type { StarsSlotProps } from "../slots";
import type { ObjectSelection } from "../ui";
import { useFocus, Panel, ErrorNotice, LongText, ShortCopy } from "./primitives";
import { AgentUsagePanel } from "./UsagePanel";
import Methodology from "./MethodologyPage";
import { useResource } from "./use-resource";
export function ObjectPanel({
  ui,
  store,
  selection,
  active = true,
}: { store: ProjectStore; selection: ObjectSelection; active?: boolean } & StarsSlotProps) {
  const live = useProjectState(store);
  const api = useDataAPI();
  const ref = useFocus(active);
  const [body, setBody] = useState("");
  const [reply, setReply] = useState<Message>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [attempt, setAttempt] = useState(0);
  const [loading, setLoading] = useState(false);
  const lock = useRef(false);
  const detail = useResource(
    (signal) =>
      selection.kind === "agent"
        ? api.getForager(selection.id, signal)
        : Promise.resolve(undefined),
    [selection.id, selection.kind],
  );
  const snapshot = live.snapshot;
  const agent =
    detail.data?.forager ?? snapshot?.agents.find((a) => a.id === selection.id);
  const local = snapshot?.localAddress;
  useEffect(() => {
    if (!snapshot || selection.kind !== "agent" || !agent || !["completed", "cancelled", "failed"].includes(agent.state)) return;
    let valid = true;
    setLoading(true);
    void store.loadAgentMessages(selection.id).catch((cause) => {
      if (valid) setError(cause instanceof Error ? cause.message : String(cause));
    }).finally(() => { if (valid) setLoading(false); });
    return () => { valid = false; };
  }, [store, selection.id, selection.kind, agent?.state, attempt]);
  const targetAddress = selection.kind === "queen" ? "queen" : `forager:${selection.id}`;
  const messages = (snapshot?.messages ?? []).filter((m) =>
    selection.kind === "signal" ? m.id === selection.id : m.from === targetAddress || m.to === targetAddress || (selection.kind === "queen" && m.to === local),
  );
  const seenTexts = agent?.task ? [agent.task] : [];
  const visibleMessages = messages.filter((message) => {
    const value = "body" in message ? message.body : message.preview;
    if (uniqueLongTexts([...seenTexts, value]).length === uniqueLongTexts(seenTexts).length) return false;
    seenTexts.push(value);
    return true;
  });
  const previewIds = visibleMessages.filter((message) => "preview" in message).map((message) => message.id).join(",");
  useEffect(() => {
    if (!previewIds) return;
    void store.hydrateMessages(previewIds.split(",")).catch((cause) => {
      setError(cause instanceof Error ? cause.message : String(cause));
    });
  }, [store, previewIds]);
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
        className={`pc-sheet pc-right ${active ? "pc-open" : ""}`}
        role="region"
        aria-label="对象详情"
        aria-hidden={!active}
        inert={!active}
      >
        <button onClick={() => ui.closeObject()}>关闭详情</button>正在读取…
      </div>
    );
  const fullTarget = selection.kind === "queen" ? "主 Agent" : selection.kind === "pr"
    ? snapshot.prs.find((p) => p.number === selection.number)?.title || `PR #${selection.number}`
    : selection.kind === "signal" ? snapshot.signals.find((s) => s.messageId === selection.id)?.title || "待处理事件"
    : agent ? oneLineSummary(agent, { prs: snapshot.prs, issues: snapshot.signals }) : "Agent";
  return (
    <div
      ref={ref}
      className={`pc-sheet pc-right ${active ? "pc-open" : ""}`}
      role="region"
      aria-label="对象详情"
      aria-hidden={!active}
      inert={!active}
    >
      <button
        className="pc-close"
        aria-label="关闭对象详情"
        onClick={() => ui.closeObject()}
      >
        ×
      </button>
      <p className="pc-kicker">{selection.kind === "pr" ? `PR #${selection.number}` : selection.kind === "agent" ? agentStateLabel(agent?.state ?? "creating") : selection.kind === "queen" ? "主 Agent" : "外部事件"}，项目 {snapshot.hive.name}</p>
      <h2 title={fullTarget} aria-label={fullTarget}>{fullTarget}</h2>
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
                {messages
                  .filter((m) => m.id === signal.messageId)
                  .map((m) => (
                    <div key={m.id}>
                      <small>{messageTrustLabel(m.trust || "agent")}</small>
                      <time dateTime={m.createdAt}>{relativeTime(m.createdAt)}</time>
                      {"body" in m ? <LongText text={m.body} label="事件正文" /> : <p>事件摘要：{m.preview}（正在读取完整正文…）</p>}
                    </div>
                  ))}
                {loading && <p role="status">正在读取事件正文…</p>}
                <a href={signal.url} target="_blank" rel="noreferrer">
                  在 GitHub 查看
                </a>
                {signal.claimed ? (
                  <p>{signal.claimedBy ? <>已由 {signal.claimedBy === "queen" ? "主 Agent" : <>
                    Agent <ShortCopy value={signal.claimedBy} short={shortAgentId(signal.claimedBy)} label="处理 Agent" />
                  </>} 处理</> : "已有人处理。"}</p>
                ) : signal.kind === "issue" ? (
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
                ) : null}
              </section>
            ))}
        </>
      ) : (
        <>
          <Panel title={agent && ["completed", "failed", "cancelled"].includes(agent.state) ? "结果" : "此刻在做什么"}>
            <p>
              {selection.kind === "queen"
                ? agentStateLabel(snapshot.queen.state)
                : agent
                  ? agentStateLabel(agent.state)
                  : "正在读取 Agent…"}
            </p>
            {agent?.task ? <LongText text={agent.task} label="任务全文" /> : selection.kind === "queen" ? <p>负责协调这个项目。</p> : null}
            {agent && <AgentFacts agent={agent} />}
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
            {!loading && !visibleMessages.length && <p>还没有对话。</p>}
            <ol className="pc-messages">
              {visibleMessages.map((m) => (
                <li key={m.id}>
                  <header>
                    <strong>
                      {addressLabel(m.from)} 对 {addressLabel(m.to)}
                    </strong>
                    <time dateTime={m.createdAt}>{relativeTime(m.createdAt)}</time>
                  </header>
                  <small>
                    {messageTrustLabel(m.trust || "agent")}{" "}
                    {m.kind === "question" ? "提问" : ""}
                  </small>
                  {"body" in m ? <LongText text={m.body} label="消息正文" /> : <p>消息摘要：{m.preview}（正在读取完整正文…）</p>}
                  {"body" in m && m.kind === "question" &&
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
                  {("artifacts" in m ? m.artifacts : undefined)?.map((a, i) => (
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
                正在回复 {addressLabel(reply.from)} 的提问。
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
        ["编号", agent.id, "id"],
        ["分支", agent.branch, "branch"],
        ["父 Agent", agent.parentId, "id"],
        ["检出提交", agent.revision, "sha"],
        ["对话链", agent.chainId, "id"],
        ["Issue", agent.issueNumber ? `#${agent.issueNumber}` : undefined],
      ]
        .filter(([, v]) => v)
        .map(([k, v, type]) => (
          <div key={k}>
            <dt>{k}</dt>
            <dd>
              {type && v ? <ShortCopy value={v} short={type === "sha" ? shortSha(v) : type === "branch" ? weightedTruncate(v, 22) : shortAgentId(v)} label={k!} /> : <code>{v}</code>}
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
      <h3>{live ? prSummary(live, 40) : `PR #${number}`}</h3>
      {details.error && (
        <ErrorNotice message={details.error} onRetry={details.reload} />
      )}
      {changedHead && (
        <div role="alert">
          <p>PR 有新提交，请重新查看</p>
          <p>
            当前提交：{live?.headSha && <ShortCopy value={live.headSha} short={shortSha(live.headSha)} label="当前提交" />}
            ；下方检查与验证对应已读取提交。
          </p>
          <button onClick={details.reload}>重新查看</button>
        </div>
      )}
      {details.loading && <p role="status">正在读取提交的检查与验证…</p>}
      <div className="pc-decision">
        <p>
          {live?.merged || pr?.merged
            ? "已合并"
            : canMerge
              ? "等你决定"
              : "尚未满足合并条件"}
        </p>
        <button
          className="go"
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
      </div>
      <p>
        已读取提交：
        <span data-testid="reviewed-pr-head" hidden>{observed ?? "尚未读取"}</span>
        {observed ? <ShortCopy value={observed} short={shortSha(observed)} label="已读取提交" /> : "尚未读取"}
      </p>
      {(pr?.reasons ?? []).map((reason) => {
        const localized = localizedReason(reason);
        return <div key={reason}><p>{localized.text}</p>{localized.detail && <details><summary>查看原因详情</summary><p>{localized.detail}</p></details>}</div>;
      })}
      {(pr?.url || live?.url) && (
        <p>
          <a href={pr?.url || live?.url} target="_blank" rel="noreferrer">
            在 GitHub 查看
          </a>
        </p>
      )}
      <h3>GitHub CI</h3>
      {pr?.checks?.length ? (
        pr.checks.map((c, i) => (
          <p key={i}>
            {c.name}：{c.conclusion || c.status}
            {c.url && <a href={c.url}> 查看</a>}
            {c.output && <LongText text={c.output} label="检查输出" />}
          </p>
        ))
      ) : (
        <p>无</p>
      )}
      <h3>独立验证</h3>
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
            ：<LongText text={a.summary} label="验证结论" />
          </p>
          <p>
            {a.isAuthor ? "作者" : "独立验证"} · {attestationStateLabel(a, observed)}
          </p>
          <details>
            <summary>查看验证详情</summary>
            <p>验证 Agent <ShortCopy value={a.attesterId} short={shortAgentId(a.attesterId)} label="验证 Agent" /></p>
            <ShortCopy value={a.headSha} short={shortSha(a.headSha)} label="验证提交" />
          </details>
          <time dateTime={a.createdAt}>{relativeTime(a.createdAt)}</time>
          {a.findings?.map((f, i) => (
            <p key={i}>
              {f.severity} {f.location} <LongText text={[f.finding, f.evidence].filter(Boolean).join("\n")} label="验证发现" />
            </p>
          ))}
          {a.commentUrl && <a href={a.commentUrl}>查看评论</a>}
        </article>
      ))}
      {!pr?.attestations?.length && <p>还没有验证结论。</p>}
    </section>
  );
}
export function Ledger({ projects }: { projects: HiveOverview[] }) {
  return (
    <>
      <h3>账簿</h3>
      <p>按项目查看今天与累计花费。</p>
      <dl className="pc-ledger-list">{projects.map((p) => (
        <LedgerRow key={p.hive.id} project={p} />
      ))}</dl>
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
  const [today, all] = data.data ?? [];
  const unknown = (today?.totals.unknownPriceRequests ?? 0) + (all?.totals.unknownPriceRequests ?? 0);
  return <div className="pc-ledger-row">
    <dt>{project.hive.name}</dt>
    <dd>{data.error ? <ErrorNotice message={data.error} onRetry={data.reload} /> : today && all ? <>
      <span>今天 ${today.totals.costUsd.toFixed(4)}</span>
      <span>累计 ${all.totals.costUsd.toFixed(4)}</span>
      {unknown > 0 && <small>有调用价格未知，金额不完整</small>}
    </> : <span role="status">正在读取…</span>}</dd>
  </div>;
}
