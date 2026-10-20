// Forager detail page: real status, branch, PR, handoff and conversation from
// the Hive REST endpoints. CI / Guard checks and the diff are not stored yet,
// so those sections keep honest placeholders instead of fixture numbers.
import { useId, useState } from "react";
import { hiveAPI } from "./api";
import type { ForagerDetailResponse } from "./api";
import type {
  Agent,
  Attestation,
  Check,
  CheckStatus,
  ConversationEntry,
  MergeCheck,
  PRStatusResponse,
} from "./types";
import {
  AttestBadge,
  BeeLoader,
  CheckList,
  EmptyState,
  ErrorNotice,
  Panel,
  StatusBadge,
  Tabs,
} from "./components";
import { ForagerAvatar, foragerNeedsYou } from "./pixel";
import { Icon } from "./icons";
import { ConversationStream } from "./ConversationStream";
import { AgentUsagePanel } from "./UsagePanel";
import { formatTimestamp, messageEntries, mergeMessages, shortSha } from "./format";
import { useResource } from "./use-resource";

const sections = [
  { id: "conversation", label: "对话" },
  { id: "handoff", label: "交接摘要" },
  { id: "merge", label: "验证与合并" },
  { id: "usage", label: "用量与花费" },
  { id: "diff", label: "Diff" },
  { id: "timeline", label: "时间线" },
  { id: "terminal", label: "终端" },
] as const;

const placeholders: Record<string, { title: string; hint: string }> = {
  diff: {
    title: "Diff",
    hint: "这里会按文件展示 Forager 的改动，大的 diff 分页拉取。这一轮先放占位。",
  },
  timeline: {
    title: "时间线",
    hint: "这里会按 chain_id 展示状态变化、每轮对话摘要和 PR 事件。这一轮先放占位。",
  },
  terminal: {
    title: "终端（人工接管）",
    hint: "这里会挂载 Cell 的实时终端，人可以查看并插话。这一轮先放占位。",
  },
};

const handoffFields: { key: "goal" | "done" | "remaining" | "risks" | "next"; label: string }[] = [
  { key: "goal", label: "目标" },
  { key: "done", label: "已完成" },
  { key: "remaining", label: "未完成" },
  { key: "risks", label: "风险" },
  { key: "next", label: "建议的下一步" },
];

export default function ForagerPage({
  hiveId,
  foragerId,
  section,
}: {
  hiveId: string;
  foragerId: string;
  section: string;
}) {
  const detailRes = useResource(
    signal => hiveAPI.getForager(foragerId, signal),
    [foragerId],
    5000,
  );
  const localAddress = detailRes.data?.localAddress ?? "";
  const messageHiveID = detailRes.data?.forager.hiveId || hiveId;
  const conversationRes = useResource(
    async signal => {
      if (!localAddress) return [];
      const [incoming, toQueen, toHive] = await Promise.all([
        hiveAPI.listMessages(messageHiveID, { to: `forager:${foragerId}` }, signal),
        hiveAPI.listMessages(messageHiveID, { to: "queen" }, signal),
        hiveAPI.listMessages(messageHiveID, { to: "hive" }, signal),
      ]);
      const outgoing = [...toQueen.messages, ...toHive.messages].filter(
        message => message.from === `forager:${foragerId}`,
      );
      return messageEntries(mergeMessages(incoming.messages, outgoing));
    },
    [messageHiveID, foragerId, localAddress],
    5000,
  );

  if (detailRes.loading && !detailRes.data) {
    return (
      <div className="hg-page">
        <BeeLoader label="正在读取 Forager…" />
      </div>
    );
  }
  if (detailRes.errorStatus === 404) {
    return (
      <div className="hg-page">
        <EmptyState
          title="找不到这只 Forager。"
          hint="它可能已经休眠或完成，链接也可能不对。"
          action={
            <a className="hg-btn" href="#/hives">
              返回 Hive 列表
            </a>
          }
        />
      </div>
    );
  }
  if (!detailRes.data) {
    return (
      <div className="hg-page">
        <ErrorNotice
          message={detailRes.error}
          onRetry={detailRes.reload}
          auth={detailRes.errorStatus === 401}
          admin={detailRes.errorStatus === 403}
        />
      </div>
    );
  }

  return (
    <ForagerDetail
      detail={detailRes.data}
      conversation={conversationRes.data ?? []}
      conversationError={conversationRes.error}
      conversationErrorStatus={conversationRes.errorStatus}
      onRetryConversation={conversationRes.reload}
      section={section}
      onChanged={() => {
        detailRes.reload();
        conversationRes.reload();
      }}
    />
  );
}

