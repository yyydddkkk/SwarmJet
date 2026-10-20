// Spend and token usage (round 4, contract section 7: GET /api/usage).
// Used twice: the Hive page shows today's total plus the per-Agent ranking, and
// the Forager page shows one Agent's own spend and model breakdown.
import { hiveAPI } from "./api";
import type { AgentUsage, UsageResponse } from "./types";
import { BeeLoader, EmptyState, ErrorNotice, Panel, StatusGrid, StatusItem, UsageBar } from "./components";
import { formatTokens, formatUsd } from "./format";
import { Icon } from "./icons";
import { useResource } from "./use-resource";

function agentUsage(usage: UsageResponse, agentId: string): AgentUsage | undefined {
  return usage.agents.find(item => item.agentId === agentId) ?? usage.agents[0];
}

export function HiveUsagePanel({ hiveId }: { hiveId: string }) {
  const usage = useResource(
    signal => hiveAPI.getUsage({ hive: hiveId }, signal),
    [hiveId],
    15_000,
  );
  const data = usage.data;
  const maxCost = Math.max(0, ...(data?.agents ?? []).map(item => item.costUsd));

  return (
    <Panel
      title={
        <span className="hg-panel-title">
          <Icon name="clock" size={16} />
          今日用量
        </span>
      }
      subtitle="平台按 Agent 记录模型 token 和花费；当天累计到本地时区的午夜。"
      extra={
        data ? (
          <button className="hg-btn hg-btn-small" type="button" onClick={usage.reload}>
            <Icon name="refresh" size={16} />
            刷新
          </button>
        ) : null
      }
    >
      {usage.loading && !data ? (
        <BeeLoader label="正在读取用量…" />
      ) : usage.error && !data ? (
        <ErrorNotice
          message={usage.error}
          onRetry={usage.reload}
          auth={usage.errorStatus === 401}
          admin={usage.errorStatus === 403}
        />
      ) : data ? (
        <>
          {usage.error ? (
            <p className="hg-form-error" role="alert">
              {usage.error}（显示的是上一次读取结果）
            </p>
          ) : null}
          <div className="hg-usage-hero">
            <div>
              <span className="hg-usage-label">今天花费</span>
              <strong className="hg-usage-total" data-testid="hive-usage-total">
                {formatUsd(data.totalCostUsd)}
              </strong>
              <span className="hg-muted">
                {data.date} · 输入 {formatTokens(data.totalInputTokens)} / 输出{" "}
                {formatTokens(data.totalOutputTokens)} tokens
              </span>
            </div>
            {data.limits?.maxCostPerDayUsd ? (
              <div className="hg-usage-limit">
                <span className="hg-usage-label">每日上限</span>
                <strong>{formatUsd(data.limits.maxCostPerDayUsd)}</strong>
                <span className="hg-muted">
                  已用
                  {Math.min(
                    100,
                    Math.round((data.totalCostUsd / data.limits.maxCostPerDayUsd) * 100),
                  )}
                  %
                </span>
              </div>
            ) : null}
          </div>

          {data.agents.length === 0 ? (
            <EmptyState
              title="今天还没有用量。"
              hint="Agent 调用模型后，这里会按花费排出名次。"
            />
          ) : (
            <ol className="hg-usage-ranking" aria-label="Agent 花费排行">
              {[...data.agents]
                .sort((left, right) => right.costUsd - left.costUsd)
                .map((agent, index) => (
                  <li key={agent.agentId} className="hg-usage-row">
                    <span className="hg-usage-rank">{index + 1}</span>
                    <div className="hg-usage-main">
                      <div className="hg-usage-row-head">
                        <a href={`#/hives/${hiveId}/foragers/${agent.agentId}`}>
                          <code>{agent.agentId}</code>
                        </a>
                        {agent.roleName ? (
                          <span className="hg-badge hg-tone-neutral">{agent.roleName}</span>
                        ) : null}
                        <span className="hg-usage-cost">{formatUsd(agent.costUsd)}</span>
                      </div>
                      <UsageBar value={agent.costUsd} max={maxCost} />
                      <span className="hg-muted hg-usage-tokens">
                        输入 {formatTokens(agent.inputTokens)} / 输出{" "}
                        {formatTokens(agent.outputTokens)} tokens
                      </span>
                    </div>
                  </li>
                ))}
            </ol>
          )}
        </>
      ) : null}
    </Panel>
  );
}

export function AgentUsagePanel({ hiveId, agentId }: { hiveId: string; agentId: string }) {
  const usage = useResource(
    signal => hiveAPI.getUsage({ hive: hiveId, agent: agentId }, signal),
    [hiveId, agentId],
    15_000,
  );
  const data = usage.data;
  const agent = data ? agentUsage(data, agentId) : undefined;

  return (
    <Panel
      title={
        <span className="hg-panel-title">
          <Icon name="clock" size={16} />
          用量与花费
        </span>
      }
      subtitle="按这个 Agent 的模型调用记录汇总；未知价格的模型会标记为按 0 计。"
      extra={
        data ? (
          <button className="hg-btn hg-btn-small" type="button" onClick={usage.reload}>
            <Icon name="refresh" size={16} />
            刷新
          </button>
        ) : null
      }
    >
      {usage.loading && !data ? (
        <BeeLoader label="正在读取这只 Agent 的用量…" />
      ) : usage.error && !data ? (
        <ErrorNotice
          message={usage.error}
          onRetry={usage.reload}
          auth={usage.errorStatus === 401}
          admin={usage.errorStatus === 403}
        />
      ) : !agent ? (
        <EmptyState
          title="这只 Agent 还没有用量。"
          hint="它调用模型之后，token 和花费会出现在这里。"
        />
      ) : (
        <>
          <StatusGrid>
            <StatusItem label="今天花费">
              <strong className="hg-usage-total" data-testid="agent-usage-cost">
                {formatUsd(agent.costUsd)}
              </strong>
            </StatusItem>
            <StatusItem label="输入 tokens">{formatTokens(agent.inputTokens)}</StatusItem>
            <StatusItem label="输出 tokens">{formatTokens(agent.outputTokens)}</StatusItem>
            <StatusItem label="角色">{agent.roleName || "—"}</StatusItem>
            {data?.limits?.maxCostPerAgentUsd ? (
              <StatusItem label="单 Agent 上限">
                {formatUsd(data.limits.maxCostPerAgentUsd)}
              </StatusItem>
            ) : null}
          </StatusGrid>

          {agent.models?.length ? (
            <div className="hg-table-wrap">
              <table className="hg-mini-table">
                <thead>
                  <tr>
                    <th scope="col">模型</th>
                    <th scope="col">输入</th>
                    <th scope="col">输出</th>
                    <th scope="col">花费</th>
                  </tr>
                </thead>
                <tbody>
                  {agent.models.map(model => (
                    <tr key={model.model}>
                      <td>
                        <code>{model.model}</code>
                        {model.priceKnown === false ? (
                          <span className="hg-badge hg-tone-amber hg-model-default">价格未知</span>
                        ) : null}
                      </td>
                      <td>{formatTokens(model.inputTokens)}</td>
                      <td>{formatTokens(model.outputTokens)}</td>
                      <td>{formatUsd(model.costUsd)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <p className="hg-muted">还没有按模型拆分的记录。</p>
          )}
        </>
      )}
    </Panel>
  );
}
