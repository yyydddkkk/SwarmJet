// Spend and token usage (round 4, contract section 7: GET /api/usage).
// Used twice: the Hive page shows today's total plus the per-Agent ranking, and
// the Forager page shows one Agent's own spend and model breakdown. Both ask
// for period=today because every label here says 今天/今日. The response has
// no role names, so they are joined from the Agents the page already loaded.
import { useDataAPI } from "../../data";
import type { Agent, UsageGroup, UsageResponse } from "../../data";
import {
  BeeLoader,
  EmptyState,
  ErrorNotice,
  Panel,
  StatusGrid,
  StatusItem,
  UsageBar,
} from "./primitives";
import { formatTokens, formatUsd } from "./format";
import { Icon } from "./icons";
import { useResource } from "./use-resource";

/** The Agent's own row, or undefined: never another Agent's row. */
export function agentUsage(
  usage: UsageResponse,
  agentId: string,
): UsageGroup | undefined {
  return (usage.byAgent ?? []).find((item) => item.agentId === agentId);
}

export function HiveUsagePanel({
  hiveId,
  agents = [],
  onAgent,
}: {
  hiveId: string;
  agents?: Agent[];
  onAgent?: (id: string) => void;
}) {
  const api = useDataAPI();
  const usage = useResource(
    (signal) => api.getUsage({ hive: hiveId, period: "today" }, signal),
    [hiveId],
    15_000,
  );
  const data = usage.data;
  const byAgent = data?.byAgent ?? [];
  const totals = data?.totals;
  const roleNames = new Map(
    agents.map((agent) => [agent.id, agent.roleName ?? ""]),
  );
  const maxCost = Math.max(0, ...byAgent.map((item) => item.costUsd));

  return (
    <Panel
      title={
        <span className="pc-panel-title">
          <Icon name="clock" size={16} />
          今日用量
        </span>
      }
      subtitle="平台按 Agent 记录模型 token 和花费；当天累计到本地时区的午夜。"
      extra={
        data ? (
          <button
            className="pc-btn pc-btn-small"
            type="button"
            onClick={usage.reload}
          >
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
            <p className="pc-form-error" role="alert">
              {usage.error}（显示的是上一次读取结果）
            </p>
          ) : null}
          <div className="pc-usage-hero">
            <div>
              <span className="pc-usage-label">今天花费</span>
              <strong className="pc-usage-total" data-testid="hive-usage-total">
                {formatUsd(totals?.costUsd ?? 0)}
              </strong>
              <span className="pc-muted">
                {totals?.requests ?? 0} 次调用 · 输入{" "}
                {formatTokens(totals?.inputTokens ?? 0)} / 输出{" "}
                {formatTokens(totals?.outputTokens ?? 0)} tokens
              </span>
              {totals?.unknownPriceRequests ? (
                <span className="pc-badge pc-tone-amber">
                  {totals.unknownPriceRequests} 次调用价格未知，金额不完整
                </span>
              ) : null}
            </div>
          </div>

          {byAgent.length === 0 ? (
            <EmptyState
              title="今天还没有用量。"
              hint="Agent 调用模型后，这里会按花费排出名次。"
            />
          ) : (
            <ol className="pc-usage-ranking" aria-label="Agent 花费排行">
              {[...byAgent]
                .sort((left, right) => right.costUsd - left.costUsd)
                .map((agent, index) => {
                  const roleName = roleNames.get(agent.agentId ?? "");
                  return (
                    <li key={agent.agentId} className="pc-usage-row">
                      <span className="pc-usage-rank">{index + 1}</span>
                      <div className="pc-usage-main">
                        <div className="pc-usage-row-head">
                          <button
                            disabled={!agent.agentId || !onAgent}
                            onClick={() =>
                              agent.agentId && onAgent?.(agent.agentId)
                            }
                          >
                            <code>{agent.agentId}</code>
                          </button>
                          {roleName ? (
                            <span className="pc-badge pc-tone-neutral">
                              {roleName}
                            </span>
                          ) : null}
                          <span className="pc-usage-cost">
                            {formatUsd(agent.costUsd)}
                          </span>
                        </div>
                        <UsageBar value={agent.costUsd} max={maxCost} />
                        <span className="pc-muted pc-usage-tokens">
                          输入 {formatTokens(agent.inputTokens)} / 输出{" "}
                          {formatTokens(agent.outputTokens)} tokens
                        </span>
                      </div>
                    </li>
                  );
                })}
            </ol>
          )}
        </>
      ) : null}
    </Panel>
  );
}

export function AgentUsagePanel({
  hiveId,
  agentId,
  roleName,
}: {
  hiveId: string;
  agentId: string;
  roleName?: string;
}) {
  const api = useDataAPI();
  const usage = useResource(
    (signal) =>
      api.getUsage({ hive: hiveId, agent: agentId, period: "today" }, signal),
    [hiveId, agentId],
    15_000,
  );
  const data = usage.data;
  const agent = data ? agentUsage(data, agentId) : undefined;
  // The request is filtered to this Agent, so byModel is its own breakdown.
  const models = agent ? (data?.byModel ?? []) : [];

  return (
    <Panel
      title={
        <span className="pc-panel-title">
          <Icon name="clock" size={16} />
          用量与花费
        </span>
      }
      subtitle="按这个 Agent 的模型调用记录汇总；未知价格的模型单独标记，金额可能不完整。"
      extra={
        data ? (
          <button
            className="pc-btn pc-btn-small"
            type="button"
            onClick={usage.reload}
          >
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
              <strong className="pc-usage-total" data-testid="agent-usage-cost">
                {formatUsd(agent.costUsd)}
              </strong>
            </StatusItem>
            <StatusItem label="输入 tokens">
              {formatTokens(agent.inputTokens)}
            </StatusItem>
            <StatusItem label="输出 tokens">
              {formatTokens(agent.outputTokens)}
            </StatusItem>
            <StatusItem label="调用次数">{agent.requests}</StatusItem>
            <StatusItem label="角色">{roleName || "—"}</StatusItem>
          </StatusGrid>

          {models.length ? (
            <div className="pc-table-wrap">
              <table className="pc-mini-table">
                <thead>
                  <tr>
                    <th scope="col">模型</th>
                    <th scope="col">输入</th>
                    <th scope="col">输出</th>
                    <th scope="col">花费</th>
                  </tr>
                </thead>
                <tbody>
                  {models.map((model) => (
                    <tr key={model.model}>
                      <td>
                        <code>{model.model}</code>
                        {model.unknownPriceRequests > 0 ? (
                          <span className="pc-badge pc-tone-amber pc-model-default">
                            价格未知
                          </span>
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
            <p className="pc-muted">还没有按模型拆分的记录。</p>
          )}
        </>
      )}
    </Panel>
  );
}