function ForagerDetail({
  detail,
  conversation,
  conversationError,
  conversationErrorStatus,
  onRetryConversation,
  section,
  onChanged,
}: {
  detail: ForagerDetailResponse;
  conversation: ConversationEntry[];
  conversationError: string;
  conversationErrorStatus: number | undefined;
  onRetryConversation: () => void;
  section: string;
  onChanged: () => void;
}) {
  const { forager: agent, hive } = detail;
  const [notice, setNotice] = useState("");
  const [error, setError] = useState("");
  const [cancelling, setCancelling] = useState(false);
  const prRes = useResource(
    signal =>
      agent.prNumber
        ? hiveAPI.getPRStatus(hive.id, agent.prNumber, signal)
        : Promise.resolve(undefined as PRStatusResponse | undefined),
    [hive.id, agent.prNumber],
  );
  const refresh = () => {
    onChanged();
    prRes.reload();
  };
  const active = section || "conversation";
  const terminal = ["completed", "failed", "cancelled"].includes(agent.state);
  const tabs = sections.map(item => ({
    ...item,
    href:
      item.id === "conversation"
        ? `#/hives/${hive.id}/foragers/${agent.id}`
        : `#/hives/${hive.id}/foragers/${agent.id}/${item.id}`,
  }));

  const cancel = () => {
    setCancelling(true);
    setError("");
    setNotice("");
    hiveAPI
      .cancelForager(agent.id)
      .then(() => {
        setNotice("已发送取消指令，等监管者处理。");
        refresh();
      })
      .catch(cause => setError(cause instanceof Error ? cause.message : String(cause)))
      .finally(() => setCancelling(false));
  };

  return (
    <div className="hg-page">
      <nav className="hg-crumbs" aria-label="面包屑">
        <a href="#/hives">Hive</a>
        <span aria-hidden="true">/</span>
        <a href={`#/hives/${hive.id}/swarm`}>{hive.name}</a>
        <span aria-hidden="true">/</span>
        <span>{agent.id}</span>
      </nav>

      <header className="hg-page-head hg-forager-head">
        <ForagerAvatar
          id={agent.id}
          state={agent.state}
          needsYou={foragerNeedsYou(agent, conversation)}
          size={64}
        />
        <div className="hg-forager-head-text">
          <h1>
            {agent.id}
            <StatusBadge state={agent.state} />
          </h1>
          <p className="hg-forager-task">{agent.task}</p>
          <div className="hg-meta-row">
            {agent.branch ? (
              <span className="hg-meta">
                <Icon name="branch" size={16} />
                <code>{agent.branch}</code>
              </span>
            ) : null}
            {agent.prNumber ? (
              <a
                className="hg-meta"
                href={`https://github.com/${hive.repository}/pull/${agent.prNumber}`}
                target="_blank"
                rel="noreferrer"
              >
                <Icon name="pr" size={16} />
                PR #{agent.prNumber}
              </a>
            ) : null}
            <span className="hg-meta">
              <Icon name="user" size={16} />
              角色 <code>{agent.roleName || agent.preset || agent.role}</code>
            </span>
            {agent.parentId ? (
              <span className="hg-meta">
                <Icon name="swarm" size={16} />
                父 Agent <code>{agent.parentId}</code>
              </span>
            ) : null}
            {agent.revision ? (
              <span className="hg-meta" title={agent.revision}>
                <Icon name="check" size={16} />
                检出提交 <code>{shortSha(agent.revision)}</code>
              </span>
            ) : null}
            {agent.chainId ? (
              <span className="hg-meta">
                <Icon name="timeline" size={16} />
                <code>{agent.chainId}</code>
              </span>
            ) : null}
          </div>
        </div>
        {!terminal ? (
          <button
            className="hg-btn hg-btn-danger"
            type="button"
            onClick={cancel}
            disabled={cancelling}
          >
            {cancelling ? "取消中…" : "取消 Forager"}
          </button>
        ) : null}
      </header>

      {error ? (
        <p className="hg-form-error" role="alert">
          {error}
        </p>
      ) : null}
      {notice ? (
        <p className="hg-form-status" role="status">
          {notice}
        </p>
      ) : null}

      <Tabs items={tabs} active={active} label={`${agent.id} 分区`} />

      {active === "conversation" ? (
        <div className="hg-conversation-main">
          <Panel title="对话" subtitle="这只 Forager 与 Queen（蜂王）、你之间的往来。">
            {conversationError ? (
              <ErrorNotice
                message={conversationError}
                onRetry={onRetryConversation}
                auth={conversationErrorStatus === 401}
                admin={conversationErrorStatus === 403}
              />
            ) : null}
            <ConversationStream
              entries={conversation}
              agents={[agent]}
              emptyTitle="这只 Forager 还没有对话。"
            />
          </Panel>
          {!terminal ? (
            <ForagerComposer hiveId={hive.id} foragerId={agent.id} onSent={refresh} />
          ) : null}
        </div>
      ) : active === "merge" ? (
        <MergePanel
          agent={agent}
          pr={prRes.data}
          error={prRes.error}
          errorStatus={prRes.errorStatus}
          onRetry={prRes.reload}
        />
      ) : active === "usage" ? (
        <AgentUsagePanel hiveId={hive.id} agentId={agent.id} roleName={agent.roleName} />
      ) : active === "handoff" ? (
        <Panel title="交接摘要" subtitle="每轮结束时更新，也是 CLI 会话丢失后的恢复依据。">
          {agent.handoff ? (
            <div className="hg-handoff">
              <dl className="hg-handoff-grid">
                {handoffFields.map(field => (
                  <div key={field.key}>
                    <dt>{field.label}</dt>
                    <dd>{agent.handoff?.[field.key]}</dd>
                  </div>
                ))}
              </dl>
              {agent.handoff.artifacts?.length ? (
                <div className="hg-handoff-artifacts">
                  <span className="hg-chip-label">产出</span>
                  <ul className="hg-artifacts">
                    {agent.handoff.artifacts.map(artifact => (
                      <li key={`${artifact.kind}-${artifact.ref}`}>
                        <Icon name={artifact.kind === "pr" ? "pr" : "branch"} size={16} />
                        <code>{artifact.ref}</code>
                      </li>
                    ))}
                  </ul>
                </div>
              ) : null}
            </div>
          ) : (
            <EmptyState
              title="还没有交接摘要。"
              hint="Forager 完成第一轮之后，这里会出现目标、进展和风险。"
            />
          )}
        </Panel>
      ) : (
        <Panel title={placeholders[active]?.title ?? "占位"}>
          <EmptyState
            title={`${placeholders[active]?.title ?? "这个分区"}还没实现。`}
            hint={placeholders[active]?.hint}
          />
        </Panel>
      )}
    </div>
  );
}

