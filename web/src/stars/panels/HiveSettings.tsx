import { useId, useState } from "react";
import { useDataAPI } from "../../data";
import {
  BeeLoader,
  Callout,
  ErrorNotice,
  Panel,
  StatusGrid,
  StatusItem,
} from "./primitives";
import { formatUsd } from "./format";

import type { Hive, MethodologyLimits } from "../../data";
import { useResource } from "./use-resource";

export default function HiveSettings({
  hive,
  onSaved,
}: {
  hive: Hive;
  onSaved: () => void;
}) {
  const api = useDataAPI();
  const id = useId();
  const models = useResource((signal) => api.listModels(signal), []);
  const methodology = useResource(
    (signal) => api.getMethodology(hive.id, undefined, signal),
    [hive.id],
  );
  const [queenModel, setQueenModel] = useState(hive.config.queenModel ?? "");
  const [concurrency, setConcurrency] = useState(
    String(hive.config.maxConcurrentForagers),
  );
  const [queenEnabled, setQueenEnabled] = useState(hive.queenEnabled);
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState("");
  const [error, setError] = useState("");
  const configured = new Set(
    models.data?.providers
      .filter((provider) => provider.configured)
      .map((provider) => provider.provider),
  );
  const availableModels =
    models.data?.models.filter((model) => configured.has(model.provider)) ?? [];
  const currentModelMissing =
    queenModel && !availableModels.some((model) => model.id === queenModel);
  const limits = !methodology.data?.builtin
    ? methodology.data?.manifest.limits
    : undefined;

  return (
    <>
      <Panel
        title="项目设置"
        subtitle="主 Agent 管理这个项目，并发上限只计算 Agent。"
      >
        <StatusGrid>
          <StatusItem label="仓库">
            <code>{hive.repository}</code>
          </StatusItem>
        </StatusGrid>
        {models.loading && !models.data ? (
          <BeeLoader label="正在读取模型…" />
        ) : null}
        {models.error ? (
          <ErrorNotice
            message={models.error}
            onRetry={models.reload}
            auth={models.errorStatus === 401}
            admin={models.errorStatus === 403}
          />
        ) : null}
        {models.data && availableModels.length === 0 ? (
          <Callout tone="amber" title="没有可用的 主 Agent 模型">
            <p>
              请先到平台设置的“模型”填写模型凭证，再回来选择主 Agent 的模型。
            </p>
          </Callout>
        ) : null}
        <form
          onSubmit={(event) => {
            event.preventDefault();
            const maxConcurrentForagers = Number(concurrency);
            setNotice("");
            setError("");
            if (
              !Number.isSafeInteger(maxConcurrentForagers) ||
              maxConcurrentForagers < 1
            ) {
              setError("并发上限必须是大于 0 的整数。");
              return;
            }
            setBusy(true);
            api
              .updateHive(hive.id, {
                queenEnabled,
                config: { queenModel, maxConcurrentForagers },
              })
              .then(() => {
                setNotice("项目设置已保存。");
                onSaved();
              })
              .catch((cause) =>
                setError(
                  cause instanceof Error ? cause.message : String(cause),
                ),
              )
              .finally(() => setBusy(false));
          }}
        >
          <div className="pc-form-grid">
            <label className="pc-field" htmlFor={`${id}-model`}>
              <span>主 Agent 模型</span>
              <select
                aria-label="主 Agent 模型"
                id={`${id}-model`}
                value={queenModel}
                disabled={busy || !models.data || availableModels.length === 0}
                onChange={(event) => {
                  setQueenModel(event.target.value);
                  setNotice("");
                }}
              >
                <option value="">
                  部署默认模型
                  {models.data?.defaultModel
                    ? `（${models.data.defaultModel}）`
                    : ""}
                </option>
                {currentModelMissing ? (
                  <option value={queenModel}>
                    {queenModel}（当前模型，暂不可用）
                  </option>
                ) : null}
                {availableModels.map((model) => (
                  <option key={model.id} value={model.id}>
                    {model.displayName
                      ? `${model.displayName} · ${model.id}`
                      : model.id}
                  </option>
                ))}
              </select>
            </label>
            <label className="pc-field" htmlFor={`${id}-concurrency`}>
              <span>并发上限</span>
              <input
                id={`${id}-concurrency`}
                type="number"
                min="1"
                step="1"
                required
                value={concurrency}
                disabled={busy}
                onChange={(event) => {
                  setConcurrency(event.target.value);
                  setNotice("");
                }}
              />
            </label>
          </div>
          <p className="pc-muted">主 Agent 不占用 Agent 的并发槽。</p>
          <label className="pc-checkbox">
            <input
              type="checkbox"
              checked={queenEnabled}
              disabled={busy}
              onChange={(event) => {
                setQueenEnabled(event.target.checked);
                setNotice("");
              }}
            />

            <span>
              <strong>开启 主 Agent</strong>
              <small>
                开启后可在对话页和主 Agent 交流，关闭后停止主 Agent。
              </small>
            </span>
          </label>
          <div className="pc-composer-foot">
            <span className="pc-composer-hint">保存后生效。</span>
            <button
              className="pc-btn pc-btn-primary"
              type="submit"
              disabled={busy}
            >
              {busy ? "保存中…" : "保存设置"}
            </button>
          </div>
          {error ? (
            <p className="pc-form-error" role="alert">
              {error}
            </p>
          ) : null}
          {notice ? (
            <p className="pc-form-status" role="status">
              {notice}
            </p>
          ) : null}
        </form>
      </Panel>
      {methodology.error && methodology.errorStatus !== 404 ? (
        <ErrorNotice
          message={methodology.error}
          onRetry={methodology.reload}
          auth={methodology.errorStatus === 401}
          admin={methodology.errorStatus === 403}
        />
      ) : null}
      {limits && Object.values(limits).some((value) => value !== undefined) ? (
        <Panel
          title="方法论声明的上限"
          subtitle="只读；由当前生效的方法论声明。"
        >
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
    {
      label: "单 Agent 花费",
      value:
        limits.maxCostPerAgentUsd === undefined
          ? undefined
          : formatUsd(limits.maxCostPerAgentUsd),
    },
    {
      label: "每日花费",
      value:
        limits.maxCostPerDayUsd === undefined
          ? undefined
          : formatUsd(limits.maxCostPerDayUsd),
    },
  ];
  return (
    <dl className="pc-limits">
      {rows
        .filter((row) => row.value !== undefined)
        .map((row) => (
          <div key={row.label}>
            <dt>{row.label}</dt>
            <dd>{row.value}</dd>
          </div>
        ))}
    </dl>
  );
}
