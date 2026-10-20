import { useId, useState } from "react";
import { hiveAPI } from "./api";
import { BeeLoader, Callout, ErrorNotice, Panel, StatusGrid, StatusItem } from "./components";
import { formatUsd } from "./format";
import { QueenAvatar } from "./pixel";
import type { Hive, MethodologyLimits } from "./types";
import { useResource } from "./use-resource";

export default function HiveSettings({ hive, onSaved }: { hive: Hive; onSaved: () => void }) {
  const id = useId();
  const models = useResource(signal => hiveAPI.listModels(signal), []);
  const methodology = useResource(
    signal => hiveAPI.getMethodology(hive.id, undefined, signal),
    [hive.id],
  );
  const [queenModel, setQueenModel] = useState(hive.config.queenModel ?? "");
  const [concurrency, setConcurrency] = useState(String(hive.config.maxConcurrentForagers));
  const [queenEnabled, setQueenEnabled] = useState(hive.queenEnabled);
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState("");
  const [error, setError] = useState("");
  const configured = new Set(
    models.data?.providers.filter(provider => provider.configured).map(provider => provider.provider),
  );
  const availableModels = models.data?.models.filter(model => configured.has(model.provider)) ?? [];
  const currentModelMissing = queenModel && !availableModels.some(model => model.id === queenModel);
  const limits = !methodology.data?.builtin ? methodology.data?.manifest.limits : undefined;

  return (
    <>
      <Panel title="Hive 设置" subtitle="Queen 管理这个 Hive，并发上限只计算 Forager。">
        <StatusGrid>
          <StatusItem label="仓库"><code>{hive.repository}</code></StatusItem>
        </StatusGrid>
        {models.loading && !models.data ? <BeeLoader label="正在读取模型…" /> : null}
        {models.error ? (
          <ErrorNotice message={models.error} onRetry={models.reload}
            auth={models.errorStatus === 401} admin={models.errorStatus === 403} />
        ) : null}
        {models.data && availableModels.length === 0 ? (
          <Callout tone="amber" title="没有可用的 Queen 模型">
            <p>请先去<a href="#/models">模型页</a>填写模型凭证，再回来选择 Queen 的模型。</p>
          </Callout>
        ) : null}
        <form onSubmit={event => {
          event.preventDefault();
          const maxConcurrentForagers = Number(concurrency);
          setNotice("");
          setError("");
          if (!Number.isSafeInteger(maxConcurrentForagers) || maxConcurrentForagers < 1) {
            setError("并发上限必须是大于 0 的整数。");
            return;
          }
          setBusy(true);
          hiveAPI.updateHive(hive.id, {
            queenEnabled,
            config: { queenModel, maxConcurrentForagers },
          }).then(() => {
            setNotice("Hive 设置已保存。");
            onSaved();
          }).catch(cause => setError(cause instanceof Error ? cause.message : String(cause)))
            .finally(() => setBusy(false));
        }}>
          <div className="hg-form-grid">
            <label className="hg-field" htmlFor={`${id}-model`}>
              <span>Queen 模型</span>
              <select aria-label="Queen 模型" id={`${id}-model`} value={queenModel}
                disabled={busy || !models.data || availableModels.length === 0}
                onChange={event => { setQueenModel(event.target.value); setNotice(""); }}>
                <option value="">部署默认模型{models.data?.defaultModel ? `（${models.data.defaultModel}）` : ""}</option>
                {currentModelMissing ? <option value={queenModel}>{queenModel}（当前模型，暂不可用）</option> : null}
                {availableModels.map(model => <option key={model.id} value={model.id}>
                  {model.displayName ? `${model.displayName} · ${model.id}` : model.id}
                </option>)}
              </select>
            </label>
            <label className="hg-field" htmlFor={`${id}-concurrency`}>
              <span>并发上限</span>
              <input id={`${id}-concurrency`} type="number" min="1" step="1" required
                value={concurrency} disabled={busy}
                onChange={event => { setConcurrency(event.target.value); setNotice(""); }} />
            </label>
          </div>
          <p className="hg-muted">Queen 不占用 Forager 的并发槽。</p>
          <label className="hg-checkbox">
            <input type="checkbox" checked={queenEnabled} disabled={busy}
              onChange={event => { setQueenEnabled(event.target.checked); setNotice(""); }} />
            <QueenAvatar size={32} off={!queenEnabled} />
            <span><strong>开启 Queen（蜂王）</strong><small>开启后可在对话页和 Queen 交流，关闭后停止 Queen。</small></span>
          </label>
          <div className="hg-composer-foot">
            <span className="hg-composer-hint">保存后生效。</span>
            <button className="hg-btn hg-btn-primary" type="submit" disabled={busy}>
              {busy ? "保存中…" : "保存设置"}
            </button>
          </div>
          {error ? <p className="hg-form-error" role="alert">{error}</p> : null}
          {notice ? <p className="hg-form-status" role="status">{notice}</p> : null}
        </form>
      </Panel>
      {methodology.error && methodology.errorStatus !== 404 ? (
        <ErrorNotice message={methodology.error} onRetry={methodology.reload}
          auth={methodology.errorStatus === 401} admin={methodology.errorStatus === 403} />
      ) : null}
      {limits && Object.values(limits).some(value => value !== undefined) ? (
        <Panel title="方法论声明的上限" subtitle="只读；由当前生效的方法论声明。">
          <ReadOnlyLimits limits={limits} />
        </Panel>
      ) : null}
    </>
  );
}

function ReadOnlyLimits({ limits }: { limits: MethodologyLimits }) {
  const rows = [
    { label: "并发 Agent", value: limits.maxConcurrentAgents },
    { label: "消息跳数", value: limits.maxHops },
    { label: "每条链 Agent", value: limits.maxAgentsPerChain },
    { label: "单 Agent 花费", value: limits.maxCostPerAgentUsd === undefined ? undefined : formatUsd(limits.maxCostPerAgentUsd) },
    { label: "每日花费", value: limits.maxCostPerDayUsd === undefined ? undefined : formatUsd(limits.maxCostPerDayUsd) },
  ];
  return <dl className="hg-limits">{rows.filter(row => row.value !== undefined).map(row => (
    <div key={row.label}><dt>{row.label}</dt><dd>{row.value}</dd></div>
  ))}</dl>;
}