function MergePanel({
  agent,
  pr,
  error,
  errorStatus,
  onRetry,
}: {
  agent: Agent;
  pr: PRStatusResponse | undefined;
  error: string;
  errorStatus: number | undefined;
  onRetry: () => void;
}) {
  return (
    <Panel
      title="验证与合并"
      subtitle={
        agent.prNumber
          ? `PR #${agent.prNumber} 的结论历史，以及平台按方法论判断的可合并状态。`
          : "这只 Forager 还没有关联 PR。"
      }
    >
      {!agent.prNumber ? (
        <EmptyState
          title="还没有 PR。"
          hint="Forager 发布分支并打开 PR 后，这里会出现结论历史和可合并状态。"
        />
      ) : null}
      {error ? (
        <ErrorNotice
          message={error}
          onRetry={onRetry}
          auth={errorStatus === 401}
          admin={errorStatus === 403}
        />
      ) : null}
      {pr ? <MergeBody pr={pr} /> : agent.prNumber && !error ? (
        <BeeLoader label="正在读取 PR 状态…" />
      ) : null}
    </Panel>
  );
}

function MergeBody({ pr }: { pr: PRStatusResponse }) {
  const { merged, ready, headSha } = pr;
  const reasons = pr.reasons ?? [];
  const tone = merged || ready ? "mint" : "coral";
  const label = merged ? "已合并" : ready ? "可合并" : "尚不可合并";

  return (
    <div className="hg-merge-body">
      <div className="hg-merge-status">
        <span className={`hg-badge hg-tone-${tone}`} data-testid="merge-readiness">
          {label}
        </span>
        <code className="hg-merge-sha" title={headSha}>
          {shortSha(headSha)}
        </code>
        <span className="hg-muted">
          PR #{pr.prNumber} · {pr.state || "unknown"}
          {pr.draft ? " · draft" : ""}
        </span>
        {pr.url ? (
          <a className="hg-meta" href={pr.url} target="_blank" rel="noreferrer">
            <Icon name="external" size={16} />
            在 GitHub 查看
          </a>
        ) : null}
      </div>

      {reasons.length ? (
        <ul className="hg-merge-reasons">
          {reasons.map(reason => (
            <li key={reason}>{reason}</li>
          ))}
        </ul>
      ) : (
        <p className="hg-form-status">所有合并条件都已满足。</p>
      )}

      <section className="hg-merge-section">
        <h3>CI 检查</h3>
        <CheckList
          checks={mergeChecks(pr.checks ?? [])}
          empty="这个 head 上还没有检查结果。"
        />
      </section>

      <section className="hg-merge-section">
        <h3>结论历史</h3>
        <AttestationTable attestations={pr.attestations ?? []} />
      </section>
    </div>
  );
}

