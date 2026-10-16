// Forager detail page: header checks plus the conversation and handoff sections.
// The Diff / timeline / terminal sections are placeholders for the next rounds.
import { foragerDetailById, hiveById } from "../fixtures/hive-console";
import type { Check } from "./types";
import { CheckList, EmptyState, Panel, StatusBadge, Tabs } from "./components";
import { ForagerAvatar } from "./pixel";
import { Icon } from "./icons";
import { ConversationStream } from "./ConversationStream";

const sections = [
  { id: "conversation", label: "对话" },
  { id: "handoff", label: "交接摘要" },
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

function CheckChips({ label, checks }: { label: string; checks: Check[] }) {
  const passed = checks.filter(check => check.status === "passed").length;
  return (
    <div className="hg-chip-group">
      <span className="hg-chip-label">
        {label} {passed}/{checks.length}
      </span>
      <ul className="hg-chip-row">
        {checks.map(check => (
          <li key={check.name} className={`hg-chip hg-tone-${check.status === "passed" ? "mint" : check.status === "failed" ? "coral" : "amber"}`}>
            <Icon name={check.status === "passed" ? "check" : check.status === "failed" ? "alert" : "clock"} size={16} />
            {check.name}
          </li>
        ))}
      </ul>
    </div>
  );
}

export default function ForagerPage({
  hiveId,
  foragerId,
  section,
}: {
  hiveId: string;
  foragerId: string;
  section: string;
}) {
  const detail = foragerDetailById(foragerId);
  const hive = hiveById(hiveId);

  if (!detail || !hive) {
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

  const { agent } = detail;
  const active = section || "conversation";
  const tabs = sections.map(item => ({
    ...item,
    href:
      item.id === "conversation"
        ? `#/hives/${hive.id}/foragers/${agent.id}`
        : `#/hives/${hive.id}/foragers/${agent.id}/${item.id}`,
  }));

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
        <ForagerAvatar id={agent.id} size={64} />
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
              <Icon name="clock" size={16} />
              预设 {agent.preset ?? "默认"}
            </span>
          </div>
        </div>
      </header>

      <div className="hg-check-strip">
        <CheckChips label="CI" checks={detail.ci} />
        <CheckChips label="Guard（守卫蜂）" checks={detail.guards} />
      </div>

      <Tabs items={tabs} active={active} label={`${agent.id} 分区`} />

      {active === "conversation" ? (
        <Panel title="对话" subtitle="Forager 与 Queen（蜂王）之间的往来。">
          <ConversationStream
            entries={detail.conversation}
            agents={[agent]}
            emptyTitle="这只 Forager 还没有对话。"
          />
        </Panel>
      ) : active === "handoff" ? (
        <Panel
          title="交接摘要"
          subtitle="每轮结束时更新，也是 CLI 会话丢失后的恢复依据。"
        >
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
      ) : active === "diff" ? (
        <Panel
          title="Diff"
          subtitle={`${detail.diff.files} 个文件 · +${detail.diff.additions} / -${detail.diff.deletions}`}
        >
          <EmptyState title="Diff 还没实现。" hint={placeholders.diff.hint} />
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