function mergeChecks(checks: MergeCheck[]): Check[] {
  return checks.map(check => {
    const passed = ["success", "neutral", "skipped"].includes(check.conclusion);
    const pending = check.status !== "completed";
    const status: CheckStatus = pending ? "pending" : passed ? "passed" : "failed";
    return {
      name: check.name,
      status,
      detail: pending ? check.status : check.conclusion,
    };
  });
}

function AttestationTable({ attestations }: { attestations: Attestation[] }) {
  if (attestations.length === 0) {
    return <EmptyState title="还没有结论。" hint="验证者给出结论后，这里会按 head SHA 记录。" />;
  }
  return (
    <div className="hg-table-wrap">
      <table className="hg-attest-table" aria-label="结论历史">
        <thead>
          <tr>
            <th scope="col">结论</th>
            <th scope="col">提交</th>
            <th scope="col">给出者</th>
            <th scope="col">是否作者</th>
            <th scope="col">时间</th>
            <th scope="col">评论</th>
            <th scope="col">状态</th>
          </tr>
        </thead>
        <tbody>
          {attestations.map(attestation => (
            <tr
              key={attestation.id}
              className={attestation.superseded ? "hg-attest-superseded" : undefined}
            >
              <td>
                <AttestBadge result={attestation.result} />
                <span className="hg-attest-summary">{attestation.summary}</span>
                {attestation.findings?.length ? (
                  <details className="hg-attest-findings">
                    <summary>{attestation.findings.length} 条发现</summary>
                    <ul>
                      {attestation.findings.map((finding, index) => (
                        <li key={`${attestation.id}-${index}`}>
                          <span className="hg-chip">{finding.severity}</span>
                          {finding.location ? <code>{finding.location}</code> : null}
                          {finding.finding}
                        </li>
                      ))}
                    </ul>
                  </details>
                ) : null}
              </td>
              <td>
                <code title={attestation.headSha}>{shortSha(attestation.headSha)}</code>
              </td>
              <td>
                <code>{attestation.attesterId}</code>
              </td>
              <td>
                <span
                  className={
                    attestation.isAuthor ? "hg-badge hg-tone-amber" : "hg-badge hg-tone-mint"
                  }
                >
                  {attestation.isAuthor ? "作者" : "独立"}
                </span>
              </td>
              <td>{formatTimestamp(attestation.createdAt)}</td>
              <td>
                {attestation.commentUrl ? (
                  <a href={attestation.commentUrl} target="_blank" rel="noreferrer">
                    在 GitHub 查看
                  </a>
                ) : (
                  <span className="hg-muted">—</span>
                )}
              </td>
              <td>
                <span
                  className={
                    attestation.superseded
                      ? "hg-badge hg-tone-neutral"
                      : "hg-badge hg-tone-mint"
                  }
                >
                  {attestation.superseded ? "已作废" : "有效"}
                </span>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function ForagerComposer({
  hiveId,
  foragerId,
  onSent,
}: {
  hiveId: string;
  foragerId: string;
  onSent: () => void;
}) {
  const id = useId();
  const [text, setText] = useState("");
  const [status, setStatus] = useState("");
  const [error, setError] = useState("");
  const [sending, setSending] = useState(false);

  return (
    <form
      className="hg-composer"
      onSubmit={event => {
        event.preventDefault();
        const body = text.trim();
        if (!body) {
          setError("先写下要对这只 Forager 说的话。");
          setStatus("");
          return;
        }
        setSending(true);
        setError("");
        setStatus("");
        hiveAPI
          .sendMessage(hiveId, { to: `forager:${foragerId}`, body })
          .then(() => {
            setText("");
            setStatus("已写入这只 Forager 的 Inbox。");
            onSent();
          })
          .catch(cause => setError(cause instanceof Error ? cause.message : String(cause)))
          .finally(() => setSending(false));
      }}
    >
      <label className="hg-field-label" htmlFor={id}>
        对这只 Forager 说话
      </label>
      <textarea
        id={id}
        rows={3}
        value={text}
        onChange={event => setText(event.target.value)}
        placeholder="补充要求、纠正方向，或者让它先汇报进展。"
      />
      <div className="hg-composer-foot">
        <span className="hg-composer-hint">消息会进入它的 Inbox，在本轮结束后送达。</span>
        <button className="hg-btn hg-btn-primary" type="submit" disabled={sending}>
          <Icon name="send" size={16} />
          {sending ? "发送中…" : "发送"}
        </button>
      </div>
      {error ? (
        <p className="hg-form-error" role="alert">
          {error}
        </p>
      ) : null}
      {status ? (
        <p className="hg-form-status" role="status">
          {status}
        </p>
      ) : null}
    </form>
  );
}
